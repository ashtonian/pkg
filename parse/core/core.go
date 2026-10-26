package core

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Codec serializes request documents and typed patch round trips.
// StdlibJSON is the default; WithCodec selects another Encode/Decode
// implementation. Cursors use an independent, fixed wire format.
type Codec interface {
	Encode(v any) ([]byte, error)
	Decode(data []byte, v any) error
}

// StdlibJSON is the default Codec: the standard library's JSON, with
// encoding/json's data model (field names, omitempty, durations as
// nanoseconds), so a document reads back the way responses are written.
// Decode is stricter than encoding/json's Unmarshal: a
// duplicate object member name, invalid UTF-8 or data after the value is an
// error, so a request cannot say a field twice and have one value win. It
// preserves numbers exactly: a number landing in an `any` becomes a
// json.Number rather than a float64, so integers beyond ±2^53 reach
// validation intact.
type StdlibJSON struct{}

func (StdlibJSON) Encode(v any) ([]byte, error) { return json.Marshal(v) }

func (StdlibJSON) Decode(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v, requestJSON)
}

// requestJSON is how StdlibJSON decodes: encoding/json's options with
// duplicate names and invalid UTF-8 refused, and numbers in an `any` kept
// as their literal.
var requestJSON = jsonv2.JoinOptions(
	json.DefaultOptionsV1(),
	jsontext.AllowDuplicateNames(false),
	jsontext.AllowInvalidUTF8(false),
	jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *any) error {
		if dec.PeekKind() != '0' {
			return errors.ErrUnsupported
		}
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		*v = json.Number(raw)
		return nil
	})),
)

// defaultCodec is the package-level codec used when no codec is configured
// on a ParserConfig. Unexported — consumers either set
// ParserConfig.Codec explicitly or rely on GetCodec()'s fallback.
var defaultCodec Codec = StdlibJSON{}

// --- Field Types ---

// FieldType represents the data type of a field in filter and patch schemas.
type FieldType string

const (
	FieldTypeString    FieldType = "string"
	FieldTypeInt       FieldType = "int"
	FieldTypeFloat     FieldType = "float"
	FieldTypeBool      FieldType = "bool"
	FieldTypeTimestamp FieldType = "timestamp"
	FieldTypeDate      FieldType = "date"
	FieldTypeUUID      FieldType = "uuid"
	FieldTypeJSON      FieldType = "json"
	FieldTypeArray     FieldType = "array"
	FieldTypeObject    FieldType = "object"
	FieldTypeAny       FieldType = "any"
	FieldTypeGeo       FieldType = "geo"
)

// SQLType declares a resource column's storage role and any dialect read cast.
// JSON and native arrays remain distinct even when their Go shapes coincide.
type SQLType string

const (
	SQLDate          SQLType = "date"
	SQLInet          SQLType = "inet"
	SQLCIDR          SQLType = "cidr"
	SQLInterval      SQLType = "interval"
	SQLInetArray     SQLType = "inet[]"
	SQLCIDRArray     SQLType = "cidr[]"
	SQLIntervalArray SQLType = "interval[]"
	SQLJSON          SQLType = "json"
	SQLJSONB         SQLType = "jsonb"
	SQLTextArray     SQLType = "text[]"
	SQLInt32Array    SQLType = "integer[]"
	SQLUUIDArray     SQLType = "uuid[]"
)

// Valid reports whether t is one of the SQLType constants.
func (t SQLType) Valid() bool {
	switch t {
	case SQLDate, SQLInet, SQLCIDR, SQLInterval, SQLInetArray, SQLCIDRArray, SQLIntervalArray,
		SQLJSON, SQLJSONB, SQLTextArray, SQLInt32Array, SQLUUIDArray:
		return true
	}
	return false
}

// IsArray reports a declared native SQL array storage role.
func (t SQLType) IsArray() bool { return strings.HasSuffix(string(t), "[]") }

// IsJSON reports a declared JSON document storage role, including scalar JSON.
func (t SQLType) IsJSON() bool { return t == SQLJSON || t == SQLJSONB }

// Validator is a function that validates a value.
type Validator func(value any) error

// --- Error Types ---

// ParseErrorKind identifies the subsystem that produced the error.
type ParseErrorKind string

const (
	ParseErrorKindFilter ParseErrorKind = "filter"
	ParseErrorKindPatch  ParseErrorKind = "patch"
	ParseErrorKindSort   ParseErrorKind = "sort"
	ParseErrorKindSchema ParseErrorKind = "schema"
)

// ParseError is the single error type for all parse operations.
type ParseError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Kind    ParseErrorKind `json:"kind"`
	Path    string         `json:"path,omitempty"`
	Index   *int           `json:"index,omitempty"` // pointer so op index 0 survives the wire
	Details map[string]any `json:"details,omitempty"`
	Cause   error          `json:"-"`
}

