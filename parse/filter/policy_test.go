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
		Name: "items",
		Fields: map[string]*FieldSchema{
			"name": {ExternalName: "name", SQLExpr: "name", DataType: core.FieldTypeString, AllowedOperators: []FilterOperator{OpEq}},
		},
		SystemFields: map[string]*FieldSchema{
			"id":         {ExternalName: "id", SQLExpr: "id", DataType: core.FieldTypeUUID, AllowedOperators: []FilterOperator{OpEq}},
			"created_at": {ExternalName: "created_at", SQLExpr: "created_at", DataType: core.FieldTypeTimestamp},
			"deleted_at": {ExternalName: "deleted_at", SQLExpr: "deleted_at", DataType: core.FieldTypeTimestamp},
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
	lq := NewListQuery(policySchema(), nil, SQLiteDialect{}).WithFilterConfig(&core.ParserConfig{Clock: func() time.Time { return anchor }})

	tests := []struct {
		name  string
		c     *PolicyConstraint
		where string
		args  []any
	}{
		{"uuid text canonical", &PolicyConstraint{Field: "id", Operator: OpEq, Value: "A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11"}, "id = ?1", []any{id.String()}},
		{"typed uuid list", &PolicyConstraint{Field: "id", Operator: OpIn, Value: []uuid.UUID{id}}, "id IN (?1)", []any{id.String()}},
		{"time to UTC text", &PolicyConstraint{Field: "created_at", Operator: OpGte, Value: local}, "created_at >= ?1", []any{"2026-01-02T02:04:05.000000000Z"}},
		{"relative time on the list clock", &PolicyConstraint{Field: "created_at", Operator: OpGte, Value: "now-1d"}, "created_at >= ?1", []any{"2026-02-28T12:00:00.000000000Z"}},
		{"null drops its value", &PolicyConstraint{Field: "deleted_at", Operator: OpNull, Value: true}, "deleted_at IS NULL", []any{}},
		{"exists is not null", &PolicyConstraint{Field: "deleted_at", Operator: OpExists, Value: true}, "deleted_at IS NOT NULL", []any{}},
		{"declared operators do not bind policies", &PolicyConstraint{Field: "id", Operator: OpNin, Value: []any{id}}, "id NOT IN (?1)", []any{id.String()}},
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
	res, err := buildPolicy(t, NewListQuery(policySchema(), nil, PostgresDialect{}), &PolicyConstraint{Field: "id", Operator: OpIn, Value: ToAnySlice(ids)})
	require.NoError(t, err)
	assert.Len(t, res.Args, len(ids))
}

func TestPolicyConstraints_Rejects(t *testing.T) {
	tests := []struct {
		name string
		c    *PolicyConstraint
		code string
	}{
		{"malformed uuid", &PolicyConstraint{Field: "id", Operator: OpEq, Value: "not-a-uuid"}, ECodeInvalidUUID.String()},
		{"operator outside the type", &PolicyConstraint{Field: "id", Operator: OpLike, Value: "a%"}, ECodeInvalidOperator.String()},
		{"exists without a boolean", &PolicyConstraint{Field: "deleted_at", Operator: OpExists, Value: "yes"}, ECodeValueTypeMismatch.String()},
		{"unknown field", &PolicyConstraint{Field: "owner", Operator: OpEq, Value: "x"}, ECodeUnknownField.String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ApplyPolicyConstraints(policySchema(), nil, &PolicyConstraints{Constraints: []*PolicyConstraint{tt.c}})
			var pe *core.ParseError
			require.True(t, errors.As(err, &pe), "got %v", err)
			assert.Equal(t, tt.code, pe.Code)
		})
	}
}

// A value no row can hold compiles to unknown when the constraint asks: the
// alternative holding it admits no row, and its negation excludes the rows
// the rest of the negated group matches.
func TestPolicyConstraints_UnknownIfInvalid(t *testing.T) {
	lq := NewListQuery(policySchema(), nil, SQLiteDialect{})
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
	assert.Equal(t, "NOT ((name = ?1 AND NULL))", res.Where)

	_, err = ApplyPolicyConstraints(policySchema(), nil, &PolicyConstraints{Constraints: []*PolicyConstraint{
		{Field: "owner", Operator: OpEq, Value: "x", UnknownIfInvalid: true},
	}})
	assert.Error(t, err, "an unknown field is the caller's error, never unknown")
}

func TestEscapeLike(t *testing.T) {
	assert.Equal(t, `50\%\_off\\`, EscapeLike(`50%_off\`))
}
