//go:build unit || integration

package parse_test

import (
	"database/sql"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

type tuning struct {
	Mode  string `json:"mode"  parse:"filter"`
	Level int64  `json:"level" parse:"filter,sort"`
}

type tunedItem struct {
	ID       string `json:"id"       db:"id"       parse:"filter"`
	Settings tuning `json:"settings" db:"settings" parse:"filter,patch"`
}

// runPathPatches merges into a typed document and reads the member back
// through its path: the canonical typed document (J02) and the path rule
// agree on every backend.
func runPathPatches(t *testing.T, db *sql.DB, d parse.Dialect, docType, cast string) {
	t.Helper()
	_, err := db.Exec("CREATE TABLE tuned_items (id TEXT PRIMARY KEY, settings " + docType + ")")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO tuned_items (id, settings) VALUES ('a', "+d.Placeholder(1)+cast+"), ('b', "+d.Placeholder(2)+cast+")",
		`{"mode":"auto","level":2}`, `{"mode":"manual","level":5}`)
	require.NoError(t, err)
	r, err := parse.NewResource[tunedItem]("tuned_items")
	require.NoError(t, err)

	ids := func(f string) []string {
		req, parseErr := r.ParseList(url.Values{"filter": {f}})
		require.NoError(t, parseErr)
		plan, parseErr := r.BuildList(d, req, parse.Scope{})
		require.NoError(t, parseErr)
		rows, parseErr := db.Query(plan.Page.SQL, plan.Page.Args...)
		require.NoError(t, parseErr, plan.Page.SQL)
		defer func() { require.NoError(t, rows.Close()) }()
		var out []string
		for rows.Next() {
			var id string
			var doc any
			require.NoError(t, rows.Scan(&id, &doc))
			out = append(out, id)
		}
		require.NoError(t, rows.Err())
		return out
	}

	p, err := r.ParseMergePatch([]byte(`{"settings":{"level":9}}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(d, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	_, err = db.Exec(plan.Owner.SQL, plan.Owner.Args...)
	require.NoError(t, err, plan.Owner.SQL)

	assert.Equal(t, []string{"a"}, ids(`{"settings.level":9}`), "the patched member reads back through its path")
	assert.Equal(t, []string{"a"}, ids(`{"settings.mode":"auto"}`), "its sibling survives the merge")
	assert.Equal(t, []string{"b"}, ids(`{"settings.level":5}`), "another row is untouched")

	_, err = r.ParseMergePatch([]byte(`{"settings":{"bogus":1}}`))
	var pe *core.ParseError
	assert.True(t, errors.As(err, &pe), "a member the type does not declare is refused, not stored: %v", err)
}
