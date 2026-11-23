package parse

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// RowUpdate narrows one row's key, a value per key column in key order, to
// its policy and applies its column patch.
type RowUpdate struct {
	Key    Key
	Patch  *patch.ParsedPatch
	Policy *filter.PolicyConstraints
}

// UpdateBatchLimits bounds total rows and parameters per statement. Zero uses
// 500 rows and the dialect's conservative parameter budget; negative refuses.
type UpdateBatchLimits struct {
	MaxRows, MaxParameters int
}

// ErrUpdateBatchTargets means the locked preimages do not match every named key.
var ErrUpdateBatchTargets = errors.New("update batch targets did not match")

// UpdateBatchPlan is executed in one caller-owned transaction. Read and check
// every chunk's Current keys before executing any Owner statement.
type UpdateBatchPlan struct {
	Chunks []UpdateBatchChunk

	dialect Dialect
}

// Dialect is the dialect the plan's statements are written for.
func (p *UpdateBatchPlan) Dialect() Dialect { return p.dialect }

// UpdateBatchChunk contains one bounded preimage read and its optional update.
// Owner returns changed rows; absent changed keys retain their Current images.
type UpdateBatchChunk struct {
	Keys    []Key
	Current Statement
	Owner   *Statement

	key keyColumns
}

// CheckCurrent verifies the keys of the rows Current returned (Key): each
// is one of the chunk's Keys, and each of those is returned once.
func (c UpdateBatchChunk) CheckCurrent(keys []Key) error {
	if len(keys) != len(c.Keys) || len(c.Keys) == 0 || c.key.batchType() != nil {
		return ErrUpdateBatchTargets
	}
	wanted := make(map[any]bool, len(c.Keys))
	for _, key := range c.Keys {
		id, err := c.key.identity(key)
		if err != nil || wanted[id] {
			return ErrUpdateBatchTargets
		}
		wanted[id] = true
	}
	seen := make(map[any]bool, len(keys))
	for _, key := range keys {
		id, err := c.key.identity(key)
		if err != nil || !wanted[id] || seen[id] {
			return ErrUpdateBatchTargets
		}
		seen[id] = true
	}
	return nil
}

// BuildUpdateBatch compiles distinct column patches through the same typed
// expressions, tenant/live floors and options as BuildUpdate. A row's patch
// the resource did not parse is ErrForeign, and a row key the key column
// cannot hold ErrKeyInvalid; relation changes, identity-column
// writes and a dialect a computed column's expression refuses fail before a
// plan is returned.
func (r *Resource[T]) BuildUpdateBatch(d Dialect, rows []RowUpdate, scope Scope, limits UpdateBatchLimits, opts ...UpdateOption) (*UpdateBatchPlan, error) {
	exprs, err := r.computedSQL(d)
	if err != nil {
		return nil, err
	}
	if scope.Governance != nil || scope.IncludeDeleted {
		return nil, fmt.Errorf("parse: %s: %w", r.table, ErrUpdateScope)
	}
	tenant, err := r.bindTenant(scope.Tenant)
	if err != nil {
		return nil, err
	}
	common := updateScope{tenant: tenant, policy: scope.Policy, related: scope.Related}
	limits, err = updateBatchLimits(d, limits)
	if err != nil {
		return nil, err
	}
	if len(rows) > limits.MaxRows {
		return nil, fmt.Errorf("parse: %s: update batch exceeds %d rows", r.table, limits.MaxRows)
	}
	config, options, err := r.updateOptions(opts)
	if err != nil {
		return nil, err
	}
	key := r.keyColumns()
	if err := key.batchType(); err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	ordered := make([]RowUpdate, len(rows))
	seen := make(map[any]bool, len(rows))
	for i, row := range rows {
		if err := r.ownsPatch(row.Patch, fmt.Sprintf("update batch row %d patch", i)); err != nil {
			return nil, err
		}
		normalized, err := key.normalize(row.Key)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		id, err := key.identity(normalized)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: %w", r.table, i, err)
		}
		if seen[id] {
			return nil, fmt.Errorf("parse: %s: update batch row %d key: duplicate key", r.table, i)
		}
		seen[id], row.Key = true, normalized
		ordered[i] = row
	}
	// Rows lock in key order, as Current's ORDER BY reads them, so two
	// batches over the same rows cannot each hold a lock the other waits on.
	slices.SortFunc(ordered, func(a, b RowUpdate) int { return key.compare(a.Key, b.Key) })
	plan := &UpdateBatchPlan{Chunks: []UpdateBatchChunk{}, dialect: d}
	var compiled []compiledRowUpdate
	var offset int
	for _, row := range ordered {
		item, err := r.compileRowUpdate(d, row, common, options, offset)
		if err != nil {
			return nil, err
		}
		if offset+len(item.args) > limits.MaxParameters {
			if len(compiled) == 0 {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", r.table)
			}
			chunk, err := r.renderUpdateBatch(d, exprs, compiled, common, key, config.skipUnchanged)
			if err != nil {
				return nil, err
			}
			plan.Chunks = append(plan.Chunks, chunk)
			compiled, offset = nil, 0
			item, err = r.compileRowUpdate(d, row, common, options, 0)
			if err != nil {
				return nil, err
			}
			if len(item.args) > limits.MaxParameters {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", r.table)
			}
		}
		compiled = append(compiled, item)
		offset += len(item.args)
	}
	if len(compiled) > 0 {
		chunk, err := r.renderUpdateBatch(d, exprs, compiled, common, key, config.skipUnchanged)
		if err != nil {
			return nil, err
		}
		plan.Chunks = append(plan.Chunks, chunk)
	}
	return plan, nil
}