// Error implements the error interface.
func (e *ParseError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause for errors.Is/As compatibility.
func (e *ParseError) Unwrap() error {
	return e.Cause
}

// Is reports whether target carries the same E-code — any error with an
// ErrorCode() string method, including Code — so a
// ParseError matches its code's sentinel across wrap chains. An empty code matches nothing; Cause stays
// reachable through Unwrap.
func (e *ParseError) Is(target error) bool {
	c, ok := target.(interface{ ErrorCode() string })
	return ok && e.Code != "" && c.ErrorCode() == e.Code
}

// ErrorCode returns the E-code string for middleware introspection.
func (e *ParseError) ErrorCode() string { return e.Code }

// ErrorMessage returns the human-readable message.
func (e *ParseError) ErrorMessage() string { return e.Message }

// ErrorHTTPStatus returns the code's status, or 422 for unknown codes.
func (e *ParseError) ErrorHTTPStatus() int { return Code(e.Code).ErrorHTTPStatus() }

// IsErrorUserFacing reports the message safe to show the caller:
// it describes the request, never the database.
func (e *ParseError) IsErrorUserFacing() bool { return true }

// --- Parser Configuration ---

// ParserConfig holds shared configuration for filter and patch parsers.
type ParserConfig struct {
	// MaxDepth bounds request structure depth: filter boolean nesting,
	// JSON filter document nesting and patch JSON Pointer segments.
	MaxDepth int
	// MaxCost bounds a filter's estimated cost: about 1 per indexed
	// condition, 10 per unindexed one, more for documents, relations,
	// unanchored patterns and $or.
	MaxCost int
	// MaxInputSize bounds a filter or patch body in bytes.
	MaxInputSize int
	// MaxConditions bounds a filter's leaf conditions.
	MaxConditions int
	// MaxArraySize bounds $in / $nin members, array $contains elements and
	// the values in a JSON filter document.
	MaxArraySize int
	// MaxPatchCopyBytes bounds the total bytes a patch's copy operations may
	// add to a document during typed apply (copy amplification guard).
	MaxPatchCopyBytes int64
	GeneratorOpts     []GeneratorOption
	Codec             Codec            // Wire format codec. Nil = encoding/json fallback.
	Clock             func() time.Time // Relative-time anchor (`now-7d`). Nil = time.Now().UTC.
}

// GetCodec returns the configured codec, falling back to the package
// default (StdlibJSON) when none is configured.
func (c *ParserConfig) GetCodec() Codec {
	if c != nil && c.Codec != nil {
		return c.Codec
	}
	return defaultCodec
}

// Option configures a Parser.
type Option func(*ParserConfig)

// DefaultConfig returns a ParserConfig with sensible defaults.
func DefaultConfig() *ParserConfig {
	return &ParserConfig{
		MaxDepth:          5,
		MaxCost:           50,
		MaxInputSize:      64 * 1024,
		MaxConditions:     20,
		MaxArraySize:      100,
		MaxPatchCopyBytes: 1 << 20,
	}
}

// WithMaxPatchCopyBytes bounds the bytes a patch's copy operations may add to
// a document during typed apply. Default 1 MiB.
func WithMaxPatchCopyBytes(n int64) Option {
	return func(c *ParserConfig) { c.MaxPatchCopyBytes = n }
}

// WithMaxDepth sets the maximum request depth: filter boolean nesting, JSON
// filter document nesting and patch JSON Pointer segments.
func WithMaxDepth(depth int) Option {
	return func(c *ParserConfig) { c.MaxDepth = depth }
}

// WithMaxCost sets the maximum estimated query cost.
func WithMaxCost(cost int) Option {
	return func(c *ParserConfig) { c.MaxCost = cost }
}

// WithMaxInputSize sets the maximum input size in bytes.
func WithMaxInputSize(size int) Option {
	return func(c *ParserConfig) { c.MaxInputSize = size }
}

// WithMaxConditions sets the maximum number of filter conditions.
func WithMaxConditions(n int) Option {
	return func(c *ParserConfig) { c.MaxConditions = n }
}

// WithMaxArraySize bounds $in / $nin members, array $contains elements and
// the values in a JSON filter document.
func WithMaxArraySize(n int) Option {
	return func(c *ParserConfig) { c.MaxArraySize = n }
}

// WithGeneratorOptions sets GeneratorOptions used by typed parsers.
func WithGeneratorOptions(opts ...GeneratorOption) Option {
	return func(c *ParserConfig) { c.GeneratorOpts = opts }
}

// WithClock pins the anchor for relative-time tokens (`now`, `now-7d`).
// Default: time.Now().UTC. Tests pass a frozen clock for deterministic
// resolution.
func WithClock(now func() time.Time) Option {
	return func(c *ParserConfig) { c.Clock = now }
}

// WithCodec sets the codec that decodes filter and patch bodies and
// round-trips documents during typed patch application. Default:
// StdlibJSON (numbers decode exactly). Cursors do not use it: their
// encoding is owned by the keyset cursor codec.
//
//	core.WithCodec(myMsgpackCodec{})
func WithCodec(codec Codec) Option {
	return func(c *ParserConfig) { c.Codec = codec }
}

// --- Naming Conventions ---

// NamingConvention transforms an identifier string according to a convention.
type NamingConvention func(string) string

// Built-in naming conventions.
var (
	// SnakeCase converts PascalCase/camelCase to snake_case (default).
	SnakeCase NamingConvention = ToSnakeCase

	// CamelCase converts PascalCase to camelCase (first letter lowercase).
	CamelCase NamingConvention = toCamelCase

	// PascalCase returns the string unchanged (Go's default naming).
	PascalCase NamingConvention = func(s string) string { return s }

	// LowerCase converts to all lowercase with no separators.
	LowerCase NamingConvention = strings.ToLower
)

// TableConvention transforms a type name into a SQL table name.
type TableConvention func(string) string

// Built-in table conventions.
var (
	// PluralSnake converts PascalCase to plural snake_case: "Product" → "products" (default).
	PluralSnake TableConvention = func(s string) string { return Pluralize(ToSnakeCase(s)) }

	// Singular converts PascalCase to singular snake_case: "Product" → "product".
	Singular TableConvention = func(s string) string { return ToSnakeCase(s) }

	// PluralCamel converts PascalCase to plural camelCase: "Product" → "products".
	PluralCamel TableConvention = func(s string) string { return Pluralize(toCamelCase(s)) }
)

// Pluralize returns the English plural form of a word using common rules.
// Handles -y → -ies, sibilant suffixes → -es, and default -s.
func Pluralize(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "ies") || strings.HasSuffix(lower, "ses") ||
		strings.HasSuffix(lower, "xes") || strings.HasSuffix(lower, "zes") ||
		strings.HasSuffix(lower, "ches") || strings.HasSuffix(lower, "shes"):
		return s // already plural
	case strings.HasSuffix(lower, "y"):
		if len(lower) >= 2 && !isVowel(lower[len(lower)-2]) {
			return s[:len(s)-1] + "ies"
		}
		return s + "s"
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"),
		strings.HasSuffix(lower, "z"), strings.HasSuffix(lower, "ch"),
		strings.HasSuffix(lower, "sh"):
		return s + "es"
	default:
		return s + "s"
	}
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return true
	}
	return false
}

