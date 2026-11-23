package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// GeneratorOption configures a SchemaGenerator.
type GeneratorOption func(*GeneratorOptions)

// GeneratorOptions holds options for schema generation. All fields have
// sensible defaults — override only what you need.
type GeneratorOptions struct {
	// SQLTableName overrides the derived table name for this specific type.
	// Takes precedence over TableConvention.
	SQLTableName string

	// ColumnTypes declares physical storage independently of Go field shape.
	ColumnTypes map[string]SQLType

	// ColumnMapper turns each field's column, as ColumnTag and
	// ColumnConvention name it, into the SQL expression that reads it.
	ColumnMapper SQLColumnMapper

	// ColumnConvention transforms field names to SQL column names.
	// Default: SnakeCase.
	ColumnConvention NamingConvention

	// TableNameConvention transforms type names to SQL table names.
	// Default: PluralSnake ("Product" → "products").
	// Ignored when SQLTableName is set.
	TableNameConvention TableConvention

	// FieldNameTag is the struct tag to read for external field names.
	// Default: "json". Set to "bson", "yaml", etc. for non-JSON APIs.
	FieldNameTag string

	// ColumnTag is the struct tag to read for SQL column names.
	// Default: "db". Set to "sql", "column", etc. for your ORM.
	ColumnTag string

	// DefaultFilterOps maps FieldType to default filter operators when the
	// filter_ops tag is omitted.
	DefaultFilterOps map[FieldType][]string

	// DefaultPatchOps maps FieldType to default patch operations when the
	// patch_ops tag is omitted.
	DefaultPatchOps map[FieldType][]string

	// SortMaxFields caps how many sort fields a generated SortSchema accepts.
	// Zero means the default of 3.
	SortMaxFields int

	// SearchParam is the query-string parameter name a generated SearchSchema
	// advertises for its free-text search box, and a resource's ParseList
	// reads the search from beside "search". Empty means "search"
	// (filter.ParamSearch). A list body's member stays "search".
	SearchParam string

	// SearchLanguage is the PostgreSQL text search configuration a
	// generated SearchSchema searches with (`to_tsvector(<lang>, …)`). Empty
	// means "english". It must be a (schema-qualified) identifier.
	SearchLanguage string

	// ForeignTagKeys are struct tag keys another library reads, left alone
	// though one is a character from a key parse reads ("path" beside
	// "paths"). Any other such key fails schema generation as a misspelling.
	ForeignTagKeys []string
}

// SQLColumnMapper maps a field's column to the SQL expression that reads it.
// The expression is emitted as returned, so it quotes any name a statement
// must quote (a reserved word, a mixed-case column). MapColumn is handed the
// column ColumnName names: the column tag's name,
// else ColumnConvention (default snake_case) applied to the field's external
// name, or to its Go name when the field-name tag hides it. Every view of a
// field (filter, sort, search, server policies) hands it that one column,
// the name patch SET clauses and column type declarations use too.
//
// A cursor reads its keys' values from the row's columns, not from the
// expressions, so a sort key's expression must order as its column's value
// does: qualify, quote or cast it, but do not transform it (lower(...)
// orders differently from the value a cursor resumes after).
type SQLColumnMapper interface {
	MapColumn(table, column string, fieldType FieldType) string
}

// WithSQLTableName overrides the table name for a specific type.
func WithSQLTableName(name string) GeneratorOption {
	return func(o *GeneratorOptions) { o.SQLTableName = name }
}

// WithColumnMapper sets the mapper that turns columns into SQL expressions.
func WithColumnMapper(mapper SQLColumnMapper) GeneratorOption {
	return func(o *GeneratorOptions) { o.ColumnMapper = mapper }
}

// WithColumnConvention sets the naming convention for SQL columns.
// Default: SnakeCase. Common alternatives: CamelCase, LowerCase.
//
//	core.WithColumnConvention(core.CamelCase)
func WithColumnConvention(conv NamingConvention) GeneratorOption {
	return func(o *GeneratorOptions) { o.ColumnConvention = conv }
}

