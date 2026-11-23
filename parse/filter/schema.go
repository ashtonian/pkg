package filter

import (
	"iter"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
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
	// OpKeyIn is a key set (PolicyConstraint.Fields under OpIn): the row's
	// key columns, together, equal one of the keys a KeySet lists. Only
	// policies build it.
	OpKeyIn FilterOperator = "key_in"
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
	// PrimaryKey are the key's columns, in key order: together they are
	// unique among the rows, and every list's keyset ends with each one its
	// sort does not name. Resource construction declares them; they are
	// never request input.
	PrimaryKey   []string
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
	// Computed are the columns the resource reads through an expression
	// (parse.ReadExpr), in declaration order. A statement built for a
	// dialect reads such a column's fields, sort keys and search fields
	// through the expression resolved for that dialect (ComputedSQL), and a
	// relation's target the same way; the schema itself, and FieldExpr,
	// name the column. A dialect an expression refuses fails every
	// statement that compiles the schema.
	Computed []ComputedColumn

	// policyRefused are the names a generated schema refuses to policies,
	// with the error each returns (PolicyField).
	policyRefused map[string]error
}

// RequestField resolves a field name a request filter uses, in either
// grammar: a request field (Fields), or a key column (PrimaryKey) from
// SystemFields. The tenant and soft-delete columns are system fields only
// policies reach, unless the tenant is a key column; a request's handle on
// deleted rows is include_deleted.
func (s *ResourceSchema) RequestField(name string) (*FieldSchema, bool) {
	if f, ok := s.Fields[name]; ok {
		return f, true
	}
	if name != "" && slices.Contains(s.PrimaryKey, name) {
		f, ok := s.SystemFields[name]
		return f, ok
	}
	return nil, false
}

// RequestFields yields every field a request filter may name, under the
// name it is named by: exactly the names RequestField resolves. A schema
// document lists these.
func (s *ResourceSchema) RequestFields() iter.Seq2[string, *FieldSchema] {
	return func(yield func(string, *FieldSchema) bool) {
		for name, f := range s.Fields {
			if !yield(name, f) {
				return
			}
		}
		for _, column := range s.PrimaryKey {
			if _, shadowed := s.Fields[column]; shadowed || column == "" {
				continue
			}
			if key, ok := s.SystemFields[column]; ok && !yield(column, key) {
				return
			}
		}
	}
}

// FieldSchema defines a single filterable field.
//
// A field reads Column of Table, which the dialect quotes, unless Expr is
// set. Schema generation sets all three; a schema built by hand sets Table
// too, since SQLite reads a lone quoted name that names no column as a
// string literal, which a misspelt column would then compare as.
type FieldSchema struct {
	ExternalName string
	// Table is the table, or the statement's alias of it, whose Column the
	// field reads.
	Table string
	// Column is the row column the field reads: its own column, or a path's
	// document column. Schema generation sets it from the field's column
	// name, so it names the column even when a column mapper makes Expr a
	// computed expression.
	Column string
	// Expr is the field's SQL when it is not Column of Table: a column
	// mapper's expression (core.SQLColumnMapper) or a computed column's,
	// emitted as written.
	Expr     string
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
	// EnumValues are the values a string field, or each element of an
	// array of strings, may take; generation refuses them on other types.
	EnumValues []string
	Validate   func(value any) error
	Relation   *core.RelationSchema
	// Related is the relation target's filter schema: requests filter the
	// related rows with it (`$some`, `$none`, `$is`, `$is_not`). Nil for a
	// relation compared by key alone.
	Related *ResourceSchema
	// RelatedBy are the owner's fields a relation correlates on (its key,
	// or a belongs-to's foreign key), paired by position with
	// Relation.OwnerColumns: they type the owner keys of includes, and
	// backends that evaluate relation predicates as memberships
	// (ResolveMemberships) test them.
	RelatedBy []*FieldSchema
	// Path makes the field a typed member of a document column; its value
	// expression is Dialect.PathExpr (render with FieldExpr), and Table,
	// Column and Expr are the document's.
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

// EffectiveOperators are the operators the field takes (AllowsOperator):
// AllowedOperators as declared, then the NULL tests a declared one implies.
// A schema document lists these, so a client reads the capability rather
// than re-deriving it.
func (f *FieldSchema) EffectiveOperators() []FilterOperator {
	ops := slices.Clone(f.AllowedOperators)
	if !slices.ContainsFunc(ops, isNullTest) {
		return ops
	}
	for _, op := range nullTests {
		if !slices.Contains(ops, op) {
			ops = append(ops, op)
		}
	}
	return ops
}

// nullTests are the NULL tests, in the order the vocabulary is documented.
var nullTests = []FilterOperator{OpNull, OpNotNull, OpExists}

func isNullTest(op FilterOperator) bool { return slices.Contains(nullTests, op) }

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

	// Keys is the set an OpKeyIn node tests its key columns against.
	Keys *KeySet
}

