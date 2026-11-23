package filter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
)

// MembershipLookup resolves the lookups of membership constraints for a
// backend that does not run them as SQL. LookupValues returns the Project
// values of the rows of l.Table that l.Where selects (MatchLookupRow), as
// ctx — its tenant and transaction — sees them and with no other scoping,
// so the answer is the SQL subquery's. NULL values are returned as nil.
// For a lookup projecting ProjectKey, which a relation correlating keys of
// several columns asks, each value is a core.Key of the row's values in
// ProjectKey's order. An error refuses the request.
type MembershipLookup interface {
	LookupValues(ctx context.Context, l *Lookup) ([]any, error)
}

// RelationLookup is a MembershipLookup that also answers lookups with a
// Filter — relation predicates on a backend that does not run SQL.
// LookupFilteredValues returns the Project values (or ProjectKey keys) of
// the rows of l.Table that both l.Where (MatchLookupRow) and l.Filter select, the filter
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
	err := n.Member.validateProjecting(1)
	if err != nil {
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

// keys answers l, which projects a key, and returns its distinct keys
// without NULL, each value normalized for its field in fields.
func (r *memberResolver) keys(ctx context.Context, fields []*FieldSchema, l *Lookup) ([]core.Key, error) {
	raw, _, err := r.lookupValues(ctx, l)
	if err != nil {
		return nil, err
	}
	types := make([]core.TypeInfo, len(fields))
	for i, f := range fields {
		types[i] = core.TypeInfo{Type: f.DataType, NumberKind: f.NumberKind}
	}
	keys := make([]core.Key, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, v := range raw {
		stored := v.(core.Key)
		k := make(core.Key, len(fields))
		for i, f := range fields {
			k[i], err = r.norm.normalizeMember(f, projectedValue(stored[i]))
			if err != nil {
				return nil, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
			}
		}
		text, err := core.KeyTextOf(types, k)
		if err != nil {
			return nil, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
		}
		_, dup := seen[text]
		if dup {
			continue
		}
		seen[text] = struct{}{}
		keys = append(keys, k)
	}
	return keys, nil
}

// inner is l with every member condition answered: a store sees only
// comparisons, the inner lookups' values as LookupOpIn and their keys as
// LookupOpKeyIn.
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
		return WhereIn(c.Column, vs...).typed(c.Types), nil
	}
	if c.Op == LookupOpKeyMember {
		vs, _, err := r.lookupValues(ctx, c.Lookup)
		if err != nil {
			return nil, err
		}
		keys := make([]core.Key, len(vs))
		for i, v := range vs {
			keys[i] = v.(core.Key)
		}
		return WhereKeyIn(c.Columns, keys...).typed(c.Types), nil
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
		if l.ProjectKey != nil {
			// A key holding NULL equals no row's, as a row value with a NULL
			// column is never IN a set.
			k, err := projectedKey(l, v)
			if err != nil {
				return nil, false, fmt.Errorf("filter: membership lookup on %s: %w", l.Table, err)
			}
			if k == nil {
				hasNull = true
				continue
			}
			out = append(out, k)
			continue
		}
		if isNullValue(v) {
			hasNull = true
			continue
		}
		out = append(out, derefValue(v))
	}
	return out, hasNull, nil
}

// projectedKey is a value a lookup projecting a key answered, which must be
// a key of a value per ProjectKey column, each followed to its stored value;
// nil for a key holding NULL. Any other answer is the store's defect.
func projectedKey(l *Lookup, v any) (core.Key, error) {
	var k []any
	switch kv := v.(type) {
	case core.Key:
		k = kv
	case []any:
		k = kv
	default:
		return nil, fmt.Errorf("lookup projecting the key (%s) answered a %T, not a core.Key", strings.Join(l.ProjectKey, ", "), v)
	}
	if len(k) != len(l.ProjectKey) {
		return nil, fmt.Errorf("lookup projecting the key (%s) answered a key of %d values", strings.Join(l.ProjectKey, ", "), len(k))
	}
	out := make(core.Key, len(k))
	for i, value := range k {
		out[i] = derefValue(value)
		if out[i] == nil {
			return nil, nil
		}
	}
	return out, nil
}

