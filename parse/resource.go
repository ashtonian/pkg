package parse

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/patch"
)

// Resource is one table's request grammar and identity on the provider it
// runs on: its key, tenant and soft-delete columns, its page bounds, and
// the filter, sort, search and patch schemas T's struct tags declare. It
// parses list requests (ParseList) and patches (ParsePatch), and runs them
// under a server-decided Scope on its provider's database: a page (List),
// facet counts (Facets) and updates (Update, UpdateBatch). Its builds
// (BuildList, BuildUpdate) compile the same statements for a caller who
// wants the SQL. Build one per type at startup, on the application's
// provider; it is safe for concurrent use.
type Resource[T any] struct {
	resourceCore
	// applier applies either patch schema's patches to a T.
	applier *patch.Applier[T]
}

// resourceCore is all of a Resource that does not depend on T, with every
// method that parses, builds or checks without a T in hand. Go compiles a
// generic type's methods once for each type argument, so Resource[T]
// keeps to the methods that read or write T's rows and embeds this for the
// rest: an application's resource types do not each compile the parsers
// and builders again.
type resourceCore struct {
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

	// rowType is T, the type Key reads a row of.
	rowType reflect.Type
	table   string
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
	// tableColumns is every column of T, columnOrder the same in field
	// order, and columnFields the field holding each.
	read         []string
	sqlTypes     map[string]core.SQLType
	tableColumns map[string]reflect.Type
	columnOrder  []string
	columnFields map[string]*core.Field
	// scalars are the scalar types of the document columns their storage
	// declares one for (filter.ScalarStorage): the type the column holds.
	scalars map[string]core.TypeInfo
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
	// view marks a WithPaths view or a resource scoped to a transaction
	// (In): a value for one request that shares its base's patch schemas
	// and tenant graph, so it declares no relation and is no relation's
	// target.
	view bool

	// provider is the provider the resource was built on, and dialect its
	// dialect, nil for one that compiles no SQL; exec runs the resource's
	// requests: the provider's database, or a transaction it joined (In).
	provider *Provider
	dialect  filter.Dialect
	exec     backend.Executor
	// desc is the descriptor every request carries (backend.Resource),
	// fixed at construction; a view's holds the view's filter schema.
	desc *backend.Resource
	// computedSQL is each computed column's parenthesized SQL for dialect,
	// by column, fixed at construction.
	computedSQL map[string]string
	// scanRules scan the rows the executor hands back; a provider that
	// reports a result's document columns gets rules built for them
	// (rulesFor).
	scanRules rowRules
}

