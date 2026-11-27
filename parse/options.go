package parse

import (
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

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
// network value, or a pointer to or nullable wrapper (core.NullWrapperOf)
// of one. Every statement compares it by value, as a filter
// compares its type (a decimal stored as "1.50" is the key 1.5), and Go
// tells keys apart by their canonical text (core.KeyTextOf). A document,
// array or geometry column has no equality to identify a row by, and
// NewResource refuses it as a key column.
func PrimaryKey(columns ...string) ResourceOption {
	return func(c *resourceConfig) { c.pk = slices.Clone(columns) }
}

// TenantColumn names the column that holds a row's tenant. Every list is then
// held to Scope.Tenant, and so are tenant-scoped relation junctions. The
// column's field is a string, a UUID or an integer, or a pointer to or
// nullable wrapper (core.NullWrapperOf) of one; Scope.Tenant is
// normalized to it, and to every tenant column the resource's relations
// reach (Relate).
func TenantColumn(column string) ResourceOption {
	return func(c *resourceConfig) { c.tenant = column }
}

// DeletedColumn names the soft-delete timestamp: a row is live while it is
// NULL, so its field must hold NULL (core.IsNullableType: a pointer, slice,
// map or interface, or a type such as sql.NullTime whose zero value writes
// NULL and scans NULL back; an sql.Scanner with no Value, or whose zero
// value writes a value, does not), and it is neither the key nor the tenant
// column. Lists exclude deleted rows unless Scope.IncludeDeleted; updates
// never reach them.
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
// (filter.Param*) nor a filterable field is an error (E2071). A name that is
// a list parameter, the search parameter, or a flat filter on a request
// field (filter.CheckAcceptedParam) is refused by NewResource, and by Relate
// and WithPaths for the fields they add: ParseList would skip that filter.
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
// built: a patch path pattern (patch.PatchSchema.AddPath), a patch path's
// validator or narrower operation set (UpdatePath), a filter field's
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
