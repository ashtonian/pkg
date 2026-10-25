package filter

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// Lookup is the set a membership constraint tests a row's field against: the
// Project column of the rows of Table that Where selects. SQL
// backends compile it to `field IN (SELECT project FROM table WHERE …)`; a
// backend that holds rows in memory resolves it once per request with
// ResolveMemberships. Identifiers are quoted, so they match names as created:
// Table is a table or view, optionally schema-qualified; Project and every
// condition column are plain column names.
type Lookup struct {
	Table   string
	Project string
	// Where selects the lookup rows; nil selects every row.
	Where *LookupCond
	// Filter further selects the lookup rows by a filter over the lookup
	// table's own schema: a relation's target filter, its tenancy, soft
	// deletion and policy composed in. SQL renders it in the
	// subquery's WHERE; ResolveMemberships sends a lookup with a Filter
	// only to a RelationLookup, so a store that cannot evaluate it refuses
	// the request instead of ignoring it.
	Filter *ParsedFilter
}

// LookupOp is the comparison a LookupCond leaf makes.
type LookupOp string

const (
	// LookupOpEq selects rows whose column equals Value.
	LookupOpEq LookupOp = "eq"
	// LookupOpIn selects rows whose column is one of Values; an empty list
	// selects no row.
	LookupOpIn LookupOp = "in"
	// LookupOpNull selects rows whose column is NULL.
	LookupOpNull LookupOp = "null"
	// LookupOpNotNull selects rows whose column is not NULL.
	LookupOpNotNull LookupOp = "not_null"
	// LookupOpMember selects rows whose column is among the values Lookup
	// projects. ResolveMemberships answers the inner lookup first, so a
	// store sees LookupOpIn in its place.
	LookupOpMember LookupOp = "member"
)

// LookupCond is a condition over one lookup row: a leaf comparing Column, or
// a group of conditions all of which (And) or one of which (Or) hold. It has
// no negation, so a comparison with a NULL column selects nothing, exactly
// as in SQL. Build one with WhereEq, WhereIn, WhereNull, WhereAll and
// WhereAny.
type LookupCond struct {
	Column string
	Op     LookupOp
	Value  any     // LookupOpEq
	Values []any   // LookupOpIn
	Lookup *Lookup // LookupOpMember

	And []*LookupCond
	Or  []*LookupCond
}

// WhereEq selects rows whose column equals v.
func WhereEq(column string, v any) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpEq, Value: v}
}

// WhereIn selects rows whose column is one of vs; no values select no row.
func WhereIn(column string, vs ...any) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpIn, Values: vs}
}

// WhereNull selects rows whose column is NULL.
func WhereNull(column string) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpNull}
}

// WhereNotNull selects rows whose column is not NULL.
func WhereNotNull(column string) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpNotNull}
}

// WhereMember selects rows whose column is among the values l projects.
func WhereMember(column string, l *Lookup) *LookupCond {
	return &LookupCond{Column: column, Op: LookupOpMember, Lookup: l}
}

// WhereAll selects rows every condition selects.
func WhereAll(conds ...*LookupCond) *LookupCond { return &LookupCond{And: conds} }

// WhereAny selects rows at least one condition selects.
func WhereAny(conds ...*LookupCond) *LookupCond { return &LookupCond{Or: conds} }

var (
	lookupIdent      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	lookupQualifiedT = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)
)

// memberFieldTypes are the field types a membership tests: identifiers and
// keys, which every backend compares by value.
var memberFieldTypes = map[core.FieldType]bool{
	core.FieldTypeString: true,
	core.FieldTypeUUID:   true,
	core.FieldTypeInt:    true,
}

func (l *Lookup) validate() error {
	if l == nil {
		return invalidLookup("a membership constraint needs a lookup")
	}
	if !lookupQualifiedT.MatchString(l.Table) {
		return invalidLookup(fmt.Sprintf("lookup table %q is not an identifier", l.Table))
	}
	if !lookupIdent.MatchString(l.Project) {
		return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", l.Project))
	}
	if l.Where != nil {
		if err := l.Where.validate(); err != nil {
			return err
		}
	}
	if l.Filter != nil && l.Filter.AST != nil && hasRelation(l.Filter.AST.Root) {
		return invalidLookup(fmt.Sprintf("lookup on %s filters by a relation; resolve it first", l.Table))
	}
	return nil
}

