package parse

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// ColumnExpr is a computed column's SQL expression for the dialect of the
// provider the resource is built on: trusted application SQL, never request
// text, naming the resource's own columns qualified by its table.
// NewResource calls it once, with its provider's dialect, so one
// declaration serves whichever provider an application's configuration
// opens; it returns an error, such as filter.ErrUnsupportedDialect, for a
// dialect it cannot serve, and NewResource fails.
type ColumnExpr func(filter.Dialect) (string, error)

// SQL is a ColumnExpr that every dialect reads alike.
func SQL(sql string) ColumnExpr {
	return func(filter.Dialect) (string, error) { return sql, nil }
}

// ReadExpr reads column through expr instead of the table's column: a value
// the table holds in no column, such as a count of related rows, a
// many-to-many's keys as an array, or a nullable column read through
// coalesce. Every statement selects the expression as `(sql) AS column`, so
// a list's page and an update's RETURNING scan it into the column's field,
// and a field tagged filter, sort, facet or search compiles those against
// the same expression, as does a server policy naming the column
// (filter.ResourceSchema.PolicyField), hidden or not filterable.
// NewResource resolves the expression once, for its provider's dialect; a
// dialect the expression refuses fails NewResource, naming the table, the
// column and the dialect.
//
// A computed column must be read, and cannot be the key, the tenant or the
// deleted column, be patched, stamped or incremented, hold document paths
// or element members, or correlate a relation. A relation to the resource
// filters by it through the same expression. The resource's schemas
// (FilterSchema, SortSchema, SearchSchema) and its schema document name the
// column; the filter schema carries the resolved expressions
// (filter.ResourceSchema.Computed), ListQuery's statements included.
func ReadExpr(column string, expr ColumnExpr) ResourceOption {
	return func(c *resourceConfig) { c.readExprs = append(c.readExprs, columnExpr{column, expr}) }
}

type columnExpr struct {
	column string
	expr   ColumnExpr
}

// computedColumns are a resource's ReadExpr declarations.
type computedColumns struct {
	table   string
	columns []columnExpr
}

// newComputedColumns validates the ReadExpr declarations: distinct columns
// of T, each read, none an identity column, each with an expression. Nil
// declares none.
func newComputedColumns(table string, cfg resourceConfig, columns map[string]reflect.Type, read map[string]bool) (*computedColumns, error) {
	if len(cfg.readExprs) == 0 {
		return nil, nil
	}
	c := &computedColumns{table: table}
	for _, d := range cfg.readExprs {
		switch {
		case columns[d.column] == nil:
			return nil, c.errorf(d.column, "is not a column of the resource")
		case c.has(d.column):
			return nil, c.errorf(d.column, "is declared twice")
		case d.expr == nil:
			return nil, c.errorf(d.column, "has no expression")
		case slices.Contains(cfg.pk, d.column), d.column == cfg.tenant, d.column == cfg.deleted:
			return nil, c.errorf(d.column, "is the key, tenant or deleted column, which a table holds itself")
		case read != nil && !read[d.column]:
			return nil, c.errorf(d.column, "is not read (ReadColumns)")
		}
		c.columns = append(c.columns, d)
	}
	return c, nil
}

func (c *computedColumns) errorf(column, format string, args ...any) error {
	return fmt.Errorf("parse: %s: computed column %q: %w", c.table, column, fmt.Errorf(format, args...))
}

// writeError refuses an update that writes a computed column: there is no
// stored column to write.
func (c *computedColumns) writeError(column, how string) error {
	return c.errorf(column, "%s; an update writes the table's own columns", how)
}

// computedCorrelation refuses a relation, declared by Relate or a relation
// tag, that correlates on a computed column: the related rows' subquery
// compares a stored column with it.
func computedCorrelation(table, relation, column string) error {
	return fmt.Errorf("parse: %s: relation %q correlates on computed column %q", table, relation, column)
}

func (c *computedColumns) has(column string) bool {
	if c == nil {
		return false
	}
	return slices.ContainsFunc(c.columns, func(d columnExpr) bool { return d.column == column })
}

// checkDocuments refuses document paths and element members on a computed
// column: they read a stored document.
func (c *computedColumns) checkDocuments(fs *filter.ResourceSchema, ss *filter.SortSchema) error {
	if c == nil {
		return nil
	}
	for _, f := range fs.Fields {
		if f.Relation != nil {
			continue
		}
		if f.Path != nil {
			if column := f.RowColumn(); c.has(column) {
				return c.errorf(column, "declares document paths, which read a stored document")
			}
			continue
		}
		if column := f.RowColumn(); c.has(column) && f.Elements != nil {
			return c.errorf(column, "declares element members, which read a stored document")
		}
	}
	if ss == nil {
		return nil
	}
	for _, f := range ss.Fields {
		if f.Path == nil {
			continue
		}
		if column := f.RowColumn(); c.has(column) {
			return c.errorf(column, "declares document sort paths, which read a stored document")
		}
	}
	return nil
}

// resolve is the declarations as the filter schema carries them
// (filter.ResourceSchema.Computed): each expression resolved once for d,
// the dialect of the provider the resource is built on, its refusal naming
// the table, the column and the dialect. Nil declares none.
func (c *computedColumns) resolve(d filter.Dialect) ([]filter.ComputedColumn, error) {
	if c == nil {
		return nil, nil
	}
	out := make([]filter.ComputedColumn, len(c.columns))
	for i, col := range c.columns {
		sql, err := col.expr(d)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: computed column %q for dialect %s: %w", c.table, col.column, d.Name(), err)
		}
		if strings.TrimSpace(sql) == "" {
			return nil, fmt.Errorf("parse: %s: computed column %q for dialect %s: no expression", c.table, col.column, d.Name())
		}
		out[i] = filter.ComputedColumn{Column: col.column, SQL: sql}
	}
	return out, nil
}

// checkComputedPatches refuses a patch path, in either patch schema, that
// writes a computed column.
func (r *resourceCore) checkComputedPatches() error {
	if r.readExprs == nil {
		return nil
	}
	for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
		if schema == nil {
			continue
		}
		for path, ps := range schema.Paths() {
			if column := ps.SQLColumn; r.computed(column) {
				return r.readExprs.writeError(column, "is patched at "+path)
			}
		}
	}
	return nil
}

// computed reports whether column is read through an expression.
func (r *resourceCore) computed(column string) bool { return r.readExprs.has(column) }