// WithTableConvention sets the naming convention for SQL table names.
// Default: PluralSnake ("Product" → "products").
//
//	core.WithTableConvention(core.Singular)      // "product"
//	core.WithTableConvention(core.PluralCamel)   // "products"
func WithTableConvention(conv TableConvention) GeneratorOption {
	return func(o *GeneratorOptions) { o.TableNameConvention = conv }
}

// WithFieldNameTag sets which struct tag provides the external field name.
// Default: "json". Use for APIs that serialize with a different format.
//
//	core.WithFieldNameTag("bson")   // read from `bson:"field_name"`
//	core.WithFieldNameTag("yaml")   // read from `yaml:"field_name"`
func WithFieldNameTag(tag string) GeneratorOption {
	return func(o *GeneratorOptions) { o.FieldNameTag = tag }
}

// WithColumnTag sets which struct tag provides the SQL column name.
// Default: "db". The column tag value takes precedence over the naming
// convention.
//
//	core.WithColumnTag("sql")      // read from `sql:"column_name"`
//	core.WithColumnTag("column")   // read from `column:"col_name"`
func WithColumnTag(tag string) GeneratorOption {
	return func(o *GeneratorOptions) { o.ColumnTag = tag }
}

// WithForeignTagKeys names struct tag keys another library reads, which
// schema generation would otherwise refuse as misspellings of keys parse
// reads: a router's path beside paths, say. Calls accumulate.
func WithForeignTagKeys(keys ...string) GeneratorOption {
	return func(o *GeneratorOptions) { o.ForeignTagKeys = append(o.ForeignTagKeys, keys...) }
}

// WithSortMaxFields caps how many sort fields a generated SortSchema accepts.
// Default 3.
func WithSortMaxFields(n int) GeneratorOption {
	return func(o *GeneratorOptions) { o.SortMaxFields = n }
}

// WithSearchParam sets the query-string parameter name a generated
// SearchSchema advertises for its free-text search box. Default "search"
// (filter.ParamSearch). A resource built with it reads the search from p
// and from "search", and refuses at construction a p that is another list
// parameter, a parameter it accepts (AcceptParams) or a flat filter on one
// of its fields.
func WithSearchParam(p string) GeneratorOption {
	return func(o *GeneratorOptions) { o.SearchParam = p }
}

// WithSearchLanguage sets the PostgreSQL text search configuration a
// generated SearchSchema searches with. Default "english".
func WithSearchLanguage(lang string) GeneratorOption {
	return func(o *GeneratorOptions) { o.SearchLanguage = lang }
}

// --- Schema Generator ---

// SchemaGenerator generates schemas from Go struct types using configurable
// naming conventions and struct tag keys.
type SchemaGenerator struct {
	// opts are fixed when the generator is built: every schema it generates
	// for the life of a resource reads the same options.
	opts GeneratorOptions
}

// NewSchemaGenerator creates a SchemaGenerator with the given options. The
// maps and slices the options hand in are copied, so a later write to them
// cannot change schemas the generator builds.
func NewSchemaGenerator(opts ...GeneratorOption) *SchemaGenerator {
	var o GeneratorOptions
	for _, opt := range opts {
		opt(&o)
	}
	o.ColumnTypes = maps.Clone(o.ColumnTypes)
	o.DefaultFilterOps = cloneOpNames(o.DefaultFilterOps)
	o.DefaultPatchOps = cloneOpNames(o.DefaultPatchOps)
	o.ForeignTagKeys = slices.Clone(o.ForeignTagKeys)
	return &SchemaGenerator{opts: o}
}

func cloneOpNames(m map[FieldType][]string) map[FieldType][]string {
	if m == nil {
		return nil
	}
	out := make(map[FieldType][]string, len(m))
	for t, names := range m {
		out[t] = slices.Clone(names)
	}
	return out
}

// DefaultFilterOps is GeneratorOptions.DefaultFilterOps for t: the operator
// names a field of type t defaults to, and whether the options name t.
func (g *SchemaGenerator) DefaultFilterOps(t FieldType) ([]string, bool) {
	names, ok := g.opts.DefaultFilterOps[t]
	return slices.Clone(names), ok
}

