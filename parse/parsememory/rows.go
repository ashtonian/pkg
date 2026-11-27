package parsememory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// rows are records served to a sink over a resource's Columns
// (backend.Rows), each value assigned to its destination as database/sql
// assigns a driver's (sql.ConvertAssign). A timestamp, held as its
// canonical text, is handed over as the time.Time it names, as a driver
// hands over a timestamp column's, so a time.Time, a *time.Time and a
// scanner of one (sql.NullTime) all read it.
type rows struct {
	columns   []string
	positions []int
	// stamps reports, by destination, a timestamp column.
	stamps  []bool
	records []*record
	at      int
}

// newRows serves records of def's table over columns.
func newRows(def *tableDef, columns []string, records []*record) (*rows, error) {
	positions := make([]int, len(columns))
	stamps := make([]bool, len(columns))
	for i, c := range columns {
		at, ok := def.index[c]
		if !ok {
			return nil, fmt.Errorf("parsememory: table %q has no column %q", def.name, c)
		}
		positions[i] = at
		stamps[i] = def.columns[at].typ.Type == core.FieldTypeTimestamp
	}
	r := &rows{columns: columns, positions: positions, stamps: stamps, records: records}
	return r, nil
}

func (r *rows) Next() bool {
	if r.at >= len(r.records) {
		return false
	}
	r.at++
	return true
}

func (r *rows) Scan(dest ...any) error {
	if r.at == 0 || r.at > len(r.records) {
		return errors.New("parsememory: Scan called without a row")
	}
	if len(dest) != len(r.positions) {
		return fmt.Errorf("parsememory: %d destinations for the %d columns", len(dest), len(r.positions))
	}
	rec := r.records[r.at-1]
	for i, d := range dest {
		v := rec.value(r.positions[i])
		text, isText := v.(string)
		if r.stamps[i] && isText {
			at, err := time.Parse(core.TimestampLayout, text)
			if err != nil {
				return fmt.Errorf("column %s: %w", r.columns[i], err)
			}
			v = at
		}
		err := sql.ConvertAssign(driver.ScanContext{}, d, v)
		if err != nil {
			return fmt.Errorf("column %s: %w", r.columns[i], err)
		}
	}
	return nil
}

func (r *rows) Err() error { return nil }

// Documents is nil: the store holds documents as JSON text, which the
// resource's row contract decodes as it decodes SQLite's.
func (r *rows) Documents() []string { return nil }

// columnRow is rec, a row of def's table, as a filter reads it
// (filter.ColumnRow): by column, bare or qualified by the table.
func columnRow(def *tableDef, rec *record) filter.ColumnRow {
	return func(column string) (any, bool) {
		name, _ := strings.CutPrefix(column, def.name+".")
		at, ok := def.index[name]
		if !ok {
			return nil, false
		}
		return rec.value(at), true
	}
}

// bareRow is rec by bare column, as a lookup condition reads it
// (filter.MatchLookupRow).
func bareRow(def *tableDef, rec *record) func(string) (any, bool) {
	return func(name string) (any, bool) {
		at, ok := def.index[name]
		if !ok {
			return nil, false
		}
		return rec.value(at), true
	}
}

// evaluate reports whether ast selects rec, a row of def's table: True, as
// a WHERE clause admits a row.
func evaluate(ast *filter.FilterAST, def *tableDef, rec *record) (bool, error) {
	if ast == nil || ast.Root == nil {
		return true, nil
	}
	truth, err := filter.Evaluate(ast.Root, columnRow(def, rec))
	if err != nil {
		return false, err
	}
	return truth == filter.True, nil
}

// errRawSQL is a filter holding a policy constraint of raw SQL
// (filter.PolicyConstraint.RawSQL), which only a SQL database evaluates.
func errRawSQL(table string) error {
	unsupported := &parse.UnsupportedError{Provider: Name, Capability: "raw SQL policy constraints"}
	return fmt.Errorf("parse: %s: %w", table, unsupported)
}

// checkEvaluable refuses a filter the store cannot evaluate: one holding
// raw SQL, which filter.Evaluate would take as unknown.
func checkEvaluable(table string, n *filter.FilterNode) error {
	if n == nil {
		return nil
	}
	if n.Op == filter.OpRawSQL {
		return errRawSQL(table)
	}
	for _, c := range n.Children {
		err := checkEvaluable(table, c)
		if err != nil {
			return err
		}
	}
	return nil
}

