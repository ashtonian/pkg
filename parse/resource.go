package parse

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Resource is one table's request grammar and identity: its key,
// tenant and soft-delete columns, its page bounds, and the filter, sort,
// search and patch schemas T's struct tags declare. It parses list requests
// (ParseList), compiles them for a dialect under a server-decided Scope
// (BuildList) and turns fetched rows into a page (Page). It also parses
// filters and patches. Build one per type at startup; it is safe for concurrent use.
type Resource[T any] struct {
	*typedParser[T]

	table       string
	pk          string
	tenant      string
	deleted     string
	pageSize    int
	maxPageSize int
	// tenantNode holds the tenant key, read from every tenant column the
	// resource's relations reach (bindTenant).
	tenantNode *tenantNode
	// accepted are the query parameters the endpoint reads itself
	// (AcceptParams).
	accepted []string
	// read are the columns the resource reads, in field order, and
	// sqlTypes the declared database types among them (ColumnType);
	// tableColumns is every column of T, and columnFields the field holding
	// each.
	read         []string
	sqlTypes     map[string]core.SQLType
	tableColumns map[string]reflect.Type
	columnFields map[string]*core.Field
	// access is what a request may learn of each column of T (ReadColumns,
	// QueryOnlyColumns).
	access map[string]columnAccess
	// maskable are the names a list's field mask may name (maskableFields).
	maskable map[string]bool
	// readExprs are the computed columns (ReadExpr) and their per-dialect
	// views; nil without any.
	readExprs *computedColumns
	// relations are the declared relations' targets (Relate), by field.
	relations map[string]RelatedResource
}

// ResourceOption configures NewResource.
type ResourceOption func(*resourceConfig)

type resourceConfig struct {
	pk, tenant, deleted string
	pageSize, maxPage   int
	parserOpts          []core.Option
	accepted            []string
	read                []string
	queryOnly           []string
	sqlTypes            []columnType
	readExprs           []columnExpr
}

type columnType struct {
	column string
	t      core.SQLType
}

// PrimaryKey names the key column; default core.DefaultPrimaryKey ("id"). It is the final keyset
// column of every list, so pages are stable over a non-unique sort.
func PrimaryKey(column string) ResourceOption { return func(c *resourceConfig) { c.pk = column } }

// TenantColumn names the column that holds a row's tenant. Every list is then
// held to Scope.Tenant, and so are tenant-scoped relation junctions. The
// column's field is a string, a UUID or an integer; Scope.Tenant is
// normalized to it, and to every tenant column the resource's relations
// reach (Relate).
func TenantColumn(column string) ResourceOption {
	return func(c *resourceConfig) { c.tenant = column }
}

// DeletedColumn names the soft-delete timestamp. Lists exclude deleted rows
// unless Scope.IncludeDeleted.
func DeletedColumn(column string) ResourceOption {
	return func(c *resourceConfig) { c.deleted = column }
}

// PageSize sets the page size a list request gets when it names none, and the
// largest it may name; default 50 and 1000.
func PageSize(def, max int) ResourceOption {
	return func(c *resourceConfig) { c.pageSize, c.maxPage = def, max }
}

// ReadColumns limits the columns the resource reads — Columns, a list's
// page, an update's RETURNING — to these columns of T; the default is every
// column. Sensitive columns (hashes, secrets, tokens) then stay in the
// database and their fields read as zero. No response carries a value of a
// column the resource does not read, so the key, every sort key's column
// (cursors carry the keys' values) and every facet's column (buckets are
// keyed by its values) must be read, and so must the columns a relation's
// includes and facets pair. A filter, search or relation over an unread
// column returns no values, but its matches disclose them by inference, so
// it must be read too unless QueryOnlyColumns names it. A relation is held to
// its target's read set as well as its owner's. NewResource, Relate and
// WithPaths refuse a declaration that needs another column.
func ReadColumns(columns ...string) ResourceOption {
	return func(c *resourceConfig) { c.read = append(c.read, columns...) }
}

