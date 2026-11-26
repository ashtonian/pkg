package parse

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Resource is one table's request grammar and identity: its key,
// tenant and soft-delete columns, its page bounds, and the filter, sort,
// search and patch schemas T's struct tags declare. It parses list requests
// (ParseList), compiles them for a dialect under a server-decided Scope
// (BuildList) and turns fetched rows into a page (Page). It also parses
// filters and patches. Build one per type at startup; it is safe for concurrent use.
type Resource[T any] struct {
	// cfg is shared by every parser, so limits, codec and clock apply
	// consistently through the resource API; gen generated the schemas.
	cfg   *core.ParserConfig
	gen   *core.SchemaGenerator
	codec core.Codec

	filterSchema *filter.ResourceSchema
	sortSchema   *filter.SortSchema
	searchSchema *filter.SearchSchema
	patchSchema  *patch.PatchSchema
	// serverPatchSchema admits the server's own writes: patchSchema's paths
	// and the `parse:"server"` fields (ParseServerMergePatch).
	serverPatchSchema *patch.PatchSchema
	// applier applies either patch schema's patches to a T.
	applier *patch.Applier[T]

	table string
	// pk are the key columns, in key order.
	pk          []string
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
	// searchParam is the query parameter the search schema advertises when
	// it is not filter.ParamSearch; ParseList reads the search from either.
	searchParam string
	// read are the columns the resource reads, in field order, and
	// sqlTypes the declared database types among them (ColumnType);
	// tableColumns is every column of T, and columnFields the field holding
	// each.
	read         []string
	sqlTypes     map[string]core.SQLType
	tableColumns map[string]reflect.Type
	columnFields map[string]*core.Field
	// undecodable holds why a read column's type, an sql.Scanner the
	// resource does not declare JSON, cannot be decoded as a stored
	// document (jsonkeys.CheckDecodable): RowScanner refuses ScanJSON on it.
	undecodable map[string]error
	// access is what a request may learn of each column of T (ReadColumns,
	// QueryOnlyColumns).
	access map[string]columnAccess
	// maskable maps the names a list's field mask may name to the JSON
	// members their fields encode as (maskableFields).
	maskable map[string]string
	// readExprs are the computed columns (ReadExpr); nil without any.
	readExprs *computedColumns
	// view marks a WithPaths view: a value for one request that shares its
	// base's patch schemas and tenant graph, so it declares no relation and
	// is no relation's target.
	view bool
}

// ResourceOption configures NewResource.
type ResourceOption func(*resourceConfig)

type resourceConfig struct {
	pk                []string
	tenant, deleted   string
	pageSize, maxPage int
	parserOpts        []core.Option
	accepted          []string
	read              []string
	queryOnly         []string
	sqlTypes          []columnType
	readExprs         []columnExpr
	customize         []func(Schemas) error
}

type columnType struct {
	column string
	t      core.SQLType
}

// PrimaryKey names the key's columns, in key order; the default is
// core.DefaultPrimaryKey ("id") alone. The columns must be unique together
// among the table's rows, as a primary key or a unique constraint over
// exactly them makes them: every list ends its keyset with them, so pages
// are stable over a non-unique sort, and an update by key (ByID, ByKey)
// reaches the rows holding the key. Columns two rows share page one of the
// rows out of every list and let an update by key change both. A key may
// include the tenant column, as (tenant_id, sku) does. NewResource refuses
// no column, a column listed twice, and a column that is not one of T's,
// is computed (ReadExpr), is not read (ReadColumns) or is the soft-delete
// column.
//
// A key of several columns is a Key of a value per column (ByKey, ByKeys,
// Resource.Key), and a relation to or from the resource pairs a column
// with each of them in key order (HasMany, BelongsTo, Through). A key
// column is of any scalar type (core.IsKeyType): a string, UUID, integer,
// float, decimal, boolean, date, time of day, timestamp, interval or
// network value. Every statement compares it by value, as a filter
// compares its type (a decimal stored as "1.50" is the key 1.5), and Go
// tells keys apart by their canonical text (core.KeyTextOf). A document,
// array or geometry column has no equality to identify a row by, and
// NewResource refuses it as a key column.
func PrimaryKey(columns ...string) ResourceOption {
	return func(c *resourceConfig) { c.pk = slices.Clone(columns) }
}

