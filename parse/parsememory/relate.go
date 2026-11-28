package parsememory

import (
	"context"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// relate applies op, one membership change of the update, to its
// relation's pair table for owners, the keys of the rows the update wrote
// and left (backend.UpdateSink.Keys), as patch.RenderRelationOp's
// statements apply it: a replace removes each owner's pairs to targets
// not among its values and pairs it with each value; an add pairs it with
// each value; a remove removes its pairs to the values. A pair already
// held is kept, as ON CONFLICT DO NOTHING keeps it. Every pair is the
// update's tenant's when the pair table holds one, and through a target
// table (core.RelationSchema.TargetTable) only targets the update may
// relate are paired and unpaired (filter.VisibleTargets). It reports how
// many pairs it removed and wrote.
func (u *updater) relate(ctx context.Context, op patch.RelationPatchOp, owners []core.Key) (int64, error) {
	if len(owners) == 0 {
		return 0, nil
	}
	err := op.Check()
	if err != nil {
		return 0, err
	}
	rel := op.Schema
	pt, err := u.pairsOf(op)
	if err != nil {
		return 0, fmt.Errorf("parsememory: relation %s: %w", op.Field, err)
	}
	owners, err = normalizedKeys(rel.SourceTypes, owners)
	if err != nil {
		return 0, fmt.Errorf("parsememory: relation %s: owners: %w", op.Field, err)
	}
	values, err := op.TargetKeys()
	if err != nil {
		return 0, err
	}
	var n int64
	if op.Verb != patch.RelationVerbAdd {
		removed, err := u.unpair(ctx, pt, owners, values, op.Verb == patch.RelationVerbReplace)
		if err != nil {
			return 0, fmt.Errorf("parsememory: relation %s: %w", op.Field, err)
		}
		n += removed
	}
	if op.Verb != patch.RelationVerbRemove && len(values) > 0 {
		if rel.TargetTable != "" {
			values, err = filter.VisibleTargets(ctx, pt.field, values, u.lookup, u.scope)
			if err != nil {
				return 0, fmt.Errorf("parsememory: relation %s: %w", op.Field, err)
			}
		}
		added, err := u.pair(pt, owners, values)
		if err != nil {
			return 0, fmt.Errorf("parsememory: relation %s: %w", op.Field, err)
		}
		n += added
	}
	return n, nil
}

// pairs is one relation's pair table as a membership change writes it: the
// table, the positions of the relation's source, target and tenant
// columns in it, and the tenant every pair is written for.
type pairs struct {
	rel   *core.RelationSchema
	field *filter.FieldSchema
	def   *tableDef
	// sources and targets are the source and target columns' positions;
	// tenantAt is the tenant column's, -1 for a pair table without one.
	sources, targets []int
	tenantAt         int
	// tenant is the update's tenant in the tenant column's storage form.
	tenant any
	// prefixed reports a table keyed first by the source columns, whose
	// pairs of one owner are adjacent in key order.
	prefixed bool
}

// pairsOf is op's pair table. A tenant-scoped pair table needs the
// update's tenant, as RenderRelationOp needs its TenantValue.
func (u *updater) pairsOf(op patch.RelationPatchOp) (*pairs, error) {
	rel := op.Schema
	def, ok := u.db.catalog.table(rel.JoinTable)
	if !ok {
		return nil, fmt.Errorf("pair table %q: no resource declares it", rel.JoinTable)
	}
	pt := &pairs{rel: rel, def: def, tenantAt: -1}
	if rel.TargetTable != "" {
		pt.field = u.res.Schema.Fields[op.Field]
		if pt.field == nil || pt.field.Relation == nil {
			return nil, fmt.Errorf("a membership change through the target table %s needs the field's schema", rel.TargetTable)
		}
	}
	var err error
	pt.sources, err = def.positions(rel.SourceColumns)
	if err != nil {
		return nil, err
	}
	pt.targets, err = def.positions(rel.TargetColumns)
	if err != nil {
		return nil, err
	}
	if rel.TenantColumn != "" {
		if u.scope.Tenant == nil {
			return nil, fmt.Errorf("pair table %s is tenant-scoped (%s) and the update binds no tenant", rel.JoinTable, rel.TenantColumn)
		}
		at, err := def.positions([]string{rel.TenantColumn})
		if err != nil {
			return nil, err
		}
		pt.tenantAt = at[0]
		pt.tenant, err = core.StorageValue(def.columns[pt.tenantAt].typ, u.scope.Tenant)
		if err != nil {
			return nil, fmt.Errorf("tenant column %s: %w", rel.TenantColumn, err)
		}
	}
	pt.prefixed = len(def.key) >= len(pt.sources) && slices.Equal(def.key[:len(pt.sources)], pt.sources)
	return pt, nil
}

// positions are columns' positions in def.
func (def *tableDef) positions(columns []string) ([]int, error) {
	at := make([]int, len(columns))
	for i, c := range columns {
		pos, ok := def.index[c]
		if !ok {
			return nil, fmt.Errorf("table %q has no column %q", def.name, c)
		}
		at[i] = pos
	}
	return at, nil
}

// unpair removes the owners' pairs, the tenant's when the table holds one:
// those whose target is among values (keep false, a remove), or those
// whose target is not (keep true, a replace; with no values, every pair).
// Through a target table only pairs to visible targets are removed. Each
// owner's pairs are found by a range of the key when the table is keyed
// by its source columns first, and by a scan otherwise.
func (u *updater) unpair(ctx context.Context, pt *pairs, owners, values []core.Key, keep bool) (int64, error) {
	t := u.w.rows(pt.def.name)
	if t == nil {
		return 0, nil
	}
	named, err := keyTexts(pt.rel.TargetTypes, values)
	if err != nil {
		return 0, fmt.Errorf("values: %w", err)
	}
	var doomed []*record
	var visitErr error
	visit := func(rec *record) {
		held, err := pt.heldFor(rec)
		if err != nil || !held {
			visitErr = err
			return
		}
		target, err := core.KeyTextOf(pt.rel.TargetTypes, pt.targetOf(rec))
		if err != nil {
			visitErr = fmt.Errorf("target: %w", err)
			return
		}
		if named[target] != keep {
			doomed = append(doomed, rec)
		}
	}
	if pt.prefixed {
		for _, owner := range owners {
			r, err := pt.def.prefixRange(owner)
			if err != nil {
				return 0, fmt.Errorf("owner: %w", err)
			}
			pt.def.eachInRange(t, r, func(rec *record) bool {
				visit(rec)
				return visitErr == nil
			})
			if visitErr != nil {
				return 0, visitErr
			}
		}
	} else {
		ownedBy, err := keyTexts(pt.rel.SourceTypes, owners)
		if err != nil {
			return 0, fmt.Errorf("owners: %w", err)
		}
		n := 0
		t.Scan(func(rec *record) bool {
			n++
			if n%checkEvery == 0 {
				visitErr = ctx.Err()
				if visitErr != nil {
					return false
				}
			}
			owner, err := core.KeyTextOf(pt.rel.SourceTypes, pt.sourceOf(rec))
			if err != nil {
				visitErr = fmt.Errorf("owner: %w", err)
				return false
			}
			if ownedBy[owner] {
				visit(rec)
			}
			return visitErr == nil
		})
		if visitErr != nil {
			return 0, visitErr
		}
	}
	if pt.rel.TargetTable != "" && len(doomed) > 0 {
		doomed, err = u.visiblePairs(ctx, pt, doomed)
		if err != nil {
			return 0, err
		}
	}
	if len(doomed) == 0 {
		return 0, nil
	}
	mt := u.w.mutable(pt.def)
	for _, rec := range doomed {
		mt.Delete(rec)
	}
	return int64(len(doomed)), nil
}

// visiblePairs are those of recs whose target is visible.
func (u *updater) visiblePairs(ctx context.Context, pt *pairs, recs []*record) ([]*record, error) {
	stored := make([]core.Key, len(recs))
	for i, rec := range recs {
		stored[i] = pt.targetOf(rec)
	}
	targets, err := normalizedKeys(pt.rel.TargetTypes, stored)
	if err != nil {
		return nil, fmt.Errorf("targets: %w", err)
	}
	visible, err := filter.VisibleTargets(ctx, pt.field, targets, u.lookup, u.scope)
	if err != nil {
		return nil, err
	}
	shown, err := keyTexts(pt.rel.TargetTypes, visible)
	if err != nil {
		return nil, err
	}
	out := recs[:0]
	for i, rec := range recs {
		text, err := core.KeyTextOf(pt.rel.TargetTypes, targets[i])
		if err != nil {
			return nil, err
		}
		if shown[text] {
			out = append(out, rec)
		}
	}
	return out, nil
}

// pair pairs every owner with every value it is not paired with: owners
// times values pairs at most. A new pair past the table's limit is
// ErrRowLimit.
func (u *updater) pair(pt *pairs, owners, values []core.Key) (int64, error) {
	if len(values) == 0 {
		return 0, nil
	}
	t := u.w.mutable(pt.def)
	var n int64
	for _, owner := range owners {
		for _, value := range values {
			rec, err := pt.record(owner, value)
			if err != nil {
				return 0, err
			}
			_, held := t.Get(rec)
			if held {
				continue
			}
			err = u.w.put(pt.def, t, rec)
			if err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

// record is the pair of owner and target, each column in its storage form:
// a pair table a resource keys by a column of its own, which no pair
// writes, cannot hold it (tableDef.record refuses its NULL key).
func (pt *pairs) record(owner, target core.Key) (*record, error) {
	row := make(Row, len(pt.sources)+len(pt.targets)+1)
	for i, c := range pt.rel.SourceColumns {
		row[c] = owner[i]
	}
	for i, c := range pt.rel.TargetColumns {
		row[c] = target[i]
	}
	if pt.tenantAt >= 0 {
		row[pt.rel.TenantColumn] = pt.tenant
	}
	rec, err := pt.def.record(row)
	if err != nil {
		return nil, fmt.Errorf("pair table %q: %w", pt.def.name, err)
	}
	return rec, nil
}

// heldFor reports whether rec is the update's tenant's pair, or the table
// holds no tenant.
func (pt *pairs) heldFor(rec *record) (bool, error) {
	if pt.tenantAt < 0 {
		return true, nil
	}
	v := rec.value(pt.tenantAt)
	if isNull(v) {
		return false, nil
	}
	c, err := core.CompareCanonical(pt.def.columns[pt.tenantAt].typ, v, pt.tenant)
	if err != nil {
		return false, fmt.Errorf("tenant column %s: %w", pt.rel.TenantColumn, err)
	}
	return c == 0, nil
}

func (pt *pairs) sourceOf(rec *record) core.Key { return valuesAt(rec, pt.sources) }

func (pt *pairs) targetOf(rec *record) core.Key { return valuesAt(rec, pt.targets) }

func valuesAt(rec *record, at []int) core.Key {
	k := make(core.Key, len(at))
	for i, pos := range at {
		k[i] = rec.value(pos)
	}
	return k
}

// keyTexts is the set of keys' texts (core.KeyTextOf), each typed by types.
func keyTexts(types []core.TypeInfo, keys []core.Key) (map[string]bool, error) {
	texts := make(map[string]bool, len(keys))
	for i, k := range keys {
		text, err := core.KeyTextOf(types, k)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		texts[text] = true
	}
	return texts, nil
}

// normalizedKeys are keys, each normalized to types (core.NormalizeKey).
func normalizedKeys(types []core.TypeInfo, keys []core.Key) ([]core.Key, error) {
	out := make([]core.Key, len(keys))
	for i, k := range keys {
		normalized, err := core.NormalizeKey(types, k)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		out[i] = normalized
	}
	return out, nil
}