// FilterAST is the root of a parsed filter tree.
type FilterAST struct {
	Root *FilterNode
}

// SQLResult contains the generated SQL WHERE clause and bound arguments.
// Where is a selection predicate: it selects exactly the rows the filter
// holds True, for a WHERE, ON or FILTER clause. On a row the filter holds
// Unknown it may be False (Dialect.TwoValuedCondition), so it is not a value to
// negate or project.
type SQLResult struct {
	Where      string
	Args       []any
	ParamCount int // Number of parameters consumed (highest param index minus start offset).
}

// ParsedFilter is a validated filter: its AST, with the AST's estimated cost
// and condition count. Its fields are read through methods, so a filter a
// resource parsed cannot be given other conditions afterwards.
type ParsedFilter struct {
	ast        *FilterAST
	cost       int
	conditions int
	// schema is the schema the filter was parsed against (ParsedFor); nil
	// for one NewParsedFilter wraps.
	schema *ResourceSchema
}

// AST returns the filter's syntax tree; nil filters nothing. The tree is
// the filter's own: read it, never change it.
func (f *ParsedFilter) AST() *FilterAST {
	if f == nil || f.ast == nil {
		return nil
	}
	// A copy, so assigning Root changes nothing the filter holds.
	ast := *f.ast
	return &ast
}

// EstimatedCost is the filter's cost under the default cost factors.
func (f *ParsedFilter) EstimatedCost() int {
	if f == nil {
		return 0
	}
	return f.cost
}

// ConditionCount is the number of leaf conditions in the filter.
func (f *ParsedFilter) ConditionCount() int {
	if f == nil {
		return 0
	}
	return f.conditions
}

// ParsedFor reports whether the filter was parsed against schema
// (NewParsedFilterFor): a resource compiles only the filters it parsed.
func (f *ParsedFilter) ParsedFor(schema *ResourceSchema) bool {
	return f != nil && schema != nil && f.schema == schema
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

// SortFieldSchema defines a single sortable field. ORDER BY and keyset
// predicates read Column of Table, quoted by the dialect, unless Expr is set,
// as a FieldSchema does.
type SortFieldSchema struct {
	// Table is the table, or the statement's alias of it, whose Column the
	// key reads.
	Table string
	// Column is the row column the key's value is read from when a cursor
	// is minted.
	Column string
	// Expr is the key's SQL when it is not Column of Table (a column
	// mapper's or a computed column's), emitted as written.
	Expr string
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

// RowColumn returns the row column the key's value is read from: Column,
// or a path's document column; "" for a computed key, which has none.
func (s *SortFieldSchema) RowColumn() string {
	if s.Column == "" && s.Path != nil {
		return s.Path.Column
	}
	return s.Column
}

// RowColumn returns the row column the field's values come from: Column, or
// a path's document column. It is "" for a relation, whose values are
// another table's, and for a computed expression.
func (f *FieldSchema) RowColumn() string {
	switch {
	case f.Relation != nil:
		return ""
	case f.Column == "" && f.Path != nil:
		return f.Path.Column
	}
	return f.Column
}

// columnSQL is the SQL that reads column of table, each quoted by d, or
// expr when it is set: a column mapper's or a computed column's SQL, as
// written. A table qualifies every column, so on SQLite a misspelt one is
// an error rather than a string.
func columnSQL(d dialect.Dialect, table, column, expr string) string {
	switch {
	case expr != "":
		return expr
	case table == "":
		return d.QuoteIdent(column)
	}
	return qualified(d, table, column)
}

// qualified is column of qualifier, a table or a statement's alias of one,
// each quoted.
func qualified(d dialect.Dialect, qualifier, column string) string {
	return d.QuoteIdent(qualifier) + "." + d.QuoteIdent(column)
}

// quoteTable is a table name that may name its schema, each part quoted.
func quoteTable(d dialect.Dialect, table string) string {
	parts := strings.Split(table, ".")
	for i, part := range parts {
		parts[i] = d.QuoteIdent(part)
	}
	return strings.Join(parts, ".")
}

// sql is the SQL that reads the field's column (columnSQL): the document's,
// for a path.
func (f *FieldSchema) sql(d dialect.Dialect) string { return columnSQL(d, f.Table, f.Column, f.Expr) }

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
