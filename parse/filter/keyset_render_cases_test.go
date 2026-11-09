//go:build unit || integration

package filter_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// renderDB is a database the rendered keyset runs on: its dialect, the
// SQL type of the document column, and how a path reads that column.
type renderDB struct {
	db      *sql.DB
	dialect filter.Dialect
	docType string
	storage filter.JSONStorage
}

// renderSeeds tie on the computed rank and on the member n, which some
// documents spell differently, hold as null, or lack.
var renderSeeds = []struct {
	id    string
	score float64
	doc   string
}{
	{"a", 0.25, `{"n": 10}`},
	{"b", 0.45, `{"n": 2.5}`},
	{"c", 0.25, `{}`},
	{"d", 0.05, `{"n": 10.0}`},
	{"e", 0.45, `{"n": null}`},
	{"f", 0.25, `{"n": -1}`},
	{"g", 0.15, `{"n": 1e1}`},
}

// renderRank is a computed key, the score doubled, which no sort schema
// declares: a statement outside ListQuery orders and seeks by it.
const renderRank = "(items.score * 2)"

// runKeysetRender walks each keyset two rows at a time with OrderByKeys
// and SeekPredicate, resuming from a token minted at each page's last row,
// and checks the walk visits every row once, in the keyset's order.
func runKeysetRender(t *testing.T, r renderDB) {
	t.Helper()
	ctx := t.Context()
	_, err := r.db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE items (id TEXT PRIMARY KEY, score DOUBLE PRECISION NOT NULL, doc %s NOT NULL)", r.docType))
	require.NoError(t, err)
	for _, s := range renderSeeds {
		_, err = r.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO items (id, score, doc) VALUES (%s, %s, %s)",
			r.dialect.Placeholder(1), r.dialect.Placeholder(2), r.dialect.Placeholder(3)), s.id, s.score, s.doc)
		require.NoError(t, err)
	}

	rank := filter.CursorKey{Name: "rank", SQLExpr: renderRank, Column: "rank", DataType: core.FieldTypeFloat}
	n := filter.CursorKey{Name: "n", Column: "doc", DataType: core.FieldTypeDecimal, Nullable: true,
		Path: &filter.JSONPath{Column: "items.doc", Segments: []string{"n"}, Storage: r.storage}}
	id := filter.CursorKey{Name: "id", SQLExpr: "items.id", Column: "id", DataType: core.FieldTypeString}
	keyset := func(pairs ...any) []filter.CursorKey {
		var keys []filter.CursorKey
		for i := 0; i < len(pairs); i += 2 {
			k := pairs[i].(filter.CursorKey)
			k.Direction = pairs[i+1].(filter.SortDirection)
			keys = append(keys, k)
		}
		return keys
	}

	for _, tt := range []struct {
		name string
		keys []filter.CursorKey
		want string
	}{
		{"a computed key descending, then the id ascending", keyset(rank, filter.SortDesc, id, filter.SortAsc), "beacfgd"},
		{"a member ascending, nulls last", keyset(n, filter.SortAsc, id, filter.SortAsc), "fbadgce"},
		{"a member descending, nulls first", keyset(n, filter.SortDesc, id, filter.SortDesc), "ecgdabf"},
		{"a computed key, a member and the id", keyset(rank, filter.SortAsc, n, filter.SortDesc, id, filter.SortAsc), "dgcafeb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var walked strings.Builder
			var token *filter.PageToken
			for range len(renderSeeds) + 1 {
				rows := renderPage(t, r, tt.keys, token, 2)
				for _, row := range rows {
					walked.WriteString(row["id"].(string))
				}
				if len(rows) < 2 {
					break
				}
				last := rows[len(rows)-1]
				token, err = filter.CursorPlan{Keys: tt.keys}.Token(false, func(column string) (any, error) {
					return last[column], nil
				})
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, walked.String())
		})
	}
}

// renderPage is the page of at most limit rows after token (from the
// start without one), in the order of keys.
func renderPage(t *testing.T, r renderDB, keys []filter.CursorKey, token *filter.PageToken, limit int) []map[string]any {
	t.Helper()
	where := "1=1"
	var args []any
	if token != nil {
		var err error
		where, err = filter.SeekPredicate(r.dialect, keys, token, func(v any) string {
			args = append(args, v)
			return r.dialect.Placeholder(len(args))
		})
		require.NoError(t, err)
	}
	query := fmt.Sprintf("SELECT items.id, items.doc, %s AS rank FROM items WHERE %s %s LIMIT %d",
		renderRank, where, filter.OrderByKeys(r.dialect, keys, false), limit)
	rows, err := r.db.QueryContext(t.Context(), query, args...)
	require.NoError(t, err, query)
	defer func() { require.NoError(t, rows.Close()) }()
	var page []map[string]any
	for rows.Next() {
		var id, doc string
		var rank float64
		require.NoError(t, rows.Scan(&id, &doc, &rank))
		page = append(page, map[string]any{"id": id, "doc": doc, "rank": rank})
	}
	require.NoError(t, rows.Err())
	return page
}
