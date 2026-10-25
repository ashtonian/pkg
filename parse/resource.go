package parse

import (
	"encoding/json"
	"fmt"
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
}

// ResourceOption configures NewResource.
type ResourceOption func(*resourceConfig)

type resourceConfig struct {
	pk, tenant, deleted string
	pageSize, maxPage   int
	parserOpts          []core.Option
	accepted            []string
	read                []string
	sqlTypes            []columnType
}

type columnType struct {
	column string
	t      core.SQLType
}

// PrimaryKey names the key column; default "id". It is the final keyset
// column of every list, so pages are stable over a non-unique sort.
func PrimaryKey(column string) ResourceOption { return func(c *resourceConfig) { c.pk = column } }

// TenantColumn names the column that holds a row's tenant. Every list is then
// held to Scope.Tenant, and so are tenant-scoped relation junctions.
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
// database and their fields read as zero. The key and every sort key's
// column must be read: pages mint cursors from the rows.
func ReadColumns(columns ...string) ResourceOption {
	return func(c *resourceConfig) { c.read = append(c.read, columns...) }
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
	cfg := resourceConfig{pk: "id", pageSize: filter.DefaultListLimit, maxPage: filter.MaxListLimit}
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
	order, fieldsByColumn, err := resourceColumns(p.gen, reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	columns := make(map[string]reflect.Type, len(order))
	for name, f := range fieldsByColumn {
		columns[name] = f.Type
	}
	for _, c := range []struct{ option, column string }{{"key", cfg.pk}, {"tenant", cfg.tenant}, {"deleted", cfg.deleted}} {
		if c.column == "" && c.option != "key" {
			continue
		}
		if _, ok := columns[c.column]; !ok {
			return nil, fmt.Errorf("parse: %s: %s column %q is not a column of %s", table, c.option, c.column, reflect.TypeFor[T]())
		}
	}
	p.filterSchema.SystemFields = systemFields(table, cfg, columns)
	p.filterSchema.PrimaryKey = cfg.pk

	r := &Resource[T]{
		typedParser:  p,
		table:        table,
		pk:           cfg.pk,
		tenant:       cfg.tenant,
		deleted:      cfg.deleted,
		pageSize:     cfg.pageSize,
		maxPageSize:  cfg.maxPage,
		accepted:     slices.Clone(cfg.accepted),
		tableColumns: columns,
		columnFields: fieldsByColumn,
	}
	read, err := readColumns(table, cfg, columns)
	if err != nil {
		return nil, err
	}
	for _, name := range order {
		if read == nil || read[name] {
			r.read = append(r.read, name)
		}
	}
	if r.sqlTypes, err = sqlTypes(table, cfg, columns); err != nil {
		return nil, err
	}
	if err := r.readsSortKeys(p.sortSchema); err != nil {
		return nil, err
	}
	for _, f := range p.filterSchema.Fields {
		if f.Relation != nil && f.RelatedBy == nil {
			if _, ok := columns[f.Relation.PrimaryKey]; !ok {
				return nil, fmt.Errorf("parse: %s: relation %q correlates on %q, which is not a column of %s", table, f.ExternalName, f.Relation.PrimaryKey, reflect.TypeFor[T]())
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

// readsSortKeys refuses a sort key whose column the resource does not read:
// its cursors would carry the zero value.
func (r *Resource[T]) readsSortKeys(ss *filter.SortSchema) error {
	if ss == nil {
		return nil
	}
	for name, f := range ss.Fields {
		col := f.RowColumn()
		if _, ok := r.tableColumns[col]; ok && !slices.Contains(r.read, col) {
			return fmt.Errorf("parse: %s: sort key %q reads column %q, which the resource does not read", r.table, name, col)
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
// among them, table-qualified; a column with a declared ColumnType is read
// as d reads that type, under the column's own name, so rows scan by name.
func (r *Resource[T]) Columns(d filter.Dialect) []string {
	out := make([]string, len(r.read))
	for i, name := range r.read {
		col := r.table + "." + name
		if t, ok := r.sqlTypes[name]; ok {
			if expr := d.ReadColumn(col, t); expr != col {
				col = expr + " AS " + name
			}
		}
		out[i] = col
	}
	return out
}

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
			ExternalName: column, SQLExpr: table + "." + column,
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
func resourceColumns(gen *core.SchemaGenerator, t reflect.Type) ([]string, map[string]*core.Field, error) {
	walk, err := gen.StructFields(t)
	if err != nil {
		return nil, nil, err
	}
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
// are declared this way; the view parses, lists and updates like r.
func (r *Resource[T]) WithPaths(decls ...filter.PathDecl) (*Resource[T], error) {
	fs, err := r.filterSchema.WithPaths(decls...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	ss, err := r.sortSchema.WithPaths(r.filterSchema, decls...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	if err := r.readsSortKeys(ss); err != nil {
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
	return &view, nil
}