// TenantColumn names the column that holds a row's tenant. Every list is then
// held to Scope.Tenant, and so are tenant-scoped relation junctions. The
// column's field is a string, a UUID or an integer; Scope.Tenant is
// normalized to it, and to every tenant column the resource's relations
// reach (Relate).
func TenantColumn(column string) ResourceOption {
	return func(c *resourceConfig) { c.tenant = column }
}

// DeletedColumn names the soft-delete timestamp: a row is live while it is
// NULL, so its field must hold NULL (a pointer or an sql.Scanner such as
// sql.NullTime),
// and it is neither the key nor the tenant column. Lists exclude deleted rows
// unless Scope.IncludeDeleted; updates never reach them.
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

// Schemas are the schemas NewResource generates from T's struct tags, as
// CustomizeSchemas hands them over.
type Schemas struct {
	Filter *filter.ResourceSchema
	Sort   *filter.SortSchema
	// Search is nil when T has no search fields.
	Search      *filter.SearchSchema
	Patch       *patch.PatchSchema
	ServerPatch *patch.PatchSchema
}

// CustomizeSchemas changes the generated schemas while the resource is
// built: a patch path pattern (patch.PatchSchema.AddPath), a validator, a
// narrower operator set. NewResource runs the functions in order, before its
// checks, so a customized schema is held to the read set (ReadColumns), the
// column declarations and the computed columns as a generated one is; an
// error fails NewResource. The key and system fields are set after the
// functions run. The resource keeps copies: a schema a function holds on to
// changes nothing once NewResource returns, and the accessors hand out
// copies too.
func CustomizeSchemas(fn func(Schemas) error) ResourceOption {
	return func(c *resourceConfig) { c.customize = append(c.customize, fn) }
}

// customizeSchemas runs fns over the generated schemas and keeps copies of
// the result.
func (r *Resource[T]) customizeSchemas(fns []func(Schemas) error) error {
	if len(fns) == 0 {
		return nil
	}
	s := Schemas{Filter: r.filterSchema, Sort: r.sortSchema, Search: r.searchSchema, Patch: r.patchSchema, ServerPatch: r.serverPatchSchema}
	for _, fn := range fns {
		if fn == nil {
			return errors.New("nil schema customization")
		}
		if err := fn(s); err != nil {
			return err
		}
	}
	if len(s.Filter.Computed) > 0 {
		return errors.New("computed columns are declared with ReadExpr, not on the filter schema")
	}
	r.filterSchema, r.sortSchema, r.searchSchema = s.Filter.Clone(), s.Sort.Clone(), s.Search.Clone()
	r.patchSchema, r.serverPatchSchema = s.Patch.Clone(), s.ServerPatch.Clone()
	return nil
}

