//go:build unit

package filter

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// steppingClock is an hour later on each reading, so two readings in one
// parse resolve two `now` tokens an hour apart.
func steppingClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Hour)
		return t
	}
}

func clockSchema() *ResourceSchema {
	return &ResourceSchema{
		Name: "events", PrimaryKey: "id",
		Fields: map[string]*FieldSchema{
			"at": {ExternalName: "at", Column: "at", DataType: core.FieldTypeTimestamp, AllowedOperators: []FilterOperator{OpGte, OpLte}},
		},
		SystemFields: map[string]*FieldSchema{
			"id": {ExternalName: "id", Column: "id", DataType: core.FieldTypeString},
		},
	}
}

func leafValues(n *FilterNode) []any {
	if n == nil {
		return nil
	}
	if len(n.Children) == 0 {
		return []any{n.Value}
	}
	var out []any
	for _, c := range n.Children {
		out = append(out, leafValues(c)...)
	}
	return out
}

func assertOneInstant(t *testing.T, values []any, n int) {
	t.Helper()
	require.Len(t, values, n)
	for _, v := range values[1:] {
		assert.Equal(t, values[0], v, "every `now` of one request is one instant")
	}
}

// One parse reads its clock once, so `$gte now` with `$lte now` can hold;
// a list request's filter and policy share the instant.
func TestClock_OneReadingPerRequest(t *testing.T) {
	both := `{"at":{"$gte":"now","$lte":"now"}}`
	policy := &PolicyConstraints{Constraints: []*PolicyConstraint{
		{Field: "at", Operator: OpGte, Value: "now"},
		{Field: "at", Operator: OpLte, Value: "now"},
	}}

	t.Run("JSON filter", func(t *testing.T) {
		ast, err := NewFilterParser(clockSchema(), &core.ParserConfig{Clock: steppingClock()}).Parse(both)
		require.NoError(t, err)
		assertOneInstant(t, leafValues(ast.Root), 2)
	})
	t.Run("decoded filter", func(t *testing.T) {
		ast, err := NewFilterParser(clockSchema(), &core.ParserConfig{Clock: steppingClock()}).ParseMap(map[string]any{"at": map[string]any{"$gte": "now", "$lte": "now"}})
		require.NoError(t, err)
		assertOneInstant(t, leafValues(ast.Root), 2)
	})
	t.Run("flat parameters", func(t *testing.T) {
		qp := NewQueryParamParser(clockSchema(), QueryParamFieldsOnly(), QueryParamConfig(&core.ParserConfig{Clock: steppingClock()}))
		ast, err := qp.Parse(url.Values{"at_gte": {"now"}, "at_lte": {"now"}})
		require.NoError(t, err)
		assertOneInstant(t, leafValues(ast.Root), 2)
	})
	t.Run("filter and policy", func(t *testing.T) {
		lq, err := NewListQuery(clockSchema(), nil, SQLiteDialect{})
		require.NoError(t, err)
		res, err := lq.WithFilterConfig(&core.ParserConfig{Clock: steppingClock()}).Build(ListFilter(both), ListPolicy(policy))
		require.NoError(t, err)
		assertOneInstant(t, res.Args, 4)
	})
	t.Run("the request's instant", func(t *testing.T) {
		at := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
		lq, err := NewListQuery(clockSchema(), nil, SQLiteDialect{})
		require.NoError(t, err)
		res, err := lq.WithFilterConfig(&core.ParserConfig{Clock: steppingClock()}).Build(ListFilter(both), ListPolicy(policy), ListNow(at))
		require.NoError(t, err)
		assertOneInstant(t, res.Args, 4)
		assert.Equal(t, core.FormatTimestamp(at), res.Args[0])
	})
}
