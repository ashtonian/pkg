package parse

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// ColumnExpr is a computed column's SQL expression for one dialect: trusted
// application SQL, never request text, naming the resource's own columns
// qualified by its table. It returns an error, such as
// filter.ErrUnsupportedDialect, for a dialect it cannot serve. Every build
// calls it, so it must be cheap and return the same SQL for the same
// dialect.
type ColumnExpr func(filter.Dialect) (string, error)

// SQL is a ColumnExpr that every dialect reads alike.
func SQL(sql string) ColumnExpr {
	return func(filter.Dialect) (string, error) { return sql, nil }
}

// ReadExpr reads column through expr instead of the table's column: a value
// the table holds in no column, such as a count of related rows, a
// many-to-many's keys as an array, or a nullable column read through
// coalesce. A statement built for dialect d selects expr(d) as
// `(sql) AS column`, so a list's page, an update's RETURNING and parsepgx
// scan it into the column's field, and a field tagged filter, sort, facet or
// search compiles those against the same expression. A resource declared once
// serves every dialect its expressions serve; a dialect an expression refuses
// fails every statement the resource builds for it — Columns, BuildList,
// BuildUpdate and BuildUpdateBatch — naming the column and the dialect.
//
// A computed column must be read, and cannot be the key, the tenant or the
// deleted column, be patched, stamped or incremented, hold document paths
// or element members, or correlate a relation. A relation to the resource cannot filter by it.
// The resource's schemas (FilterSchema, SortSchema, SearchSchema) and its
// schema document stay dialect-neutral and name the column; ListQuery
// compiles those schemas as they are, so list a resource with computed
// columns through BuildList.
func ReadExpr(column string, expr ColumnExpr) ResourceOption {
	return func(c *resourceConfig) { c.readExprs = append(c.readExprs, columnExpr{column, expr}) }
}

type columnExpr struct {
	column string
	expr   ColumnExpr
}

// maxDialectViews bounds the views one resource caches. A process serves a
// few dialects; a ColumnExpr whose SQL varies between calls builds views
// that are used once and dropped instead of growing the cache.
const maxDialectViews = 16

// computedColumns are a resource's ReadExpr declarations and the views of
// its schemas compiled for each dialect's expressions.
type computedColumns struct {
	table   string
	columns []columnExpr

	mu    sync.RWMutex
	views map[string]*dialectView
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
		case d.column == cfg.pk, d.column == cfg.tenant, d.column == cfg.deleted:
			return nil, c.errorf(d.column, "is the key, tenant or deleted column, which a table holds itself")
		case read != nil && !read[d.column]:
			return nil, c.errorf(d.column, "is not read (ReadColumns)")
		}
		c.columns = append(c.columns, d)
	}
	return c, nil
}

func (c *computedColumns) errorf(column, format string, args ...any) error {
	return fmt.Errorf("parse: %s: computed column %q: %s", c.table, column, fmt.Sprintf(format, args...))
}

// writeError refuses an update that writes a computed column: there is no
// stored column to write.
func (c *computedColumns) writeError(column, how string) error {
	return c.errorf(column, "%s; an update writes the table's own columns", how)
}

func (c *computedColumns) has(column string) bool {
	if c == nil {
		return false
	}
	return slices.ContainsFunc(c.columns, func(d columnExpr) bool { return d.column == column })
}

// column is the computed column a schema expression names, table-qualified
// as the schema generator writes a stored column.
func (c *computedColumns) column(sqlExpr string) (string, bool) {
	if c == nil {
		return "", false
	}
	column, ok := strings.CutPrefix(sqlExpr, c.table+".")
	return column, ok && c.has(column)
}

// field reports whether f is a computed column's own filter field.
func (c *computedColumns) field(f *filter.FieldSchema) bool {
	_, ok := c.column(f.SQLExpr)
	return ok && f.Relation == nil && f.Path == nil
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
			if column, ok := c.column(f.Path.Column); ok {
				return c.errorf(column, "declares document paths, which read a stored document")
			}
			continue
		}
		if column, ok := c.column(f.SQLExpr); ok && f.Elements != nil {
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
		if column, ok := c.column(f.Path.Column); ok {
			return c.errorf(column, "declares document sort paths, which read a stored document")
		}
	}
	return nil
}

