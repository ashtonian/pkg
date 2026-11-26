package filter

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// rawPlaceholderRe matches a `$N` positional placeholder as a whole token so a
// renumber of `$1` never substring-corrupts a multi-digit `$10` / `$11`.
var rawPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

// Dialect generates database-specific SQL: every piece of SQL parse writes
// whose form differs between databases is one of its methods, for filters,
// lists, facets and includes here, for patches (the methods
// patch.SQLDialect and patch.RelationDialect name), and for a row scan
// (StoresAsText). Nothing chooses SQL by which dialect it has, so a SQL
// provider is one type implementing Dialect; PostgresDialect and
// SQLiteDialect are the two there are.
type Dialect interface {
	dialect.Dialect

	// Condition renders the predicate `field <op> value`. bind appends one
	// argument and returns its placeholder, so SQL text and arguments are
	// built together and cannot disagree in count or order. value is the
	// parser's normalized value (see FilterParser.normalizeValue); a shape
	// the dialect cannot evaluate is a *core.ParseError (E2115 for geo).
	// Every predicate is NULL when the field is NULL, except the NULL tests
	// and an empty `$in` / `$nin`, so negation keeps SQL's three-valued
	// meaning.
	Condition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error)

	// CTEMaterializeHint returns the dialect-specific hint string emitted
	// after "WITH base AS" so the planner does not inline a CTE referenced
	// multiple times. Postgres returns " MATERIALIZED" (PG ≥ 12); SQLite
	// returns "" (CTEs are always materialized).
	//
	// Used by BuildFacetQuery when composing the single CTE + UNION ALL
	// facet plan; the filter scan must happen exactly once.
	CTEMaterializeHint() string

	// NativeRowCompare reports whether keyset seeks may use a native
	// row-value comparison `(a, b) > ($1, $2)` (used only when no key is
	// nullable and every key walks the same direction). Dialects without it
	// get the lexicographic OR-expansion.
	NativeRowCompare() bool

	// CodePointOrder returns expr under code-point (bytewise) collation, the
	// order string comparisons, keyset walks and the memory backend share; a
	// column's linguistic collation would order "B" before "a".
	CodePointOrder(expr string) string

	// RowLock is the clause that locks the rows a SELECT reads until its
	// transaction ends — " FOR UPDATE" — or "" where the database locks
	// writers as a whole (SQLite).
	RowLock() string

	// PathExpr renders a JSON path's typed value: the member when it has
	// type t's JSON kind (and shape), else NULL.
	PathExpr(p *JSONPath, t core.FieldType) string

	// ReadColumn renders how a statement reads column, whose database type
	// the resource declared as t: an expression the column's string field
	// scans, or column itself when the database's own encoding scans.
	ReadColumn(column string, t core.SQLType) string

	// DecimalValue renders a decimal operand as the dialect's exact decimal
	// value: equal decimals are equal values, whether a column, a document
	// member (the value PathExpr reads a decimal member as) or a bound
	// canonical text holds them. FieldExpr reads a decimal through it.
	DecimalValue(o DecimalOperand) string

	// DecimalOrder renders a decimal operand as what compares and orders
	// decimals exactly: the exact value itself where the database's own
	// type orders it; where there is none, a key read from the operand that
	// orders as the value does and is equal exactly where the values are.
	// Filters and lookups compare decimals through it, equality and sets
	// included, as sorts and keyset seeks do, so one index on it serves
	// them all.
	DecimalOrder(o DecimalOperand) string

	// TwoValuedCondition renders `field <op> value` a second way, one an
	// index serves better, True exactly where Condition's is True: it is
	// exact only where a predicate's Unknown and False select alike.
	// SQLBuilder uses it for a condition an even number of negations
	// (usually none) enclose within its statement or subquery: the
	// statement is then monotone in the condition (False < Unknown <
	// True), and a Kleene result that is True with the condition Unknown is
	// True whatever it is, so a row is selected exactly when it would be
	// with the condition False. ok is false when the dialect has no second
	// rendering of the predicate; it then binds nothing.
	TwoValuedCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (sql string, ok bool, err error)

	// MarshalValue converts a value to the form the dialect binds for a
	// column of type t: composite values (arrays, documents) as the
	// database's driver takes them.
	MarshalValue(value any, t core.TypeInfo) (any, error)

	// elementRows is a FROM item yielding a row aliased alias per element
	// of the array f holds — a JSON array document, an array path member or
	// an array column — and none when f holds no array. `$some` / `$none`,
	// an array path's NULL tests and `$contains`, and list facets read it.
	elementRows(f *FieldSchema, alias string) string
	// objectElement is row alias's element as the document its element
	// paths read, NULL for an element that is not an object.
	objectElement(alias string) string
	// scalarElement is row alias's element as a field of f's element type
	// (f.Element), NULL for an element of another kind.
	scalarElement(f *FieldSchema, alias string) *FieldSchema
	// containsElement answers `$some` of an element equal to a value, or to
	// one of a set, by containment, which an index on the document serves;
	// ok is false where the dialect reads the element table instead.
	containsElement(f *FieldSchema, cond *FilterNode, bind func(any) string) (sql string, ok bool, err error)
	// setRows binds a set's members as one parameter and reads them back
	// as rows of one column, v, of the type FieldExpr reads field as.
	setRows(field *FieldSchema, members []any, bind func(any) string) (string, error)
	// memberSet tests field for membership in members (in) or absence
	// from them, the set bound as one parameter and compared as field's
	// equality compares: a decimal through DecimalOrder. `$in` / `$nin`
	// and a lookup's WhereIn render through it.
	memberSet(field *FieldSchema, members []any, in bool, bind func(any) string) (string, error)
	// KeyIn renders the key columns of table, each qualified by alias (none
	// when "") and typed by types, as a row IN keys — NOT IN when member is
	// false — the keys bound as one parameter whatever their number. Each
	// column and each key's value compare as a filter compares the
	// column's type: a decimal by value, a date by its calendar day. A key
	// constraint over several columns, an include's owners and a relation
	// patch's pairs render through it.
	KeyIn(table, alias string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error)
	// keyIn is KeyIn with each column read as read renders it, table
	// possibly schema-qualified: a lookup's WhereKeyIn renders through it.
	keyIn(table string, read func(column string) string, columns []string, types []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error)

	// KeyOperand renders what a key column, read as expr and typed t,
	// compares by: the operand a filter's equality on the type compares,
	// a decimal through DecimalOrder and a date by its calendar day. Two
	// key columns are equal where their operands are.
	KeyOperand(expr string, t core.TypeInfo) string

	// StoredKeyValue is a key column of type t's canonical value
	// (core.NormalizeScalar) as the database holds it, so two values the
	// database holds equal have one: a timestamp at the database's
	// precision.
	StoredKeyValue(t core.TypeInfo, v any) (any, error)

	// numberKey renders an int, float or decimal field's value as its
	// terms facet key: the canonical decimal text (core.DecimalText)
	// FacetKeys gives the same value.
	numberKey(f *FieldSchema) string
	// timestampKey renders a timestamp column's value as its terms facet
	// key: the canonical text (core.FormatTimestamp) FacetKeys gives the
	// same instant.
	timestampKey(expr string) string
	// textKey renders any other value's facet key as TEXT, so the UNION
	// ALL branches of a facet query share one key column type.
	textKey(expr string) string
	// keyText renders the text core.KeyText gives a key of several
	// columns, the columns' values being exprs: includes and facets name a
	// row of such a key by it.
	keyText(exprs []string) string

	// search renders a free-text search of schema's fields for query, its
	// parameters numbered after startIdx.
	search(schema *SearchSchema, query string, startIdx int) (*searchCondition, error)

	// The SQL a patch writes, documented method by method where patch
	// consumes it (patch.SQLDialect, patch.RelationDialect): a changed
	// column's condition, an RFC 7396 merge, a native array append, a JSON
	// document member's read, set, removal and preconditions, and the rows
	// a pair table's insert reads when no target table holds its values.
	ChangedCondition(column, expr string, t core.TypeInfo) string
	MergePatch(doc string, storage core.SQLType, patch map[string]any, bind func(any) string) (string, error)
	ArrayAppend(array string, element any, bind func(any) string) (string, error)
	MemberDocument(expr string) string
	SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) string
	RemoveMember(doc string, member []string, bind func(any) string) string
	HasMember(doc string, member []string, bind func(any) string) string
	IsObject(doc string, member []string, bind func(any) string) string
	RelationRows(rel *core.RelationSchema, owners, values []core.Key, tenant *dialect.PairTenant, bind func(any) string) (dialect.PairRows, error)

	// StoresAsText reports whether the database stores a value of type t,
	// in a column declared no JSON type, as text a row scan decodes rather
	// than the driver's own value: on SQLite a timestamp as its canonical
	// text (core.FormatTimestamp), a document or an array as JSON.
	// parse.RowScanner decodes what it reports; MarshalValue writes it.
	StoresAsText(t core.FieldType) bool
}

