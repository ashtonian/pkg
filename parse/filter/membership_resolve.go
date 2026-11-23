package filter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

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
// need a lookup and, for tenant-scoped relations, a RelationScope.Tenant (ResolveRelationScope); without one
// they are refused rather than answered empty.
func ResolveMemberships(ctx context.Context, f *ParsedFilter, lookup MembershipLookup, opts ...ResolveOption) (*ParsedFilter, error) {
	if f == nil || f.ast == nil || !hasMember(f.ast.Root) && !hasRelation(f.ast.Root) {
		return f, nil
	}
	cfg := resolveConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.related = cfg.related.at(time.Now().UTC())
	if cfg.scope.row.column != "" {
		return nil, errors.New("filter: ResolveMemberships reads one tenant's rows; resolve each tenant's with RelationScope.Tenant instead of RowTenant")
	}
	r := newMemberResolver(lookup, cfg)
	root, err := r.resolve(ctx, f.ast.Root)
	if err != nil {
		return nil, err
	}
	// The resolved filter keeps the schema the input was parsed for.
	out := *f
	out.ast = &FilterAST{Root: root}
	return &out, nil
}

type memberResolver struct {
	lookup MembershipLookup
	// answers holds each lookup's answer by the lookup, not the node built
	// from it: constraints on different fields may share one lookup, and
	// each normalizes the values for its own field.
	answers map[*Lookup]lookupAnswer
	norm    *FilterParser
	cfg     resolveConfig
}

// lookupAnswer is what a lookup determines: its non-NULL values as stored,
// and whether a NULL was among them.
type lookupAnswer struct {
	values  []any
	hasNull bool
}

func newMemberResolver(lookup MembershipLookup, cfg resolveConfig) *memberResolver {
	return &memberResolver{
		lookup: lookup, cfg: cfg,
		answers: map[*Lookup]lookupAnswer{},
		norm:    NewFilterParser(&ResourceSchema{}, &core.ParserConfig{MaxDepth: math.MaxInt, MaxArraySize: math.MaxInt}),
	}
}

func (r *memberResolver) resolve(ctx context.Context, n *FilterNode) (*FilterNode, error) {
	if n == nil || !hasMember(n) && !hasRelation(n) {
		return n, nil
	}
	if n.Field != nil && n.Field.Relation != nil {
		return r.relation(ctx, n)
	}
	if n.Op == OpMember {
		return r.member(ctx, n)
	}
	out := *n
	out.Children = make([]*FilterNode, len(n.Children))
	for i, c := range n.Children {
		rc, err := r.resolve(ctx, c)
		if err != nil {
			return nil, err
		}
		out.Children[i] = rc
	}
	return &out, nil
}

func (r *memberResolver) member(ctx context.Context, n *FilterNode) (*FilterNode, error) {
	if r.lookup == nil {
		return unknownNode(), nil
	}
	if err := n.Member.validate(); err != nil {
		return nil, err
	}
	members, hasNull, err := r.values(ctx, n.Field, n.Member)
	if err != nil {
		return nil, err
	}
	node := &FilterNode{Field: n.Field, Op: OpIn, Value: members}
	if hasNull {
		node = &FilterNode{Op: OpOr, Children: []*FilterNode{node, unknownNode()}}
	}
	return node, nil
}

// values answers l and returns its distinct non-NULL values normalized for
// field, and whether a NULL was among them.
func (r *memberResolver) values(ctx context.Context, field *FieldSchema, l *Lookup) ([]any, bool, error) {
	raw, hasNull, err := r.lookupValues(ctx, l)
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
func (r *memberResolver) inner(ctx context.Context, l *Lookup) (*Lookup, error) {
	if l.Where == nil || !hasMemberCond(l.Where) {
		return l, nil
	}
	where, err := r.innerCond(ctx, l.Where)
	if err != nil {
		return nil, err
	}
	out := *l
	out.Where = where
	return &out, nil
}

func (r *memberResolver) innerCond(ctx context.Context, c *LookupCond) (*LookupCond, error) {
	if c.Op == LookupOpMember {
		vs, _, err := r.lookupValues(ctx, c.Lookup)
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
			rc, err := r.innerCond(ctx, child)
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
// NULLs dropped and reported. Each lookup is asked once per resolution.
func (r *memberResolver) lookupValues(ctx context.Context, l *Lookup) ([]any, bool, error) {
	if a, ok := r.answers[l]; ok {
		return a.values, a.hasNull, nil
	}
	values, hasNull, err := r.ask(ctx, l)
	if err != nil {
		return nil, false, err
	}
	r.answers[l] = lookupAnswer{values: values, hasNull: hasNull}
	return values, hasNull, nil
}

func (r *memberResolver) ask(ctx context.Context, l *Lookup) ([]any, bool, error) {
	l, err := r.inner(ctx, l)
	if err != nil {
		return nil, false, err
	}
	if err = ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
	}
	var values []any
	if l.Filter != nil {
		rl, ok := r.lookup.(RelationLookup)
		if !ok {
			return nil, false, invalidLookup(fmt.Sprintf("lookup on %s filters its rows; the membership lookup cannot evaluate filters (RelationLookup)", l.Table))
		}
		values, err = rl.LookupFilteredValues(ctx, l)
	} else {
		values, err = r.lookup.LookupValues(ctx, l)
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
	if isLookupDecimal(a) || isLookupDecimal(b) {
		// As SQL compares a decimal lookup value: by value, the other side
		// read as a decimal ("1.50" is 1.5), beyond float64's precision.
		c, ok := core.CompareDecimals(a, b)
		return ok && c == 0
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
