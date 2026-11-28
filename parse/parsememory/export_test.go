//go:build integration

package parsememory

import (
	"context"
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// DeclareTable declares table, unless it is declared, keyed by key: the
// corpus's fixture tables no resource reads. It reports whether it
// declared the table.
func (d *Database) DeclareTable(table string, columns []backend.Column, key []string) (bool, error) {
	_, declared := d.catalog.table(table)
	if declared {
		return false, nil
	}
	return true, d.catalog.declare(&backend.Resource{Table: table, Key: key, TableColumns: columns})
}

// Select is column, as text, of each row of schema's table that f
// selects, as a list of the table selects its rows: one pass, its
// relations and memberships resolved first, held to scope.
func (d *Database) Select(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, scope filter.RelationScope, column string) ([]string, error) {
	q := query{schema: schema, filter: f.AST(), limit: d.store.maxRows, scope: scope}
	l, err := executor{db: d}.lister(schema.Name, q, true)
	if err != nil {
		return nil, err
	}
	return l.texts(ctx, column)
}

// Walk is the id column, as text, of the rows of schema's table f selects,
// every row for a nil f, in the walk order of keys past token (from the
// first for a nil token), as a list reads them before its page is
// presented: a reverse token's rows walk backward.
func (d *Database) Walk(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, keys []filter.CursorKey, token *filter.PageToken) ([]string, error) {
	q := query{schema: schema, keys: keys, cursor: token, limit: d.store.maxRows}
	if f != nil {
		q.filter = f.AST()
	}
	l, err := executor{db: d}.lister(schema.Name, q, true)
	if err != nil {
		return nil, err
	}
	return l.texts(ctx, "id")
}

// texts is column, as text, of each row a pass of l pages, in walk order.
func (l *lister) texts(ctx context.Context, column string) ([]string, error) {
	at, ok := l.def.index[column]
	if !ok {
		return nil, fmt.Errorf("parsememory: table %q has no column %q", l.table, column)
	}
	res, err := l.pass(ctx)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(res.page))
	for i, rec := range res.page {
		texts[i], err = core.CanonicalText(l.def.columns[at].typ, rec.value(at))
		if err != nil {
			return nil, err
		}
	}
	return texts, nil
}

// FacetsOf counts reqs over the rows of schema's table f selects, every row
// for a nil f, as a list's facets count them, relations held to scope.
func (d *Database) FacetsOf(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, reqs []filter.FacetRequest, scope filter.RelationScope) ([]filter.FacetResult, error) {
	q := query{schema: schema, facets: reqs, scope: scope}
	if f != nil {
		q.filter = f.AST()
	}
	l, err := executor{db: d}.lister(schema.Name, q, false)
	if err != nil {
		return nil, err
	}
	res, err := l.pass(ctx)
	if err != nil {
		return nil, err
	}
	return l.facetResults(ctx, res)
}

// RowsOf is every row of table, in key order: a value per column by name,
// as a scan of the column reads it (nil for NULL, a timestamp as its
// time.Time).
func (d *Database) RowsOf(table string) ([]map[string]any, error) {
	return executor{db: d}.rowsOf(table)
}

// RowsOf is Database.RowsOf in the transaction, which sees its own writes.
func (t *Txn) RowsOf(table string) ([]map[string]any, error) {
	return executor{db: t.db, txn: t}.rowsOf(table)
}

func (e executor) rowsOf(table string) ([]map[string]any, error) {
	v, err := e.view()
	if err != nil {
		return nil, err
	}
	def, err := e.db.declared(table)
	if err != nil {
		return nil, fmt.Errorf("parsememory: reading %q: %w", table, err)
	}
	var records []*record
	t := v.rows(table)
	if t != nil {
		records = t.Items()
	}
	names := make([]string, len(def.columns))
	for i, c := range def.columns {
		names[i] = c.name
	}
	r, err := newRows(def, names, records)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(records))
	for r.Next() {
		values := make([]any, len(names))
		dest := make([]any, len(names))
		for i := range values {
			dest[i] = &values[i]
		}
		err = r.Scan(dest...)
		if err != nil {
			return nil, fmt.Errorf("parsememory: reading %q: %w", table, err)
		}
		row := make(map[string]any, len(names))
		for i, name := range names {
			row[name] = values[i]
		}
		out = append(out, row)
	}
	return out, nil
}

// IncludeOf is the targets field's relation pairs owners with, as a list
// includes them (filter.ResolveInclude) over the database's pair tables.
func (d *Database) IncludeOf(ctx context.Context, field *filter.FieldSchema, owners []core.Key, opts ...filter.IncludeQueryOption) (filter.IncludeRows, error) {
	v, err := executor{db: d}.view()
	if err != nil {
		return filter.IncludeRows{}, err
	}
	return filter.ResolveInclude(ctx, field, owners, lookup{catalog: d.catalog, view: v}, opts...)
}
