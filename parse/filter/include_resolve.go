package filter

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"time"

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
	rel := field.Relation
	// Column by column, as the statement ranks a key of several. Targets
	// are normalized keys (core.NormalizeKey), which their columns' types
	// always order; the first refusal is kept and returned all the same.
	var orderErr error
	compare := func(a, b core.Key) int {
		for i, t := range rel.TargetTypes {
			c, err := core.CompareCanonical(t, a[i], b[i])
			if err != nil && orderErr == nil {
				orderErr = err
			}
			if c != 0 {
				return c
			}
		}
		return 0
	}
	byOwner := map[string][]core.Key{}
	visit := func(owner string, target core.Key) error {
		keys := byOwner[owner]
		at, found := slices.BinarySearchFunc(keys, target, compare)
		if orderErr != nil {
			return fmt.Errorf("target: %w", orderErr)
		}
		if found || at > cfg.maxPerOwner {
			return nil
		}
		// Keep only the lowest cap+1 distinct keys. The extra key proves
		// truncation without retaining an unbounded related-row set.
		keys = slices.Insert(keys, at, target)
		if len(keys)-1 > cfg.maxPerOwner {
			keys = keys[:cfg.maxPerOwner+1]
		}
		byOwner[owner] = keys
		return nil
	}
	err = relationPairs(ctx, field, ownerKeys, lookup, resolveConfig{scope: cfg.scope, related: cfg.related}, visit)
	if err != nil {
		return IncludeRows{}, fmt.Errorf("resolving include %s: %w", field.ExternalName, err)
	}
	out := IncludeRows{IDs: map[string][]string{}}
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

// ResolveRelationFacet answers a relation facet of BuildFacetQuery outside
// SQL: for each visible target of field's relation, under scope as an
// include reads them (ResolveInclude), the number of distinct owners among
// ownerKeys that it pairs with, keyed by the target key's text
// (core.KeyTextOf), the key the facet's bucket carries. FacetBuckets
// orders and cuts the counts. Owners of more than one tenant resolve per
// tenant, each with its scope's Tenant: a RowTenant is refused.
func ResolveRelationFacet(ctx context.Context, field *FieldSchema, ownerKeys []core.Key, lookup IncludeLookup, scope RelationScope) (map[string]int64, error) {
	if field == nil || field.Relation == nil {
		return nil, fmt.Errorf("resolving relation facet: field is no relation")
	}
	if !field.Facetable {
		return nil, fmt.Errorf("resolving relation facet: field %q is not facetable", field.ExternalName)
	}
	cfg := resolveConfig{scope: scope.tenancy(), related: scope.policies().at(time.Now().UTC())}
	err := cfg.scope.check()
	if err != nil {
		return nil, fmt.Errorf("resolving relation facet %s: %w", field.ExternalName, err)
	}
	if cfg.scope.row.column != "" {
		return nil, fmt.Errorf("resolving relation facet %s: a lookup reads one tenant's rows; resolve each tenant's owners with RelationScope.Tenant instead of RowTenant", field.ExternalName)
	}
	rel := field.Relation
	// Owners, not pairs: two pairs of one owner and one target (typed
	// edges) count the owner once, as COUNT(DISTINCT owner) does.
	owners := map[string]map[string]struct{}{}
	visit := func(owner string, target core.Key) error {
		name, err := core.KeyTextOf(rel.TargetTypes, target)
		if err != nil {
			return fmt.Errorf("target: %w", err)
		}
		seen, ok := owners[name]
		if !ok {
			seen = map[string]struct{}{}
			owners[name] = seen
		}
		seen[owner] = struct{}{}
		return nil
	}
	err = relationPairs(ctx, field, ownerKeys, lookup, cfg, visit)
	if err != nil {
		return nil, fmt.Errorf("resolving relation facet %s: %w", field.ExternalName, err)
	}
	counts := make(map[string]int64, len(owners))
	for name, seen := range owners {
		counts[name] = int64(len(seen))
	}
	return counts, nil
}

