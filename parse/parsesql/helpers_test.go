//go:build unit || integration

package parsesql

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

type tagged struct {
	ID   string   `json:"id"   db:"id"   parse:"filter"`
	Name string   `json:"name" db:"name" parse:"patch"`
	Tags []string `json:"tags" db:"-"    parse:"patch" relation:"tagged_tags,item_id,tag_id,id" indexed:"true"`
}

// taggedItems opens the provider on a fresh SQLite database whose
// statements dbtest.OnStatement can observe, holding row a named one, and
// returns the resource each test runs on with the database under it.
func taggedItems(t *testing.T) (*parse.Resource[tagged], *sql.DB) {
	t.Helper()
	db := dbtest.SQLiteHooked(t)
	for _, stmt := range []string{
		`CREATE TABLE tagged (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE tagged_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id))`,
		`INSERT INTO tagged VALUES ('a', 'one')`,
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	p, err := New(t.Context(), db)
	require.NoError(t, err)
	items := parse.MustResource[tagged](p, "tagged")
	return items, db
}

// rename is a merge patch of row a's name.
func rename(t *testing.T, items *parse.Resource[tagged], name string) *patch.ParsedPatch {
	t.Helper()
	p, err := items.ParseMergePatch([]byte(`{"name":"` + name + `"}`))
	require.NoError(t, err)
	return p
}

// storedName reads row a's name through items, which may be scoped to a
// transaction.
func storedName(t *testing.T, items *parse.Resource[tagged]) string {
	t.Helper()
	page, err := items.List(t.Context(), nil, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	return page.Items[0].Name
}