// ErrUnknownDSN is returned by DialectFromDSN for a DSN that names no
// database parse compiles SQL for, CockroachDB included.
var ErrUnknownDSN = dialect.ErrUnknownDSN

// DialectFromDSN returns the Dialect of a database DSN, decided by its form
// alone: PostgresDialect for a postgres:// or postgresql:// URL;
// SQLiteDialect for a sqlite:// or sqlite3:// URL, a file: URI, :memory:,
// or an absolute, ./ or ../ path to a .db, .sqlite or .sqlite3 file. It
// refuses forms other drivers' DSNs take: CockroachDB's cockroach:// and
// crdb://, where PostgresDialect's SQL has never been verified; a bare name
// such as app.db, which keyword/value, MySQL and Oracle DSNs also end as;
// //host/… and a one-segment /name.db. A form is evidence, not proof:
// another driver's DSN that takes an accepted one, such as a postgres://
// URL to a CockroachDB cluster or a MySQL user named file, selects that
// form's dialect. Construct the dialect for a refused DSN. The error names
// the scheme, never the DSN.
func DialectFromDSN(dsn string) (Dialect, error) {
	base, err := dialect.FromDSN(dsn)
	if err != nil {
		return nil, err
	}
	switch k := dialect.KindOf(base); k {
	case dialect.KindPostgres:
		return PostgresDialect{}, nil
	case dialect.KindSQLite:
		return SQLiteDialect{}, nil
	default:
		return nil, dialect.Unsupported(k, "DialectFromDSN")
	}
}

