package parse

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// RowUpdate narrows one primary key to its policy and applies its column patch.
type RowUpdate struct {
	Key    any
	Patch  *patch.ParsedPatch
	Policy *filter.PolicyConstraints
}

// UpdateBatchLimits bounds total rows and parameters per statement. Zero uses
// 500 rows and the dialect's conservative parameter budget; negative refuses.
type UpdateBatchLimits struct {
	MaxRows, MaxParameters int
}

// ErrUpdateBatchTargets means the locked preimages do not match every named key.
var ErrUpdateBatchTargets = errors.New("parse: update batch targets did not match")

// UpdateBatchPlan is executed in one caller-owned transaction. Read and check
// every chunk's Current keys before executing any Owner statement.
type UpdateBatchPlan struct {
	Chunks []UpdateBatchChunk
}

// UpdateBatchChunk contains one bounded preimage read and its optional update.
// Owner returns changed rows; absent changed keys retain their Current images.
type UpdateBatchChunk struct {
	Keys    []any
	Current Statement
	Owner   *Statement

	keyType core.TypeInfo
}

// CheckCurrent verifies the model-typed primary keys returned by Current.
func (c UpdateBatchChunk) CheckCurrent(keys []any) error {
	if len(keys) != len(c.Keys) || len(c.Keys) == 0 {
		return ErrUpdateBatchTargets
	}
	switch c.keyType.Type {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
	default:
		return ErrUpdateBatchTargets
	}
	wanted := make(map[any]bool, len(c.Keys))
	for _, key := range c.Keys {
		normalized, err := core.NormalizeScalar(c.keyType.Type, c.keyType.NumberKind, key)
		if err != nil || wanted[normalized] {
			return ErrUpdateBatchTargets
		}
		wanted[normalized] = true
	}
	seen := make(map[any]bool, len(keys))
	for _, key := range keys {
		normalized, err := core.NormalizeScalar(c.keyType.Type, c.keyType.NumberKind, key)
		if err != nil || !wanted[normalized] || seen[normalized] {
			return ErrUpdateBatchTargets
		}
		seen[normalized] = true
	}
	return nil
}

// BuildUpdateBatch compiles distinct column patches through the same typed
// expressions, tenant/live floors and options as BuildUpdate. Relation changes
// and identity-column writes refuse before a plan is returned.
func (r *Resource[T]) BuildUpdateBatch(d Dialect, rows []RowUpdate, scope Scope, limits UpdateBatchLimits, opts ...UpdateOption) (*UpdateBatchPlan, error) {
	if scope.Governance != nil || scope.IncludeDeleted {
		return nil, fmt.Errorf("%s: %w", r.table, ErrUpdateScope)
	}
	if r.tenant != "" && scope.Tenant == "" {
		return nil, fmt.Errorf("%s: %w", r.table, ErrTenantRequired)
	}
	limits, err := updateBatchLimits(d, limits)
	if err != nil {
		return nil, err
	}
	if len(rows) > limits.MaxRows {
		return nil, fmt.Errorf("%s: update batch exceeds %d rows", r.table, limits.MaxRows)
	}
	config, options, err := r.updateOptions(opts)
	if err != nil {
		return nil, err
	}
	keyType := core.TypeOf(r.tableColumns[r.pk])
	switch keyType.Type {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
	default:
		return nil, fmt.Errorf("%s: update batch key type %s is unsupported", r.table, keyType.Type)
	}
	ordered := make([]orderedRowUpdate, len(rows))
	seen := make(map[any]bool, len(rows))
	for i, row := range rows {
		key, err := core.NormalizeScalar(keyType.Type, keyType.NumberKind, row.Key)
		if err != nil {
			return nil, fmt.Errorf("%s: update batch key: %w", r.table, err)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s: duplicate update batch key", r.table)
		}
		seen[key], row.Key = true, key
		ordered[i].row = row
		switch value := key.(type) {
		case string:
			ordered[i].text = value
		case int64:
			ordered[i].signed = value
		case uint64:
			ordered[i].unsigned = value
		default:
			return nil, fmt.Errorf("%s: unsupported normalized update key", r.table)
		}
	}
	slices.SortFunc(ordered, func(a, b orderedRowUpdate) int {
		return cmp.Or(cmp.Compare(a.signed, b.signed), cmp.Compare(a.unsigned, b.unsigned), strings.Compare(a.text, b.text))
	})
	plan := &UpdateBatchPlan{Chunks: []UpdateBatchChunk{}}
	var compiled []compiledRowUpdate
	var offset int
	for _, orderedRow := range ordered {
		row := orderedRow.row
		item, err := r.compileRowUpdate(d, row, scope, options, offset)
		if err != nil {
			return nil, err
		}
		if offset+len(item.args) > limits.MaxParameters {
			if len(compiled) == 0 {
				return nil, fmt.Errorf("%s: one update exceeds the parameter budget", r.table)
			}
			chunk, err := r.renderUpdateBatch(d, compiled, scope, keyType, config.skipUnchanged)
			if err != nil {
				return nil, err
			}
			plan.Chunks = append(plan.Chunks, chunk)
			compiled, offset = nil, 0
			item, err = r.compileRowUpdate(d, row, scope, options, 0)
			if err != nil {
				return nil, err
			}
			if len(item.args) > limits.MaxParameters {
				return nil, fmt.Errorf("%s: one update exceeds the parameter budget", r.table)
			}
		}
		compiled = append(compiled, item)
		offset += len(item.args)
	}
	if len(compiled) > 0 {
		chunk, err := r.renderUpdateBatch(d, compiled, scope, keyType, config.skipUnchanged)
		if err != nil {
			return nil, err
		}
		plan.Chunks = append(plan.Chunks, chunk)
	}
	return plan, nil
}

