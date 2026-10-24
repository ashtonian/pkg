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

// ValidFilterOps is the set of all recognized filter operators.
// Used by the schema generator to validate filter_ops tag values.
var ValidFilterOps = map[FilterOperator]bool{
	OpEq: true, OpNe: true, OpGt: true, OpGte: true, OpLt: true, OpLte: true,
	OpIn: true, OpNin: true, OpLike: true, OpILike: true,
	OpNull: true, OpNotNull: true, OpExists: true, OpContains: true, OpHasKey: true,
	OpNear: true, OpWithin: true, OpIntersects: true,
}

// AllOperatorStrings returns every supported value-comparison operator as a
// stable, sorted string slice. Used by the OPTIONS endpoint to advertise
// the operator vocabulary to clients.
//
// Logical operators ($and/$or/$not) and the geo operators are intentionally
// excluded — the former are query-tree shape, not field-level comparisons,
// and the latter only apply to geo-typed fields whose schema entry already
// declares them.
func AllOperatorStrings() []string {
	return []string{
		string(OpEq), string(OpNe),
		string(OpGt), string(OpGte), string(OpLt), string(OpLte),
		string(OpIn), string(OpNin),
		string(OpLike), string(OpILike), string(OpContains), string(OpHasKey),
		string(OpNull), string(OpNotNull), string(OpExists),
	}
}

// OperatorFromString maps a JSON operator key (e.g. "$eq") to FilterOperator.
var OperatorFromString = map[string]FilterOperator{
	"$eq":         OpEq,
	"$ne":         OpNe,
	"$gt":         OpGt,
	"$gte":        OpGte,
	"$lt":         OpLt,
	"$lte":        OpLte,
	"$in":         OpIn,
	"$nin":        OpNin,
	"$like":       OpLike,
	"$ilike":      OpILike,
	"$null":       OpNull,
	"$not_null":   OpNotNull,
	"$exists":     OpExists,
	"$contains":   OpContains,
	"$has_key":    OpHasKey,
	"$near":       OpNear,
	"$within":     OpWithin,
	"$intersects": OpIntersects,
	"$some":       OpSome,
	"$none":       OpNone,
	"$is":         OpIs,
	"$is_not":     OpIsNot,
	"$or":         OpOr,
	"$and":        OpAnd,
	"$not":        OpNot,
}

// --- Filter Schema ---

// ResourceSchema defines filterable fields for a resource.
type ResourceSchema struct {
	Name string
	// PrimaryKey is the unique column appended to every list's keyset.
	// Resource construction declares it; it is never request input.
	PrimaryKey   string
	Fields       map[string]*FieldSchema
	SystemFields map[string]*FieldSchema
}

// FieldSchema defines a single filterable field.
type FieldSchema struct {
	ExternalName string
	SQLExpr      string
	DataType     core.FieldType
	// NumberKind is the Go numeric kind the field decodes into
	// (reflect.Int8 … reflect.Float64). Integer and float values are
	// range-checked against it; reflect.Invalid (hand-built schemas)
	// validates against the widest domain, int64 / float64.
	NumberKind reflect.Kind
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
	// Placeholder params are rewritten by the SQL builder to use the correct
	// dialect-specific parameter indices.
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

// AuditInfo returns a map of audit-relevant information for the parsed filter.
func (f *ParsedFilter) AuditInfo() map[string]any {
	return map[string]any{
		"has_filter":      f.AST != nil && f.AST.Root != nil,
		"estimated_cost":  f.EstimatedCost,
		"condition_count": f.ConditionCount,
	}
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
	if !identifierRe.MatchString(s.SQLExpr) {
		return ""
	}
	if i := strings.LastIndexByte(s.SQLExpr, '.'); i >= 0 {
		return s.SQLExpr[i+1:]
	}
	return s.SQLExpr
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

// --- Policy Types ---

// PolicyConstraint represents a single policy-derived filter condition.
type PolicyConstraint struct {
	Field    string
	Operator FilterOperator
	Value    any
	Negate   bool
	// UnknownIfInvalid compiles a value that does not apply to the field —
	// its type, or the operator's — to SQL's unknown instead of an error:
	// no row satisfies it, nor its negation, so it narrows whatever group
	// holds it. Authorization sets it: a policy value no row can hold
	// neither fails a list nor widens one.
	UnknownIfInvalid bool

	// RawSQL is a pre-built SQL expression injected directly into the WHERE clause.
	// When set, Field/Operator/Value are ignored. Use parameterized placeholders ($1, $2)
	// with RawArgs. This enables subquery-based constraints like grant-aware scoping.
	RawSQL  string
	RawArgs []any

	// Member makes the constraint a membership: Field's value is one of the
	// values the lookup projects. Operator, Value and RawSQL
	// are unused. Every backend evaluates it — SQL as a subquery, memory
	// through ResolveMemberships — unlike RawSQL, which memory cannot.
	Member *Lookup
}

// PolicyConstraints is a composable collection of constraints.
type PolicyConstraints struct {
	Constraints []*PolicyConstraint
	Or          []*PolicyConstraints
	And         []*PolicyConstraints
	// Not negates the group: a row is admitted when it does not satisfy it.
	Not      bool
	Source   string
	PolicyID string
}

// IsEmpty returns true when there are no constraints.
func (pc *PolicyConstraints) IsEmpty() bool {
	if pc == nil {
		return true
	}
	return len(pc.Constraints) == 0 && len(pc.Or) == 0 && len(pc.And) == 0
}

// Merge combines two PolicyConstraints with AND semantics.
func (pc *PolicyConstraints) Merge(other *PolicyConstraints) *PolicyConstraints {
	if other == nil || other.IsEmpty() {
		return pc
	}
	if pc == nil || pc.IsEmpty() {
		return other
	}
	return &PolicyConstraints{
		And: []*PolicyConstraints{pc, other},
	}
}