func hasMemberCond(c *LookupCond) bool {
	if c == nil {
		return false
	}
	if c.Op == LookupOpMember || c.Op == LookupOpKeyMember {
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
	switch cond.Op {
	case LookupOpKeyIn:
		return matchKey(cond, column)
	case LookupOpKeyMember:
		return false, invalidLookup(fmt.Sprintf("lookup key (%s) is a member of an unanswered lookup; ResolveMemberships answers it", strings.Join(cond.Columns, ", ")))
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
		return equalAs(cond.Types, 0, v, derefValue(cond.Value)), nil
	case LookupOpIn:
		for _, want := range cond.Values {
			if equalAs(cond.Types, 0, v, derefValue(want)) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, invalidLookup(fmt.Sprintf("lookup column %q has unknown operator %q", cond.Column, cond.Op))
}

// equalAs is a lookup condition's equality of a stored value and its value
// in compared column i: lookupEqual, or, for a typed condition
// (LookupCond.Types), the two values' canonical texts as the column's type
// (core.KeyValueText), one exactly when the database holds them equal.
func equalAs(types []core.TypeInfo, i int, stored, want any) bool {
	if types == nil {
		return lookupEqual(stored, want)
	}
	if isNullValue(stored) || isNullValue(want) {
		return false
	}
	x, err := core.KeyValueText(types[i], projectedValue(stored))
	if err != nil {
		return false
	}
	y, err := core.KeyValueText(types[i], projectedValue(want))
	return err == nil && x == y
}

// lookupEqual is SQL equality of a stored value and a lookup's value, as
// the lookup's statement compares them (lookupCondSQL): typed by the
// lookup value's Go type (core.TypeOf), each side read as that type's
// canonical value (core.NormalizeScalar), so a timestamp compares at its
// instant whether stored as a time or as its text, a UUID in either case,
// and a date by its day. Numbers compare by value, a decimal beyond
// float64's precision ("1.50" is 1.5).
func lookupEqual(stored, want any) bool {
	if isNullValue(stored) || isNullValue(want) {
		return false
	}
	if isLookupDecimal(stored) || isLookupDecimal(want) {
		c, ok := core.CompareDecimals(stored, want)
		return ok && c == 0
	}
	c, ok := core.CompareNumbers(stored, want)
	if ok {
		return c == 0
	}
	info := core.TypeOf(reflect.TypeOf(want))
	if !core.IsKeyType(info.Type) {
		x, xok := lookupText(stored)
		y, yok := lookupText(want)
		return xok && yok && x == y
	}
	x, err := core.NormalizeScalar(info.Type, info.NumberKind, stored)
	if err != nil {
		return false
	}
	y, err := core.NormalizeScalar(info.Type, info.NumberKind, want)
	return err == nil && x == y
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

// matchKey reports whether a row's key columns equal one of a WhereKeyIn's
// keys, column by column as WhereEq compares (lookupEqual); a NULL column
// equals none.
func matchKey(cond *LookupCond, column func(name string) (value any, ok bool)) (bool, error) {
	row := make([]any, len(cond.Columns))
	for i, name := range cond.Columns {
		v, ok := column(name)
		if !ok {
			return false, invalidLookup(fmt.Sprintf("lookup row has no column %q", name))
		}
		row[i] = derefValue(v)
		if row[i] == nil {
			return false, nil
		}
	}
	for _, k := range cond.Keys {
		if len(k) != len(row) {
			return false, invalidLookup(fmt.Sprintf("lookup key (%s) has a key of %d values", strings.Join(cond.Columns, ", "), len(k)))
		}
		equal := true
		for i, want := range k {
			if !equalAs(cond.Types, i, row[i], derefValue(want)) {
				equal = false
				break
			}
		}
		if equal {
			return true, nil
		}
	}
	return false, nil
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
