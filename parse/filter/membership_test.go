//go:build unit

package filter

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func memberSchema() *ResourceSchema {
	return &ResourceSchema{Name: "docs", Fields: map[string]*FieldSchema{
		"id":        {ExternalName: "id", SQLExpr: "docs.id", DataType: core.FieldTypeString},
		"parent_id": {ExternalName: "parent_id", SQLExpr: "docs.parent_id", DataType: core.FieldTypeUUID},
		"count":     {ExternalName: "count", SQLExpr: "docs.count", DataType: core.FieldTypeInt},
		"at":        {ExternalName: "at", SQLExpr: "docs.at", DataType: core.FieldTypeTimestamp},
	}}
}

func memberAST(t *testing.T, field string, l *Lookup, negate bool) *FilterAST {
	t.Helper()
	ast, err := ApplyPolicyConstraints(memberSchema(), nil, &PolicyConstraints{
		Constraints: []*PolicyConstraint{{Field: field, Member: l, Negate: negate}},
	})
	require.NoError(t, err)
	return ast
}

func TestMembership_CompilesToAnInSubquery(t *testing.T) {
	l := &Lookup{Table: "authz.node_closure", Project: "descendant_id", Where: WhereAll(
		WhereIn("root_id", "r1", "r2"),
		WhereAny(WhereEq("user_id", "u1"), WhereNull("ended_at")),
	)}
	for name, tc := range map[string]struct {
		d     Dialect
		where string
	}{
		"postgres": {PostgresDialect{}, `docs.parent_id IN (SELECT "descendant_id" FROM "authz"."node_closure" WHERE ("root_id" IN ($1, $2) AND ("user_id" = $3 OR "ended_at" IS NULL)))`},
		"sqlite":   {SQLiteDialect{}, `docs.parent_id IN (SELECT "descendant_id" FROM "authz"."node_closure" WHERE ("root_id" IN (?1, ?2) AND ("user_id" = ?3 OR "ended_at" IS NULL)))`},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := NewSQLBuilder(memberSchema(), tc.d).Build(memberAST(t, "parent_id", l, false))
			require.NoError(t, err)
			assert.Equal(t, tc.where, res.Where)
			assert.Equal(t, []any{"r1", "r2", "u1"}, res.Args)
		})
	}
}

func TestMembership_SQLShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		l      *Lookup
		negate bool
		where  string
	}{
		"every row":      {&Lookup{Table: "t", Project: "p"}, false, `docs.id IN (SELECT "p" FROM "t")`},
		"empty list":     {&Lookup{Table: "t", Project: "p", Where: WhereIn("c")}, false, `docs.id IN (SELECT "p" FROM "t" WHERE 1=0)`},
		"negated":        {&Lookup{Table: "t", Project: "p", Where: WhereEq("c", "x")}, true, `NOT (docs.id IN (SELECT "p" FROM "t" WHERE "c" = $1))`},
		"one-child And":  {&Lookup{Table: "t", Project: "p", Where: WhereAll(WhereNull("c"))}, false, `docs.id IN (SELECT "p" FROM "t" WHERE ("c" IS NULL))`},
		"placeholder ok": {&Lookup{Table: "t", Project: "p", Where: WhereEq("c", "$1; DROP TABLE t")}, false, `docs.id IN (SELECT "p" FROM "t" WHERE "c" = $1)`},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := NewSQLBuilder(memberSchema(), PostgresDialect{}).Build(memberAST(t, "id", tc.l, tc.negate))
			require.NoError(t, err)
			assert.Equal(t, tc.where, res.Where)
		})
	}
}