// --- Generator Options ---

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

	// ColumnMapper provides full control over column expression generation.
	// Takes precedence over ColumnConvention and ColumnTag.
	ColumnMapper SQLColumnMapper

	// ColumnConvention transforms field names to SQL column names.
	// Default: SnakeCase. Ignored when ColumnMapper is set.
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
	// advertises for its free-text search box. Empty means "search", the
	// parameter list handlers read (filter.ParamSearch).
	SearchParam string
}

// SQLColumnMapper maps struct fields to SQL column expressions.
// Implement this for full control over column naming — it takes precedence
// over ColumnConvention and the db/column tag.
type SQLColumnMapper interface {
	MapColumn(table, field string, fieldType FieldType) string
}

// WithSQLTableName overrides the table name for a specific type.
func WithSQLTableName(name string) GeneratorOption {
	return func(o *GeneratorOptions) { o.SQLTableName = name }
}

// WithColumnMapper sets a custom column mapper (full control).
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

// WithSortMaxFields caps how many sort fields a generated SortSchema accepts.
// Default 3.
func WithSortMaxFields(n int) GeneratorOption {
	return func(o *GeneratorOptions) { o.SortMaxFields = n }
}

// WithSearchParam sets the query-string parameter name a generated
// SearchSchema advertises for its free-text search box. Default "search"
// (filter.ParamSearch).
func WithSearchParam(p string) GeneratorOption {
	return func(o *GeneratorOptions) { o.SearchParam = p }
}

// --- Schema Generator ---

// SchemaGenerator generates schemas from Go struct types using configurable
// naming conventions and struct tag keys.
type SchemaGenerator struct {
	Opts *GeneratorOptions
}

// NewSchemaGenerator creates a SchemaGenerator with the given options.
func NewSchemaGenerator(opts ...GeneratorOption) *SchemaGenerator {
	o := &GeneratorOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return &SchemaGenerator{Opts: o}
}