// QueryOnlyColumns names columns ReadColumns leaves out that requests may
// still filter and search by, and filter by a relation correlating on: a
// large text column searched but never listed, for example. Their values
// never appear in a response, but a request can infer them from which rows
// match, so name only columns whose values the caller may learn. A
// query-only column is still no sort key or facet, and no relation that
// includes or facets correlates on it. Each must be a column of T the
// resource does not read.
func QueryOnlyColumns(columns ...string) ResourceOption {
	return func(c *resourceConfig) { c.queryOnly = append(c.queryOnly, columns...) }
}

// ColumnType declares a column's storage role, including JSON documents and
// native SQL arrays, or a dialect read cast for network/interval values.
// NewResource validates the field representation and refuses duplicate or
// unknown declarations. Values still require their declared storage codec.
func ColumnType(column string, t core.SQLType) ResourceOption {
	return func(c *resourceConfig) { c.sqlTypes = append(c.sqlTypes, columnType{column, t}) }
}

// ParserOptions sets the parser's limits, clock, codec and schema generation
// (core.With*).
func ParserOptions(opts ...core.Option) ResourceOption {
	return func(c *resourceConfig) { c.parserOpts = append(c.parserOpts, opts...) }
}

// AcceptParams names query parameters the endpoint reads itself. ParseList
// leaves them alone; any other parameter that is neither a list parameter
// (filter.Param*) nor a filterable field is an error (E2071).
func AcceptParams(names ...string) ResourceOption {
	return func(c *resourceConfig) { c.accepted = append(c.accepted, names...) }
}

