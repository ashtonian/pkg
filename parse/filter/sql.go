package filter

import (
	"errors"
	"fmt"
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
}

// ErrUnknownDSN is returned by DialectFromDSN for a DSN whose scheme names
// neither PostgreSQL nor SQLite.
var ErrUnknownDSN = dialect.ErrUnknownDSN

// DialectFromDSN returns the Dialect of a database DSN: postgres:// (and
// postgresql://, cockroach://, crdb://) or a SQLite file or :memory:.
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

// comparison renders the operators every dialect spells alike: equality,
// ordering, membership and the NULL tests. ok is false for the rest.
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
	case OpIn, OpNin:
		sql, err := membership(expr, op, value, bind)
		return sql, true, err
	}
	return "", false, nil
}

// membership renders `$in` / `$nin`. An empty set matches nothing (`$in`)
// or everything (`$nin`); `col IN ()` is a syntax error on PostgreSQL
// .
func membership(expr string, op FilterOperator, value any, bind func(any) string) (string, error) {
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
	var sb strings.Builder
	sb.WriteString(expr)
	if op == OpIn {
		sb.WriteString(" IN (")
	} else {
		sb.WriteString(" NOT IN (")
	}
	for i, item := range arr {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(bind(item))
	}
	sb.WriteByte(')')
	return sb.String(), nil
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
// tenant, or explicitly unscoped. The zero value is neither, and any
// relation declaring a TenantColumn fails closed.
type relationScope struct {
	tenant    any
	hasTenant bool
	unscoped  bool
}

// clause returns " AND <alias>.<tenant_col> = <ph>" for a tenant-scoped
// relation (binding the tenant through bind), "" for an unscoped junction,
// or an error when the relation is tenant-scoped but no scope was chosen.
func (sc relationScope) clause(rel *core.RelationSchema, alias string, bind func(any) string) (string, error) {
	if rel.TenantColumn == "" || sc.unscoped {
		return "", nil
	}
	if !sc.hasTenant {
		return "", fmt.Errorf("relation junction %s is tenant-scoped (%s): bind a tenant or opt out explicitly with an unscoped-relations option", rel.JoinTable, rel.TenantColumn)
	}
	return " AND " + alias + "." + rel.TenantColumn + " = " + bind(sc.tenant), nil
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
		scope:      cfg.scope,
		related:    cfg.related,
	}
}

// Build walks the AST and generates a parameterized SQL WHERE clause.
func (b *SQLBuilder) Build(ast *FilterAST) (*SQLResult, error) {
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
	case OpMember:
		return b.buildMember(s, node)
	default:
		return b.buildCondition(s, node)
	}
}

// buildRawSQL injects a pre-built SQL expression, renumbering its local `$N`
// placeholders to the builder's current parameter indices and binding each to
// the matching RawArgs entry.
//
// Renumbering is token-based (via rawPlaceholderRe), not substring-based: the
// old two-pass `strings.Replace(sql, "$1", …, 1)` corrupted multi-digit
// placeholders because "$1" substring-matches inside "$10"/"$11". Each `$N`
// OCCURRENCE — in left-to-right order — gets its own parameter slot and its own
// RawArgs[N-1] append, so the result is correct for both numbered (`$K`) and
// positional (`?`) dialects, including repeated and out-of-order placeholders.
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
	return out, nil
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
	sql, err := b.buildNode(s, node.Children[0])
	if err != nil {
		return "", err
	}
	return "NOT (" + sql + ")", nil
}

func (b *SQLBuilder) buildCondition(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil {
		return "", errors.New("condition node has no field schema")
	}
	if node.Field.Elements != nil && node.Field.Relation == nil && (node.Op == OpSome || node.Op == OpNone) {
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
		if node.Op == OpSome || node.Op == OpNone {
			return b.buildRelated(s, node)
		}
		return b.buildRelationCondition(s, node)
	}
	return b.dialect.Condition(node.Field, node.Op, node.Value, s.binder(b.dialect))
}

// relationAlias names the junction inside a relation predicate's
// subquery; each EXISTS has its own scope, so the alias never collides with
// a sibling predicate.
const relationAlias = "rel__"

// buildRelationCondition compiles a relation predicate as a correlated
// EXISTS / NOT EXISTS against the junction, so boolean
// composition (AND / OR / NOT) keeps set semantics and no join multiplies
// owner rows:
//
//	$eq / $in / other ops  EXISTS     (… AND rel__.<tgt> <op> …)
//	$ne / $nin             NOT EXISTS (… AND rel__.<tgt> = / IN …)
//	$null: true            NOT EXISTS (…)   — no related rows
//	$null: false, $exists  EXISTS     (…)   — at least one related row
//
// The old subquery form negated $nin twice and the join form bound an
// array to a scalar placeholder, repeated unaliased joins and multiplied
// rows.
func (b *SQLBuilder) buildRelationCondition(s *buildState, node *FilterNode) (string, error) {
	rel := node.Field.Relation
	pk := rel.PrimaryKey
	if pk == "" {
		pk = "id"
	}
	bind := s.binder(b.dialect)
	target := &FieldSchema{ExternalName: node.Field.ExternalName, SQLExpr: relationAlias + "." + rel.TargetColumn, DataType: node.Field.DataType}

	negate := false
	cond := ""
	switch node.Op {
	case OpNull:
		negate = true
	case OpNotNull:
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
		sql, err := membership(target.SQLExpr, OpIn, arr, bind)
		if err != nil {
			return "", err
		}
		cond = sql
		negate = node.Op == OpNin
	default:
		op := node.Op
		if op == OpNe {
			op, negate = OpEq, true
		}
		sql, err := b.dialect.Condition(target, op, node.Value, bind)
		if err != nil {
			return "", fmt.Errorf("building relation condition: %w", err)
		}
		cond = sql
	}

	var sb strings.Builder
	if negate {
		sb.WriteString("NOT ")
	}
	sb.WriteString("EXISTS (SELECT 1 FROM ")
	sb.WriteString(rel.JoinTable)
	sb.WriteByte(' ')
	sb.WriteString(relationAlias)
	sb.WriteString(" WHERE ")
	sb.WriteString(relationAlias + "." + rel.SourceColumn + " = " + b.schema.Name + "." + pk)
	if cond != "" {
		sb.WriteString(" AND ")
		sb.WriteString(cond)
	}
	scope, err := b.scope.clause(rel, relationAlias, bind)
	if err != nil {
		return "", err
	}
	sb.WriteString(scope)
	sb.WriteByte(')')
	return sb.String(), nil
}
