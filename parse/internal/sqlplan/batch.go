package sqlplan

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// Batch is a composed update batch compiled for a dialect: its rows in
// chunks, in key order, each chunk's statements bound by the parameters
// one statement binds.
type Batch struct {
	Chunks []Chunk
}

// Chunk is one bounded preimage read and its optional update. Owner
// returns the changed rows; a key Owner leaves keeps its Current image.
type Chunk struct {
	Keys    []core.Key
	Current Statement
	Owner   *Statement
}

// CompileBatch compiles req's rows for d through the same typed
// expressions, floors and options as CompileUpdate, in chunks whose
// statements bind at most req.MaxParameters parameters (the dialect's
// conservative budget when zero). A row that changes relations, or writes
// a member of a JSON document the stored row must decide, is refused.
func CompileBatch(d filter.Dialect, req backend.UpdateBatchRequest) (*Batch, error) {
	sql, err := sqlOf(req.Resource)
	if err != nil {
		return nil, err
	}
	maxParameters := req.MaxParameters
	if maxParameters == 0 {
		maxParameters = d.MaxParams()
	}
	options := renderOptions(req.Options)
	batch := &Batch{Chunks: []Chunk{}}
	var compiled []compiledRow
	var offset int
	for _, row := range req.Rows {
		update, err := req.Options.Compose(row.Patch)
		if err != nil {
			return nil, err
		}
		err = req.Resource.CheckBatchRow(row.Key, update)
		if err != nil {
			return nil, err
		}
		item, err := compileRow(d, req, row, update, options, offset)
		if err != nil {
			return nil, err
		}
		if offset+len(item.args) > maxParameters {
			if len(compiled) == 0 {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", req.Resource.Table)
			}
			chunk, err := renderChunk(d, req, sql, compiled)
			if err != nil {
				return nil, err
			}
			batch.Chunks = append(batch.Chunks, chunk)
			compiled, offset = nil, 0
			item, err = compileRow(d, req, row, update, options, 0)
			if err != nil {
				return nil, err
			}
			if len(item.args) > maxParameters {
				return nil, fmt.Errorf("parse: %s: one update exceeds the parameter budget", req.Resource.Table)
			}
		}
		compiled = append(compiled, item)
		offset += len(item.args)
	}
	if len(compiled) > 0 {
		chunk, err := renderChunk(d, req, sql, compiled)
		if err != nil {
			return nil, err
		}
		batch.Chunks = append(batch.Chunks, chunk)
	}
	return batch, nil
}

type compiledRow struct {
	row         backend.BatchRow
	assignments []patch.ColumnAssignment
	where       string
	change      string
	args        []any
}

// compileRow renders row's update, which CheckBatchRow admitted, with its
// placeholders after offset ones.
func compileRow(d filter.Dialect, req backend.UpdateBatchRequest, row backend.BatchRow, update *patch.ComposedPatch, options []patch.PatchSQLOption, offset int) (compiledRow, error) {
	set, err := update.SQL(d, append(slices.Clone(options), patch.WithStartParam(offset))...)
	if err != nil {
		return compiledRow{}, err
	}
	where, args, err := compileWhere(d, req.Resource, row.Where, req.Relations, offset+set.ParamCount)
	if err != nil {
		return compiledRow{}, err
	}
	compiled := compiledRow{row: row, assignments: set.Assignments, where: where, change: set.ChangePredicate, args: append(set.Args, args...)}
	return compiled, nil
}

func renderChunk(d filter.Dialect, req backend.UpdateBatchRequest, sql *backend.SQLResource, rows []compiledRow) (Chunk, error) {
	res := req.Resource
	chunk := Chunk{Keys: make([]core.Key, len(rows))}
	var currentWhere, changedWhere []string
	var args []any
	assignments := map[string][]string{}
	for i, row := range rows {
		chunk.Keys[i] = row.row.Key
		where, currentArgs, err := compileWhere(d, res, row.row.Where, req.Relations, len(chunk.Current.Args))
		if err != nil {
			return Chunk{}, err
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
		if req.Options.SkipUnchanged {
			guard += " AND (" + row.change + ")"
		}
		changedWhere = append(changedWhere, "("+guard+")")
		for _, assignment := range row.assignments {
			assignments[assignment.Column] = append(assignments[assignment.Column], "WHEN ("+row.where+") THEN "+assignment.Expression)
		}
	}
	table := d.QuoteIdent(res.Table)
	chunk.Current.SQL = "SELECT " + sql.Projection + " FROM " + table + " WHERE " + strings.Join(currentWhere, " OR ") + " ORDER BY " + keyOrder(d, res) + d.RowLock()
	if len(assignments) != 0 {
		clauses := make([]string, 0, len(assignments))
		for _, column := range slices.Sorted(maps.Keys(assignments)) {
			quoted := d.QuoteIdent(column)
			clauses = append(clauses, quoted+" = CASE "+strings.Join(assignments[column], " ")+" ELSE "+quoted+" END")
		}
		chunk.Owner = &Statement{SQL: "UPDATE " + table + " SET " + strings.Join(clauses, ", ") + " WHERE " + strings.Join(changedWhere, " OR ") + " RETURNING " + sql.Projection, Args: args}
	}
	return chunk, nil
}
