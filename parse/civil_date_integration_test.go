//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCivilDateSQL(t *testing.T) {
	for _, backend := range []struct {
		name    string
		open    func(*testing.T) *sql.DB
		dialect parse.Dialect
	}{
		{"sqlite", func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{}},
		{"postgres", func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{}},
	} {
		t.Run(backend.name, func(t *testing.T) {
			db := backend.open(t)
			_, err := db.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY, due_on DATE)`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO tasks VALUES ('leap','2028-02-29'),('today','2026-10-26'),('empty',NULL)`)
			require.NoError(t, err)
			if backend.name == "sqlite" {
				_, err = db.Exec(`INSERT INTO tasks VALUES ('legacy','2026-10-26T00:00:00Z')`)
				require.NoError(t, err)
			}
			resource := parse.MustResource[civilTask]("tasks")
			query, err := resource.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"2026-10-26"}}`}, "sort": {"due_on,id"}})
			require.NoError(t, err)
			plan, err := resource.BuildList(backend.dialect, query, parse.Scope{})
			require.NoError(t, err)
			rows, err := db.Query(plan.Page.SQL, plan.Page.Args...)
			require.NoError(t, err)
			var found []civilTask
			for rows.Next() {
				var row civilTask
				require.NoError(t, rows.Scan(&row.ID, &row.DueOn))
				found = append(found, row)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			if backend.name == "sqlite" {
				require.Len(t, found, 2)
				assert.Equal(t, "legacy", found[0].ID)
				found = found[1:]
			} else {
				require.Len(t, found, 1)
			}
			wire, err := json.Marshal(found[0])
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"today","due_on":"2026-10-26"}`, string(wire))
			for _, body := range []string{`{"due_on":"2028-02-29"}`, `{"due_on":null}`} {
				patch, err := resource.ParseMergePatch([]byte(body))
				require.NoError(t, err)
				update, err := resource.BuildUpdate(backend.dialect, patch, parse.ByID("today"), parse.Scope{})
				require.NoError(t, err)
				var row civilTask
				require.NoError(t, db.QueryRow(update.Owner.SQL, update.Owner.Args...).Scan(&row.ID, &row.DueOn))
				if body == `{"due_on":null}` {
					assert.Nil(t, row.DueOn)
				} else {
					require.NotNil(t, row.DueOn)
					assert.Equal(t, core.Date("2028-02-29"), *row.DueOn)
				}
			}
		})
	}
}