// TableName derives the SQL table name for a type.
// Priority: SQLTableName (explicit) → TableNameConvention → PluralSnake default.
// The name is emitted into SQL verbatim, so anything but a plain identifier
// is an error at schema generation.
func (g *SchemaGenerator) TableName(t interface{ Name() string }) (string, error) {
	name := PluralSnake(t.Name())
	switch {
	case g.Opts.SQLTableName != "":
		name = g.Opts.SQLTableName
	case g.Opts.TableNameConvention != nil:
		name = g.Opts.TableNameConvention(t.Name())
	}
	if !IsIdentifier(name) {
		return "", fmt.Errorf("table name %q is not a SQL identifier", name)
	}
	return name, nil
}

// ColumnExpr builds the SQL column expression for a field.
// Priority: ColumnMapper → columnTag value → ColumnConvention → SnakeCase default.
// Without a ColumnMapper the result is "table.column" and the column must be
// a plain identifier; a mapper's expressions are the developer's own SQL and
// are emitted as returned.
func (g *SchemaGenerator) ColumnExpr(table, field string, fieldType FieldType, dbTag string) (string, error) {
	if g.Opts.ColumnMapper != nil {
		return g.Opts.ColumnMapper.MapColumn(table, field, fieldType), nil
	}
	col, err := g.ColumnName(field, dbTag)
	if err != nil {
		return "", err
	}
	return table + "." + col, nil
}

// ColumnName returns just the column name (without table prefix), for patch
// SQL SET clauses and cursor row reads.
// Priority: columnTag value → ColumnConvention → SnakeCase default.
func (g *SchemaGenerator) ColumnName(field, dbTag string) (string, error) {
	col := ToSnakeCase(field)
	switch {
	case dbTag != "" && dbTag != "-":
		col, _, _ = strings.Cut(dbTag, ",")
	case g.Opts.ColumnConvention != nil:
		col = g.Opts.ColumnConvention(field)
	}
	if !IsIdentifier(col) {
		return "", fmt.Errorf("column %q for field %s is not a SQL identifier", col, field)
	}
	return col, nil
}

// FieldNameTag returns the struct tag key to read for external field names.
// Default: "json".
func (g *SchemaGenerator) FieldNameTag() string {
	if g.Opts.FieldNameTag != "" {
		return g.Opts.FieldNameTag
	}
	return "json"
}

// ColumnTagKey returns the struct tag key to read for SQL column names.
// Default: "db".
func (g *SchemaGenerator) ColumnTagKey() string {
	if g.Opts.ColumnTag != "" {
		return g.Opts.ColumnTag
	}
	return "db"
}

// --- Policy Helpers (shared by filter and patch) ---

// PolicySubject describes the actor making the request.
// Used by both filter and patch policy enforcement.
type PolicySubject struct {
	ID          string
	TenantID    string
	TenantPath  string
	Roles       []string
	Groups      []string
	Permissions []string
	Attributes  map[string]any
}

// MatchWildcard checks if a wildcard pattern matches a JSON Pointer path,
// token by token: * matches exactly one token and ** any number. Tokens are
// the pointer's own (RFC 6901), compared in their escaped form, so an empty
// key is a token like any other — "/a//b" is three tokens and "/a/*" does not
// match it. "/*" means exactly one token under root — the old
// match-everything special case over-granted patch policies intending
// top-level fields only.
func MatchWildcard(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if pattern == "**" {
		return true
	}
	return matchParts(splitPathParts(pattern), splitPathParts(path))
}

// splitPathParts returns the reference tokens of a JSON Pointer, still
// escaped: "" (the whole document) has none, "/" one empty token, and
// "/a//b" the three tokens "a", "" and "b". A leading slash is optional.
func splitPathParts(s string) []string {
	if s == "" {
		return nil
	}
	if s[0] == '/' {
		s = s[1:]
	}
	return strings.Split(s, "/")
}

func matchParts(pattern, path []string) bool {
	pi, pa := 0, 0
	for pi < len(pattern) && pa < len(path) {
		if pattern[pi] == "**" {
			if pi == len(pattern)-1 {
				return true
			}
			for pa < len(path) {
				if matchParts(pattern[pi+1:], path[pa:]) {
					return true
				}
				pa++
			}
			return false
		}
		if pattern[pi] != "*" && pattern[pi] != path[pa] {
			return false
		}
		pi++
		pa++
	}
	return pi == len(pattern) && pa == len(path)
}

// ToSnakeCase converts PascalCase or camelCase to snake_case.
func ToSnakeCase(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				prev := s[i-1]
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
					result = append(result, '_')
				} else if prev >= 'A' && prev <= 'Z' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
					result = append(result, '_')
				}
			}
			result = append(result, c+32) // toLower
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}

func toCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}
	// Lowercase first letter.
	first := s[0]
	if first >= 'A' && first <= 'Z' {
		return string(first+32) + s[1:]
	}
	return s
}