// customizeSchemas runs fns over the generated schemas and keeps copies of
// the result.
func (r *resourceCore) customizeSchemas(fns []func(Schemas) error) error {
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

// NewResource builds the resource for T stored in table, on provider p for
// its whole life. Identifiers are declared here, never taken from a
// request: table and every column option must be SQL identifiers, and the
// columns must be T's (a `db` tag, or the snake-case field name). What the
// provider's dialect decides is decided here too: a computed column's
// expression (ReadExpr) the dialect refuses, or a column type the
// dialect stores as text no row scan decodes, fails construction.
func NewResource[T any](p *Provider, table string, opts ...ResourceOption) (*Resource[T], error) {
	c, err := newResourceCore(p, table, reflect.TypeFor[T](), generateSchemas[T], opts)
	if err != nil {
		return nil, err
	}
	applier, err := patch.NewApplier[T](c.gen, c.cfg)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	for _, schema := range []*patch.PatchSchema{c.patchSchema, c.serverPatchSchema} {
		err = applier.CheckSchema(schema)
		if err != nil {
			return nil, fmt.Errorf("parse: %s: %w", table, err)
		}
	}
	r := &Resource[T]{resourceCore: *c, applier: applier}
	err = r.declare(r.desc)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// declare declares desc's tables to the provider's database when the
// database holds its tables itself (backend.Declarer): the last step of
// building or relating a resource, so a refusal leaves nothing declared
// that the resource does not hold.
func (r *resourceCore) declare(desc *backend.Resource) error {
	d, ok := r.provider.db.(backend.Declarer)
	if !ok {
		return nil
	}
	err := d.Declare(desc)
	if err != nil {
		return fmt.Errorf("parse: %s: declaring the table: %w", r.table, err)
	}
	return nil
}

// newResourceCore is NewResource's resource without what depends on T:
// rowType is T, and generate generates the schemas T's struct tags declare.
func newResourceCore(p *Provider, table string, rowType reflect.Type, generate func(*core.SchemaGenerator) (Schemas, error), opts []ResourceOption) (*resourceCore, error) {
	cfg := resourceConfig{pk: []string{core.DefaultPrimaryKey}, pageSize: filter.DefaultListLimit, maxPage: filter.MaxListLimit}
	for _, opt := range opts {
		opt(&cfg)
	}
	if !core.IsIdentifier(table) {
		return nil, fmt.Errorf("parse: resource table %q is not a SQL identifier", table)
	}
	if p == nil {
		return nil, fmt.Errorf("parse: %s: no provider", table)
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
	r := &resourceCore{
		rowType:     rowType,
		table:       table,
		pk:          cfg.pk,
		tenant:      cfg.tenant,
		deleted:     cfg.deleted,
		pageSize:    cfg.pageSize,
		maxPageSize: cfg.maxPage,
		accepted:    slices.Clone(cfg.accepted),
		provider:    p,
		dialect:     p.dialect,
		exec:        p.db,
	}
	err := r.generateSchemas(parserOpts, generate)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	walk, err := r.gen.StructFields(rowType)
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
	err = checkKeyColumns(cfg.pk, columns, rowType)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	for _, c := range []struct{ option, column string }{{"tenant", cfg.tenant}, {"deleted", cfg.deleted}} {
		if c.column == "" {
			continue
		}
		if _, ok := columns[c.column]; !ok {
			return nil, fmt.Errorf("parse: %s: %s column %q is not a column of %s", table, c.option, c.column, gowalker.TypeText(rowType))
		}
	}
	if err := checkDeletedColumn(cfg, columns[cfg.deleted]); err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	if err := r.customizeSchemas(cfg.customize); err != nil {
		return nil, fmt.Errorf("parse: %s: customizing schemas: %w", table, err)
	}
	r.filterSchema.SystemFields = systemFields(table, cfg, columns, fieldsByColumn)
	err = checkSystemFields(r.filterSchema.SystemFields)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}
	r.filterSchema.PrimaryKey = slices.Clone(cfg.pk)
	r.setSearchParam()
	err = r.checkQueryParams(r.filterSchema)
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
	r.columnOrder = order
	r.columnFields = fieldsByColumn
	r.scalars, err = scalarColumns(r.gen, fieldsByColumn)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
	}

	read, err := readColumns(table, cfg, columns)
	if err != nil {
		return nil, err
	}
	if r.readExprs, err = newComputedColumns(table, cfg, columns, read); err != nil {
		return nil, err
	}
	if r.readExprs != nil {
		err = p.supports(table, backend.ComputedColumns)
		if err != nil {
			return nil, err
		}
	}
	r.filterSchema.Computed, err = r.readExprs.resolve(r.dialect)
	if err != nil {
		return nil, err
	}
	r.computedSQL = r.filterSchema.ComputedSQL()
	if err := r.readExprs.checkDocuments(r.filterSchema, r.sortSchema); err != nil {
		return nil, err
	}
	if err := r.checkComputedPatches(); err != nil {
		return nil, err
	}
	if err := r.checkIdentityPatches(); err != nil {
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
	r.desc = r.descriptor()
	r.scanRules, err = r.rowRules()
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", table, err)
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
					return nil, fmt.Errorf("parse: %s: relation %q correlates on %q, which is not a column of %s", table, f.ExternalName, c, gowalker.TypeText(rowType))
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
	if core.MarshalsJSON(t) {
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
func (r *resourceCore) admits(column string, need columnAccess) error {
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
func (r *resourceCore) checkUnreadColumns(fields map[string]*filter.FieldSchema, keys map[string]*filter.SortFieldSchema, search []filter.SearchField) error {
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
func (r *resourceCore) checkRelation(name string, rs *core.RelationSchema, target RelatedResource, returnsValues bool) error {
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

// MustResource is NewResource for declarations that cannot fail but for a
// programming error; it panics on a declaration error.
func MustResource[T any](p *Provider, table string, opts ...ResourceOption) *Resource[T] {
	r, err := NewResource[T](p, table, opts...)
	if err != nil {
		panic(err)
	}
	return r
}

// Table is the table the resource reads.
func (r *resourceCore) Table() string { return r.table }

// PrimaryKeyColumns are the key columns, in key order.
func (r *resourceCore) PrimaryKeyColumns() []string { return slices.Clone(r.pk) }

// TenantColumnName is the tenant column; "" for a resource without tenants.
func (r *resourceCore) TenantColumnName() string { return r.tenant }

// DeletedColumnName is the soft-delete column; "" without soft delete.
func (r *resourceCore) DeletedColumnName() string { return r.deleted }

// ColumnSQLType returns the SQL type a column is read and written as: its
// declaration (ColumnType), or, for a value type that declares none, the
// type its Go type implies (core.ImpliedSQLType: a civil date, an interval,
// an array of intervals). false means neither: the column holds its Go
// type's own representation. The returned value cannot mutate the
// resource's metadata.
func (r *resourceCore) ColumnSQLType(column string) (core.SQLType, bool) {
	t, ok := r.sqlTypes[column]
	return t, ok
}

// Columns is the projection every statement over the resource reads: a
// list's page, an update's RETURNING, and a repository's own reads of the
// table (Query). It is T's columns in field order, or the ReadColumns among
// them, table-qualified and quoted for the provider's dialect; a computed
// column (ReadExpr) is read through its expression and a column with a
// declared ColumnType as the dialect reads that type, each under the
// column's own name, so rows scan by name. Nil on a provider that compiles
// no SQL.
func (r *resourceCore) Columns() []string {
	d := r.dialect
	if d == nil {
		return nil
	}
	out := make([]string, len(r.read))
	for i, name := range r.read {
		stored := d.QuoteIdent(r.table) + "." + d.QuoteIdent(name)
		col := stored
		expr, computed := r.computedSQL[name]
		if computed {
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

// scalarColumns are the scalar types the storage of each document column
// of fields declares (filter.ScalarStorage).
func scalarColumns(g *core.SchemaGenerator, fields map[string]*core.Field) (map[string]core.TypeInfo, error) {
	scalars := map[string]core.TypeInfo{}
	for name, f := range fields {
		info, err := g.FieldType(f)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		if !core.IsDocument(info.Type) {
			continue
		}
		scalar, err := filter.ScalarStorage(g, f)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		if scalar.Type != "" {
			scalars[name] = scalar
		}
	}
	return scalars, nil
}

// storedTypeInfo is the type of the values column holds: its field's
// (columnTypeInfo), or the scalar its storage declares a document field's
// column holds.
func (r *resourceCore) storedTypeInfo(column string) core.TypeInfo {
	info := r.columnTypeInfo(column)
	scalar, ok := r.scalars[column]
	if ok {
		scalar.SQLType = info.SQLType
		return scalar
	}
	return info
}

// descriptor is the resource's descriptor (backend.Resource), with its
// projection for a provider with a dialect.
func (r *resourceCore) descriptor() *backend.Resource {
	table := make([]backend.Column, len(r.columnOrder))
	for i, name := range r.columnOrder {
		table[i] = backend.Column{Name: name, Type: r.storedTypeInfo(name)}
	}
	desc := &backend.Resource{
		Table:          r.table,
		Columns:        slices.Clone(r.read),
		Key:            slices.Clone(r.pk),
		KeyTypes:       r.keyColumns().types,
		TableColumns:   table,
		Tenant:         r.tenant,
		Deleted:        r.deleted,
		Schema:         r.filterSchema,
		PatchRelations: r.patchRelations(),
		Computed:       r.readExprs != nil,
	}
	if r.dialect != nil {
		desc.SQL = &backend.SQLResource{Projection: strings.Join(r.Columns(), ", ")}
	}
	return desc
}

// patchRelations are the many-to-many relations the resource's patch
// schemas write that its filter schema does not hold, by field name
// (backend.Resource.PatchRelations).
func (r *resourceCore) patchRelations() map[string]*core.RelationSchema {
	var relations map[string]*core.RelationSchema
	for _, schema := range []*patch.PatchSchema{r.patchSchema, r.serverPatchSchema} {
		for key, path := range schema.Paths() {
			rel := path.Relation
			if rel == nil || rel.Kind != core.ManyToMany {
				continue
			}
			name := strings.TrimPrefix(key, "/")
			field, filtered := r.filterSchema.Fields[name]
			if filtered && field.Relation != nil {
				continue
			}
			if relations == nil {
				relations = map[string]*core.RelationSchema{}
			}
			relations[name] = rel
		}
	}
	return relations
}

// ColumnNames is the unqualified name of each Columns entry, in the same
// order: the name every row's value scans under.
func (r *resourceCore) ColumnNames() []string { return slices.Clone(r.read) }

// Key reads the row's key from a model value or a pointer to one.
func (r *resourceCore) Key(item any) (Key, error) {
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
func (r *resourceCore) columnValue(rv reflect.Value, column string) (any, error) {
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
	model := r.rowType
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	if rv.Type() != model {
		return nil, fmt.Errorf("parse: %s: %s is not %s", r.table, gowalker.TypeText(rv.Type()), gowalker.TypeText(r.rowType))
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
	case !core.IsNullableType(t):
		return fmt.Errorf("deleted column %q is %s, which cannot hold NULL: a row is live while the column is NULL, so declare a pointer or a nullable type such as sql.NullTime", cfg.deleted, gowalker.TypeText(t))
	}
	return nil
}

// checkIdentityPatches refuses a patch path, request or server, that writes
// the key or the tenant column. An update's WHERE holds the scope's tenant
// and the row's key, so such a write would move the row out of its tenant
// or away from every relation correlated on its key; a tenant transfer is
// not a patch.
func (r *resourceCore) checkIdentityPatches() error {
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
func (r *resourceCore) checkIdentityWrite(column string) error {
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
// key's filter operands are JSON strings). A key column takes the operators
// its type orders and compares by (filter.KeyOperators), and is Indexed,
// since a primary key is an index by construction, so a filter on it costs
// as one.
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
		key := field(column, filter.KeyOperators(core.TypeOf(columns[column]).Type)...)
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

// checkSystemFields holds every system field to its type's operators, as
// schema generation holds a declared field (filter.CheckOperators).
func checkSystemFields(fields map[string]*filter.FieldSchema) error {
	for _, column := range slices.Sorted(maps.Keys(fields)) {
		err := filter.CheckOperators(fields[column])
		if err != nil {
			return fmt.Errorf("system field %q: %w", column, err)
		}
	}
	return nil
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
	fs, ss, err := r.pathSchemas(decls)
	if err != nil {
		return nil, err
	}
	view := *r
	view.filterSchema, view.sortSchema, view.view = fs, ss, true
	desc := *r.desc
	desc.Schema = fs
	view.desc = &desc
	return &view, nil
}

// pathSchemas are r's filter and sort schemas with decls declared
// (WithPaths).
func (r *resourceCore) pathSchemas(decls []filter.PathDecl) (*filter.ResourceSchema, *filter.SortSchema, error) {
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
			return nil, nil, fmt.Errorf("parse: %s: path %q: %w", r.table, d.Path, err)
		}
		if w != "" {
			return nil, nil, fmt.Errorf("parse: %s: path %q: json:\",string\" stores the member as text, which its path cannot compare; drop the option, or declare the member a string", r.table, d.Path)
		}
	}
	fs, err := r.filterSchema.WithPaths(decls...)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	err = r.checkQueryParams(fs)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	ss, err := r.sortSchema.WithPaths(r.filterSchema, decls...)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	if err := r.readExprs.checkDocuments(fs, ss); err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	return fs, ss, nil
}

// generateSchemas builds the parser configuration from opts, and from it
// the schemas T's struct tags declare (generate).
func (r *resourceCore) generateSchemas(opts []core.Option, generate func(*core.SchemaGenerator) (Schemas, error)) error {
	cfg := core.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	err := cfg.Validate()
	if err != nil {
		return fmt.Errorf("parser options: %w", err)
	}
	cfg = cfg.Effective()
	gen := core.NewSchemaGenerator(cfg.GeneratorOpts...)
	s, err := generate(gen)
	if err != nil {
		return err
	}
	r.filterSchema, r.sortSchema, r.searchSchema = s.Filter, s.Sort, s.Search
	r.patchSchema, r.serverPatchSchema = s.Patch, s.ServerPatch
	r.cfg, r.gen, r.codec = cfg, gen, cfg.GetCodec()
	return nil
}

// generateSchemas generates the filter, sort, search and patch schemas T's
// struct tags declare.
func generateSchemas[T any](gen *core.SchemaGenerator) (Schemas, error) {
	var (
		s   Schemas
		err error
	)
	s.Filter, err = filter.GenerateFilterSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating filter schema: %w", err)
	}
	s.Patch, err = patch.GeneratePatchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating patch schema: %w", err)
	}
	s.ServerPatch, err = patch.GenerateServerPatchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating server patch schema: %w", err)
	}
	s.Sort, err = filter.GenerateSortSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("generating sort schema: %w", err)
	}
	s.Search, err = filter.GenerateSearchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating search schema: %w", err)
	}
	return s, nil
}

// FilterSchema returns a copy of the filter schema: changing it changes
// nothing the resource parses. Customize the schemas when the resource is
// built (CustomizeSchemas).
func (r *resourceCore) FilterSchema() *filter.ResourceSchema {
	return r.filterSchema.Clone()
}

// PatchSchema returns a copy of the patch schema; see FilterSchema.
func (r *resourceCore) PatchSchema() *patch.PatchSchema {
	return r.patchSchema.Clone()
}

// ServerPatchSchema returns a copy of the schema of the server's own writes:
// the patch schema's paths and the `parse:"server"` fields. Build a patch
// against it only for a write the server makes, never from a request.
func (r *resourceCore) ServerPatchSchema() *patch.PatchSchema {
	return r.serverPatchSchema.Clone()
}

// SortSchema returns a copy of the sort schema; see FilterSchema.
func (r *resourceCore) SortSchema() *filter.SortSchema {
	return r.sortSchema.Clone()
}

// SearchSchema returns a copy of the search schema, nil when T has no
// search fields; see FilterSchema.
func (r *resourceCore) SearchSchema() *filter.SearchSchema {
	return r.searchSchema.Clone()
}