// NewResource builds the resource for T stored in table. Identifiers are
// declared here, never taken from a request: table and every column option
// must be SQL identifiers, and the columns must be T's (a `db` tag, or the
// snake-case field name).
func NewResource[T any](table string, opts ...ResourceOption) (*Resource[T], error) {
	cfg := resourceConfig{pk: []string{core.DefaultPrimaryKey}, pageSize: filter.DefaultListLimit, maxPage: filter.MaxListLimit}
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
	r := &Resource[T]{
		table:       table,
		pk:          cfg.pk,
		tenant:      cfg.tenant,
		deleted:     cfg.deleted,
		pageSize:    cfg.pageSize,
		maxPageSize: cfg.maxPage,
		accepted:    slices.Clone(cfg.accepted),
	}
	if err := r.generateSchemas(parserOpts); err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	walk, err := r.gen.StructFields(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	order, fieldsByColumn, err := resourceColumns(r.gen, walk)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	columns := make(map[string]reflect.Type, len(order))
	for name, f := range fieldsByColumn {
		columns[name] = f.Type()
	}
	err = checkKeyColumns(cfg.pk, columns, reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	for _, c := range []struct{ option, column string }{{"tenant", cfg.tenant}, {"deleted", cfg.deleted}} {
		if c.column == "" {
			continue
		}
		if _, ok := columns[c.column]; !ok {
			return nil, fmt.Errorf("parse: %s: %s column %q is not a column of %s", table, c.option, c.column, gowalker.TypeText(reflect.TypeFor[T]()))
		}
	}
	if err := checkDeletedColumn(cfg, columns[cfg.deleted]); err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	if err := r.customizeSchemas(cfg.customize); err != nil {
		return nil, fmt.Errorf("parse: %s: customizing schemas: %w", table, err)
	}
	r.filterSchema.SystemFields = systemFields(table, cfg, columns, fieldsByColumn)
	r.filterSchema.PrimaryKey = slices.Clone(cfg.pk)
	err = r.setSearchParam()
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	var tenant *tenantColumn
	if cfg.tenant != "" {
		if tenant, err = newTenantColumn(table, cfg.tenant, columns[cfg.tenant]); err != nil {
			return nil, fmt.Errorf("parse: %s: %w", table, err)
		}
	}
	r.tenantNode = newTenantNode(table, tenant)
	r.tableColumns = columns
	r.columnFields = fieldsByColumn

	read, err := readColumns(table, cfg, columns)
	if err != nil {
		return nil, err
	}
	if r.readExprs, err = newComputedColumns(table, cfg, columns, read); err != nil {
		return nil, err
	}
	r.filterSchema.Computed = r.readExprs.schemaColumns()
	if err := r.readExprs.checkDocuments(r.filterSchema, r.sortSchema); err != nil {
		return nil, err
	}
	if err := r.checkComputedPatches(); err != nil {
		return nil, err
	}
	if err := r.checkIdentityPatches(); err != nil {
		return nil, err
	}
	for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
		err = r.applier.CheckSchema(schema)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: %w", table, err)
		}
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
	r.maskable, err = maskableFields(r.gen, walk, fieldsByColumn, r.access)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
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
	err = r.checkScannable()
	if err != nil {
		return nil, err
	}

	if err := r.checkUnreadColumns(r.filterSchema.Fields, sortKeys(r.sortSchema), searchFields(r.searchSchema)); err != nil {
		return nil, err
	}
	for _, f := range r.filterSchema.Fields {
		if f.Relation != nil && f.RelatedBy == nil {
			owner := f.Relation.OwnerColumns()
			for _, c := range owner {
				_, ok := columns[c]
				if !ok {
					return nil, fmt.Errorf("parse: %s: relation %q correlates on %q, which is not a column of %s", table, f.ExternalName, c, gowalker.TypeText(reflect.TypeFor[T]()))
				}
				if r.computed(c) {
					return nil, computedCorrelation(table, f.ExternalName, c)
				}
			}
			if (f.Includable || f.Facetable) && !slices.Equal(owner, r.pk) {
				return nil, fmt.Errorf("parse: %s: relation %q: an include or facet groups related keys by the owner's key (%s), and the relation correlates on %s",
					table, f.ExternalName, strings.Join(r.pk, ", "), strings.Join(owner, ", "))
			}
			f.RelatedBy = r.correlationFields(owner)
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
// that include every key column; nil reads every column.
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
	for _, column := range cfg.pk {
		if !read[column] {
			return nil, fmt.Errorf("parse: %s: the read columns omit the key column %q", table, column)
		}
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
				return nil, fmt.Errorf("parse: %s: column %q declares %s with unsupported JSON type %s", table, name, t, gowalker.TypeText(ft))
			}
		case t == core.SQLInt32Array:
			if ft.Kind() != reflect.Slice || ft.Elem().Kind() != reflect.Int32 {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of int32, not %s", table, name, t, gowalker.TypeText(ft))
			}
		case t == core.SQLUUIDArray:
			if ft.Kind() != reflect.Slice || !core.IsUUIDType(ft.Elem()) {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of UUIDs, not %s", table, name, t, gowalker.TypeText(ft))
			}
		case t.IsArray():
			if ft.Kind() != reflect.Slice || ft.Elem().Kind() != reflect.String {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a slice of strings, not %s", table, name, t, gowalker.TypeText(ft))
			}
		default:
			if ft.Kind() != reflect.String {
				return nil, fmt.Errorf("parse: %s: column %q is %s, read into a string, not %s", table, name, t, gowalker.TypeText(ft))
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

// maskableFields maps the names a list's field mask may name to the JSON
// member each field encodes as. The names are T's fields under the
// field-name tag whose values a response can carry: a field whose column the
// resource reads, or a field with no column, which only the service fills (a
// relation's keys, a derived value). A field whose column the resource does
// not read is zero in every response. Under a field-name tag other than
// json, the member is the name encoding/json gives the same struct field,
// and "" for a field it writes no member for.
func maskableFields(gen *core.SchemaGenerator, walk *core.StructFields, byColumn map[string]*core.Field, access map[string]columnAccess) (map[string]string, error) {
	member, err := jsonMembers(gen, walk)
	if err != nil {
		return nil, err
	}
	columnOf := make(map[*core.Field]string, len(byColumn))
	for column, f := range byColumn {
		columnOf[f] = column
	}
	out := make(map[string]string, walk.Len())
	for f := range walk.Fields() {
		column, isColumn := columnOf[f]
		if f.Hidden() || (isColumn && access[column] != columnRead) {
			continue
		}
		out[f.External()] = member(f)
	}
	return out, nil
}

// jsonMembers returns the JSON member encoding/json writes a field of walk
// under, "" for none: the field's own name when the field-name tag is json.
func jsonMembers(gen *core.SchemaGenerator, walk *core.StructFields) (func(*core.Field) string, error) {
	if gen.FieldNameTag() == core.JSONTagKey {
		external := func(f *core.Field) string { return f.External() }
		return external, nil
	}
	encoded, err := core.JSONFieldsOf(walk.Type())
	if err != nil {
		return nil, fmt.Errorf("reading the JSON member names of %s: %w", gowalker.TypeText(walk.Type()), err)
	}
	byRoute := make(map[string]string, encoded.Len())
	for f := range encoded.Fields() {
		byRoute[fmt.Sprint(f.Route())] = f.External()
	}
	member := func(f *core.Field) string { return byRoute[fmt.Sprint(f.Route())] }
	return member, nil
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
			err := r.checkRelationKeys(name, f.Relation, nil)
			if err != nil {
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
// owner's correlation columns and, for a has-many or belongs-to, the
// target's (target is nil for a pair table declared by tag). Includes and
// facets carry the correlation's values, so those columns must be read; a
// filter by the relation discloses them by inference, which a query-only
// column admits.
func (r *Resource[T]) checkRelation(name string, rs *core.RelationSchema, target RelatedResource, returnsValues bool) error {
	need := columnQueryOnly
	if returnsValues {
		need = columnRead
	}
	for _, c := range rs.OwnerColumns() {
		err := r.admits(c, need)
		if err != nil {
			return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
		}
	}
	if target != nil && (rs.Kind == core.HasMany || rs.Kind == core.BelongsTo) {
		for _, c := range rs.SourceColumns {
			err := target.admits(c, need)
			if err != nil {
				return fmt.Errorf("parse: %s: relation %q: %w", r.table, name, err)
			}
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

// PrimaryKeyColumns are the key columns, in key order.
func (r *Resource[T]) PrimaryKeyColumns() []string { return slices.Clone(r.pk) }

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
// among them, table-qualified and quoted by d; a computed column (ReadExpr) is read through
// d's expression and a column with a declared ColumnType as d reads that
// type, each under the column's own name, so rows scan by name. A dialect a
// computed column's expression refuses is an error.
func (r *Resource[T]) Columns(d filter.Dialect) ([]string, error) {
	exprs, err := r.computedSQL(d)
	if err != nil {
		return nil, err
	}
	return r.columns(d, exprs), nil
}

// columns is Columns with the computed columns' SQL for d already resolved.
func (r *Resource[T]) columns(d filter.Dialect, exprs map[string]string) []string {
	out := make([]string, len(r.read))
	for i, name := range r.read {
		stored := d.QuoteIdent(r.table) + "." + d.QuoteIdent(name)
		col := stored
		if expr, ok := exprs[name]; ok {
			col = expr
		}
		if t, ok := r.sqlTypes[name]; ok {
			col = d.ReadColumn(col, t)
		}
		if col != stored {
			col += " AS " + d.QuoteIdent(name)
		}
		out[i] = col
	}
	return out
}

// ColumnNames is the unqualified name of each Columns entry, in the same
// order: the name every row's value scans under, whichever dialect read it.
func (r *Resource[T]) ColumnNames() []string { return slices.Clone(r.read) }

// Key reads the row's key from a model value or a pointer to one.
func (r *Resource[T]) Key(item any) (Key, error) {
	rv := reflect.ValueOf(item)
	key := make(Key, len(r.pk))
	for i, column := range r.pk {
		v, err := r.columnValue(rv, column)
		if err != nil {
			return nil, err
		}
		key[i] = v
	}
	return key, nil
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
		return nil, fmt.Errorf("parse: %s: %s is not %s", r.table, gowalker.TypeText(rv.Type()), gowalker.TypeText(reflect.TypeFor[T]()))
	}
	fv, err := f.Value(rv)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: column %s: %w", r.table, column, err)
	}
	return fv.Interface(), nil
}

// checkDeletedColumn refuses a deleted column no row can be live under: a
// key column or the tenant column, which a live row always holds, or a
// field that cannot hold NULL, such as a bool soft-delete flag, under which
// every row would be live.
func checkDeletedColumn(cfg resourceConfig, t reflect.Type) error {
	switch {
	case cfg.deleted == "":
		return nil
	case slices.Contains(cfg.pk, cfg.deleted):
		return fmt.Errorf("deleted column %q is a key column", cfg.deleted)
	case cfg.deleted == cfg.tenant:
		return fmt.Errorf("deleted column %q is the tenant column", cfg.deleted)
	case !holdsNull(t):
		return fmt.Errorf("deleted column %q is %s, which cannot hold NULL: a row is live while the column is NULL, so declare a pointer or an sql.Scanner such as sql.NullTime", cfg.deleted, gowalker.TypeText(t))
	}
	return nil
}

// holdsNull reports whether a field of type t scans a NULL: a pointer, or
// an sql.Scanner (sql.NullTime, sql.Null[T], a driver's nullable type), to
// which the driver hands NULL as nil.
func holdsNull(t reflect.Type) bool {
	return t.Kind() == reflect.Pointer || reflect.PointerTo(t).Implements(reflect.TypeFor[sql.Scanner]())
}

// checkIdentityPatches refuses a patch path, request or server, that writes
// the key or the tenant column. An update's WHERE holds the scope's tenant
// and the row's key, so such a write would move the row out of its tenant
// or away from every relation correlated on its key; a tenant transfer is
// not a patch.
func (r *Resource[T]) checkIdentityPatches() error {
	for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
		if schema == nil {
			continue
		}
		for path, ps := range schema.Paths() {
			if err := r.checkIdentityWrite(ps.SQLColumn); err != nil {
				return fmt.Errorf("parse: %s: patch path %s: %w", r.table, path, err)
			}
		}
	}
	return nil
}

// checkIdentityWrite refuses a write of column when it is the key or the
// tenant column.
func (r *Resource[T]) checkIdentityWrite(column string) error {
	switch {
	case column == "":
		return nil
	case slices.Contains(r.pk, column):
		return fmt.Errorf("writes the key column %q", column)
	case column == r.tenant:
		return fmt.Errorf("writes the tenant column %q", column)
	}
	return nil
}

// systemFields are the columns policies constrain without the request
// filtering on them: the key, the tenant and the soft-delete column, typed
// from T's fields, in the wire form their fields travel in (a json:",string"
// key's filter operands are JSON strings). A key column is Indexed, since a
// primary key is an index by construction, so a filter on it costs as one.
func systemFields(table string, cfg resourceConfig, columns map[string]reflect.Type, byColumn map[string]*core.Field) map[string]*filter.FieldSchema {
	field := func(column string, ops ...filter.FilterOperator) *filter.FieldSchema {
		info := core.TypeOf(columns[column])
		var wire core.Wire
		if f, ok := byColumn[column]; ok {
			wire = f.Wire()
		}
		return &filter.FieldSchema{
			ExternalName: column, Table: table, Column: column,
			DataType: info.Type, NumberKind: info.NumberKind, Wire: wire, AllowedOperators: ops,
		}
	}
	fields := make(map[string]*filter.FieldSchema, len(cfg.pk)+2)
	for _, column := range cfg.pk {
		key := field(column, filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte, filter.OpIn)
		key.Indexed = true
		fields[column] = key
	}
	if cfg.tenant != "" && !slices.Contains(cfg.pk, cfg.tenant) {
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
	byColumn := make(map[string]*core.Field, walk.Len())
	for f := range walk.Fields() {
		col, ok, err := gen.FieldColumn(f)
		if err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", f.GoName(), err)
		}
		if !ok {
			continue
		}
		if prev, dup := byColumn[col]; dup {
			return nil, nil, fmt.Errorf("fields %s and %s are both column %q", prev.GoName(), f.GoName(), col)
		}
		byColumn[col] = f
		order = append(order, col)
	}
	return order, byColumn, nil
}

// WithPaths returns a view of r for one request with decls declared as JSON
// paths — filterable, and sortable or facetable as each declares — leaving
// r and its other requests unchanged. A tenant's custom fields are declared
// this way; the view parses, lists and updates like r, with the relations r
// declared before it. A path into a computed column (ReadExpr) is refused:
// it reads a stored document. A view declares no relations and is no
// relation's target (Relate).
func (r *Resource[T]) WithPaths(decls ...filter.PathDecl) (*Resource[T], error) {
	// A struct document's quoted member (json:",string") is stored as text
	// its path reads as NULL; the bare schema has no Go types to tell.
	for _, d := range decls {
		name, members, _ := strings.Cut(d.Path, ".")
		doc, ok := r.filterSchema.Fields[name]
		if !ok || members == "" {
			continue // filter.ResourceSchema.WithPaths refuses these
		}
		f, ok := r.columnFields[doc.Column]
		if !ok {
			continue
		}
		w, err := r.gen.MemberWire(f.Type(), strings.Split(members, "."))
		if err != nil {
			return nil, fmt.Errorf("parse: %s: path %q: %w", r.table, d.Path, err)
		}
		if w != "" {
			return nil, fmt.Errorf("parse: %s: path %q: json:\",string\" stores the member as text, which its path cannot compare; drop the option, or declare the member a string", r.table, d.Path)
		}
	}
	fs, err := r.filterSchema.WithPaths(decls...)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	err = r.checkSearchParam(fs)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	ss, err := r.sortSchema.WithPaths(r.filterSchema, decls...)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, err)
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
	view := *r
	view.filterSchema, view.sortSchema, view.view = fs, ss, true
	return &view, nil
}