// DecimalOperand is what a decimal value is read from: a column, a document
// member or a bound parameter. A dialect renders it as the exact decimal
// value (Dialect.DecimalValue) or as what compares and orders it
// (Dialect.DecimalOrder), each from the operand itself, so the key of a
// value never re-reads the value's own SQL. Build one with DecimalColumn, DecimalMember or
// DecimalParam; the zero DecimalOperand is none, and a dialect panics on it.
type DecimalOperand struct {
	source decimalSource
	sql    string
	path   *JSONPath
}

type decimalSource uint8

const (
	decimalFromColumn decimalSource = iota + 1
	decimalFromMember
	decimalFromParam
)

// DecimalColumn is a decimal column, or a column mapper's expression, as
// expr reads it: the database's own encoding of the value.
func DecimalColumn(expr string) DecimalOperand {
	return DecimalOperand{source: decimalFromColumn, sql: expr}
}

// DecimalMember is a decimal member of a document: a JSON number, or a
// string holding a decimal.
func DecimalMember(p *JSONPath) DecimalOperand {
	return DecimalOperand{source: decimalFromMember, path: p}
}

// DecimalParam is a bound decimal's placeholder: its canonical text
// (core.DecimalText).
func DecimalParam(placeholder string) DecimalOperand {
	return DecimalOperand{source: decimalFromParam, sql: placeholder}
}

func (o DecimalOperand) invalid() string {
	return "parse: DecimalOperand of source " + strconv.Itoa(int(o.source)) + " is no operand"
}

// fieldDecimal is the operand a decimal field's value is read from.
func fieldDecimal(d Dialect, f *FieldSchema) DecimalOperand {
	if f.Path != nil {
		return DecimalMember(f.Path)
	}
	return DecimalColumn(f.sql(d))
}

// decimalComparison renders a decimal field's equality or ordering: the
// field and its canonical text, bound as a parameter, both read through
// Dialect.DecimalOrder. ok is false for any other operator.
func decimalComparison(d Dialect, field *FieldSchema, op FilterOperator, value any, bind func(any) string) (sql string, ok bool, err error) {
	switch op {
	case OpEq, OpNe, OpGt, OpGte, OpLt, OpLte:
	default:
		return "", false, nil
	}
	if value != nil {
		if value, err = core.DecimalText(value); err != nil {
			return "", true, fmt.Errorf("field %s: %w", field.ExternalName, err)
		}
	}
	param := func(v any) string { return d.DecimalOrder(DecimalParam(bind(v))) }
	return comparison(d.DecimalOrder(fieldDecimal(d, field)), op, value, param)
}