func (c *LookupCond) validate() error {
	if c == nil {
		return invalidLookup("lookup condition is nil")
	}
	forms := 0
	if c.Column != "" || c.Op != "" {
		forms++
	}
	if len(c.And) > 0 {
		forms++
	}
	if len(c.Or) > 0 {
		forms++
	}
	if forms != 1 {
		return invalidLookup("a lookup condition is exactly one of a comparison, And or Or")
	}
	for _, g := range [][]*LookupCond{c.And, c.Or} {
		for _, child := range g {
			if err := child.validate(); err != nil {
				return err
			}
		}
	}
	if len(c.And) > 0 || len(c.Or) > 0 {
		return nil
	}
	if !lookupIdent.MatchString(c.Column) {
		return invalidLookup(fmt.Sprintf("lookup column %q is not an identifier", c.Column))
	}
	switch c.Op {
	case LookupOpEq:
		if isNullValue(c.Value) {
			return invalidLookup(fmt.Sprintf("lookup column %q compares with NULL; use WhereNull", c.Column))
		}
	case LookupOpIn:
		if slices.ContainsFunc(c.Values, isNullValue) {
			return invalidLookup(fmt.Sprintf("lookup column %q lists NULL; use WhereNull", c.Column))
		}
	case LookupOpNull, LookupOpNotNull:
	case LookupOpMember:
		if c.Lookup == nil {
			return invalidLookup(fmt.Sprintf("lookup column %q is a member of no lookup", c.Column))
		}
		return c.Lookup.validate()
	default:
		return invalidLookup(fmt.Sprintf("lookup column %q has unknown operator %q", c.Column, c.Op))
	}
	return nil
}

func invalidLookup(msg string) error {
	return &core.ParseError{Kind: core.ParseErrorKindFilter, Code: ECodeInvalidRelation.String(), Message: msg}
}

// memberNode is the AST node of a membership constraint on field.
func memberNode(field *FieldSchema, l *Lookup) (*FilterNode, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	if !memberFieldTypes[field.DataType] {
		return nil, invalidLookup(fmt.Sprintf("membership on %s field '%s'; a membership tests a string, UUID or integer field",
			field.DataType, field.ExternalName))
	}
	return &FilterNode{Field: field, Op: OpMember, Member: l}, nil
}

// buildMember compiles a membership node to an IN subquery.
func (b *SQLBuilder) buildMember(s *buildState, node *FilterNode) (string, error) {
	if node.Field == nil || FieldExpr(b.dialect, node.Field) == "" || node.Member == nil {
		return "", invalidLookup("membership node has no field expression or lookup")
	}
	l := node.Member
	if err := l.validate(); err != nil {
		return "", err
	}
	sub, err := b.lookupSQL(s, l)
	if err != nil {
		return "", err
	}
	return FieldExpr(b.dialect, node.Field) + " IN " + sub, nil
}

