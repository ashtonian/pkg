package filter

import (
	"context"
	"fmt"
	"iter"
	"reflect"
	"slices"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// IncludePair is one related target key and the owner key it belongs to:
// each the value of its one key column, or a core.Key of a value per
// column for a key of several.
type IncludePair struct {
	Owner  any
	Target any
}

// IncludeLookup reads relation pairs on a backend that does not run SQL.
// LookupPairs projects the owner's key columns (owner) and l's projection
// (Lookup.Project, or Lookup.ProjectKey) from the rows both l.Where and
// l.Filter select: each pair's Owner is owner's one column's value, or a
// core.Key of a value per column for several, and its Target likewise.
// The two may share columns: a has-many whose foreign key is part of the
// target's key reads it as both. A store that cannot evaluate the filter
// must refuse it.
type IncludeLookup interface {
	MembershipLookup
	LookupPairs(ctx context.Context, l *Lookup, owner []string) (iter.Seq2[IncludePair, error], error)
}

// ResolveInclude answers BuildIncludeQuery's projection outside SQL. It uses
// the relation predicate resolver for tenant, live-row and target-policy
// visibility; keys are distinct and ordered before the per-owner cap, a
// key of several columns column by column, and named by their text
// (core.KeyTextOf) as SQL names them. Owners of more than one tenant
// resolve per tenant, each with its RelationScope.Tenant: a RowTenant is
// refused.
func ResolveInclude(ctx context.Context, field *FieldSchema, ownerKeys []core.Key, lookup IncludeLookup, opts ...IncludeQueryOption) (IncludeRows, error) {
	cfg, err := includeConfig(field, opts...)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include: %w", err)
	}
	if cfg.scope.row.column != "" {
		return IncludeRows{}, fmt.Errorf("resolving include %s: a lookup reads one tenant's rows; resolve each tenant's owners with RelationScope.Tenant instead of RowTenant", field.ExternalName)
	}
	err = checkResolvable(field)
	if err != nil {
		return IncludeRows{}, err
	}
	rel := field.Relation
	out := IncludeRows{IDs: map[string][]string{}}
	if len(ownerKeys) == 0 {
		return out, nil
	}
	if lookup == nil {
		return IncludeRows{}, unresolvableRelation(field, "needs an include lookup to be evaluated outside SQL")
	}
	for i, k := range ownerKeys {
		if len(k) != len(rel.SourceColumns) {
			return IncludeRows{}, fmt.Errorf("resolving include %s: owner key %d has %d values for the %d key columns", field.ExternalName, i, len(k), len(rel.SourceColumns))
		}
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
	projecting(l, rel.TargetColumns)
	l.Where = WhereAll(l.Where, ownersIn(rel.SourceColumns, ownerKeys).typed(rel.SourceTypes))
	l, err = r.inner(ctx, l)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return IncludeRows{}, contextErr
	}
	pairs, err := lookup.LookupPairs(ctx, l, rel.SourceColumns)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	if pairs == nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: lookup returned no iterator", field.ExternalName)
	}
	orders := make([]CursorKey, len(rel.TargetTypes))
	for i, t := range rel.TargetTypes {
		orders[i] = CursorKey{DataType: t.Type, NumberKind: t.NumberKind}
	}
	// Column by column, as the statement ranks a key of several.
	compare := func(a, b core.Key) int {
		for i, order := range orders {
			c := CompareKeyValues(order, a[i], b[i])
			if c != 0 {
				return c
			}
		}
		return 0
	}
	byOwner := map[string][]core.Key{}
	for pair, err := range pairs {
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return IncludeRows{}, contextErr
		}
		owner, err := pairKey(pair.Owner, len(rel.SourceColumns))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s owner: %w", field.ExternalName, err)
		}
		// Named by its canonical text, the text SQL names it by.
		name, err := core.KeyTextOf(rel.SourceTypes, owner)
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s owner: %w", field.ExternalName, err)
		}
		stored, err := pairKey(pair.Target, len(rel.TargetColumns))
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
		}
		target, err := core.NormalizeKey(rel.TargetTypes, stored)
		if err != nil {
			return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
		}
		keys := byOwner[name]
		at, found := slices.BinarySearchFunc(keys, target, compare)
		if found || at > cfg.maxPerOwner {
			continue
		}
		// Keep only the lowest cap+1 distinct keys. The extra key proves
		// truncation without retaining an unbounded related-row set.
		keys = slices.Insert(keys, at, target)
		if len(keys)-1 > cfg.maxPerOwner {
			keys = keys[:cfg.maxPerOwner+1]
		}
		byOwner[name] = keys
	}
	for owner, keys := range byOwner {
		if len(keys) > cfg.maxPerOwner {
			out.Truncated = append(out.Truncated, owner)
			keys = keys[:cfg.maxPerOwner]
		}
		ids := make([]string, len(keys))
		for i, k := range keys {
			ids[i], err = core.KeyTextOf(rel.TargetTypes, k)
			if err != nil {
				return IncludeRows{}, fmt.Errorf("resolving include %s target: %w", field.ExternalName, err)
			}
		}
		out.IDs[owner] = ids
	}
	slices.Sort(out.Truncated)
	return out, nil
}

// ownersIn is the condition holding a lookup's rows to the owner keys:
// WhereIn their values for a key of one column, WhereKeyIn for several.
func ownersIn(columns []string, keys []core.Key) *LookupCond {
	if len(columns) > 1 {
		return WhereKeyIn(columns, keys...)
	}
	values := make([]any, len(keys))
	for i, k := range keys {
		values[i] = k[0]
	}
	return WhereIn(columns[0], values...)
}

// pairKey is an include pair's owner or target as the key of n columns it
// names: the value of the one column, or a core.Key of a value per column,
// each as the database holds it (core.StoredValue), nil for NULL, and as
// normalization reads it (projectedValue).
func pairKey(v any, n int) (core.Key, error) {
	if n == 1 {
		value, err := pairValue(v)
		if err != nil {
			return nil, err
		}
		key := core.Key{value}
		return key, nil
	}
	var k []any
	switch kv := v.(type) {
	case core.Key:
		k = kv
	case []any:
		k = kv
	default:
		return nil, fmt.Errorf("a key of %d columns is a core.Key, not a %s", n, gowalker.TypeText(reflect.TypeOf(v)))
	}
	if len(k) != n {
		return nil, fmt.Errorf("a key of %d columns has %d values", n, len(k))
	}
	out := make(core.Key, n)
	for i, value := range k {
		v, err := pairValue(value)
		if err != nil {
			return nil, fmt.Errorf("key value %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// pairValue is one value of a pair key (pairKey).
func pairValue(v any) (any, error) {
	stored, _, err := core.StoredValue(v)
	if err != nil {
		return nil, err
	}
	return projectedValue(stored), nil
}
