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
// Owners of more than one tenant resolve per tenant, each with its
// RelationScope.Tenant: a RowTenant is refused. A relation correlating keys
// of several columns is refused: a lookup projects one column.
func ResolveInclude(ctx context.Context, field *FieldSchema, ownerKeys []core.Key, lookup IncludeLookup, opts ...IncludeQueryOption) (IncludeRows, error) {
	cfg, err := includeConfig(field, opts...)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include: %w", err)
	}
	if cfg.scope.row.column != "" {
		return IncludeRows{}, fmt.Errorf("resolving include %s: a lookup reads one tenant's rows; resolve each tenant's owners with RelationScope.Tenant instead of RowTenant", field.ExternalName)
	}
	if err := singleColumnRelation(field); err != nil {
		return IncludeRows{}, err
	}
	out := IncludeRows{IDs: map[string][]string{}}
	if len(ownerKeys) == 0 {
		return out, nil
	}
	if lookup == nil {
		return IncludeRows{}, invalidRelation(field, "needs an include lookup to be evaluated outside SQL")
	}
	ownerPKs := make([]any, len(ownerKeys))
	for i, k := range ownerKeys {
		if len(k) != 1 {
			return IncludeRows{}, fmt.Errorf("resolving include %s: owner key %d has %d values for the one key column", field.ExternalName, i, len(k))
		}
		ownerPKs[i] = k[0]
	}
	r := newMemberResolver(lookup, resolveConfig{scope: cfg.scope, related: cfg.related})
	op := OpSome
	if field.Related == nil {
		op = OpNotNull
	}
	l, _, err := r.relationLookup(ctx, &FilterNode{Field: field, Op: op})
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	rel := field.Relation
	l.Project = rel.TargetColumns[0]
	l.Where = WhereAll(l.Where, WhereIn(rel.SourceColumns[0], ownerPKs...))
	l, err = r.inner(ctx, l)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return IncludeRows{}, contextErr
	}
	pairs, err := lookup.LookupPairs(ctx, l, rel.SourceColumns[0])
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if pairs == nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: lookup returned no iterator", field.ExternalName)
	}
	targetType := rel.TargetTypes[0]
	key := CursorKey{DataType: targetType.Type, NumberKind: targetType.NumberKind}
	compare := func(a, b any) int { return CompareKeyValues(key, a, b) }
	byOwner := map[string][]any{}
	for pair, err := range pairs {
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return IncludeRows{}, contextErr
		}
		// Named by their canonical texts, the texts SQL names them by.
		name, err := core.KeyValueText(rel.SourceTypes[0], projectedValue(pair.Owner))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s owner: %w", field.ExternalName, err)
		}
		target, err := core.NormalizeScalar(targetType.Type, targetType.NumberKind, projectedValue(pair.Target))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
		}
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
			if ids[i], err = core.KeyValueText(targetType, value); err != nil {
				return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
			}
		}
		out.IDs[owner] = ids
	}
	slices.Sort(out.Truncated)
	return out, nil
}