// reset drops the cached views; Relate calls it, since a view copies the
// filter schema's fields.
func (c *computedColumns) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.views = nil
	c.mu.Unlock()
}

// withoutViews is c's declarations with an empty cache, for a resource view
// whose schemas differ from c's resource's (Resource.WithPaths).
func (c *computedColumns) withoutViews() *computedColumns {
	if c == nil {
		return nil
	}
	return &computedColumns{table: c.table, columns: c.columns}
}

// view resolves every computed column's SQL for d and returns fs, ss and
// search compiled against it, built on first use.
//
// Views are keyed by the resolved SQL, not by d: a view holds nothing of the
// dialect but those expressions, and every build compiles it under its own
// dialect. Dialect values then need not be comparable; dialects that differ
// only in settings the expressions ignore, such as two PostgresDialects'
// Collation, share a view, and dialects whose expressions differ never do.
func (c *computedColumns) view(d filter.Dialect, fs *filter.ResourceSchema, ss *filter.SortSchema, search *filter.SearchSchema) (*dialectView, error) {
	exprs := make([]string, len(c.columns))
	var key strings.Builder
	for i, col := range c.columns {
		sql, err := col.expr(d)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: computed column %q for dialect %s: %w", c.table, col.column, d.Name(), err)
		}
		if strings.TrimSpace(sql) == "" {
			return nil, fmt.Errorf("parse: %s: computed column %q for dialect %s: no expression", c.table, col.column, d.Name())
		}
		exprs[i] = "(" + sql + ")"
		// Length-prefixed, so no expression's text can run into the next's.
		key.WriteString(strconv.Itoa(len(sql)))
		key.WriteByte(':')
		key.WriteString(sql)
	}
	k := key.String()

	c.mu.RLock()
	v, ok := c.views[k]
	c.mu.RUnlock()
	if ok {
		return v, nil
	}
	v = c.newView(exprs, fs, ss, search)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.views[k]; ok {
		return cached, nil
	}
	if len(c.views) < maxDialectViews {
		if c.views == nil {
			c.views = make(map[string]*dialectView)
		}
		c.views[k] = v
	}
	return v, nil
}

// newView copies fs, ss and search with each computed column's fields
// compiled against exprs, its parenthesized SQL in declaration order.
func (c *computedColumns) newView(exprs []string, fs *filter.ResourceSchema, ss *filter.SortSchema, search *filter.SearchSchema) *dialectView {
	v := &dialectView{
		exprs:  make(map[string]string, len(c.columns)),
		fields: make(map[*filter.FieldSchema]*filter.FieldSchema, len(c.columns)),
		sorts:  make(map[*filter.SortFieldSchema]*filter.SortFieldSchema, len(c.columns)),
	}
	for i, col := range c.columns {
		v.exprs[col.column] = exprs[i]
	}

	filters := *fs
	filters.Fields = maps.Clone(fs.Fields)
	for name, f := range fs.Fields {
		if !c.field(f) {
			continue
		}
		column, _ := c.column(f.SQLExpr)
		computed := *f
		computed.SQLExpr = v.exprs[column]
		if f.FacetSchema != nil {
			facet := *f.FacetSchema
			facet.SQLExpr = computed.SQLExpr
			computed.FacetSchema = &facet
		}
		filters.Fields[name] = &computed
		v.fields[f] = &computed
	}
	v.filter = &filters

	if ss != nil {
		sorts := *ss
		sorts.Fields = maps.Clone(ss.Fields)
		for name, f := range ss.Fields {
			column, ok := c.column(f.SQLExpr)
			if !ok || f.Path != nil {
				continue
			}
			// A cursor reads the key's value from the row's column, which
			// the expression's text no longer names.
			computed := *f
			computed.SQLExpr, computed.Column = v.exprs[column], column
			sorts.Fields[name] = &computed
			v.sorts[f] = &computed
		}
		v.sort = &sorts
	}

	if search != nil {
		searched := *search
		searched.Fields = slices.Clone(search.Fields)
		for i, f := range searched.Fields {
			if column, ok := c.column(f.SQLExpr); ok {
				searched.Fields[i].SQLExpr = v.exprs[column]
			}
		}
		v.search = &searched
	}
	return v
}