func TestMembership_RefusesMalformedLookups(t *testing.T) {
	for name, tc := range map[string]struct {
		field string
		l     *Lookup
	}{
		"table injection":      {"id", &Lookup{Table: "t; DROP TABLE docs", Project: "p"}},
		"three-part table":     {"id", &Lookup{Table: "a.b.c", Project: "p"}},
		"project injection":    {"id", &Lookup{Table: "t", Project: "p) OR (1=1"}},
		"column injection":     {"id", &Lookup{Table: "t", Project: "p", Where: WhereEq(`c" = c --`, "x")}},
		"comparison and group": {"id", &Lookup{Table: "t", Project: "p", Where: &LookupCond{Column: "c", Op: LookupOpNull, And: []*LookupCond{WhereNull("d")}}}},
		"And and Or":           {"id", &Lookup{Table: "t", Project: "p", Where: &LookupCond{And: []*LookupCond{WhereNull("c")}, Or: []*LookupCond{WhereNull("d")}}}},
		"empty condition":      {"id", &Lookup{Table: "t", Project: "p", Where: &LookupCond{}}},
		"nil child":            {"id", &Lookup{Table: "t", Project: "p", Where: WhereAny(nil)}},
		"equality with NULL":   {"id", &Lookup{Table: "t", Project: "p", Where: WhereEq("c", nil)}},
		"typed nil equality":   {"id", &Lookup{Table: "t", Project: "p", Where: WhereEq("c", (*string)(nil))}},
		"NULL in a list":       {"id", &Lookup{Table: "t", Project: "p", Where: WhereIn("c", "x", nil)}},
		"unknown operator":     {"id", &Lookup{Table: "t", Project: "p", Where: &LookupCond{Column: "c", Op: "gt", Value: 1}}},
		"timestamp field":      {"at", &Lookup{Table: "t", Project: "p"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyPolicyConstraints(memberSchema(), nil, &PolicyConstraints{
				Constraints: []*PolicyConstraint{{Field: tc.field, Member: tc.l}},
			})
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, ECodeInvalidRelation.String(), pe.Code)
		})
	}
	_, err := ApplyPolicyConstraints(memberSchema(), nil, &PolicyConstraints{
		Constraints: []*PolicyConstraint{{Field: "nope", Member: &Lookup{Table: "t", Project: "p"}}},
	})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, ECodeUnknownField.String(), pe.Code)
}

type recordingLookup struct {
	values map[string][]any
	err    error
	calls  int
}

func (r *recordingLookup) LookupValues(ctx context.Context, l *Lookup) ([]any, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return r.values[l.Table], nil
}

func TestResolveMemberships_RewritesToTheEquivalentIn(t *testing.T) {
	a := uuid.MustParse("0198a3c2-7a1e-7c00-8000-00000000000a")
	lookup := &recordingLookup{values: map[string][]any{
		"plain": {a, a.String(), &a},
		"nulls": {a, nil, (*uuid.UUID)(nil)},
		"empty": {},
	}}
	for name, tc := range map[string]struct {
		table string
		want  *FilterNode
	}{
		"values, normalized and deduplicated": {"plain", &FilterNode{Op: OpIn, Value: []any{a.String()}}},
		"a NULL makes non-members unknown": {"nulls", &FilterNode{Op: OpOr, Children: []*FilterNode{
			{Op: OpIn, Value: []any{a.String()}}, unknownNode(),
		}}},
		"no values is an empty $in": {"empty", &FilterNode{Op: OpIn, Value: []any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			ast := memberAST(t, "parent_id", &Lookup{Table: tc.table, Project: "p"}, false)
			before := *ast.Root
			out, err := ResolveMemberships(context.Background(), NewParsedFilter(ast), lookup)
			require.NoError(t, err)
			got := out.AST.Root
			stripFields(got)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, before, *ast.Root, "the input filter is not modified")
		})
	}
}

func stripFields(n *FilterNode) {
	n.Field = nil
	for _, c := range n.Children {
		stripFields(c)
	}
}

