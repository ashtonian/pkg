//go:build unit || integration

package parsetest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

// ResourceRows runs st, a statement over r's Columns, on r's provider and
// scans its rows through r's row contract (Resource.Query).
func ResourceRows[T any](t testing.TB, r *parse.Resource[T], st parse.Statement) []T {
	t.Helper()
	rows, err := r.Query(t.Context(), st)
	require.NoError(t, err, st.SQL)
	return rows
}

// JoinColumns is a SELECT list.
func JoinColumns(columns []string) string { return strings.Join(columns, ", ") }

// ItemIDs is each item's id, in the items' order, or nil for none.
func ItemIDs[T, K any](items []T, id func(T) K) []K {
	var out []K
	for _, item := range items {
		out = append(out, id(item))
	}
	return out
}