// comparison renders the operators every dialect spells alike: equality,
// ordering and the NULL tests. ok is false for the rest.
func comparison(expr string, op FilterOperator, value any, bind func(any) string) (sql string, ok bool, err error) {
	switch op {
	case OpEq:
		return expr + " = " + bind(value), true, nil
	case OpNe:
		return expr + " <> " + bind(value), true, nil
	case OpGt:
		return expr + " > " + bind(value), true, nil
	case OpGte:
		return expr + " >= " + bind(value), true, nil
	case OpLt:
		return expr + " < " + bind(value), true, nil
	case OpLte:
		return expr + " <= " + bind(value), true, nil
	case OpNull:
		return expr + " IS NULL", true, nil
	case OpNotNull:
		return expr + " IS NOT NULL", true, nil
	}
	return "", false, nil
}

// membership renders `$in` / `$nin` through render, which receives the
// set's members, none NULL and at least one, bound as one parameter so a
// statement's parameter count is independent of the set's size. An empty
// set matches nothing (`$in`) or everything (`$nin`), NULL or not. A NULL
// member makes a miss Unknown, as it does in `x IN (…, NULL)`.
func membership(op FilterOperator, value any, render func(members []any, in bool) (string, error)) (string, error) {
	arr, ok := core.AnySlice(value)
	if !ok {
		return "", fmt.Errorf("$%s requires an array value, got %s", op, gowalker.TypeText(reflect.TypeOf(value)))
	}
	if len(arr) == 0 {
		if op == OpIn {
			return "1=0", nil
		}
		return "1=1", nil
	}
	members := make([]any, 0, len(arr))
	for _, m := range arr {
		if m != nil {
			members = append(members, m)
		}
	}
	if len(members) == 0 {
		return unknownSQL, nil // x IN (NULL) and x NOT IN (NULL) are Unknown for every x
	}
	sql, err := render(members, op == OpIn)
	if err != nil || len(members) == len(arr) {
		return sql, err
	}
	if op == OpIn {
		return "(" + sql + " OR NULL)", nil
	}
	return "(" + sql + " AND NULL)", nil
}

// isOrdering reports the range operators; on strings they compare by code
// point on every backend.
func isOrdering(op FilterOperator) bool {
	return op == OpGt || op == OpGte || op == OpLt || op == OpLte
}

// textValue returns a `$contains` / `$like` / `$ilike` operand.
func textValue(op FilterOperator, value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("$%s requires a string value, got %s", op, gowalker.TypeText(reflect.TypeOf(value)))
	}
	return s, nil
}

// unsupportedOperator is the error for an operator the field type does not
// define; parsed filters never reach it (checkOperators), programmatic
// ASTs can.
func unsupportedOperator(field *FieldSchema, op FilterOperator) error {
	return fmt.Errorf("operator %q is not defined for %s field %q", op, field.DataType, field.ExternalName)
}

// SQLBuilderOption configures a SQLBuilder.
type SQLBuilderOption func(*sqlBuilderConfig)

type sqlBuilderConfig struct {
	startParam int
	scope      relationScope
	related    relatedPolicies
}

// RelationScope is how a statement reaches related rows: which tenant's
// rows a tenant-scoped relation holds, and which rows of a policy-scoped
// relation's target the server's policy admits. Every builder takes one
// (WithRelationScope, ListRelationScope, WithFacetRelationScope,
// WithIncludeRelationScope, ResolveRelationScope), so a list's page,
// count, facets and includes are scoped alike. The zero value scopes no
// tenant, and a relation declaring a tenant column fails closed.
type RelationScope struct {
	// Tenant binds every tenant-scoped relation, at every depth, to one
	// tenant, in its tenant column's type. Nil binds none.
	Tenant any
	// RowTenant holds every tenant-scoped relation to the tenant of the
	// row it hangs from, read from this column of the owner's table: for
	// rows of more than one tenant, such as a governed list's
	// (ComposeConstraints). It excludes Tenant and Unscoped.
	RowTenant string
	// Unscoped deliberately reaches tenant-scoped relations without a
	// tenant predicate, for platform tooling that works across tenants.
	// Never set it on a request path.
	Unscoped bool
	// Related is the server's policy for each policy-scoped relation's
	// target table (core.RelationSchema.Policy), by table. A relation
	// predicate on one without its table's policy is
	// ErrRelatedPolicyRequired.
	Related map[string]*PolicyConstraints
	// Now is the instant Related's relative-time tokens (`now-30d`)
	// resolve against: the request's, so its page, count, facets and
	// includes admit the same related rows. Zero is one reading per
	// statement: the list's own instant (ListNow), the wall clock
	// elsewhere.
	Now time.Time
}

