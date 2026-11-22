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
// search compiles those against the same expression, as does a server policy
// naming the column (filter.ResourceSchema.PolicyField), hidden or not
// filterable. A resource declared once serves every dialect its expressions
// serve; a dialect an expression refuses fails every statement the resource
// builds for it — Columns, BuildList, BuildUpdate and BuildUpdateBatch —
// naming the column and the dialect.
//
// A computed column must be read, and cannot be the key, the tenant or the
// deleted column, be patched, stamped or incremented, hold document paths
// or element members, or correlate a relation. A relation to the resource
// filters by it through the expression of the dialect its owner's build is
// for, and a dialect the expression refuses fails that build too. The
// resource's schemas (FilterSchema, SortSchema, SearchSchema) and its
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

// field reports whether f is a computed column's own filter field. Fields
// are matched by their row column, not their SQL, which a column mapper
// need not write as table.column.
func (c *computedColumns) field(f *filter.FieldSchema) bool {
	return f.Relation == nil && f.Path == nil && c.has(f.RowColumn())
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
func (c *computedColumns) view(d filter.Dialect, base *dialectView) (*dialectView, error) {
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
	v = c.newView(exprs, base)
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

// newView copies base's schemas with each computed column's fields compiled
// against exprs, its parenthesized SQL in declaration order.
func (c *computedColumns) newView(exprs []string, base *dialectView) *dialectView {
	fs, ss, search := base.filter, base.sort, base.search
	v := &dialectView{
		exprs:     make(map[string]string, len(c.columns)),
		fields:    make(map[*filter.FieldSchema]*filter.FieldSchema, len(c.columns)),
		sorts:     make(map[*filter.SortFieldSchema]*filter.SortFieldSchema, len(c.columns)),
		relations: base.relations,
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
		column := f.RowColumn()
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
	// A server policy names a computed column by its column or, without the
	// filter capability, its external name (PolicyField): those entries
	// compile against the expression as the request fields do. A request
	// field reached by its column, and a field reached by both names, stay
	// one field. The copy keeps the names PolicyField refuses.
	if fs.PolicyFields != nil {
		filters.PolicyFields = maps.Clone(fs.PolicyFields)
		for name, f := range fs.PolicyFields {
			if moved, ok := v.fields[f]; ok {
				filters.PolicyFields[name] = moved
				continue
			}
			if !c.field(f) {
				continue
			}
			column := f.RowColumn()
			computed := *f
			computed.SQLExpr = v.exprs[column]
			filters.PolicyFields[name] = &computed
			v.fields[f] = &computed
		}
	}
	v.filter = &filters

	if ss != nil {
		sorts := *ss
		sorts.Fields = maps.Clone(ss.Fields)
		for name, f := range ss.Fields {
			column := f.RowColumn()
			if !c.has(column) || f.Path != nil {
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
			if column := f.RowColumn(); c.has(column) {
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
// onto the view's (filterAST, sortResult), and a relation's onto its
// target's view for the same dialect (withTargets, filterAST).
type dialectView struct {
	filter *filter.ResourceSchema
	sort   *filter.SortSchema
	search *filter.SearchSchema
	// exprs are the computed columns' parenthesized SQL, by column.
	exprs map[string]string
	// fields and sorts map each field of the dialect-neutral schemas the
	// view compiles differently to the view's: a computed column's, and a
	// relation's that withTargets points at its target's view.
	fields map[*filter.FieldSchema]*filter.FieldSchema
	sorts  map[*filter.SortFieldSchema]*filter.SortFieldSchema
	// relations are the resource's relation targets (Relate), by field.
	relations map[string]RelatedResource
}

// dialectView is r's schemas compiled for d: its own when it has no computed
// columns, else the view of d's expressions.
func (r *Resource[T]) dialectView(d filter.Dialect) (*dialectView, error) {
	if d == nil {
		return nil, fmt.Errorf("parse: %s: no dialect", r.table)
	}
	base := &dialectView{filter: r.filterSchema, sort: r.sortSchema, search: r.searchSchema, relations: r.relations}
	if r.readExprs == nil {
		return base, nil
	}
	return r.readExprs.view(d, base)
}

func (v *dialectView) listQuery(d filter.Dialect, cfg *core.ParserConfig) *filter.ListQuery {
	return newListQuery(v.filter, v.sort, v.search, d, cfg)
}

// withTargets is v with each named relation's field filtering its target
// through the target's view for d, as a relation compiles in one of v's
// builds: the target's policies (Scope.Related), relation facets, includes
// and membership changes. Names of other fields are ignored. A dialect a
// target's expression refuses is an error.
func (v *dialectView) withTargets(d filter.Dialect, names ...string) (*dialectView, error) {
	var out *dialectView
	for _, name := range slices.Compact(slices.Sorted(slices.Values(names))) {
		target, ok := v.relations[name]
		field := v.filter.Fields[name]
		if !ok || field == nil {
			continue
		}
		tv, err := target.dialectView(d)
		if err != nil {
			return nil, err
		}
		if field.Related == tv.filter {
			continue
		}
		if out == nil {
			filters := *v.filter
			filters.Fields = maps.Clone(v.filter.Fields)
			copied := *v
			copied.filter, copied.fields = &filters, maps.Clone(v.fields)
			if copied.fields == nil {
				copied.fields = make(map[*filter.FieldSchema]*filter.FieldSchema, len(names))
			}
			out = &copied
		}
		related := *field
		related.Related = tv.filter
		out.filter.Fields[name] = &related
		out.fields[field] = &related
	}
	if out == nil {
		return v, nil
	}
	return out, nil
}

// filterAST is ast compiled for d: its computed fields replaced by the
// view's, and each relation's children by its target's view for d, at any
// depth. A dialect a target's expression refuses is an error.
func (v *dialectView) filterAST(d filter.Dialect, ast *filter.FilterAST) (*filter.FilterAST, error) {
	if ast == nil || (len(v.fields) == 0 && len(v.relations) == 0) {
		return ast, nil
	}
	root, err := v.node(d, ast.Root)
	if err != nil || root == ast.Root {
		return ast, err
	}
	return &filter.FilterAST{Root: root}, nil
}

// node is n, copied where it or a descendant compiles differently in the
// view. It walks the filter's finite tree, never the relation graph, so
// relations in a cycle end where the filter does.
func (v *dialectView) node(d filter.Dialect, n *filter.FilterNode) (*filter.FilterNode, error) {
	if n == nil {
		return nil, nil
	}
	field, moved := v.fields[n.Field]
	if !moved {
		field = n.Field
	}
	// A relation's children filter its target with the target's fields, so
	// they compile in the target's view.
	within := v
	if field != nil && field.Relation != nil {
		target, ok := v.relations[field.ExternalName]
		if !ok || field.Related == nil {
			within = nil
		} else {
			tv, err := target.dialectView(d)
			if err != nil {
				return nil, err
			}
			if field.Related != tv.filter {
				related := *field
				related.Related = tv.filter
				field = &related
			}
			within = tv
		}
	}
	children, changed := n.Children, false
	if within != nil {
		var err error
		if children, changed, err = within.nodes(d, n.Children); err != nil {
			return nil, err
		}
	}
	if field == n.Field && !changed {
		return n, nil
	}
	out := *n
	out.Field, out.Children = field, children
	return &out, nil
}

// nodes is ns compiled in v, and whether any node changed.
func (v *dialectView) nodes(d filter.Dialect, ns []*filter.FilterNode) ([]*filter.FilterNode, bool, error) {
	var out []*filter.FilterNode
	for i, n := range ns {
		c, err := v.node(d, n)
		if err != nil {
			return nil, false, err
		}
		if c != n && out == nil {
			out = slices.Clone(ns)
		}
		if out != nil {
			out[i] = c
		}
	}
	if out == nil {
		return ns, false, nil
	}
	return out, true, nil
}

// policyFields names every field pc constrains, at any depth.
func policyFields(pc *filter.PolicyConstraints) []string {
	if pc == nil {
		return nil
	}
	var out []string
	for _, c := range pc.Constraints {
		if c.Field != "" {
			out = append(out, c.Field)
		}
	}
	for _, group := range slices.Concat(pc.And, pc.Or) {
		out = append(out, policyFields(group)...)
	}
	return out
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
		for path, ps := range schema.Paths() {
			if column := ps.SQLColumn; r.computed(column) {
				return r.readExprs.writeError(column, "is patched at "+path)
			}
		}
	}
	return nil
}

// computed reports whether column is read through an expression.
func (r *Resource[T]) computed(column string) bool { return r.readExprs.has(column) }
