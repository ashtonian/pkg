//go:build integration

package parse_test

import (
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

// A merged typed-document member reads back through its path on each
// backend: the canonical typed document (J02) and the path rule agree.
func TestPathPatches(t *testing.T) {
	runSQLBackends(t, runPathPatches)
}

func runPathPatches(t *testing.T, b sqlBackend) {
	t.Helper()
	provider, db := b.open(t)
	b.load(t, db, sqlTable{name: "tuned_items", columns: []sqlColumn{column[string]("id"), column[tuning]("settings")}, key: []string{"id"}},
		[][]any{{"a", `{"mode":"auto","level":2}`}, {"b", `{"mode":"manual","level":5}`}})
	r, err := parse.NewResource[tunedItem](provider, "tuned_items")
	require.NoError(t, err)

	ids := func(f string) []string {
		req, err := r.ParseList(url.Values{"filter": {f}})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{})
		require.NoError(t, err)
		return itemIDs(resourceRows(t, r, plan.Page), func(it tunedItem) string { return it.ID })
	}

	p, err := r.ParseMergePatch([]byte(`{"settings":{"level":9}}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), plan.Owner.SQL, plan.Owner.Args...)
	require.NoError(t, err, plan.Owner.SQL)

	assert.Equal(t, []string{"a"}, ids(`{"settings.level":9}`), "the patched member reads back through its path")
	assert.Equal(t, []string{"a"}, ids(`{"settings.mode":"auto"}`), "its sibling survives the merge")
	assert.Equal(t, []string{"b"}, ids(`{"settings.level":5}`), "another row is untouched")

	_, err = r.ParseMergePatch([]byte(`{"settings":{"bogus":1}}`))
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "a member the type does not declare is refused, not stored")
}
