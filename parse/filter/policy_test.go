//go:build unit

package filter

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func policySchema() *ResourceSchema {
	return &ResourceSchema{
		Name:       "items",
		PrimaryKey: []string{"id"},
		Fields: map[string]*FieldSchema{
			"name": {ExternalName: "name", Column: "name", DataType: core.FieldTypeString, AllowedOperators: []FilterOperator{OpEq}},
		},
		SystemFields: map[string]*FieldSchema{
			"id":         {ExternalName: "id", Column: "id", DataType: core.FieldTypeUUID, AllowedOperators: []FilterOperator{OpEq}},
			"created_at": {ExternalName: "created_at", Column: "created_at", DataType: core.FieldTypeTimestamp},
			"deleted_at": {ExternalName: "deleted_at", Column: "deleted_at", DataType: core.FieldTypeTimestamp},
		},
	}
}

func buildPolicy(t *testing.T, lq *ListQuery, cs ...*PolicyConstraint) (*ListResult, error) {
	t.Helper()
	return lq.Build(ListPolicy(&PolicyConstraints{Constraints: cs}))
}

// Policy values reach the dialect in the canonical form request values have.
func TestPolicyConstraints_NormalizeValues(t *testing.T) {
	id := uuid.MustParse("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")
	anchor := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	local := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))
	lq := policyListQuery(t, SQLiteDialect{}).WithFilterConfig(&core.ParserConfig{Clock: func() time.Time { return anchor }})

	tests := []struct {
		name  string
		c     *PolicyConstraint
		where string
		args  []any
	}{
		{"uuid text canonical", &PolicyConstraint{Field: "id", Operator: OpEq, Value: "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"}, `"id" = ?1`, []any{id.String()}},
		{"typed uuid list", &PolicyConstraint{Field: "id", Operator: OpIn, Value: []uuid.UUID{id}}, `"id" IN (SELECT value AS v FROM json_each(?1))`, []any{`["` + id.String() + `"]`}},
		{"time to UTC text", &PolicyConstraint{Field: "created_at", Operator: OpGte, Value: local}, `"created_at" >= ?1`, []any{"2026-01-02T02:04:05.000000000Z"}},
		{"relative time on the list clock", &PolicyConstraint{Field: "created_at", Operator: OpGte, Value: "now-1d"}, `"created_at" >= ?1`, []any{"2026-02-28T12:00:00.000000000Z"}},
		{"null drops its value", &PolicyConstraint{Field: "deleted_at", Operator: OpNull, Value: true}, `"deleted_at" IS NULL`, []any{}},
		{"null false is not null", &PolicyConstraint{Field: "deleted_at", Operator: OpNull, Value: false}, `"deleted_at" IS NOT NULL`, []any{}},
		{"not null false is null", &PolicyConstraint{Field: "deleted_at", Operator: OpNotNull, Value: false}, `"deleted_at" IS NULL`, []any{}},
		{"null without a value", &PolicyConstraint{Field: "deleted_at", Operator: OpNull}, `"deleted_at" IS NULL`, []any{}},
		{"exists is not null", &PolicyConstraint{Field: "deleted_at", Operator: OpExists, Value: true}, `"deleted_at" IS NOT NULL`, []any{}},
		{"declared operators do not bind policies", &PolicyConstraint{Field: "id", Operator: OpNin, Value: []any{id}}, `"id" NOT IN (SELECT value AS v FROM json_each(?1))`, []any{`["` + id.String() + `"]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := buildPolicy(t, lq, tt.c)
			require.NoError(t, err)
			assert.Equal(t, tt.where, res.Where)
			assert.Equal(t, tt.args, res.Args)
		})
	}
}

// Request limits bound callers, not policies: an allow-list may exceed MaxArraySize.
func TestPolicyConstraints_ListsAreUnbounded(t *testing.T) {
	ids := make([]string, core.DefaultConfig().MaxArraySize+50)
	for i := range ids {
		ids[i] = uuid.NewSHA1(uuid.NameSpaceOID, fmt.Appendf(nil, "item-%d", i)).String()
	}
	res, err := buildPolicy(t, policyListQuery(t, PostgresDialect{}), &PolicyConstraint{Field: "id", Operator: OpIn, Value: ids})
	require.NoError(t, err)
	assert.Equal(t, []any{ids}, res.Args, "one parameter holds the whole set")
}

// A policy the schema cannot compile is the server's fault: ErrPolicyInvalid,
// never a ParseError, whose code would answer the request as at fault. The
// message names the field and what is wrong with it.
func TestPolicyConstraints_Rejects(t *testing.T) {
	tests := []struct {
		name string
		c    *PolicyConstraint
		want string
	}{
		{"malformed uuid", &PolicyConstraint{Field: "id", Operator: OpEq, Value: "not-a-uuid"}, `field "id": `},
		{"operator outside the type", &PolicyConstraint{Field: "id", Operator: OpLike, Value: "a%"}, "operator 'like' does not apply"},
		{"exists without a boolean", &PolicyConstraint{Field: "deleted_at", Operator: OpExists, Value: "yes"}, "$exists requires a boolean value"},
		{"unknown field", &PolicyConstraint{Field: "owner", Operator: OpEq, Value: "x"}, `field "owner" is not a field of`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ApplyPolicyConstraints(policySchema(), nil, &PolicyConstraints{Constraints: []*PolicyConstraint{tt.c}})
			require.ErrorIs(t, err, ErrPolicyInvalid)
			assert.ErrorContains(t, err, tt.want)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError, "%v", err)
		})
	}
}

// A value no row can hold compiles to unknown when the constraint asks: the
// alternative holding it admits no row, and its negation excludes the rows
// the rest of the negated group matches.
func TestPolicyConstraints_UnknownIfInvalid(t *testing.T) {
	lq := policyListQuery(t, SQLiteDialect{})
	for _, c := range []*PolicyConstraint{
		{Field: "id", Operator: OpEq, Value: "not-a-uuid", UnknownIfInvalid: true},
		{Field: "id", Operator: OpLike, Value: "a%", UnknownIfInvalid: true},
		{Field: "id", Operator: OpIn, Value: []any{"not-a-uuid"}, UnknownIfInvalid: true, Negate: true},
	} {
		res, err := buildPolicy(t, lq, c)
		require.NoError(t, err)
		assert.Equal(t, "NULL", res.Where)
		assert.Empty(t, res.Args)
	}

	res, err := lq.Build(ListPolicy(&PolicyConstraints{Not: true, Constraints: []*PolicyConstraint{
		{Field: "name", Operator: OpEq, Value: "x"},
		{Field: "id", Operator: OpEq, Value: "not-a-uuid", UnknownIfInvalid: true},
	}}))
	require.NoError(t, err)
	assert.Equal(t, `NOT (("name" = ?1 AND NULL))`, res.Where)

	_, err = ApplyPolicyConstraints(policySchema(), nil, &PolicyConstraints{Constraints: []*PolicyConstraint{
		{Field: "owner", Operator: OpEq, Value: "x", UnknownIfInvalid: true},
	}})
	assert.Error(t, err, "an unknown field is the caller's error, never unknown")
}

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, `50\%\_off\\`, EscapeLike(`50%_off\`))
}

func policyListQuery(t *testing.T, d Dialect) *ListQuery {
	t.Helper()
	lq, err := NewListQuery(policySchema(), nil, d)
	require.NoError(t, err)
	return lq
}

// A negated empty group is refused wherever it stands: alone, merged into
// another policy or composed into a repository read. Nothing drops it as
// empty first, which would admit every row.
func TestPolicyConstraints_NegatedEmptyGroupIsRefused(t *testing.T) {
	name := &PolicyConstraints{Constraints: []*PolicyConstraint{{Field: "name", Operator: OpEq, Value: "x"}}}
	for label, pc := range map[string]*PolicyConstraints{
		"alone":               {Not: true},
		"merged":              name.Merge(&PolicyConstraints{Not: true}),
		"merged into":         (&PolicyConstraints{Not: true}).Merge(name),
		"composed":            ComposeConstraints(RepositoryScope{}, nil, &PolicyConstraints{Not: true}),
		"composed governance": ComposeConstraints(RepositoryScope{}, &PolicyConstraints{Not: true}, nil),
	} {
		t.Run(label, func(t *testing.T) {
			assert.False(t, pc.IsEmpty())
			_, err := ApplyPolicyConstraints(policySchema(), nil, pc)
			require.ErrorIs(t, err, ErrPolicyInvalid)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError)
		})
	}
}

// A tree with no constraint anywhere in it is empty however it nests, so
// composing it as governance keeps the tenant floor rather than replacing it
// with no predicate; one constraint at any depth makes it non-empty.
func TestPolicyConstraints_EmptyTreesAreEmpty(t *testing.T) {
	name := &PolicyConstraint{Field: "name", Operator: OpEq, Value: "x"}
	schema := policySchema()
	schema.SystemFields["tenant_id"] = &FieldSchema{ExternalName: "tenant_id", Column: "tenant_id", DataType: core.FieldTypeString}
	floor := RepositoryScope{TenantColumn: "tenant_id", TenantID: "t1"}
	for label, tc := range map[string]struct {
		pc    *PolicyConstraints
		empty bool
	}{
		"nil":                  {nil, true},
		"no groups":            {&PolicyConstraints{}, true},
		"an empty And":         {&PolicyConstraints{And: []*PolicyConstraints{{}}}, true},
		"an empty Or":          {&PolicyConstraints{Or: []*PolicyConstraints{{}}}, true},
		"nil groups":           {&PolicyConstraints{And: []*PolicyConstraints{nil}, Or: []*PolicyConstraints{nil}}, true},
		"nested empty groups":  {&PolicyConstraints{Or: []*PolicyConstraints{{And: []*PolicyConstraints{{}, {Or: []*PolicyConstraints{{}}}}}}}, true},
		"a deep constraint":    {&PolicyConstraints{Or: []*PolicyConstraints{{And: []*PolicyConstraints{{}, {Constraints: []*PolicyConstraint{name}}}}}}, false},
		"a deep negated group": {&PolicyConstraints{And: []*PolicyConstraints{{Or: []*PolicyConstraints{{Not: true}}}}}, false},
	} {
		t.Run(label, func(t *testing.T) {
			assert.Equal(t, tc.empty, tc.pc.IsEmpty())
			composed := ComposeConstraints(floor, tc.pc, nil)
			ast, err := ApplyPolicyConstraints(schema, nil, composed)
			if label == "a deep negated group" {
				require.ErrorIs(t, err, ErrPolicyInvalid)
				return
			}
			require.NoError(t, err)
			if tc.empty {
				require.NotNil(t, ast)
				require.NotNil(t, ast.Root.Field, "the tenant floor alone")
				assert.Equal(t, OpEq, ast.Root.Op)
				assert.Equal(t, "tenant_id", ast.Root.Field.Column)
				assert.Equal(t, "t1", ast.Root.Value)
			}
		})
	}
}

// A policy fault carries a ParseError's message and cause, never the
// ParseError itself, even when the cause holds one.
func TestPolicyFault_HoldsNoParseError(t *testing.T) {
	cause := errors.New("bad value")
	for name, err := range map[string]error{
		"plain cause":      &core.ParseError{Code: ECodeValueTypeMismatch.String(), Message: "m", Cause: cause},
		"ParseError cause": &core.ParseError{Code: ECodeValueTypeMismatch.String(), Message: "m", Cause: newFilterError(ECodeInvalidUUID, "inner")},
		"wrapped":          fmt.Errorf("context: %w", &core.ParseError{Code: ECodeInvalidOperator.String(), Message: "m"}),
	} {
		t.Run(name, func(t *testing.T) {
			fault := policyFault("owner", err)
			assert.ErrorIs(t, fault, ErrPolicyInvalid)
			assert.Equal(t, `filter: invalid policy constraint: field "owner": m`, fault.Error())
			_, isParseError := errors.AsType[*core.ParseError](fault)
			assert.False(t, isParseError)
		})
	}
	assert.ErrorIs(t, policyFault("owner", &core.ParseError{Code: "E2073", Message: "m", Cause: cause}), cause, "a plain cause stays in the chain")
}