// DefaultPatchOps is GeneratorOptions.DefaultPatchOps for t: the operation
// names a path of type t defaults to, and whether the options name t.
func (g *SchemaGenerator) DefaultPatchOps(t FieldType) ([]string, bool) {
	names, ok := g.opts.DefaultPatchOps[t]
	return slices.Clone(names), ok
}

// SortMaxFields is GeneratorOptions.SortMaxFields: zero is the default.
func (g *SchemaGenerator) SortMaxFields() int { return g.opts.SortMaxFields }

// SearchParam is GeneratorOptions.SearchParam: empty is the default.
func (g *SchemaGenerator) SearchParam() string { return g.opts.SearchParam }

// SearchLanguage is GeneratorOptions.SearchLanguage: empty is the default.
func (g *SchemaGenerator) SearchLanguage() string { return g.opts.SearchLanguage }

// MapsColumns reports whether a GeneratorOptions.ColumnMapper turns columns
// into expressions.
func (g *SchemaGenerator) MapsColumns() bool { return g.opts.ColumnMapper != nil }

// TableName derives the SQL table name for a type.
// Priority: SQLTableName (explicit) → TableNameConvention → PluralSnake default.
// Statements quote the name, so it reads the table spelt exactly so; anything
// but a plain identifier (IsIdentifier) is an error at schema generation.
func (g *SchemaGenerator) TableName(t interface{ Name() string }) (string, error) {
	name := PluralSnake(t.Name())
	switch {
	case g.opts.SQLTableName != "":
		name = g.opts.SQLTableName
	case g.opts.TableNameConvention != nil:
		name = g.opts.TableNameConvention(t.Name())
	}
	if !IsIdentifier(name) {
		return "", fmt.Errorf("table name %q is not a SQL identifier", name)
	}
	return name, nil
}

// MappedExpr is the expression the ColumnMapper writes for the column
// ColumnName names for field, or "" without a mapper, when statements read
// the column itself, qualified by table and quoted by the dialect; the
// column must then be a plain identifier. A mapper's expressions are the
// developer's own SQL and are emitted as returned, so its column is not
// checked.
func (g *SchemaGenerator) MappedExpr(table, field string, fieldType FieldType, dbTag string) (string, error) {
	if g.opts.ColumnMapper != nil {
		return g.opts.ColumnMapper.MapColumn(table, g.column(field, dbTag), fieldType), nil
	}
	if _, err := g.ColumnName(field, dbTag); err != nil {
		return "", err
	}
	return "", nil
}

// ColumnName returns the column name a field reads, for every statement:
// the dialect qualifies and quotes it.
// Priority: columnTag value → ColumnConvention → SnakeCase default.
func (g *SchemaGenerator) ColumnName(field, dbTag string) (string, error) {
	col := g.column(field, dbTag)
	if !IsIdentifier(col) {
		return "", fmt.Errorf("column %q for field %s is not a SQL identifier", col, field)
	}
	return col, nil
}

// column is ColumnName without the identifier check.
func (g *SchemaGenerator) column(field, dbTag string) string {
	switch {
	case dbTag != "" && dbTag != "-":
		col, _, _ := strings.Cut(dbTag, ",")
		return col
	case g.opts.ColumnConvention != nil:
		return g.opts.ColumnConvention(field)
	}
	return SnakeCase(field)
}

// JSONTagKey is the struct tag encoding/json names members by, and the
// default FieldNameTag; DefaultColumnTagKey is the default ColumnTag.
const (
	JSONTagKey          = "json"
	DefaultColumnTagKey = "db"
)

// FieldNameTag returns the struct tag key to read for external field names.
// Default: JSONTagKey.
func (g *SchemaGenerator) FieldNameTag() string {
	if g.opts.FieldNameTag != "" {
		return g.opts.FieldNameTag
	}
	return JSONTagKey
}

// ColumnTagKey returns the struct tag key to read for SQL column names.
// Default: DefaultColumnTagKey.
func (g *SchemaGenerator) ColumnTagKey() string {
	if g.opts.ColumnTag != "" {
		return g.opts.ColumnTag
	}
	return DefaultColumnTagKey
}
