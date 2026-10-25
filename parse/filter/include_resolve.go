package filter

import (
	"context"
	"fmt"
	"iter"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// IncludePair is one related target key and the owner key it belongs to.
type IncludePair struct {
	Owner  any
	Target any
}

// IncludeLookup reads relation pairs on a backend that does not run SQL.
// LookupPairs projects ownerColumn and l.Project from the rows both l.Where
// and l.Filter select. A store that cannot evaluate the filter must refuse it.
type IncludeLookup interface {
	MembershipLookup
	LookupPairs(ctx context.Context, l *Lookup, ownerColumn string) (iter.Seq2[IncludePair, error], error)
}

// ResolveInclude answers BuildIncludeQuery's projection outside SQL. It uses
// the relation predicate resolver for tenant, live-row and target-policy
// visibility; keys are distinct and ordered before the per-owner cap.
func ResolveInclude(ctx context.Context, field *FieldSchema, ownerPKs []any, lookup IncludeLookup, opts ...IncludeQueryOption) (IncludeRows, error) {
	cfg, err := includeConfig(field, opts...)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include: %w", err)
	}
	out := IncludeRows{IDs: map[string][]string{}}
	if len(ownerPKs) == 0 {
		return out, nil
	}
	if lookup == nil {
		return IncludeRows{}, invalidRelation(field, "needs an include lookup to be evaluated outside SQL")
	}
	r := newMemberResolver(ctx, lookup, resolveConfig{scope: cfg.scope, related: cfg.related})
	op := OpSome
	if field.Related == nil {
		op = OpNotNull
	}
	l, _, err := r.relationLookup(&FilterNode{Field: field, Op: op})
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	rel := field.Relation
	l.Project = rel.TargetColumn
	l.Where = WhereAll(l.Where, WhereIn(rel.SourceColumn, ownerPKs...))
	l, err = r.inner(l)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return IncludeRows{}, contextErr
	}
	pairs, err := lookup.LookupPairs(ctx, l, rel.SourceColumn)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if pairs == nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: lookup returned no iterator", field.ExternalName)
	}
	key := CursorKey{DataType: field.DataType, NumberKind: field.NumberKind}
	compare := func(a, b any) int { return CompareKeyValues(key, a, b) }
	byOwner := map[string][]any{}
	for pair, err := range pairs {
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return IncludeRows{}, contextErr
		}
		owner, err := CursorValue(projectedValue(pair.Owner))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s owner: %w", field.ExternalName, err)
		}
		target, err := core.NormalizeScalar(field.DataType, field.NumberKind, projectedValue(pair.Target))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
		}
		name := stringifyKey(owner)
		values := byOwner[name]
		at, found := slices.BinarySearchFunc(values, target, compare)
		if found || at > cfg.maxPerOwner {
			continue
		}
		// Keep only the lowest cap+1 distinct keys. The extra key proves
		// truncation without retaining an unbounded related-row set.
		values = slices.Insert(values, at, target)
		if len(values)-1 > cfg.maxPerOwner {
			values = values[:cfg.maxPerOwner+1]
		}
		byOwner[name] = values
	}
	for owner, values := range byOwner {
		if len(values) > cfg.maxPerOwner {
			out.Truncated = append(out.Truncated, owner)
			values = values[:cfg.maxPerOwner]
		}
		ids := make([]string, len(values))
		for i, value := range values {
			ids[i] = stringifyKey(value)
		}
		out.IDs[owner] = ids
	}
	slices.Sort(out.Truncated)
	return out, nil
}