// tenancy is s's hold on tenant-scoped relations.
func (s RelationScope) tenancy() relationScope {
	return relationScope{tenant: s.Tenant, unscoped: s.Unscoped, row: rowTenant{column: s.RowTenant}}
}

// policies is s's policy for policy-scoped relations.
func (s RelationScope) policies() relatedPolicies {
	return relatedPolicies{byTable: s.Related, now: s.Now}
}

// relationScope is how a builder scopes tenant-scoped junctions: bound to a
// tenant, held to the tenant of the row they hang from, or explicitly
// unscoped. The zero value is none of them, and any relation declaring a
// TenantColumn fails closed.
type relationScope struct {
	tenant   any
	unscoped bool
	row      rowTenant
}

// rowTenant is the owner's tenant column a list whose rows span tenants
// holds every tenant-scoped relation to (RelationScope.RowTenant): each related row
// is in the tenant of the row it hangs from, at every depth.
type rowTenant struct {
	column string
	// typ is column's type, "" when the schema does not declare it.
	typ core.FieldType
	// expr names column where the statement can see the owner's row: the
	// owner's table in a filter, the facet base's projection, the include
	// query's join.
	expr string
}

// over is sc for a statement over schema's table: a row tenant names the
// table's column, typed as schema declares it.
func (sc relationScope) over(d Dialect, schema *ResourceSchema) relationScope {
	if sc.row.column != "" && schema != nil {
		sc.row.typ = tenantColumnType(schema, sc.row.column)
		sc.row.expr = qualified(d, schema.Name, sc.row.column)
	}
	return sc
}

// tenantColumnType is the declared type of schema's column, "" when schema
// declares none: a pair table's tenant column, or a hand-built schema.
func tenantColumnType(schema *ResourceSchema, column string) core.FieldType {
	if schema == nil {
		return ""
	}
	if f, ok := schema.SystemFields[column]; ok {
		return f.DataType
	}
	for _, f := range schema.Fields {
		if f.Relation == nil && f.Path == nil && f.RowColumn() == column {
			return f.DataType
		}
	}
	return ""
}

// check refuses a scope that holds relations two ways, or a row tenant
// column that is no identifier.
func (sc relationScope) check() error {
	if sc.row.column == "" {
		return nil
	}
	if sc.tenant != nil || sc.unscoped {
		return errors.New("filter: relations follow the row's tenant, a bound tenant or none; more than one was set")
	}
	if !core.IsIdentifier(sc.row.column) {
		return fmt.Errorf("filter: row tenant column %q is not an identifier", sc.row.column)
	}
	return nil
}

// ErrRelationTenantRequired is a tenant-scoped pair table joined under a
// scope that names no tenant and does not opt out: the server built the
// list without the tenant it must hold relations to.
var ErrRelationTenantRequired = errors.New("filter: a tenant-scoped pair table is joined without a tenant; bind one or opt out with an unscoped relation scope")

// clause returns " AND <alias>.<column>" compared with the tenant for
// table's tenant column of type typ ("" when undeclared): the bound tenant
// (through bind), or the owner row's tenant column. It is "" for a table
// without one or an unscoped scope, and an error when no scope was chosen.
func (sc relationScope) clause(d Dialect, table, column, alias string, typ core.FieldType, bind func(any) string) (string, error) {
	err := sc.tenantMissing(table, column)
	if err != nil {
		return "", err
	}
	switch {
	case column == "" || sc.unscoped:
		return "", nil
	case sc.row.column != "":
		return sc.row.match(qualified(d, alias, column), typ), nil
	}
	return " AND " + qualified(d, alias, column) + " = " + bind(sc.tenant), nil
}

// tenantMissing is ErrRelationTenantRequired for a relation table whose
// tenant column sc holds to no tenant: sc binds none, follows no row's and
// does not opt out. Every reader of a relation table, its SQL and its
// lookups outside SQL, refuses it alike.
func (sc relationScope) tenantMissing(table, column string) error {
	if column == "" || sc.unscoped || sc.row.column != "" || sc.tenant != nil {
		return nil
	}
	return fmt.Errorf("relation table %s (tenant column %s): %w", table, column, ErrRelationTenantRequired)
}

