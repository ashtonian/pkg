//go:build unit

package parse_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// An update reaches the rows a list in the same scope reads: its WHERE, for
// a filter target, is the list's, the tenant floor, live rows and the
// policy composed by one rule (filter.ComposeConstraints), so a floor that
// rule gains bounds updates as it bounds lists.
func TestBuildUpdate_ScopesRowsAsAListDoes(t *testing.T) {
	r := notes(t)
	req, err := r.ParseList(url.Values{"title": {"one"}})
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"rank":2}`))
	require.NoError(t, err)
	s := parse.Scope{Tenant: "t1", Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "rank", Operator: filter.OpGte, Value: 1}}}}

	for _, d := range []parse.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		t.Run(d.Name(), func(t *testing.T) {
			list, err := r.BuildList(d, req, s)
			require.NoError(t, err)
			update, err := r.BuildUpdate(d, p, parse.ByFilter(req.Filter()), s, parse.SkipUnchanged())
			require.NoError(t, err)
			require.NotNil(t, update.Current, "SkipUnchanged reads the target rows first")

			// Current selects the rows the update may write, its WHERE's
			// placeholders numbered from the first as the list's are.
			_, where, found := strings.Cut(update.Current.SQL, " WHERE ")
			require.True(t, found, update.Current.SQL)
			where, _, _ = strings.Cut(where, " ORDER BY ")
			assert.True(t, strings.HasPrefix(list.Fragment, "WHERE "+where+" ORDER BY "),
				"update: %s\nlist:   %s", where, list.Fragment)
			require.GreaterOrEqual(t, len(list.Page.Args), len(update.Current.Args))
			assert.Equal(t, update.Current.Args, list.Page.Args[:len(update.Current.Args)])
		})
	}
}
