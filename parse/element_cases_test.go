//go:build unit || integration

package parse_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// The reference evaluator selects the rows the SQL dialects select.
func TestElementCasesEvaluate(t *testing.T) {
	orders := parsetest.OrderResource(t, onPostgres)
	for _, tc := range parsetest.ElementCases() {
		t.Run(tc.Name, func(t *testing.T) {
			req, err := orders.ParseList(url.Values{filter.ParamFilter: {tc.Filter}})
			require.NoError(t, err)
			var got []string
			for _, r := range parsetest.OrderRows() {
				row := filter.ColumnRow(func(column string) (any, bool) {
					_, name, _ := strings.Cut(column, ".")
					i := slices.Index(parsetest.OrderColumns(), name)
					if i < 0 {
						return nil, false
					}
					return r[i], true
				})
				truth, err := filter.Evaluate(req.Filter().AST().Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					got = append(got, r[0].(string))
				}
			}
			assert.Equal(t, tc.Want, got)
		})
	}
}