// lookupSQL renders a lookup as the subquery `(SELECT project FROM table
// WHERE …)`: its condition, then its filter on the table's own columns.
func (b *SQLBuilder) lookupSQL(s *buildState, l *Lookup) (string, error) {
	q := b.dialect.QuoteIdent
	table := make([]string, 0, 2)
	for part := range strings.SplitSeq(l.Table, ".") {
		table = append(table, q(part))
	}
	var sb strings.Builder
	sb.WriteString("(SELECT ")
	sb.WriteString(q(l.Project))
	sb.WriteString(" FROM ")
	sb.WriteString(strings.Join(table, "."))
	var where []string
	if l.Where != nil {
		cond, err := b.lookupCondSQL(s, l.Where)
		if err != nil {
			return "", err
		}
		where = append(where, cond)
	}
	if l.Filter != nil && l.Filter.AST != nil && l.Filter.AST.Root != nil {
		fb := &SQLBuilder{schema: &ResourceSchema{Name: l.Table}, dialect: b.dialect, scope: b.scope, related: b.related}
		cond, err := fb.buildNode(s, l.Filter.AST.Root)
		if err != nil {
			return "", err
		}
		where = append(where, cond)
	}
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

func (b *SQLBuilder) lookupCondSQL(s *buildState, c *LookupCond) (string, error) {
	q, bind := b.dialect.QuoteIdent, s.binder(b.dialect)
	join := func(children []*LookupCond, sep string) (string, error) {
		parts := make([]string, len(children))
		for i, child := range children {
			part, err := b.lookupCondSQL(s, child)
			if err != nil {
				return "", err
			}
			parts[i] = part
		}
		return "(" + strings.Join(parts, sep) + ")", nil
	}
	switch {
	case len(c.And) > 0:
		return join(c.And, " AND ")
	case len(c.Or) > 0:
		return join(c.Or, " OR ")
	}
	switch c.Op {
	case LookupOpEq:
		return q(c.Column) + " = " + bind(c.Value), nil
	case LookupOpIn:
		if len(c.Values) == 0 {
			return "1=0", nil
		}
		phs := make([]string, len(c.Values))
		for i, v := range c.Values {
			phs[i] = bind(v)
		}
		return q(c.Column) + " IN (" + strings.Join(phs, ", ") + ")", nil
	case LookupOpNotNull:
		return q(c.Column) + " IS NOT NULL", nil
	case LookupOpMember:
		sub, err := b.lookupSQL(s, c.Lookup)
		if err != nil {
			return "", err
		}
		return q(c.Column) + " IN " + sub, nil
	}
	return q(c.Column) + " IS NULL", nil
}

// MembershipLookup resolves the lookups of membership constraints for a
// backend that does not run them as SQL. LookupValues returns the Project
// values of the rows of l.Table that l.Where selects (MatchLookupRow), as
// ctx — its tenant and transaction — sees them and with no other scoping,
// so the answer is the SQL subquery's. NULL values are returned as nil. An
// error refuses the request.
type MembershipLookup interface {
	LookupValues(ctx context.Context, l *Lookup) ([]any, error)
}

// RelationLookup is a MembershipLookup that also answers lookups with a
// Filter — relation predicates on a backend that does not run SQL.
// LookupFilteredValues returns the Project values of the rows of l.Table
// that both l.Where (MatchLookupRow) and l.Filter select, the filter
// evaluated by the store's own filter evaluator over the table's rows.
type RelationLookup interface {
	MembershipLookup
	LookupFilteredValues(ctx context.Context, l *Lookup) ([]any, error)
}

// ResolveMemberships returns f with every membership constraint replaced by
// the predicate its resolved set amounts to, which a backend evaluating rows
// in memory already implements:
//
//   - the field `$in` the non-NULL values, normalized as policy values are —
//     no rows make it false, NULL or not, as SQL's `x IN (empty subquery)`;
//   - OR'd with SQL's unknown when a resolved value is NULL, as SQL's
//     `x IN (…, NULL)` is unknown unless x is a member.
//
// Each lookup is resolved once, before any row is read. With a nil lookup
// every membership is SQL's unknown: it admits no row the constraint would
// refuse, under AND, OR and NOT. f itself is not modified.
//
// Relation predicates resolve the same way (RelationLookup): `$some` is the
// owner's correlation field `$in` the related rows' correlation values,
// `$none` its negation or a NULL correlation, as NOT EXISTS admits one. They
// need a lookup and, for tenant-scoped relations, ResolveTenant; without one
// they are refused rather than answered empty.
func ResolveMemberships(ctx context.Context, f *ParsedFilter, lookup MembershipLookup, opts ...ResolveOption) (*ParsedFilter, error) {
	if f == nil || f.AST == nil || !hasMember(f.AST.Root) && !hasRelation(f.AST.Root) {
		return f, nil
	}
	cfg := resolveConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	r := newMemberResolver(ctx, lookup, cfg)
	root, err := r.resolve(f.AST.Root)
	if err != nil {
		return nil, err
	}
	out := *f
	out.AST = &FilterAST{Root: root}
	return &out, nil
}

type memberResolver struct {
	ctx      context.Context
	lookup   MembershipLookup
	resolved map[*Lookup]*FilterNode
	norm     *FilterParser
	cfg      resolveConfig
}

func newMemberResolver(ctx context.Context, lookup MembershipLookup, cfg resolveConfig) *memberResolver {
	return &memberResolver{
		ctx: ctx, lookup: lookup, cfg: cfg,
		resolved: map[*Lookup]*FilterNode{},
		norm:     NewFilterParser(&ResourceSchema{}, &core.ParserConfig{MaxDepth: math.MaxInt, MaxArraySize: math.MaxInt}),
	}
}

func (r *memberResolver) resolve(n *FilterNode) (*FilterNode, error) {
	if n == nil || !hasMember(n) && !hasRelation(n) {
		return n, nil
	}
	if n.Field != nil && n.Field.Relation != nil {
		return r.relation(n)
	}
	if n.Op == OpMember {
		return r.member(n)
	}
	out := *n
	out.Children = make([]*FilterNode, len(n.Children))
	for i, c := range n.Children {
		rc, err := r.resolve(c)
		if err != nil {
			return nil, err
		}
		out.Children[i] = rc
	}
	return &out, nil
}

func (r *memberResolver) member(n *FilterNode) (*FilterNode, error) {
	if r.lookup == nil {
		return unknownNode(), nil
	}
	if node, ok := r.resolved[n.Member]; ok {
		return node, nil
	}
	if err := n.Member.validate(); err != nil {
		return nil, err
	}
	members, hasNull, err := r.values(n.Field, n.Member)
	if err != nil {
		return nil, err
	}
	node := &FilterNode{Field: n.Field, Op: OpIn, Value: members}
	if hasNull {
		node = &FilterNode{Op: OpOr, Children: []*FilterNode{node, unknownNode()}}
	}
	r.resolved[n.Member] = node
	return node, nil
}

// values answers l and returns its distinct non-NULL values normalized for
// field, and whether a NULL was among them.
func (r *memberResolver) values(field *FieldSchema, l *Lookup) ([]any, bool, error) {
	raw, hasNull, err := r.lookupValues(l)
	if err != nil {
		return nil, false, err
	}
	members := make([]any, 0, len(raw))
	seen := make(map[any]struct{}, len(raw))
	for _, v := range raw {
		nv, err := r.norm.normalizeMember(field, projectedValue(v))
		if err != nil {
			return nil, false, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
		}
		if reflect.TypeOf(nv).Comparable() {
			if _, dup := seen[nv]; dup {
				continue
			}
			seen[nv] = struct{}{}
		}
		members = append(members, nv)
	}
	return members, hasNull, nil
}

// inner is l with every member condition answered: a store sees only
// comparisons, the inner lookups' values as LookupOpIn.
func (r *memberResolver) inner(l *Lookup) (*Lookup, error) {
	if l.Where == nil || !hasMemberCond(l.Where) {
		return l, nil
	}
	where, err := r.innerCond(l.Where)
	if err != nil {
		return nil, err
	}
	out := *l
	out.Where = where
	return &out, nil
}

func (r *memberResolver) innerCond(c *LookupCond) (*LookupCond, error) {
	if c.Op == LookupOpMember {
		vs, _, err := r.lookupValues(c.Lookup)
		if err != nil {
			return nil, err
		}
		return WhereIn(c.Column, vs...), nil
	}
	if len(c.And) == 0 && len(c.Or) == 0 {
		return c, nil
	}
	out := *c
	group := func(cs []*LookupCond) ([]*LookupCond, error) {
		res := make([]*LookupCond, len(cs))
		for i, child := range cs {
			rc, err := r.innerCond(child)
			if err != nil {
				return nil, err
			}
			res[i] = rc
		}
		return res, nil
	}
	var err error
	if out.And, err = group(c.And); err != nil {
		return nil, err
	}
	if out.Or, err = group(c.Or); err != nil {
		return nil, err
	}
	return &out, nil
}

// lookupValues answers l — its inner lookups first — as stored values,
// NULLs dropped and reported.
func (r *memberResolver) lookupValues(l *Lookup) ([]any, bool, error) {
	l, err := r.inner(l)
	if err != nil {
		return nil, false, err
	}
	if err = r.ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
	}
	var values []any
	if l.Filter != nil {
		rl, ok := r.lookup.(RelationLookup)
		if !ok {
			return nil, false, invalidLookup(fmt.Sprintf("lookup on %s filters its rows; the membership lookup cannot evaluate filters (RelationLookup)", l.Table))
		}
		values, err = rl.LookupFilteredValues(r.ctx, l)
	} else {
		values, err = r.lookup.LookupValues(r.ctx, l)
	}
	if err != nil {
		return nil, false, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
	}
	out := make([]any, 0, len(values))
	hasNull := false
	for _, v := range values {
		if isNullValue(v) {
			hasNull = true
			continue
		}
		out = append(out, derefValue(v))
	}
	return out, hasNull, nil
}