// match is " AND <col> = <the row's tenant>". A UUID column and a string
// column compare by the UUID's canonical text, which the string column
// holds (a tenant key binds a UUID's canonical text to both): PostgreSQL
// has no operator between the two types. The string side is left bare so
// its index serves the comparison.
func (rt rowTenant) match(col string, typ core.FieldType) string {
	row := rt.expr
	switch {
	case typ == core.FieldTypeUUID && rt.typ == core.FieldTypeString:
		col = "CAST(" + col + " AS TEXT)"
	case typ == core.FieldTypeString && rt.typ == core.FieldTypeUUID:
		row = "CAST(" + row + " AS TEXT)"
	}
	return " AND " + col + " = " + row
}

// WithStartParam sets the starting parameter index offset. The first generated
// placeholder will be startParam+1. For example, WithStartParam(3) produces
// $4, $5, ... (Postgres) or ?4, ?5, ... (SQLite). Default is 0 (first param $1).
// A negative offset is an error from Build.
func WithStartParam(n int) SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.startParam = n }
}

// WithRelationScope scopes the relations the filter reaches
// (RelationScope).
func WithRelationScope(s RelationScope) SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.scope, c.related = s.tenancy(), s.policies() }
}

// checkStartParam refuses a negative placeholder offset: the first
// placeholder, n+1, would be one no dialect numbers.
func checkStartParam(n int) error {
	if n < 0 {
		return fmt.Errorf("filter: start parameter %d is negative: the first placeholder is the offset plus one", n)
	}
	return nil
}

// SQLBuilder generates parameterized SQL from a filter AST using a dialect.
type SQLBuilder struct {
	schema     *ResourceSchema
	dialect    Dialect
	startParam int
	scope      relationScope
	related    relatedPolicies
	// depth and visible are a relation predicate's nesting and the table
	// names its enclosing queries already use (buildRelated).
	depth   int
	visible []string
	// exprs are the computed columns of the schema the builder compiles,
	// resolved for its dialect.
	exprs columnExprs
}

type buildState struct {
	paramIndex int
	args       []any
	// bind, when set, binds values instead: a predicate rendered into a
	// statement another package numbers (VisibleRelated).
	bind func(any) string
	// negated is whether an odd number of $not enclose the node being built
	// within its statement or subquery, where a condition's Unknown and
	// False may select differently (Dialect.TwoValuedCondition).
	negated bool
}

// selecting builds a predicate a WHERE clause selects with: Unknown and
// False agree there, whatever negation encloses the subquery it filters.
func (s *buildState) selecting(build func() (string, error)) (string, error) {
	enclosing := s.negated
	s.negated = false
	defer func() { s.negated = enclosing }()
	return build()
}

func (s *buildState) nextParam() int {
	s.paramIndex++
	return s.paramIndex
}

// binder returns the bind callback Dialect.Condition uses: append the value,
// return its placeholder.
func (s *buildState) binder(d Dialect) func(any) string {
	if s.bind != nil {
		return s.bind
	}
	return func(v any) string {
		s.args = append(s.args, v)
		return d.Placeholder(s.nextParam())
	}
}

// NewSQLBuilder creates a new SQLBuilder for the given schema and dialect.
func NewSQLBuilder(schema *ResourceSchema, dialect Dialect, opts ...SQLBuilderOption) *SQLBuilder {
	cfg := &sqlBuilderConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return &SQLBuilder{
		schema:     schema,
		dialect:    dialect,
		startParam: cfg.startParam,
		scope:      cfg.scope.over(dialect, schema),
		related:    cfg.related,
	}
}

// Build walks the AST and generates a parameterized SQL WHERE clause. A
// *core.ParseError means the request is at fault; anything the server
// supplied that cannot be built — scope, policy, a relation's declaration,
// a programmatic AST, the dialect — is a plain error wrapping its cause,
// such as ErrRelatedPolicyRequired or ErrPolicyInvalid.
func (b *SQLBuilder) Build(ast *FilterAST) (*SQLResult, error) {
	exprs, err := b.schema.ComputedSQL(b.dialect)
	if err != nil {
		return nil, err
	}
	return b.build(ast, exprs)
}

