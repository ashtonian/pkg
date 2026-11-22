package filter

import (
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// FilterOperator represents a comparison or logical operator.
type FilterOperator string

const (
	OpEq       FilterOperator = "eq"
	OpNe       FilterOperator = "ne"
	OpGt       FilterOperator = "gt"
	OpGte      FilterOperator = "gte"
	OpLt       FilterOperator = "lt"
	OpLte      FilterOperator = "lte"
	OpIn       FilterOperator = "in"
	OpNin      FilterOperator = "nin"
	OpLike     FilterOperator = "like"
	OpILike    FilterOperator = "ilike"
	OpNull     FilterOperator = "null"
	OpNotNull  FilterOperator = "not_null"
	OpExists   FilterOperator = "exists"
	OpRawSQL   FilterOperator = "raw_sql" // pre-built SQL expression (subqueries, etc.)
	OpContains FilterOperator = "contains"
	// OpHasKey tests a document for a top-level key, a string element of a
	// top-level array, or a scalar string equal to the key: jsonb `?`.
	OpHasKey FilterOperator = "has_key"
	// OpMember is a membership constraint (PolicyConstraint.Member): the
	// field is among the values a Lookup projects. Only policies build it.
	OpMember FilterOperator = "member"
	// OpUnknown is SQL's unknown: no row satisfies it, nor its negation. It
	// names no field. Only policies build it (UnknownConstraint,
	// PolicyConstraint.UnknownIfInvalid, a membership resolved without a
	// lookup).
	OpUnknown FilterOperator = "unknown"

	// Relation predicates on a field with a Related schema:
	// some related row satisfies the node's child (the target's own
	// filter; no child means any row), or none does. `$is` / `$is_not` on
	// a belongs-to parse to OpSome / OpNone.
	OpSome  FilterOperator = "some"
	OpNone  FilterOperator = "none"
	OpIs    FilterOperator = "is"
	OpIsNot FilterOperator = "is_not"

	// Geo-spatial operators.
	OpNear       FilterOperator = "near"
	OpWithin     FilterOperator = "within"
	OpIntersects FilterOperator = "intersects"

	// Logical operators.
	OpAnd FilterOperator = "and"
	OpOr  FilterOperator = "or"
	OpNot FilterOperator = "not"
)

// --- Filter Schema ---

// ResourceSchema defines filterable fields for a resource.
type ResourceSchema struct {
	Name string
	// PrimaryKey is the unique column appended to every list's keyset.
	// Resource construction declares it; it is never request input.
	PrimaryKey   string
	Fields       map[string]*FieldSchema
	SystemFields map[string]*FieldSchema
	// PolicyFields are the names server policies may use besides Fields and
	// SystemFields (PolicyField): the external name of a field without the
	// filter capability, and the column of any field with one, hidden fields
	// included. A generated schema leaves out a name that designates more
	// than one field and a field whose type a filter field could not take;
	// PolicyField refuses both. A request never reaches these fields, and
	// schema documents leave them out.
	PolicyFields map[string]*FieldSchema

	// policyRefused are the names a generated schema refuses to policies,
	// with the error each returns (PolicyField).
	policyRefused map[string]error
}

// RequestField resolves a field name a request filter uses, in either
// grammar: a request field (Fields), or the key (PrimaryKey) from
// SystemFields. The tenant and soft-delete columns are system fields only
// policies reach; a request's handle on deleted rows is include_deleted.
func (s *ResourceSchema) RequestField(name string) (*FieldSchema, bool) {
	if f, ok := s.Fields[name]; ok {
		return f, true
	}
	if name != "" && name == s.PrimaryKey {
		f, ok := s.SystemFields[name]
		return f, ok
	}
	return nil, false
}

// FieldSchema defines a single filterable field.
type FieldSchema struct {
	ExternalName string
	SQLExpr      string
	// Column is the row column the field reads: its own column, or a path's
	// document column. Schema generation sets it from the field's column
	// name, so it names the column even when a column mapper makes SQLExpr
	// a computed expression. RowColumn falls back to SQLExpr without it.
	Column   string
	DataType core.FieldType
	// NumberKind is the Go numeric kind the field decodes into
	// (reflect.Int8 … reflect.Float64). Integer and float values are
	// range-checked against it; reflect.Invalid (hand-built schemas)
	// validates against the widest domain, int64 / float64.
	NumberKind reflect.Kind
	// Wire is the form requests give the field's values in: core.WireString
	// for a bool, integer or float field tagged json:",string", whose
	// comparison and set operands are JSON strings ("42", not 42), as
	// encoding/json writes the field. The zero Wire is the data type's form.
	Wire core.Wire
	// Elem is the element type of a FieldTypeArray field; `$contains`
	// values normalize to it (and NumberKind is the element's kind).
	Elem             core.FieldType
	AllowedOperators []FilterOperator
	Indexed          bool
	EnumValues       []string
	Validate         func(value any) error
	Relation         *core.RelationSchema
	// Related is the relation target's filter schema: requests filter the
	// related rows with it (`$some`, `$none`, `$is`, `$is_not`). Nil for a
	// relation compared by key alone.
	Related *ResourceSchema
	// RelatedBy is the owner's field a relation correlates on (its key, or
	// a belongs-to's foreign key), for backends that evaluate relation
	// predicates as memberships (ResolveMemberships).
	RelatedBy *FieldSchema
	// Path makes the field a typed member of a document column; its value
	// expression is Dialect.PathExpr (render with FieldExpr) and SQLExpr is
	// empty.
	Path *JSONPath
	// Storage is how a document column holds its JSON (storage tag);
	// empty means jsonb.
	Storage JSONStorage
	// Elements is a JSON array document's element schema: paths into one
	// element, which `$some` / `$none` filters test on one element at a
	// time. Nil for a document without element paths.
	Elements *ResourceSchema
	// Element is a scalar array's element, an array column's or an array
	// path's: `$some` / `$none` conditions compare it, one element at a
	// time, with the element type's operators. Nil for any other field.
	Element *FieldSchema

	// UI metadata (populated from struct tags, used by SchemaResponse).
	Label       string
	Description string
	Group       string
	Order       int
	Facetable   bool
	Searchable  bool
	Immutable   bool
	// Includable opts a relation field into the `?_include=<field>`
	// projection. Valid only when Relation is non-nil.
	Includable bool

	FacetSchema *FacetSchema    // Parsed facet configuration (from `facet` tag).
	GeoConfig   *GeoFieldConfig // Geo-spatial metadata (from `geo` tag). Non-nil when DataType == FieldTypeGeo.

	// role marks the fields `$some` / `$none` conditions stand on until the
	// builder or an evaluator binds them to one element. It is unexported so
	// that no declared column, whatever its name, reads as one.
	role elementRole
}

// AllowsOperator checks whether op is in the field's allowed operators list.
// The NULL tests — null, not_null and exists — are one capability: each is
// the negation or alias of another, so a field that declares any of them
// allows all three.
func (f *FieldSchema) AllowsOperator(op FilterOperator) bool {
	if isNullTest(op) {
		return slices.ContainsFunc(f.AllowedOperators, isNullTest)
	}
	return slices.Contains(f.AllowedOperators, op)
}

func isNullTest(op FilterOperator) bool {
	return op == OpNull || op == OpNotNull || op == OpExists
}

// --- Filter AST ---

// FilterNode represents a node in the filter AST.
type FilterNode struct {
	Field    *FieldSchema
	Op       FilterOperator
	Value    any
	Children []*FilterNode

	// RawSQL/RawArgs hold a pre-built SQL expression for OpRawSQL nodes.
	// The SQL builder renders it in parentheses and rewrites every `$N` in
	// it, string literals included, to the dialect's parameter indices.
	RawSQL  string
	RawArgs []any

	// Member is the set an OpMember node tests Field against.
	Member *Lookup
}

// FilterAST is the root of a parsed filter tree.
type FilterAST struct {
	Root *FilterNode
}

// SQLResult contains the generated SQL WHERE clause and bound arguments.
// Where is a selection predicate: it selects exactly the rows the filter
// holds True, for a WHERE, ON or FILTER clause. On a row the filter holds
// Unknown it may be False (TwoValuedDialect), so it is not a value to
// negate or project.
type SQLResult struct {
	Where      string
	Args       []any
	ParamCount int // Number of parameters consumed (highest param index minus start offset).
}

// ParsedFilter wraps a FilterAST with metadata.
type ParsedFilter struct {
	AST            *FilterAST
	EstimatedCost  int
	ConditionCount int
}

// --- Sort Schema ---

// SortDirection represents ascending or descending sort order.
type SortDirection string

const (
	SortAsc  SortDirection = "asc"
	SortDesc SortDirection = "desc"
)

// SortSchema defines allowed sort fields for a resource.
type SortSchema struct {
	Fields       map[string]*SortFieldSchema
	DefaultField string
	DefaultDir   SortDirection
	MaxFields    int
}

// SortFieldSchema defines a single sortable field.
type SortFieldSchema struct {
	// SQLExpr is the expression emitted into ORDER BY and keyset
	// predicates, usually table-qualified (`items.rank`).
	SQLExpr string
	// Column is the row column the key's value is read from when a cursor
	// is minted — the bare column name, never the qualified expression
	//. Empty falls back to SQLExpr's last identifier
	// segment (see RowColumn).
	Column string
	// DataType lets keyset values bind and compare by type (timestamps
	// chronologically, numbers exactly).
	DataType core.FieldType
	// NumberKind is the Go numeric kind used to range-check cursor values.
	NumberKind reflect.Kind
	// Nullable marks keys that can hold NULL; they order NULL-greatest with
	// explicit NULLS FIRST/LAST and a NULL-aware keyset predicate.
	Nullable bool
	Indexed  bool
	// Path makes the key a typed member of a document column: ORDER BY and
	// seeks render Dialect.PathExpr, Column is the document's row column and
	// a cursor reads the member with the path value rule. Path keys are
	// nullable: an absent member sorts as NULL.
	Path *JSONPath
}

// RowColumn returns the row column for the key: Column, or the last
// segment of a plain (optionally table-qualified) identifier SQLExpr, or ""
// when the expression is computed and has no row column.
func (s *SortFieldSchema) RowColumn() string {
	if s.Column != "" {
		return s.Column
	}
	return identifierColumn(s.SQLExpr)
}

// RowColumn returns the row column the field's values come from: Column, or
// the last segment of a plain (optionally table-qualified) identifier — a
// path's document column, else SQLExpr. It is "" for a relation, whose values
// are another table's, and for a computed expression.
func (f *FieldSchema) RowColumn() string {
	switch {
	case f.Relation != nil:
		return ""
	case f.Column != "":
		return f.Column
	case f.Path != nil:
		return identifierColumn(f.Path.Column)
	default:
		return identifierColumn(f.SQLExpr)
	}
}

// identifierColumn is the column a plain, optionally table-qualified,
// identifier names; "" for any other expression.
func identifierColumn(expr string) string {
	if !identifierRe.MatchString(expr) {
		return ""
	}
	if i := strings.LastIndexByte(expr, '.'); i >= 0 {
		return expr[i+1:]
	}
	return expr
}

// identifierRe admits plain, optionally table-qualified, SQL identifiers.
var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// SortResult holds parsed sort fields.
type SortResult struct {
	Fields []SortField
}

// SortField represents a single parsed sort field with direction.
type SortField struct {
	Name      string
	Schema    *SortFieldSchema
	Direction SortDirection
}
