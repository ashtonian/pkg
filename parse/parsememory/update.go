package parsememory

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// Update runs the update under the writer (executor.write), deciding what
// the SQL providers' statements decide (internal/sqlplan.CompileUpdate),
// in the order they decide it:
//
//  1. the patch is composed (backend.UpdateOptions.Compose) and refused if
//     it assigns a key or the tenant (backend.Resource.CheckUpdate), before
//     the writer is waited for;
//  2. the targets are selected: by key, each found in O(log n), or by a
//     scan of the table for a filter, held to req.Where with its relations
//     and memberships resolved once under req.Relations;
//  3. the composed patch is applied to each target's stored values
//     (patch.ComposedPatch.Apply), in key order: the first target that
//     does not hold what a member operation needs fails the update with
//     that operation's error, before anything is written;
//  4. with SkipUnchanged, a target the patch's columns leave as they are is
//     not written;
//  5. the rows written go to the sink as changed and the others as
//     unchanged (backend.UpdateSink), then the relation changes are made
//     for the keys of both, as patch.RenderRelationOp's statements make
//     them (relate).
//
// A failure names its part as the SQL providers name their statements:
// owner for the targets and their writes, relations for the membership
// changes. A target a member operation does not apply to fails with the
// operation's own error, as the SQL providers' conflict statement does.
func (e executor) Update(ctx context.Context, req backend.UpdateRequest, sink backend.UpdateSink) error {
	res := req.Resource
	update, err := req.Options.Compose(req.Patch)
	if err != nil {
		return err
	}
	err = res.CheckUpdate(update)
	if err != nil {
		return err
	}
	return e.write(ctx, res.Table, func(w *write) error {
		u, err := e.updater(w, res, req.Relations)
		if err != nil {
			return err
		}
		where, err := u.resolve(ctx, req.Where, "owner")
		if err != nil {
			return err
		}
		targets, err := u.targets(ctx, where, req.Keys)
		if err != nil {
			return parse.StatementFailed(res.Table, "owner", err)
		}
		changed, unchanged, err := u.apply(update, targets, req.Options.SkipUnchanged)
		if err != nil {
			return err
		}
		err = u.scan(sink.Changed, changed)
		if err == nil {
			err = u.scan(sink.Unchanged, unchanged)
		}
		if err != nil {
			return parse.StatementFailed(res.Table, "owner", err)
		}
		err = sink.Split()
		ops := update.RelationOps()
		if err != nil || len(ops) == 0 {
			return err
		}
		changedKeys, unchangedKeys, err := sink.Keys()
		if err != nil {
			return err
		}
		owners := slices.Concat(changedKeys, unchangedKeys)
		for _, op := range ops {
			n, err := u.relate(ctx, op, owners)
			if err != nil {
				return parse.StatementFailed(res.Table, "relations", err)
			}
			sink.RelationRows(n)
		}
		return nil
	})
}

// UpdateBatch runs the batch under the writer, as the SQL providers run
// its statements (internal/sqlplan.CompileBatch): every row's patch is
// composed and admitted (backend.Resource.CheckBatchRow) before the writer
// is waited for; every row's target is then found by its key, in O(log n),
// and held to the row's filter, and the targets go to the sink as one part
// (backend.BatchSink.Targets), which refuses the batch unless every key
// found its row (Check) before anything is written. Each row's patch is
// then applied to its target, a row the patch's columns leave as they are
// skipped with SkipUnchanged, and the rows written go to the sink. A batch
// of no rows reads nothing.
func (e executor) UpdateBatch(ctx context.Context, req backend.UpdateBatchRequest, sink backend.BatchSink) error {
	if len(req.Rows) == 0 {
		return nil
	}
	res := req.Resource
	updates := make([]*patch.ComposedPatch, len(req.Rows))
	keys := make([]core.Key, len(req.Rows))
	for i, row := range req.Rows {
		update, err := req.Options.Compose(row.Patch)
		if err != nil {
			return err
		}
		err = res.CheckBatchRow(row.Key, update)
		if err != nil {
			return err
		}
		updates[i], keys[i] = update, row.Key
	}
	return e.write(ctx, res.Table, func(w *write) error {
		u, err := e.updater(w, res, req.Relations)
		if err != nil {
			return err
		}
		found := make([]*record, len(req.Rows))
		var targets []*record
		for i, row := range req.Rows {
			where, err := u.resolve(ctx, row.Where, "current")
			if err != nil {
				return err
			}
			rec, err := u.target(where, row.Key)
			if err != nil {
				return parse.StatementFailed(res.Table, "current", err)
			}
			if rec != nil {
				found[i] = rec
				targets = append(targets, rec)
			}
		}
		err = u.scan(func(rows backend.Rows) error { return sink.Targets(keys, rows) }, targets)
		if err != nil {
			return parse.StatementFailed(res.Table, "current", err)
		}
		err = sink.Check()
		if err != nil {
			return err
		}
		var changed []*record
		for i, rec := range found {
			if rec == nil {
				continue
			}
			written, _, err := u.apply(updates[i], []*record{rec}, req.Options.SkipUnchanged)
			if err != nil {
				return err
			}
			changed = append(changed, written...)
		}
		err = u.scan(func(rows backend.Rows) error { return sink.Changed(0, rows) }, changed)
		if err != nil {
			return parse.StatementFailed(res.Table, "owner", err)
		}
		return sink.Split()
	})
}

// updater is one update or batch of a resource's table, in a write: its
// reads, relation lookups included, see what the write has written.
type updater struct {
	db     *Database
	w      *write
	res    *backend.Resource
	def    *tableDef
	reader *rowReader
	scope  filter.RelationScope
	lookup lookup
}