// NewResource builds the resource for T stored in table. Identifiers are
// declared here, never taken from a request: table and every column option
// must be SQL identifiers, and the columns must be T's (a `db` tag, or the
// snake-case field name).
func NewResource[T any](table string, opts ...ResourceOption) (*Resource[T], error) {
	cfg := resourceConfig{pk: core.DefaultPrimaryKey, pageSize: filter.DefaultListLimit, maxPage: filter.MaxListLimit}
	for _, opt := range opts {
		opt(&cfg)
	}
	if !core.IsIdentifier(table) {
		return nil, fmt.Errorf("parse: resource table %q is not a SQL identifier", table)
	}
	if cfg.pageSize <= 0 || cfg.maxPage < cfg.pageSize {
		return nil, fmt.Errorf("parse: %s: page size %d must be positive and at most the maximum %d", table, cfg.pageSize, cfg.maxPage)
	}
	parserOpts := append(cfg.parserOpts, func(pc *core.ParserConfig) {
		types := make(map[string]core.SQLType, len(cfg.sqlTypes))
		for _, declaration := range cfg.sqlTypes {
			types[declaration.column] = declaration.t
		}
		pc.GeneratorOpts = append(pc.GeneratorOpts, core.WithSQLTableName(table), core.WithColumnTypes(types))
	})
	p, err := newTypedParser[T](parserOpts...)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	if p.cfg.MaxListBodySize < 0 {
		return nil, fmt.Errorf("parse: %s: list body limit %d must not be negative", table, p.cfg.MaxListBodySize)
	}
	walk, err := p.gen.StructFields(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	order, fieldsByColumn, err := resourceColumns(p.gen, walk)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	columns := make(map[string]reflect.Type, len(order))
	for name, f := range fieldsByColumn {
		columns[name] = f.Type
	}
	for _, c := range []struct {
		option, column string
		required       bool
	}{{"key", cfg.pk, true}, {"tenant", cfg.tenant, false}, {"deleted", cfg.deleted, false}} {
		if c.column == "" && !c.required {
			continue
		}
		if _, ok := columns[c.column]; !ok {
			return nil, fmt.Errorf("parse: %s: %s column %q is not a column of %s", table, c.option, c.column, reflect.TypeFor[T]())
		}
	}
	p.filterSchema.SystemFields = systemFields(table, cfg, columns)
	p.filterSchema.PrimaryKey = cfg.pk
	var tenant *tenantColumn
	if cfg.tenant != "" {
		if tenant, err = newTenantColumn(table, cfg.tenant, columns[cfg.tenant]); err != nil {
			return nil, fmt.Errorf("parse: %s: %w", table, err)
		}
	}

	r := &Resource[T]{
		typedParser:  p,
		table:        table,
		pk:           cfg.pk,
		tenant:       cfg.tenant,
		deleted:      cfg.deleted,
		pageSize:     cfg.pageSize,
		maxPageSize:  cfg.maxPage,
		tenantNode:   newTenantNode(table, tenant),
		accepted:     slices.Clone(cfg.accepted),
		tableColumns: columns,
		columnFields: fieldsByColumn,
		relations:    map[string]RelatedResource{},
	}
	read, err := readColumns(table, cfg, columns)
	if err != nil {
		return nil, err
	}
	if r.readExprs, err = newComputedColumns(table, cfg, columns, read); err != nil {
		return nil, err
	}
	if err := r.readExprs.checkDocuments(p.filterSchema, p.sortSchema); err != nil {
		return nil, err
	}
	if err := r.checkComputedPatches(); err != nil {
		return nil, err
	}
	for _, name := range order {
		if read == nil || read[name] {
			r.read = append(r.read, name)
		}
	}
	queryOnly, err := queryOnlyColumns(table, cfg, columns, read)
	if err != nil {
		return nil, err
	}
	r.access = columnAccesses(columns, read, queryOnly)
	r.maskable = maskableFields(walk, fieldsByColumn, r.access)
	if r.sqlTypes, err = sqlTypes(table, cfg, columns); err != nil {
		return nil, err
	}
	// A value type owns its SQL representation unless an explicit
	// declaration stores the value as a JSON document instead.
	for name, typ := range columns {
		implied, ok := core.ImpliedSQLType(core.TypeOf(typ))
		if !ok {
			continue
		}
		if _, declared := r.sqlTypes[name]; declared {
			continue
		}
		if r.sqlTypes == nil {
			r.sqlTypes = make(map[string]core.SQLType)
		}
		r.sqlTypes[name] = implied
	}

	if err := r.checkUnreadColumns(p.filterSchema.Fields, sortKeys(p.sortSchema), searchFields(p.searchSchema)); err != nil {
		return nil, err
	}
	for _, f := range p.filterSchema.Fields {
		if f.Relation != nil && f.RelatedBy == nil {
			if _, ok := columns[f.Relation.PrimaryKey]; !ok {
				return nil, fmt.Errorf("parse: %s: relation %q correlates on %q, which is not a column of %s", table, f.ExternalName, f.Relation.PrimaryKey, reflect.TypeFor[T]())
			}
			if r.computed(f.Relation.PrimaryKey) {
				return nil, computedCorrelation(table, f.ExternalName, f.Relation.PrimaryKey)
			}
			f.RelatedBy = r.correlationField(f.Relation.PrimaryKey)
		}
	}
	return r, nil
}

// ParseFilterWithCost applies a caller-selected cost budget for this call while
// preserving the resource's schema, codec, clock and other parser limits. The
// positive budget comes from server policy; request parsing uses ParseFilter.
func (r *Resource[T]) ParseFilterWithCost(query string, maxCost int) (*filter.ParsedFilter, error) {
	if maxCost <= 0 {
		return nil, fmt.Errorf("parse: filter cost budget must be positive")
	}
	cfg := *r.cfg
	cfg.MaxCost = maxCost
	return r.parseFilter(query, &cfg)
}

// readColumns is the ReadColumns set, checked to be distinct columns of T
// that include the key; nil reads every column.
func readColumns(table string, cfg resourceConfig, columns map[string]reflect.Type) (map[string]bool, error) {
	if cfg.read == nil {
		return nil, nil
	}
	read := make(map[string]bool, len(cfg.read))
	for _, name := range cfg.read {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("parse: %s: read column %q is not a column of the resource", table, name)
		}
		if read[name] {
			return nil, fmt.Errorf("parse: %s: read column %q is listed twice", table, name)
		}
		read[name] = true
	}
	if !read[cfg.pk] {
		return nil, fmt.Errorf("parse: %s: the read columns omit the key %q", table, cfg.pk)
	}
	return read, nil
}