type compiledRowUpdate struct {
	row         RowUpdate
	assignments []patch.ColumnAssignment
	where       string
	change      string
	args        []any
}

func (r *Resource[T]) compileRowUpdate(d Dialect, row RowUpdate, scope updateScope, options []patch.PatchSQLOption, offset int) (compiledRowUpdate, error) {
	set, err := patch.BuildPatchSQL(row.Patch, d, append(slices.Clone(options), patch.WithStartParam(offset))...)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	if len(set.RelationOps) != 0 {
		return compiledRowUpdate{}, fmt.Errorf("parse: %s: update batch relation mutations are unsupported", r.table)
	}
	if len(set.Preconditions) != 0 {
		// A batch locks and checks its rows by key alone; which operation a
		// row's document fails is BuildUpdate's Conflict to report.
		return compiledRowUpdate{}, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: patch.ECodeOpNotSQLCompilable.String(),
			Message: fmt.Sprintf("row %v: a batch update does not compile an operation below a JSON document field that the stored row must decide; update the row with BuildUpdate", row.Key),
			Path:    set.Preconditions[0].Path,
		}
	}
	if err := r.checkAssignments(set.Assignments, "update batch"); err != nil {
		return compiledRowUpdate{}, err
	}
	scope.policy = updateRowPolicy(scope.policy, row.Policy)
	where, args, err := r.updateWhere(d, ByKey(row.Key), scope, offset+set.ParamCount)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	return compiledRowUpdate{row: row, assignments: set.Assignments, where: where, change: set.ChangePredicate, args: append(set.Args, args...)}, nil
}

func (r *Resource[T]) renderUpdateBatch(d Dialect, exprs map[string]string, rows []compiledRowUpdate, scope updateScope, key keyColumns, skipUnchanged bool) (UpdateBatchChunk, error) {
	chunk := UpdateBatchChunk{Keys: make([]Key, len(rows)), key: key}
	var currentWhere, changedWhere []string
	var args []any
	assignments := map[string][]string{}
	for i, row := range rows {
		chunk.Keys[i] = row.row.Key
		currentScope := scope
		currentScope.policy = updateRowPolicy(scope.policy, row.row.Policy)
		where, currentArgs, err := r.updateWhere(d, ByKey(row.row.Key), currentScope, len(chunk.Current.Args))
		if err != nil {
			return UpdateBatchChunk{}, err
		}
		currentWhere = append(currentWhere, "("+where+")")
		chunk.Current.Args = append(chunk.Current.Args, currentArgs...)
		args = append(args, row.args...)
		if len(row.assignments) == 0 {
			// Keep every bound predicate parameter typed even for a no-op item.
			changedWhere = append(changedWhere, "(("+row.where+") AND FALSE)")
			continue
		}
		guard := "(" + row.where + ")"
		if skipUnchanged {
			guard += " AND (" + row.change + ")"
		}
		changedWhere = append(changedWhere, "("+guard+")")
		for _, assignment := range row.assignments {
			assignments[assignment.Column] = append(assignments[assignment.Column], "WHEN ("+row.where+") THEN "+assignment.Expression)
		}
	}
	order := make([]string, len(key.names))
	for i, column := range key.names {
		order[i] = d.QuoteIdent(r.table) + "." + d.QuoteIdent(column)
		if key.types[i].Type == core.FieldTypeString {
			order[i] = d.CodePointOrder(order[i])
		}
	}
	columns := strings.Join(r.columns(d, exprs), ", ")
	chunk.Current.SQL = "SELECT " + columns + " FROM " + d.QuoteIdent(r.table) + " WHERE " + strings.Join(currentWhere, " OR ") + " ORDER BY " + strings.Join(order, ", ") + d.RowLock()
	if len(assignments) != 0 {
		clauses := make([]string, 0, len(assignments))
		for _, column := range slices.Sorted(maps.Keys(assignments)) {
			quoted := d.QuoteIdent(column)
			clauses = append(clauses, quoted+" = CASE "+strings.Join(assignments[column], " ")+" ELSE "+quoted+" END")
		}
		chunk.Owner = &Statement{SQL: "UPDATE " + d.QuoteIdent(r.table) + " SET " + strings.Join(clauses, ", ") + " WHERE " + strings.Join(changedWhere, " OR ") + " RETURNING " + columns, Args: args}
	}
	return chunk, nil
}

func updateRowPolicy(common, row *filter.PolicyConstraints) *filter.PolicyConstraints {
	if common == nil {
		return row
	}
	if row == nil {
		return common
	}
	return &filter.PolicyConstraints{And: []*filter.PolicyConstraints{common, row}}
}

func updateBatchLimits(d Dialect, limits UpdateBatchLimits) (UpdateBatchLimits, error) {
	if limits.MaxRows < 0 || limits.MaxParameters < 0 {
		return limits, errors.New("parse: update batch limits must not be negative")
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = 500
	}
	if limits.MaxParameters == 0 {
		limits.MaxParameters = d.MaxParams()
	}
	return limits, nil
}