// VisibleTargets are those of keys whose target rows of field's declared
// many-to-many a request may relate under scope, read through lookup
// outside SQL: the rows VisibleRelated admits, in the tenant (or admitted
// by the target's policy) and live. A store that writes pairs itself
// pairs and unpairs only these, as patch.RenderRelationOp's statements
// do. Each key holds a value per target key column (TargetKeys),
// normalized to its type (core.NormalizeKey); the visible keys are
// returned in keys' order. scope's Tenant, Related and Now are read, as
// VisibleRelated reads them.
func VisibleTargets(ctx context.Context, field *FieldSchema, keys []core.Key, lookup RelationLookup, scope RelationScope) ([]core.Key, error) {
	if field == nil || field.Relation == nil || field.Relation.TargetTable == "" {
		return nil, errors.New("filter: VisibleTargets: field is not a many-to-many with a target")
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rel := field.Relation
	now := scope.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r := newMemberResolver(lookup, resolveConfig{scope: relationScope{tenant: scope.Tenant}, related: scope.policies().at(now)})
	var policy *ParsedFilter
	if rel.Policy {
		if field.Related == nil {
			return nil, unresolvableRelation(field, "is policy-scoped without a target schema to hold its policy")
		}
		root, err := r.cfg.related.on(field, nil)
		if err != nil {
			return nil, err
		}
		// The policy's own relations, as a policy-scoped predicate's.
		root, err = r.resolve(ctx, root)
		if err != nil {
			return nil, err
		}
		if root != nil {
			policy = NewParsedFilter(&FilterAST{Root: root})
		}
	}
	where, err := r.scoped(rel.TargetTable, rel.TargetKeys, rel.TargetTenant, rel.TargetDeleted, ownersIn(rel.TargetKeys, keys).typed(rel.TargetTypes))
	if err != nil {
		return nil, err
	}
	values, _, err := r.lookupValues(ctx, projecting(&Lookup{Table: rel.TargetTable, Where: where, Filter: policy}, rel.TargetKeys))
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(values))
	for _, v := range values {
		k, err := pairKey(v, len(rel.TargetKeys))
		if err != nil {
			return nil, fmt.Errorf("filter: visible targets of %s: %w", field.ExternalName, err)
		}
		text, err := core.KeyTextOf(rel.TargetTypes, k)
		if err != nil {
			return nil, fmt.Errorf("filter: visible targets of %s: %w", field.ExternalName, err)
		}
		held[text] = true
	}
	visible := make([]core.Key, 0, len(keys))
	for _, k := range keys {
		text, err := core.KeyTextOf(rel.TargetTypes, k)
		if err != nil {
			return nil, fmt.Errorf("filter: visible targets of %s: %w", field.ExternalName, err)
		}
		if held[text] {
			visible = append(visible, k)
		}
	}
	return visible, nil
}

// relationPairs reads the visible pairs of field's relation whose owner is
// among ownerKeys from lookup, under cfg's scope, and hands each to visit:
// its owner named by its text (core.KeyTextOf), its target normalized
// (core.NormalizeKey). A pair table holding one pair twice hands it twice.
func relationPairs(ctx context.Context, field *FieldSchema, ownerKeys []core.Key, lookup IncludeLookup, cfg resolveConfig, visit func(owner string, target core.Key) error) error {
	err := checkResolvable(field)
	if err != nil {
		return err
	}
	rel := field.Relation
	if len(ownerKeys) == 0 {
		return nil
	}
	if lookup == nil {
		return unresolvableRelation(field, "needs an include lookup to be evaluated outside SQL")
	}
	for i, k := range ownerKeys {
		if len(k) != len(rel.SourceColumns) {
			return fmt.Errorf("owner key %d has %d values for the %d key columns", i, len(k), len(rel.SourceColumns))
		}
	}
	r := newMemberResolver(lookup, cfg)
	op := OpSome
	if field.Related == nil {
		op = OpNotNull
	}
	l, _, err := r.relationLookup(ctx, &FilterNode{Field: field, Op: op})
	if err != nil {
		return err
	}
	projecting(l, rel.TargetColumns)
	l.Where = WhereAll(l.Where, ownersIn(rel.SourceColumns, ownerKeys).typed(rel.SourceTypes))
	l, err = r.inner(ctx, l)
	if err != nil {
		return err
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	pairs, err := lookup.LookupPairs(ctx, l, rel.SourceColumns)
	if err != nil {
		return err
	}
	if pairs == nil {
		return errors.New("lookup returned no iterator")
	}
	for pair, err := range pairs {
		if err != nil {
			return err
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		owner, err := pairKey(pair.Owner, len(rel.SourceColumns))
		if err != nil {
			return fmt.Errorf("owner: %w", err)
		}
		// Named by its canonical text, the text SQL names it by.
		name, err := core.KeyTextOf(rel.SourceTypes, owner)
		if err != nil {
			return fmt.Errorf("owner: %w", err)
		}
		stored, err := pairKey(pair.Target, len(rel.TargetColumns))
		if err != nil {
			return fmt.Errorf("target: %w", err)
		}
		target, err := core.NormalizeKey(rel.TargetTypes, stored)
		if err != nil {
			return fmt.Errorf("target: %w", err)
		}
		err = visit(name, target)
		if err != nil {
			return err
		}
	}
	return nil
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