// queryOnlyColumns is the QueryOnlyColumns set, checked to be distinct
// columns of T that the resource does not read; read nil reads every column.
func queryOnlyColumns(table string, cfg resourceConfig, columns map[string]reflect.Type, read map[string]bool) (map[string]bool, error) {
	if len(cfg.queryOnly) == 0 {
		return nil, nil
	}
	out := make(map[string]bool, len(cfg.queryOnly))
	for _, name := range cfg.queryOnly {
		if _, ok := columns[name]; !ok {
			return nil, fmt.Errorf("parse: %s: query-only column %q is not a column of the resource", table, name)
		}
		if out[name] {
			return nil, fmt.Errorf("parse: %s: query-only column %q is listed twice", table, name)
		}
		if read == nil || read[name] {
			return nil, fmt.Errorf("parse: %s: query-only column %q is read; QueryOnlyColumns names columns ReadColumns leaves out", table, name)
		}
		out[name] = true
	}
	return out, nil
}

// sortKeys is ss's keys; a nil schema has none.
func sortKeys(ss *filter.SortSchema) map[string]*filter.SortFieldSchema {
	if ss == nil {
		return nil
	}
	return ss.Fields
}

// searchFields is ss's fields; a nil schema has none.
func searchFields(ss *filter.SearchSchema) []filter.SearchField {
	if ss == nil {
		return nil
	}
	return ss.Fields
}

// sqlTypes validates declarations against their column's Go representation.
func sqlTypes(table string, cfg resourceConfig, columns map[string]reflect.Type) (map[string]core.SQLType, error) {
	if len(cfg.sqlTypes) == 0 {
		return nil, nil
	}
	out := make(map[string]core.SQLType, len(cfg.sqlTypes))
	for _, decl := range cfg.sqlTypes {
		name, t := decl.column, decl.t
		ft, ok := columns[name]
		if !ok {
			return nil, fmt.Errorf("parse: %s: column type for %q, which is not a column of the resource", table, name)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("parse: %s: column %q has its type declared twice", table, name)
		}
		if !t.Valid() {
			return nil, fmt.Errorf("parse: %s: column %q declares type %q, which has no read rule", table, name, t)
		}
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if info := core.TypeOf(ft); core.IsValueType(info) && !t.IsJSON() {
			// A value type scans the text its declaration reads.
			if err := core.CheckValueStorage(name, info, t); err != nil {
				return nil, fmt.Errorf("parse: %s: %w", table, err)
			}
			out[name] = t
			continue
		}
		switch {
		case t.IsJSON():
			if !jsonRepresentableType(ft) {
				return nil, fmt.Errorf("parse: %s: column %q declares %s with unsupported JSON type %s", table, name, t, ft)
			}
		case t == core.SQLInt32Array:
			if ft.Kind() != reflect.Slice || ft.Elem().Kind() != reflect.Int32 {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of int32, not %s", table, name, t, ft)
			}
		case t == core.SQLUUIDArray:
			if ft.Kind() != reflect.Slice || !core.IsUUIDType(ft.Elem()) {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of UUIDs, not %s", table, name, t, ft)
			}
		case t.IsArray():
			if ft.Kind() != reflect.Slice || ft.Elem().Kind() != reflect.String {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of strings, not %s", table, name, t, ft)
			}
		default:
			if ft.Kind() != reflect.String {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a string, not %s", table, name, t, ft)
			}
		}
		out[name] = t
	}
	return out, nil
}

// Values such as interfaces and nested structs are checked by the JSON encoder
// at the write boundary; construction must not invoke user encoding hooks.
func jsonRepresentableType(t reflect.Type) bool {
	marshaler := reflect.TypeFor[json.Marshaler]()
	if t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler) {
		return true
	}
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		return false
	default:
		return true
	}
}