// build is Build with the schema's computed columns resolved for the
// builder's dialect.
func (b *SQLBuilder) build(ast *FilterAST, exprs columnExprs) (*SQLResult, error) {
	if err := checkStartParam(b.startParam); err != nil {
		return nil, err
	}
	if err := b.scope.check(); err != nil {
		return nil, err
	}
	if ast == nil || ast.Root == nil {
		return &SQLResult{Where: "1=1"}, nil
	}
	root := *b
	root.exprs = exprs
	root.related = b.related.at(time.Now().UTC())

	s := &buildState{paramIndex: b.startParam, args: make([]any, 0, 8)}

	where, err := root.buildNode(s, ast.Root)
	if err != nil {
		if pe, ok := errors.AsType[*core.ParseError](err); ok {
			return nil, pe
		}
		if ce, ok := errors.AsType[*computedError](err); ok {
			return nil, ce.err
		}
		return nil, fmt.Errorf("filter: building SQL: %w", err)
	}

	return &SQLResult{
		Where:      where,
		Args:       s.args,
		ParamCount: s.paramIndex - b.startParam,
	}, nil
}

func (b *SQLBuilder) buildNode(s *buildState, node *FilterNode) (string, error) {
	if node == nil { // backstop: the parser rejects nil-producing inputs, but programmatic ASTs may not
		return "", errors.New("filter: nil AST node")
	}
	switch node.Op {
	case OpAnd:
		return b.buildLogical(s, node, " AND ")
	case OpOr:
		return b.buildLogical(s, node, " OR ")
	case OpNot:
		return b.buildNot(s, node)
	case OpRawSQL:
		return b.buildRawSQL(s, node)
	case OpUnknown:
		return unknownSQL, nil
	case OpMember:
		return b.buildMember(s, node)
	case OpKeyIn:
		return b.buildKeySet(s, node)
	default:
		return b.buildCondition(s, node)
	}
}

// buildRawSQL injects a pre-built SQL expression in parentheses, so it is one
// operand of whatever joins it: a raw `a OR b` ANDed with the tenant floor
// must not bind the floor to `a` alone. Its local `$N` placeholders are
// renumbered to the builder's parameter indices, each bound to the matching
// RawArgs entry.
//
// Each `$N` occurrence, left to right, gets its own parameter slot and its own
// RawArgs[N-1] append, so the result is correct for numbered (`$K`) and
// positional (`?`) dialects, repeated and out-of-order placeholders included.
// rawPlaceholderRe matches whole tokens, so `$1` never matches inside `$10`.
func (b *SQLBuilder) buildRawSQL(s *buildState, node *FilterNode) (string, error) {
	if strings.TrimSpace(node.RawSQL) == "" { // an empty predicate would admit every row
		return "", errors.New("filter: raw SQL node has no SQL")
	}
	var rebuildErr error
	out := rawPlaceholderRe.ReplaceAllStringFunc(node.RawSQL, func(tok string) string {
		n, err := strconv.Atoi(tok[1:])
		if err != nil || n < 1 || n > len(node.RawArgs) {
			rebuildErr = fmt.Errorf("raw SQL placeholder %s out of range [1,%d]", tok, len(node.RawArgs))
			return tok
		}
		idx := s.nextParam()
		s.args = append(s.args, node.RawArgs[n-1])
		return b.dialect.Placeholder(idx)
	})
	if rebuildErr != nil {
		return "", rebuildErr
	}
	return "(" + out + ")", nil
}

