//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

type filterBudgetDocument struct {
	ID      string         `json:"id" db:"id" parse:"filter"`
	Enabled bool           `json:"enabled" db:"enabled" parse:"filter"`
	Labels  map[string]any `json:"labels" db:"labels" parse:"filter"`
}

func TestParseFilterWithCostIsolatesResourceBudget(t *testing.T) {
	resource, err := parse.NewResource[filterBudgetDocument]("budget_documents")
	require.NoError(t, err)
	const query = `{"enabled":true,"labels":{"$contains":{"role":"worker"}}}`
	_, err = resource.ParseFilter(query)
	require.Error(t, err)
	_, err = resource.ParseFilterWithCost(query, 60)
	require.NoError(t, err)
	_, err = resource.ParseFilter(query)
	require.Error(t, err, "a per-call allowance cannot change public request policy")

	t.Run("public", func(t *testing.T) {
		t.Parallel()
		for range 16 {
			_, err := resource.ParseFilter(query)
			require.Error(t, err)
		}
	})
	t.Run("internal", func(t *testing.T) {
		t.Parallel()
		for range 16 {
			_, err := resource.ParseFilterWithCost(query, 60)
			require.NoError(t, err)
		}
	})
}

func TestParseFilterWithCostPreservesResolvedOptions(t *testing.T) {
	t.Run("codec and clock", func(t *testing.T) {
		frozen := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		decodes := 0
		parser, err := parse.NewResource[limitsDevice]("filter_budget_devices", parse.ParserOptions(
			core.WithMaxCost(1),
			core.WithClock(func() time.Time { return frozen }),
			core.WithCodec(countingCodec{unmarshals: &decodes}),
		))
		require.NoError(t, err)
		parsed, err := parser.ParseFilterWithCost(`{"created_at":{"$gt":"now-7d"}}`, 10)
		require.NoError(t, err)
		require.Positive(t, decodes)
		require.Equal(t, core.FormatTimestamp(frozen.Add(-7*24*time.Hour)), parsed.AST.Root.Value)
	})
	for _, test := range []struct {
		name  string
		limit core.Option
		query string
	}{
		{"input bytes", core.WithMaxInputSize(4), `{"status":"active"}`},
		{"conditions", core.WithMaxConditions(1), `{"status":"active","count":1}`},
		{"array values", core.WithMaxArraySize(1), `{"count":{"$in":[1,2]}}`},
		{"depth", core.WithMaxDepth(1), `{"$and":[{"$or":[{"status":"active"},{"count":1}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parser, err := parse.NewResource[limitsDevice]("filter_budget_devices", parse.ParserOptions(core.WithMaxCost(1), test.limit))
			require.NoError(t, err)
			_, err = parser.ParseFilterWithCost(test.query, 1000)
			require.Error(t, err, "cost allowance must retain the resource's other limits")
		})
	}
}

func TestParseFilterWithCostRejectsInvalidBudget(t *testing.T) {
	parser, err := parse.NewResource[filterBudgetDocument]("budget_documents")
	require.NoError(t, err)
	for _, cost := range []int{0, -1} {
		_, err := parser.ParseFilterWithCost("", cost)
		require.Error(t, err, "invalid server policy is refused even for an empty filter")
	}
	_, err = parser.ParseFilterWithCost("", 1)
	require.NoError(t, err)
	_, err = parser.ParseFilter("")
	require.NoError(t, err)
}