// columnAccess is what a request may learn of a column of T. The order
// matters: a capability that needs some access is served by any lower one.
type columnAccess int

const (
	// columnRead is a column the resource reads: responses carry its values.
	columnRead columnAccess = iota
	// columnQueryOnly is an unread column QueryOnlyColumns names: a request
	// learns its values only from which rows match.
	columnQueryOnly
	// columnWithheld is any other unread column.
	columnWithheld
)

// columnAccesses records each column's access, from the read set (nil reads
// every column) and the query-only set.
func columnAccesses(columns map[string]reflect.Type, read, queryOnly map[string]bool) map[string]columnAccess {
	out := make(map[string]columnAccess, len(columns))
	for name := range columns {
		switch {
		case read == nil || read[name]:
			out[name] = columnRead
		case queryOnly[name]:
			out[name] = columnQueryOnly
		default:
			out[name] = columnWithheld
		}
	}
	return out
}

// maskableFields are the names a list's field mask may name: T's fields
// under the field-name tag whose values a response can carry. That is a
// field whose column the resource reads, or a field with no column, which
// only the service fills (a relation's keys, a derived value). A field whose
// column the resource does not read is zero in every response.
func maskableFields(walk *core.StructFields, byColumn map[string]*core.Field, access map[string]columnAccess) map[string]bool {
	columnOf := make(map[*core.Field]string, len(byColumn))
	for column, f := range byColumn {
		columnOf[f] = column
	}
	out := make(map[string]bool, len(walk.Fields))
	for _, f := range walk.Fields {
		column, isColumn := columnOf[f]
		if f.Hidden() || (isColumn && access[column] != columnRead) {
			continue
		}
		out[f.External] = true
	}
	return out
}

// admits refuses a capability over column that needs at least need. A
// computed expression names no column ("") and passes: its SQL is the
// application's own. Any other name must be a column of T, so a field whose
// SQL reads a column T does not declare (`db:"-"`) cannot bypass the read set.
func (r *Resource[T]) admits(column string, need columnAccess) error {
	if column == "" {
		return nil
	}
	got, ok := r.access[column]
	switch {
	case !ok:
		return fmt.Errorf("%q is not a column of %s", column, r.table)
	case got <= need:
		return nil
	case need == columnQueryOnly:
		return fmt.Errorf("%s does not read column %q; QueryOnlyColumns admits a filter or search on it", r.table, column)
	default:
		return fmt.Errorf("%s does not read column %q", r.table, column)
	}
}