// noTenant is the tenant a governed list's row without one binds its
// tenant-scoped relations to: SQL compares their tenant columns with the
// row's NULL, which no pair matches, so a lookup bound to noTenant selects
// no row.
type noTenant struct{}

// lookup answers the lookups of relation and membership predicates,
// includes and relation facets (filter.RelationLookup,
// filter.IncludeLookup) from the tables of one view.
type lookup struct {
	catalog *catalog
	view    view
}

// selected are the rows of l.Table that l.Where and l.Filter select.
func (lk lookup) selected(ctx context.Context, l *filter.Lookup) (*tableDef, []*record, error) {
	def, ok := lk.catalog.table(l.Table)
	if !ok {
		return nil, nil, fmt.Errorf("parsememory: lookup of %q: no resource declares the table", l.Table)
	}
	var ast *filter.FilterAST
	if l.Filter != nil {
		ast = l.Filter.AST()
	}
	if ast != nil {
		err := checkEvaluable(l.Table, ast.Root)
		if err != nil {
			return nil, nil, err
		}
	}
	t := lk.view.rows(l.Table)
	if t == nil || selectsNone(l.Where) {
		return def, nil, nil
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
		ok, err = filter.MatchLookupRow(l.Where, bareRow(def, rec))
		if err == nil && ok {
			ok, err = evaluate(ast, def, rec)
		}
		if err != nil {
			return false
		}
		if ok {
			out = append(out, rec)
		}
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	return def, out, nil
}

// checkEvery is how many rows a scan reads between checks of its context.
const checkEvery = 1024

// selectsNone reports a condition bound to noTenant under AND, which no row
// satisfies.
func selectsNone(cond *filter.LookupCond) bool {
	if cond == nil {
		return false
	}
	if cond.Op == filter.LookupOpEq {
		_, none := cond.Value.(noTenant)
		return none
	}
	return slices.ContainsFunc(cond.And, selectsNone)
}

func (lk lookup) LookupValues(ctx context.Context, l *filter.Lookup) ([]any, error) {
	def, recs, err := lk.selected(ctx, l)
	if err != nil {
		return nil, err
	}
	columns := projected(l)
	out := make([]any, len(recs))
	for i, rec := range recs {
		out[i], err = project(def, rec, columns)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (lk lookup) LookupFilteredValues(ctx context.Context, l *filter.Lookup) ([]any, error) {
	return lk.LookupValues(ctx, l)
}

func (lk lookup) LookupPairs(ctx context.Context, l *filter.Lookup, owner []string) (iter.Seq2[filter.IncludePair, error], error) {
	def, recs, err := lk.selected(ctx, l)
	if err != nil {
		return nil, err
	}
	columns := projected(l)
	pairs := func(yield func(filter.IncludePair, error) bool) {
		for _, rec := range recs {
			o, err := project(def, rec, owner)
			if err != nil {
				yield(filter.IncludePair{}, err)
				return
			}
			target, err := project(def, rec, columns)
			if err != nil {
				yield(filter.IncludePair{}, err)
				return
			}
			if !yield(filter.IncludePair{Owner: o, Target: target}, nil) {
				return
			}
		}
	}
	return pairs, nil
}

// projected are the columns l projects.
func projected(l *filter.Lookup) []string {
	if l.ProjectKey != nil {
		return l.ProjectKey
	}
	columns := []string{l.Project}
	return columns
}

// project is rec's value of its one column, or a core.Key of a value per
// column for several, as a lookup answers a projection.
func project(def *tableDef, rec *record, columns []string) (any, error) {
	key := make(core.Key, len(columns))
	for i, c := range columns {
		at, ok := def.index[c]
		if !ok {
			return nil, fmt.Errorf("parsememory: table %q has no column %q", def.name, c)
		}
		key[i] = rec.value(at)
	}
	if len(key) == 1 {
		return key[0], nil
	}
	return key, nil
}

var _ backend.Rows = (*rows)(nil)

var _ filter.IncludeLookup = lookup{}

var _ filter.RelationLookup = lookup{}