func (e executor) updater(w *write, res *backend.Resource, scope filter.RelationScope) (*updater, error) {
	def, ok := e.db.catalog.table(res.Table)
	if !ok {
		return nil, fmt.Errorf("parsememory: table %q: no resource declares it", res.Table)
	}
	u := &updater{db: e.db, w: w, res: res, def: def, reader: newRowReader(def), scope: scope, lookup: lookup{catalog: e.db.catalog, view: w}}
	return u, nil
}

// resolve is where, a filter over the resource's table, with its relations
// and memberships resolved under the update's scope, so each row is
// evaluated without a lookup; a lookup's failure fails the part named
// step. A filter holding raw SQL is refused.
func (u *updater) resolve(ctx context.Context, where *filter.FilterAST, step string) (*filter.FilterAST, error) {
	if where == nil {
		return nil, nil
	}
	err := checkEvaluable(u.res.Table, where.Root)
	if err != nil {
		return nil, err
	}
	resolved, err := filter.ResolveMemberships(ctx, filter.NewParsedFilterFor(u.res.Schema, where), u.lookup, filter.ResolveRelationScope(u.scope))
	if err != nil {
		return nil, parse.StatementFailed(u.res.Table, step, err)
	}
	return resolved.AST(), nil
}

// targets are the rows where selects, in key order: those of keys, each
// looked up by key, or, for no keys, those a scan of the table finds.
func (u *updater) targets(ctx context.Context, where *filter.FilterAST, keys []core.Key) ([]*record, error) {
	t := u.w.rows(u.def.name)
	if t == nil {
		return nil, nil
	}
	if len(keys) > 0 {
		var out []*record
		for _, k := range keys {
			rec, err := u.target(where, k)
			if err != nil {
				return nil, err
			}
			if rec != nil {
				out = append(out, rec)
			}
		}
		key, types := u.def.key, u.def.keyTypes()
		cmp := func(a, b *record) int { return compareKeys(key, types, a, b) }
		slices.SortFunc(out, cmp)
		return slices.CompactFunc(out, func(a, b *record) bool { return cmp(a, b) == 0 }), nil
	}
	var out []*record
	var err error
	n := 0
	t.Scan(func(rec *record) bool {
		n++
		if n%checkEvery == 0 {
			err = ctx.Err()
			if err != nil {
				return false
			}
		}
		var ok bool
		ok, err = u.reader.evaluate(where, rec)
		if err != nil {
			return false
		}
		if ok {
			out = append(out, rec)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// target is the row of key that where selects, nil for none.
func (u *updater) target(where *filter.FilterAST, key core.Key) (*record, error) {
	t := u.w.rows(u.def.name)
	if t == nil {
		return nil, nil
	}
	probe, err := u.def.probe(key)
	if err != nil {
		return nil, err
	}
	rec, ok := t.Get(probe)
	if !ok {
		return nil, nil
	}
	ok, err = u.reader.evaluate(where, rec)
	if err != nil || !ok {
		return nil, err
	}
	return rec, nil
}

// apply applies update to each target's stored values and writes the rows
// it changes, all or none: the targets it wrote, as it left them, and
// those it left, which are every target when it assigns no column. A
// target the patch does not apply to is the patch's error, returned as it
// is (a *core.ParseError); a stored value the patch cannot read fails the
// owner part.
func (u *updater) apply(update *patch.ComposedPatch, targets []*record, skipUnchanged bool) (changed, unchanged []*record, err error) {
	if !update.Assigns() || len(targets) == 0 {
		return nil, targets, nil
	}
	columns := update.Columns()
	at := make([]int, len(columns))
	for i, c := range columns {
		pos, ok := u.def.index[c]
		if !ok {
			return nil, nil, parse.StatementFailed(u.res.Table, "owner", fmt.Errorf("parsememory: table %q: no resource declares the column %s the update assigns", u.def.name, c))
		}
		at[i] = pos
	}
	for _, rec := range targets {
		write, err := update.Apply(u.reader.bareOf(rec))
		if err != nil {
			_, refused := errors.AsType[*core.ParseError](err)
			if refused {
				return nil, nil, err
			}
			return nil, nil, parse.StatementFailed(u.res.Table, "owner", err)
		}
		if skipUnchanged && !write.Changed {
			unchanged = append(unchanged, rec)
			continue
		}
		next, err := u.def.rewritten(rec, at, write.Values)
		if err != nil {
			return nil, nil, parse.StatementFailed(u.res.Table, "owner", err)
		}
		changed = append(changed, next)
	}
	if len(changed) > 0 {
		t := u.w.mutable(u.def)
		for _, rec := range changed {
			t.Set(rec)
		}
	}
	return changed, unchanged, nil
}

// rewritten is rec with values at the positions at, each a value in its
// column's storage form that the column can hold (checkHolds).
func (def *tableDef) rewritten(rec *record, at []int, values []any) (*record, error) {
	next := &record{values: make([]any, len(def.columns))}
	copy(next.values, rec.values)
	for i, pos := range at {
		c := def.columns[pos]
		err := checkHolds(c.typ, values[i])
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c.name, err)
		}
		next.values[pos] = values[i]
	}
	return next, nil
}

// scan hands records to read, a sink's scan, over the resource's Columns.
func (u *updater) scan(read func(backend.Rows) error, records []*record) error {
	r, err := newRows(u.def, u.res.Columns, records)
	if err != nil {
		return err
	}
	return read(r)
}