// checkUnreadColumns refuses a request capability over a column a request
// may not learn (ReadColumns, QueryOnlyColumns). A cursor carries its sort
// keys' values and a facet's buckets are keyed by its field's values, so
// their columns must be read. A filter or search discloses its column's
// values by inference, which a query-only column admits. Relations are
// checked as Relate checks them.
func (r *Resource[T]) checkUnreadColumns(fields map[string]*filter.FieldSchema, keys map[string]*filter.SortFieldSchema, search []filter.SearchField) error {
	for _, name := range slices.Sorted(maps.Keys(keys)) {
		if err := r.admits(keys[name].RowColumn(), columnRead); err != nil {
			return fmt.Errorf("parse: %s: sort key %q: %w", r.table, name, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		f := fields[name]
		if f.Relation != nil {
			if err := r.checkRelation(name, f.Relation, nil, f.Includable || f.Facetable); err != nil {
				return err
			}
			continue
		}
		capability, need := "filter field", columnQueryOnly
		if f.Facetable {
			capability, need = "facet", columnRead
		}
		if err := r.admits(f.RowColumn(), need); err != nil {
			return fmt.Errorf("parse: %s: %s %q: %w", r.table, capability, name, err)
		}
	}
	for _, f := range search {
		if err := r.admits(f.RowColumn(), columnQueryOnly); err != nil {
			return fmt.Errorf("parse: %s: search field %q: %w", r.table, f.Name, err)
		}
	}
	return nil
}

// checkRelation refuses a relation over a column a request may not learn: the
// owner's correlation column and, for a has-many or belongs-to, the target's
// (target is nil for a pair table declared by tag). Includes and facets carry
// the correlation's values, so those columns must be read; a filter by the
// relation discloses them by inference, which a query-only column admits.
func (r *Resource[T]) checkRelation(name string, rs *core.RelationSchema, target RelatedResource, returnsValues bool) error {
	need := columnQueryOnly
	if returnsValues {
		need = columnRead
	}
	if err := r.admits(rs.PrimaryKey, need); err != nil {
		return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
	}
	if target != nil && (rs.Kind == core.HasMany || rs.Kind == core.BelongsTo) {
		if err := target.admits(rs.SourceColumn, need); err != nil {
			return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
		}
	}
	return nil
}

// MustResource is NewResource for package-level declarations; it panics on
// a declaration error.
func MustResource[T any](table string, opts ...ResourceOption) *Resource[T] {
	r, err := NewResource[T](table, opts...)
	if err != nil {
		panic(err)
	}
	return r
}

// Table is the table the resource reads.
func (r *Resource[T]) Table() string { return r.table }

// PrimaryKeyColumn is the key column.
func (r *Resource[T]) PrimaryKeyColumn() string { return r.pk }

// TenantColumnName is the tenant column; "" for a resource without tenants.
func (r *Resource[T]) TenantColumnName() string { return r.tenant }

// DeletedColumnName is the soft-delete column; "" without soft delete.
func (r *Resource[T]) DeletedColumnName() string { return r.deleted }

// ColumnSQLType returns a column's explicit storage declaration. The returned
// value cannot mutate the resource's metadata; false means no declaration.
func (r *Resource[T]) ColumnSQLType(column string) (core.SQLType, bool) {
	t, ok := r.sqlTypes[column]
	return t, ok
}

// Columns is the projection every statement over the resource reads for
// dialect d: a list's page, an update's RETURNING, and a repository's own
// reads of the table. It is T's columns in field order, or the ReadColumns
// among them, table-qualified; a computed column (ReadExpr) is read through
// d's expression and a column with a declared ColumnType as d reads that
// type, each under the column's own name, so rows scan by name. A dialect a
// computed column's expression refuses is an error.
func (r *Resource[T]) Columns(d filter.Dialect) ([]string, error) {
	v, err := r.dialectView(d)
	if err != nil {
		return nil, err
	}
	return r.columns(d, v), nil
}

// columns is Columns for v, r's schemas already compiled for d.
func (r *Resource[T]) columns(d filter.Dialect, v *dialectView) []string {
	out := make([]string, len(r.read))
	for i, name := range r.read {
		stored := r.table + "." + name
		col := stored
		if expr, ok := v.exprs[name]; ok {
			col = expr
		}
		if t, ok := r.sqlTypes[name]; ok {
			col = d.ReadColumn(col, t)
		}
		if col != stored {
			col += " AS " + name
		}
		out[i] = col
	}
	return out
}

// ColumnNames is the unqualified name of each Columns entry, in the same
// order: the name every row's value scans under, whichever dialect read it.
func (r *Resource[T]) ColumnNames() []string { return slices.Clone(r.read) }

// Key reads the primary key from a model value or a pointer to one.
func (r *Resource[T]) Key(item any) (any, error) {
	return r.columnValue(reflect.ValueOf(item), r.pk)
}

// columnValue resolves row pointers before reading the field's struct route.
func (r *Resource[T]) columnValue(rv reflect.Value, column string) (any, error) {
	f, ok := r.columnFields[column]
	if !ok {
		return nil, fmt.Errorf("parse: %s has no column %q", r.table, column)
	}
	if !rv.IsValid() {
		return nil, fmt.Errorf("parse: %s: column %s: nil row", r.table, column)
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("parse: %s: column %s: nil row", r.table, column)
		}
		rv = rv.Elem()
	}
	model := reflect.TypeFor[T]()
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	if rv.Type() != model {
		return nil, fmt.Errorf("parse: %s: %s is not %s", r.table, rv.Type(), reflect.TypeFor[T]())
	}
	fv, err := f.Value(rv)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: column %s: %w", r.table, column, err)
	}
	return fv.Interface(), nil
}

