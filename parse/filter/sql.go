package filter

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// rawPlaceholderRe matches a `$N` positional placeholder as a whole token so a
// renumber of `$1` never substring-corrupts a multi-digit `$10` / `$11`.
var rawPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

// Dialect generates database-specific SQL.
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

	// DecimalColumn renders a decimal column as the dialect's exact decimal
	// value, the value PathExpr reads a decimal member as and DecimalParam
	// binds: equal decimals are equal values.
	DecimalColumn(column string) string

	// DecimalParam renders the placeholder of a bound decimal, its
	// canonical text (core.DecimalText), as the dialect's exact decimal
	// value.
	DecimalParam(placeholder string) string

	// DecimalOrder renders a decimal value (DecimalColumn, PathExpr or
	// DecimalParam) as what orders decimals exactly: the value itself where
	// the database's own type orders it, a key that orders as it does where
	// there is none.
	DecimalOrder(value string) string
}

// TwoValuedDialect is a Dialect with a second rendering of some predicates,
// one an index serves better, that is exact only where a predicate's Unknown
// and False select alike. SQLBuilder uses it for a condition an even number
// of negations (usually none) enclose within its statement or subquery: the
// statement is then monotone in the condition (False < Unknown < True), and
// a Kleene result that is True with the condition Unknown is True whatever
// it is, so a row is selected exactly when it would be with the condition
// False.
type TwoValuedDialect interface {
	Dialect

	// TwoValuedCondition renders `field <op> value` as a predicate True
	// exactly where Condition's is True. ok is false when it has no such
	// rendering for the predicate; it then binds nothing.
	TwoValuedCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (sql string, ok bool, err error)
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
	if _, ok := base.(dialect.SQLite); ok {
		return SQLiteDialect{}, nil
	}
	return PostgresDialect{}, nil
}

// decimalComparison renders a decimal field's equality or ordering, expr
// being the field's exact value (FieldExpr): it compares with the value's
// canonical text bound as Dialect.DecimalParam reads it, and orders through
// Dialect.DecimalOrder. ok is false for any other operator.
func decimalComparison(d Dialect, field *FieldSchema, expr string, op FilterOperator, value any, bind func(any) string) (sql string, ok bool, err error) {
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
	param := func(v any) string { return d.DecimalParam(bind(v)) }
	if isOrdering(op) {
		expr = d.DecimalOrder(expr)
		param = func(v any) string { return d.DecimalOrder(d.DecimalParam(bind(v))) }
	}
	return comparison(expr, op, value, param)
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
		return "", fmt.Errorf("$%s requires an array value, got %T", op, value)
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
		return "", fmt.Errorf("$%s requires a string value, got %T", op, value)
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
	related    map[string]*PolicyConstraints
}

// relationScope is how a builder scopes tenant-scoped junctions: bound to a
// tenant, held to the tenant of the row they hang from, or explicitly
// unscoped. The zero value is none of them, and any relation declaring a
// TenantColumn fails closed.
type relationScope struct {
	tenant    any
	hasTenant bool
	unscoped  bool
	row       rowTenant
}

// rowTenant is the owner's tenant column a list whose rows span tenants
// holds every tenant-scoped relation to (ListRowTenant): each related row
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
func (sc relationScope) over(schema *ResourceSchema) relationScope {
	if sc.row.column != "" && schema != nil {
		sc.row.typ = tenantColumnType(schema, sc.row.column)
		sc.row.expr = schema.Name + "." + sc.row.column
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
	if sc.hasTenant || sc.unscoped {
		return errors.New("filter: relations follow the row's tenant, a bound tenant or none; more than one was set")
	}
	if !core.IsIdentifier(sc.row.column) {
		return fmt.Errorf("filter: row tenant column %q is not an identifier", sc.row.column)
	}
	return nil
}

// clause returns " AND <alias>.<column>" compared with the tenant for
// table's tenant column of type typ ("" when undeclared): the bound tenant
// (through bind), or the owner row's tenant column. It is "" for a table
// without one or an unscoped scope, and an error when no scope was chosen.
func (sc relationScope) clause(table, column, alias string, typ core.FieldType, bind func(any) string) (string, error) {
	switch {
	case column == "" || sc.unscoped:
		return "", nil
	case sc.row.column != "":
		return sc.row.match(alias+"."+column, typ), nil
	case !sc.hasTenant:
		return "", fmt.Errorf("relation junction %s is tenant-scoped (%s): bind a tenant or opt out explicitly with an unscoped-relations option", table, column)
	}
	return " AND " + alias + "." + column + " = " + bind(sc.tenant), nil
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
func WithStartParam(n int) SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.startParam = n }
}

