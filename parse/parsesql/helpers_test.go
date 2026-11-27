//go:build unit || integration

package parsesql

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type tagged struct {
	ID   string   `json:"id"   db:"id"   parse:"filter"`
	Name string   `json:"name" db:"name" parse:"patch"`
	Tags []string `json:"tags" db:"-"    parse:"patch" relation:"tagged_tags,item_id,tag_id,id" indexed:"true"`
}

// taggedItems builds the resource each test runs on.
func taggedItems() *parse.Resource[tagged] {
	return parse.MustResource[tagged]("tagged")
}

func taggedDB(t *testing.T) *sql.DB {
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
	return db
}

func renamePlan(t *testing.T, items *parse.Resource[tagged], body string) *parse.UpdatePlan {
	t.Helper()
	p, err := items.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := items.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("a"), parse.Scope{})
	require.NoError(t, err)
	return plan
}

// storedName reads row a's name through db, which may be any Queryer.
func storedName(t *testing.T, db Queryer, items *parse.Resource[tagged]) string {
	t.Helper()
	list, err := items.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	page, err := QueryPage(t.Context(), db, items, list)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	return page.Items[0].Name
}