func hasMemberCond(c *LookupCond) bool {
	if c == nil {
		return false
	}
	if c.Op == LookupOpMember {
		return true
	}
	return slices.ContainsFunc(c.And, hasMemberCond) || slices.ContainsFunc(c.Or, hasMemberCond)
}

func hasMember(n *FilterNode) bool {
	if n == nil {
		return false
	}
	if n.Op == OpMember {
		return true
	}
	return slices.ContainsFunc(n.Children, hasMember)
}

// MatchLookupRow reports whether cond selects a lookup row, given its column
// values: values compare as SQL compares stored values — numbers by exact
// value, times by instant, UUIDs as their canonical text, strings by content
// and a NULL column selects nothing. A nil cond selects every row; a
// column the row lacks is an error, so a misspelt column cannot silently
// select nothing.
func MatchLookupRow(cond *LookupCond, column func(name string) (value any, ok bool)) (bool, error) {
	if cond == nil {
		return true, nil
	}
	switch {
	case len(cond.And) > 0:
		for _, c := range cond.And {
			ok, err := MatchLookupRow(c, column)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case len(cond.Or) > 0:
		for _, c := range cond.Or {
			ok, err := MatchLookupRow(c, column)
			if err != nil || ok {
				return ok, err
			}
		}
		return false, nil
	}
	v, ok := column(cond.Column)
	if !ok {
		return false, invalidLookup(fmt.Sprintf("lookup row has no column %q", cond.Column))
	}
	v = derefValue(v)
	switch cond.Op {
	case LookupOpNull:
		return isNullValue(v), nil
	case LookupOpNotNull:
		return !isNullValue(v), nil
	case LookupOpMember:
		return false, invalidLookup(fmt.Sprintf("lookup column %q is a member of an unanswered lookup; ResolveMemberships answers it", cond.Column))
	case LookupOpEq:
		return lookupEqual(v, derefValue(cond.Value)), nil
	case LookupOpIn:
		for _, want := range cond.Values {
			if lookupEqual(v, derefValue(want)) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, invalidLookup(fmt.Sprintf("lookup column %q has unknown operator %q", cond.Column, cond.Op))
}

// lookupEqual is SQL equality of two non-NULL stored values.
func lookupEqual(a, b any) bool {
	if isNullValue(a) || isNullValue(b) {
		return false
	}
	if c, ok := core.CompareNumbers(a, b); ok {
		return c == 0
	}
	if ta, ok := a.(time.Time); ok {
		tb, ok := b.(time.Time)
		return ok && ta.Equal(tb)
	}
	if ba, ok := a.(bool); ok {
		bb, ok := b.(bool)
		return ok && ba == bb
	}
	x, xok := lookupText(a)
	y, yok := lookupText(b)
	return xok && yok && x == y
}

func lookupText(v any) (string, bool) {
	if u, ok := core.UUIDText(v); ok {
		return u, true
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.String {
		return rv.String(), true
	}
	return "", false
}

// projectedValue is a stored value as normalization takes it: a UUID
// column scans to a UUID type (core.IsUUIDType), and normalization reads
// UUIDs as text.
func projectedValue(v any) any {
	v = derefValue(v)
	if u, ok := core.UUIDText(v); ok {
		return u
	}
	return v
}

// derefValue follows pointers and interfaces to the stored value; a nil one
// is NULL.
func derefValue(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

func isNullValue(v any) bool { return derefValue(v) == nil }