// WithTenant binds the tenant every tenant-scoped relation junction is
// restricted to. Required whenever a filter touches a relation
// whose RelationSchema.TenantColumn is set; without it (or
// WithUnscopedRelations) the build fails rather than matching owners
// through other tenants' junction rows.
func WithTenant(value any) SQLBuilderOption {
	return func(c *sqlBuilderConfig) {
		c.scope.tenant = value
		c.scope.hasTenant = value != nil
	}
}

// WithUnscopedRelations deliberately builds tenant-scoped relation
// junctions without a tenant predicate — for platform-level tooling that
// operates across tenants. Never use it on a request path.
func WithUnscopedRelations() SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.scope.unscoped = true }
}

// WithRowTenant holds every tenant-scoped relation to the tenant of the row
// it hangs from, read from column of the schema's table, instead of a bound
// tenant: for a filter over rows of more than one tenant, such as a
// governed list's (ComposeConstraints). Each relation's tenant columns, at
// every depth, are compared with the row's. It excludes WithTenant and
// WithUnscopedRelations.
func WithRowTenant(column string) SQLBuilderOption {
	return func(c *sqlBuilderConfig) { c.scope.row = rowTenant{column: column} }
}

// SQLBuilder generates parameterized SQL from a filter AST using a dialect.
type SQLBuilder struct {
	schema     *ResourceSchema
	dialect    Dialect
	startParam int
	scope      relationScope
	related    map[string]*PolicyConstraints
	// depth and visible are a relation predicate's nesting and the table
	// names its enclosing queries already use (buildRelated).
	depth   int
	visible []string
}

type buildState struct {
	paramIndex int
	args       []any
	// bind, when set, binds values instead: a predicate rendered into a
	// statement another package numbers (VisibleRelated).
	bind func(any) string
	// negated is whether an odd number of $not enclose the node being built
	// within its statement or subquery, where a condition's Unknown and
	// False may select differently (TwoValuedDialect).
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
		scope:      cfg.scope.over(schema),
		related:    cfg.related,
	}
}

// Build walks the AST and generates a parameterized SQL WHERE clause.
func (b *SQLBuilder) Build(ast *FilterAST) (*SQLResult, error) {
	if err := b.scope.check(); err != nil {
		return nil, err
	}
	if ast == nil || ast.Root == nil {
		return &SQLResult{Where: "1=1"}, nil
	}

	s := &buildState{paramIndex: b.startParam, args: make([]any, 0, 8)}

	where, err := b.buildNode(s, ast.Root)
	if err != nil {
		if pe, ok := errors.AsType[*core.ParseError](err); ok {
			return nil, pe
		}
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindFilter,
			Code:    ECodeSQLGenerationFailed.String(),
			Message: "SQL generation failed: " + err.Error(),
			Cause:   err,
		}
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
	if node.RawSQL == "" {
		return "1=1", nil
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
	if tv, ok := b.dialect.(TwoValuedDialect); ok && !s.negated {
		sql, ok, err := tv.TwoValuedCondition(node.Field, node.Op, node.Value, bind)
		if err != nil || ok {
			return sql, err
		}
	}
	if node.Field.Path != nil && node.Field.DataType == core.FieldTypeArray {
		return b.buildArrayPath(s, node)
	}
	return b.dialect.Condition(node.Field, node.Op, node.Value, bind)
}

// buildPairTable compiles a key comparison on a pair table the relation tag
// declares as the relation predicate it is, through buildRelated: the pair
// table is the target, its one typed key the target column.
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
			return "", fmt.Errorf("$%s requires an array value, got %T", node.Op, node.Value)
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
	key := &FieldSchema{
		ExternalName: f.ExternalName, SQLExpr: rel.JoinTable + "." + rel.TargetColumn, Column: rel.TargetColumn,
		DataType: f.DataType, NumberKind: f.NumberKind,
	}
	pair := *f
	pair.Related = &ResourceSchema{Name: rel.JoinTable, PrimaryKey: rel.TargetColumn, SystemFields: map[string]*FieldSchema{rel.TargetColumn: key}}
	lowered := &FilterNode{Field: &pair, Op: op}
	if cond != "" {
		lowered.Children = []*FilterNode{{Field: key, Op: cond, Value: node.Value}}
	}
	return b.buildRelated(s, lowered)
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