func (b *SQLBuilder) buildLogical(s *buildState, node *FilterNode, joiner string) (string, error) {
	if len(node.Children) == 0 { // never emit `()` — parser rejects these; programmatic ASTs fail loud
		return "", fmt.Errorf("filter: %s node has no children", node.Op)
	}
	if len(node.Children) == 1 {
		return b.buildNode(s, node.Children[0])
	}
	var sb strings.Builder
	sb.WriteByte('(')
	for i, child := range node.Children {
		if i > 0 {
			sb.WriteString(joiner)
		}
		sql, err := b.buildNode(s, child)
		if err != nil {
			return "", err
		}
		sb.WriteString(sql)
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

func (b *SQLBuilder) buildNot(s *buildState, node *FilterNode) (string, error) {
	if len(node.Children) == 0 { // parser guarantees one child; programmatic ASTs fail loud
		return "", errors.New("filter: $not node has no children")
	}
	s.negated = !s.negated
	sql, err := b.buildNode(s, node.Children[0])
	s.negated = !s.negated
	if err != nil {
		return "", err
	}
	return "NOT (" + sql + ")", nil
}

func (b *SQLBuilder) buildCondition(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil {
		return "", errors.New("condition node has no field schema")
	}
	node = b.computed(node)
	if (node.Field.Elements != nil || isScalarArray(node.Field)) && node.Field.Relation == nil && (node.Op == OpSome || node.Op == OpNone) {
		return b.buildElements(s, node)
	}
	if node.Field.Relation != nil {
		if node.Field.Related != nil && node.Op != OpSome && node.Op != OpNone {
			// A key comparison built without the parser — a policy, a
			// programmatic AST — still reaches only the visible related rows.
			n, err := relatedShorthand(node)
			if err != nil {
				return "", err
			}
			node = n
		}
		if node.Field.Related == nil {
			return b.buildPairTable(s, node)
		}
		return b.buildRelated(s, node)
	}
	bind := s.binder(b.dialect)
	if !s.negated {
		sql, ok, err := b.dialect.TwoValuedCondition(node.Field, node.Op, node.Value, bind)
		if err != nil || ok {
			return sql, err
		}
	}
	if node.Field.Path != nil && node.Field.DataType == core.FieldTypeArray {
		return b.buildArrayPath(s, node)
	}
	return b.dialect.Condition(node.Field, node.Op, node.Value, bind)
}

// computed is n reading its field through the computed column's expression
// when the field is one.
func (b *SQLBuilder) computed(n *FilterNode) *FilterNode {
	f := b.exprs.field(n.Field)
	if f == n.Field {
		return n
	}
	out := *n
	out.Field = f
	return &out
}

// buildPairTable compiles a key comparison on a pair table the relation tag
// declares as the relation predicate it is, through buildRelated: the pair
// table is the target, its typed key the target columns. A key of several
// columns takes only the NULL tests (checkPairKeyOp).
//
//	$eq / $in / other ops  EXISTS     (… AND <tgt> <op> …)
//	$ne / $nin             NOT EXISTS (… AND <tgt> = / IN …)
//	$null: true            NOT EXISTS (…)   — no related rows
//	$null: false           EXISTS     (…)   — at least one related row
//
// An empty `$in` matches no owner and an empty `$nin` every owner, without
// a subquery.
func (b *SQLBuilder) buildPairTable(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	rel := f.Relation
	err := checkPairKeyOp(f, node.Op)
	if err != nil {
		return "", err
	}
	op, cond := OpSome, node.Op
	switch node.Op {
	case OpNull:
		op, cond = OpNone, ""
	case OpNotNull:
		cond = ""
	case OpNe:
		op, cond = OpNone, OpEq
	case OpIn, OpNin:
		arr, ok := core.AnySlice(node.Value)
		if !ok {
			return "", fmt.Errorf("$%s requires an array value, got %s", node.Op, gowalker.TypeText(reflect.TypeOf(node.Value)))
		}
		if len(arr) == 0 {
			if node.Op == OpIn {
				return "1=0", nil
			}
			return "1=1", nil
		}
		if node.Op == OpNin {
			op, cond = OpNone, OpIn
		}
	}
	pair := *f
	pair.Related = &ResourceSchema{Name: rel.JoinTable, PrimaryKey: slices.Clone(rel.TargetColumns)}
	lowered := &FilterNode{Field: &pair, Op: op}
	if len(rel.TargetColumns) == 1 {
		target := rel.TargetColumns[0]
		key := &FieldSchema{
			ExternalName: f.ExternalName, Table: rel.JoinTable, Column: target,
			DataType: f.DataType, NumberKind: f.NumberKind,
		}
		pair.Related.SystemFields = map[string]*FieldSchema{target: key}
		if cond != "" {
			lowered.Children = []*FilterNode{{Field: key, Op: cond, Value: node.Value}}
		}
	}
	return b.buildRelated(s, lowered)
}

// checkPairKeyOp refuses a comparison on a pair table compared by a target
// key of several columns, which one value cannot name: such a field takes
// only the NULL tests, whether the owner has pairs.
func checkPairKeyOp(f *FieldSchema, op FilterOperator) error {
	if len(f.Relation.TargetColumns) == 1 || op == OpNull || op == OpNotNull {
		return nil
	}
	return invalidRelation(f, fmt.Sprintf("compares a key of several columns, which $%s cannot name; it takes $null", op))
}

// nullableDateValue is the SQL boundary: typed nil pointers are SQL NULL,
// while request normalization separately refuses null scalar operands.
func nullableDateValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	return core.NormalizeScalar(core.FieldTypeDate, reflect.Invalid, rv.Interface())
}