func TestResolveMemberships_WithoutALookupIsUnknown(t *testing.T) {
	ast := memberAST(t, "id", &Lookup{Table: "t", Project: "p"}, true)
	out, err := ResolveMemberships(context.Background(), NewParsedFilter(ast), nil)
	require.NoError(t, err)
	require.Equal(t, OpNot, out.AST.Root.Op)
	assert.Equal(t, unknownNode(), out.AST.Root.Children[0], "NOT unknown is unknown: the deny still admits nothing it might refuse")
}

func TestResolveMemberships_ResolvesEachLookupOnce(t *testing.T) {
	l := &Lookup{Table: "t", Project: "p"}
	ast, err := ApplyPolicyConstraints(memberSchema(), nil, &PolicyConstraints{
		Constraints: []*PolicyConstraint{{Field: "id", Member: l}, {Field: "id", Member: l, Negate: true}},
	})
	require.NoError(t, err)
	lookup := &recordingLookup{values: map[string][]any{"t": {"x"}}}
	_, err = ResolveMemberships(context.Background(), NewParsedFilter(ast), lookup)
	require.NoError(t, err)
	assert.Equal(t, 1, lookup.calls)
}

func TestResolveMemberships_FailuresRefuseTheRequest(t *testing.T) {
	ast := memberAST(t, "id", &Lookup{Table: "t", Project: "p"}, false)
	boom := errors.New("store unavailable")
	_, err := ResolveMemberships(context.Background(), NewParsedFilter(ast), &recordingLookup{err: boom})
	assert.ErrorIs(t, err, boom)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lookup := &recordingLookup{}
	_, err = ResolveMemberships(ctx, NewParsedFilter(ast), lookup)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, lookup.calls, "a cancelled request reads no lookup")

	intAST := memberAST(t, "count", &Lookup{Table: "t", Project: "p"}, false)
	_, err = ResolveMemberships(context.Background(), NewParsedFilter(intAST), &recordingLookup{values: map[string][]any{"t": {"not a number"}}})
	var pe *core.ParseError
	assert.ErrorAs(t, err, &pe, "a projected value the field cannot hold is a data error, not a non-member")
}

func TestResolveMemberships_LeavesFiltersWithoutMembershipsAlone(t *testing.T) {
	f := NewParsedFilter(&FilterAST{Root: &FilterNode{Op: OpRawSQL, RawSQL: "NULL"}})
	out, err := ResolveMemberships(context.Background(), f, nil)
	require.NoError(t, err)
	assert.Same(t, f, out)
}

func TestMatchLookupRow(t *testing.T) {
	id := uuid.MustParse("0198a3c2-7a1e-7c00-8000-00000000000a")
	row := map[string]any{"name": "a", "big": int64(9007199254740993), "small": int32(7), "ref": id, "ended": (*string)(nil)}
	get := func(name string) (any, bool) { v, ok := row[name]; return v, ok }
	for name, tc := range map[string]struct {
		cond *LookupCond
		want bool
	}{
		"nil selects":              {nil, true},
		"string equality":          {WhereEq("name", "a"), true},
		"case-sensitive":           {WhereEq("name", "A"), false},
		"exact beyond 2^53":        {WhereEq("big", int64(9007199254740992)), false},
		"integer kinds compare":    {WhereEq("small", 7), true},
		"uuid against its text":    {WhereEq("ref", id.String()), true},
		"text against a uuid":      {WhereEq("name", id), false},
		"in":                       {WhereIn("name", "b", "a"), true},
		"empty in":                 {WhereIn("name"), false},
		"NULL column is NULL":      {WhereNull("ended"), true},
		"NULL column never equals": {WhereIn("ended", "x"), false},
		"and":                      {WhereAll(WhereEq("name", "a"), WhereNull("ended")), true},
		"or":                       {WhereAny(WhereEq("name", "b"), WhereEq("small", 7)), true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := MatchLookupRow(tc.cond, get)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err := MatchLookupRow(WhereEq("missing", "x"), get)
	var pe *core.ParseError
	assert.ErrorAs(t, err, &pe, "a misspelt column cannot silently select nothing")
}