// systemFields are the columns policies constrain without the request
// filtering on them: the key, the tenant and the soft-delete column, typed
// from T's fields.
func systemFields(table string, cfg resourceConfig, columns map[string]reflect.Type) map[string]*filter.FieldSchema {
	field := func(column string, ops ...filter.FilterOperator) *filter.FieldSchema {
		info := core.TypeOf(columns[column])
		return &filter.FieldSchema{
			ExternalName: column, SQLExpr: table + "." + column, Column: column,
			DataType: info.Type, NumberKind: info.NumberKind, AllowedOperators: ops,
		}
	}
	fields := map[string]*filter.FieldSchema{
		cfg.pk: field(cfg.pk, filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte, filter.OpIn),
	}
	if cfg.tenant != "" && cfg.tenant != cfg.pk {
		fields[cfg.tenant] = field(cfg.tenant, filter.OpEq, filter.OpIn)
	}
	if cfg.deleted != "" {
		fields[cfg.deleted] = field(cfg.deleted, filter.OpNull, filter.OpNotNull)
	}
	return fields
}

// resourceColumns lists T's columns in field order with the field holding
// each, from the parser's field walk: a field's column tag name, else the
// generator's convention on its name; a column tag of "-" has none. Two
// fields with one column are an error.
func resourceColumns(gen *core.SchemaGenerator, walk *core.StructFields) ([]string, map[string]*core.Field, error) {
	var order []string
	byColumn := make(map[string]*core.Field, len(walk.Fields))
	for _, f := range walk.Fields {
		col, ok, err := gen.FieldColumn(f)
		if err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", f.GoName, err)
		}
		if !ok {
			continue
		}
		if prev, dup := byColumn[col]; dup {
			return nil, nil, fmt.Errorf("fields %s and %s are both column %q", prev.GoName, f.GoName, col)
		}
		byColumn[col] = f
		order = append(order, col)
	}
	return order, byColumn, nil
}

// WithPaths returns a view of r for one request with decls declared as JSON
// paths — filterable, and sortable or facetable as each declares — leaving r and its other requests unchanged. A tenant's custom fields
// are declared this way; the view parses, lists and updates like r. A path
// into a computed column (ReadExpr) is refused: it reads a stored document.
func (r *Resource[T]) WithPaths(decls ...filter.PathDecl) (*Resource[T], error) {
	fs, err := r.filterSchema.WithPaths(decls...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	ss, err := r.sortSchema.WithPaths(r.filterSchema, decls...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	if err := r.readExprs.checkDocuments(fs, ss); err != nil {
		return nil, err
	}
	// The resource's own fields were checked when it was built.
	added := make(map[string]*filter.FieldSchema, len(decls))
	addedKeys := make(map[string]*filter.SortFieldSchema, len(decls))
	for _, d := range decls {
		if f, ok := fs.Fields[d.Path]; ok {
			added[d.Path] = f
		}
		if k, ok := sortKeys(ss)[d.Path]; ok {
			addedKeys[d.Path] = k
		}
	}
	if err := r.checkUnreadColumns(added, addedKeys, nil); err != nil {
		return nil, err
	}
	tp := &typedParser[T]{
		filterSchema:      fs,
		patchSchema:       r.patchSchema,
		serverPatchSchema: r.serverPatchSchema,
		hiddenPaths:       r.hiddenPaths,
		sortSchema:        ss,
		codec:             r.codec,
		searchSchema:      r.searchSchema,
		cfg:               r.cfg,
		fieldNameTag:      r.fieldNameTag,
		patchFields:       r.patchFields,
		gen:               r.gen,
	}
	view := *r
	view.typedParser = tp
	view.readExprs = r.readExprs.withoutViews()
	return &view, nil
}