// dialectView is a resource's filter, sort and search schemas with its
// computed columns compiled against one dialect's expressions. Requests
// parse against the dialect-neutral schemas, so a build moves their fields
// onto the view's (filterAST, sortResult).
type dialectView struct {
	filter *filter.ResourceSchema
	sort   *filter.SortSchema
	search *filter.SearchSchema
	// exprs are the computed columns' parenthesized SQL, by column.
	exprs map[string]string
	// fields and sorts map each computed field of the dialect-neutral
	// schemas to the view's.
	fields map[*filter.FieldSchema]*filter.FieldSchema
	sorts  map[*filter.SortFieldSchema]*filter.SortFieldSchema
}

// dialectView is r's schemas compiled for d: its own when it has no computed
// columns, else the view of d's expressions.
func (r *Resource[T]) dialectView(d filter.Dialect) (*dialectView, error) {
	if d == nil {
		return nil, fmt.Errorf("parse: %s: no dialect", r.table)
	}
	if r.readExprs == nil {
		return &dialectView{filter: r.filterSchema, sort: r.sortSchema, search: r.searchSchema}, nil
	}
	return r.readExprs.view(d, r.filterSchema, r.sortSchema, r.searchSchema)
}

func (v *dialectView) listQuery(d filter.Dialect, cfg *core.ParserConfig) *filter.ListQuery {
	return newListQuery(v.filter, v.sort, v.search, d, cfg)
}

// filterAST is ast with every computed field replaced by the view's.
func (v *dialectView) filterAST(ast *filter.FilterAST) *filter.FilterAST {
	if ast == nil || len(v.fields) == 0 {
		return ast
	}
	root := v.node(ast.Root)
	if root == ast.Root {
		return ast
	}
	return &filter.FilterAST{Root: root}
}

// node is n, copied where it or a descendant names a computed field. A
// relation's children filter its target, whose fields are its own.
func (v *dialectView) node(n *filter.FilterNode) *filter.FilterNode {
	if n == nil {
		return nil
	}
	field, moved := v.fields[n.Field]
	var children []*filter.FilterNode
	if n.Field == nil || n.Field.Relation == nil {
		for i, child := range n.Children {
			if c := v.node(child); c != child {
				if children == nil {
					children = slices.Clone(n.Children)
				}
				children[i] = c
			}
		}
	}
	if !moved && children == nil {
		return n
	}
	out := *n
	if moved {
		out.Field = field
	}
	if children != nil {
		out.Children = children
	}
	return &out
}

// sortResult is sr with every computed key replaced by the view's.
func (v *dialectView) sortResult(sr *filter.SortResult) *filter.SortResult {
	if sr == nil || len(v.sorts) == 0 {
		return sr
	}
	var out *filter.SortResult
	for i, f := range sr.Fields {
		computed, ok := v.sorts[f.Schema]
		if !ok {
			continue
		}
		if out == nil {
			out = &filter.SortResult{Fields: slices.Clone(sr.Fields)}
		}
		out.Fields[i].Schema = computed
	}
	if out == nil {
		return sr
	}
	return out
}

// checkComputedPatches refuses a patch path, in either patch schema, that
// writes a computed column.
func (r *Resource[T]) checkComputedPatches() error {
	if r.readExprs == nil {
		return nil
	}
	for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
		if schema == nil {
			continue
		}
		for _, path := range slices.Sorted(maps.Keys(schema.AllowedPaths)) {
			if column := schema.AllowedPaths[path].SQLColumn; r.computed(column) {
				return r.readExprs.writeError(column, "is patched at "+path)
			}
		}
	}
	return nil
}

// computed reports whether column is read through an expression.
func (r *Resource[T]) computed(column string) bool { return r.readExprs.has(column) }

// relatedFilterSchema is the filter schema a relation to r filters r's rows
// with: r's own less its computed columns, whose expressions only r's own
// builds resolve for their dialect.
func (r *Resource[T]) relatedFilterSchema() *filter.ResourceSchema { return r.relatedSchema }