type orderedRowUpdate struct {
	row      RowUpdate
	text     string
	signed   int64
	unsigned uint64
}

type compiledRowUpdate struct {
	row         RowUpdate
	assignments []patch.ColumnAssignment
	where       string
	change      string
	args        []any
}

func (r *Resource[T]) compileRowUpdate(d Dialect, row RowUpdate, scope Scope, options []patch.PatchSQLOption, offset int) (compiledRowUpdate, error) {
	set, err := patch.BuildPatchSQL(row.Patch, d, append(slices.Clone(options), patch.WithStartParam(offset))...)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	if len(set.RelationOps) != 0 {
		return compiledRowUpdate{}, fmt.Errorf("%s: update batch relation mutations are unsupported", r.table)
	}
	for _, assignment := range set.Assignments {
		if assignment.Column == r.pk || assignment.Column == r.tenant {
			return compiledRowUpdate{}, fmt.Errorf("%s: update batch cannot change identity column %s", r.table, assignment.Column)
		}
	}
	scope.Policy = updateRowPolicy(scope.Policy, row.Policy)
	where, args, err := r.updateWhere(d, ByID(row.Key), scope, offset+set.ParamCount)
	if err != nil {
		return compiledRowUpdate{}, err
	}
	return compiledRowUpdate{row: row, assignments: set.Assignments, where: where, change: set.ChangePredicate, args: append(set.Args, args...)}, nil
}

func (r *Resource[T]) renderUpdateBatch(d Dialect, rows []compiledRowUpdate, scope Scope, keyType core.TypeInfo, skipUnchanged bool) (UpdateBatchChunk, error) {
	chunk := UpdateBatchChunk{Keys: make([]any, len(rows)), keyType: keyType}
	var currentWhere, changedWhere []string
	var args []any
	assignments := map[string][]string{}
	for i, row := range rows {
		chunk.Keys[i] = row.row.Key
		currentScope := scope
		currentScope.Policy = updateRowPolicy(scope.Policy, row.row.Policy)
		where, currentArgs, err := r.updateWhere(d, ByID(row.row.Key), currentScope, len(chunk.Current.Args))
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
	order := r.table + "." + r.pk
	if keyType.Type == core.FieldTypeString {
		if d.Name() == "sqlite" {
			order += " COLLATE BINARY"
		} else {
			order += ` COLLATE "C"`
		}
	}
	columns := strings.Join(r.Columns(d), ", ")
	chunk.Current.SQL = "SELECT " + columns + " FROM " + r.table + " WHERE " + strings.Join(currentWhere, " OR ") + " ORDER BY " + order + d.RowLock()
	if len(assignments) != 0 {
		clauses := make([]string, 0, len(assignments))
		for _, column := range slices.Sorted(maps.Keys(assignments)) {
			clauses = append(clauses, column+" = CASE "+strings.Join(assignments[column], " ")+" ELSE "+column+" END")
		}
		chunk.Owner = &Statement{SQL: "UPDATE " + r.table + " SET " + strings.Join(clauses, ", ") + " WHERE " + strings.Join(changedWhere, " OR ") + " RETURNING " + columns, Args: args}
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
	if d.Name() != "sqlite" && d.Name() != "postgres" {
		return limits, fmt.Errorf("parse: update batch dialect %q is unsupported", d.Name())
	}
	if limits.MaxRows < 0 || limits.MaxParameters < 0 {
		return limits, errors.New("parse: update batch limits must not be negative")
	}
	if limits.MaxRows == 0 {
		limits.MaxRows = 500
	}
	if limits.MaxParameters == 0 {
		switch d.Name() {
		case "sqlite":
			limits.MaxParameters = 999
		case "postgres":
			limits.MaxParameters = 65535
		default:
			return limits, fmt.Errorf("parse: update batch dialect %q is unsupported", d.Name())
		}
	}
	return limits, nil
}
