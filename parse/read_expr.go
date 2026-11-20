package parse

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/patch"
)

// ReadExpr reads column through a SQL expression instead of the table's
// column: a value the table holds in no column, such as a count of related
// rows, a many-to-many's keys as an array, or a nullable column read through
// coalesce. Columns selects it as `(expr) AS column`, so a list's page, an
// update's RETURNING and parsepgx scan it into the column's field, and a
// field tagged filter, sort, facet or search compiles those against the same
// expression.
//
// The expression is trusted application SQL, never request text, written
// for the dialect the resource serves; it names the resource's own columns
// qualified by its table. A computed column must be read, and cannot be the
// key, the tenant or the deleted column, be patched, hold document paths or
// element members, or correlate a relation.
func ReadExpr(column, expr string) ResourceOption {
	return func(c *resourceConfig) { c.readExprs = append(c.readExprs, columnExpr{column, expr}) }
}

type columnExpr struct {
	column, expr string
}

// readExprs validates the ReadExpr declarations: distinct columns of T, each
// read, none an identity column, each with an expression.
func readExprs(table string, cfg resourceConfig, columns map[string]reflect.Type, read map[string]bool) (map[string]string, error) {
	if len(cfg.readExprs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(cfg.readExprs))
	for _, d := range cfg.readExprs {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("parse: %s: computed column %q: %s", table, d.column, fmt.Sprintf(format, args...))
		}
		switch {
		case columns[d.column] == nil:
			return nil, fail("is not a column of the resource")
		case out[d.column] != "":
			return nil, fail("is declared twice")
		case strings.TrimSpace(d.expr) == "":
			return nil, fail("has no expression")
		case d.column == cfg.pk, d.column == cfg.tenant, d.column == cfg.deleted:
			return nil, fail("is the key, tenant or deleted column, which a table holds itself")
		case read != nil && !read[d.column]:
			return nil, fail("is not read (ReadColumns)")
		}
		out[d.column] = "(" + d.expr + ")"
	}
	return out, nil
}

// computeSchemas points every schema at the computed columns' expressions:
// filters, facets, sorts and search compile against them, and a cursor reads
// a sort key's value from its column's field. A schema feature with no
// meaning on an expression is refused.
func (r *Resource[T]) computeSchemas() error {
	for column, expr := range r.readExprs {
		qualified := r.table + "." + column
		fail := func(format string, args ...any) error {
			return fmt.Errorf("parse: %s: computed column %q: %s", r.table, column, fmt.Sprintf(format, args...))
		}
		for _, f := range r.filterSchema.Fields {
			if f.Path != nil && f.Path.Column == qualified {
				return fail("declares document paths, which read a stored document")
			}
			if f.SQLExpr != qualified || f.Relation != nil {
				continue
			}
			if f.Elements != nil {
				return fail("declares element members, which read a stored document")
			}
			f.SQLExpr = expr
			if f.FacetSchema != nil {
				f.FacetSchema.SQLExpr = expr
			}
		}
		for _, f := range r.sortSchema.Fields {
			if f.Path != nil && f.Path.Column == qualified {
				return fail("declares document sort paths, which read a stored document")
			}
			if f.SQLExpr == qualified {
				f.SQLExpr, f.Column = expr, column
			}
		}
		if r.searchSchema != nil {
			for i := range r.searchSchema.Fields {
				if r.searchSchema.Fields[i].SQLExpr == qualified {
					r.searchSchema.Fields[i].SQLExpr = expr
				}
			}
		}
		for _, schema := range r.patchSchemas() {
			if schema == nil {
				continue
			}
			for path, p := range schema.AllowedPaths {
				if p.SQLColumn == column {
					return fail("is patched at %s; an update writes the table's own columns", path)
				}
			}
		}
	}
	return nil
}

func (r *Resource[T]) patchSchemas() []*patch.PatchSchema {
	return []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema}
}

// computed reports whether column is read through an expression.
func (r *Resource[T]) computed(column string) bool {
	_, ok := r.readExprs[column]
	return ok
}
