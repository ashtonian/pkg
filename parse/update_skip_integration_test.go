//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type pgNote struct {
	ID        string         `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string         `json:"tenant_id"  db:"tenant_id"`
	Title     *string        `json:"title"      db:"title"      parse:"filter,patch"`
	Rank      int64          `json:"rank"       db:"rank"       parse:"filter,patch"`
	Labels    []string       `json:"labels"     db:"labels"     parse:"patch"`
	Attrs     map[string]any `json:"attrs"      db:"attrs"      parse:"patch"`
	Version   int64          `json:"version"    db:"version"`
	UpdatedAt *time.Time     `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at" db:"deleted_at"`
}

// pgNoteColumns are the columns the statements return; labels and attrs are
// compared by the database and read back on their own.
var pgNoteColumns = []string{"id", "tenant_id", "title", "rank", "version", "updated_at", "deleted_at"}

func pgNoteDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Postgres(t)
	for _, stmt := range []string{
		`CREATE TABLE pg_notes (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, title TEXT, rank BIGINT NOT NULL DEFAULT 1,
			labels TEXT[] NOT NULL DEFAULT '{}', attrs JSONB NOT NULL DEFAULT '{}', version BIGINT NOT NULL DEFAULT 1,
			updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ)`,
		`INSERT INTO pg_notes (id, tenant_id, title, labels, attrs) VALUES
			('n1', 't1', 'one', '{a,b}', '{"b":[2], "a":1}'), ('n2', 't1', 'two', '{a,b}', '{"a":1}'),
			('n3', 't1', 'gone', '{}', '{}'), ('m1', 't2', 'other', '{}', '{}')`,
		`UPDATE pg_notes SET deleted_at = now() WHERE id = 'n3'`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func queryPGNotes(t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, st parse.Statement) []pgNote {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer rows.Close()
	var out []pgNote
	for rows.Next() {
		var n pgNote
		require.NoError(t, rows.Scan(&n.ID, &n.TenantID, &n.Title, &n.Rank, &n.Version, &n.UpdatedAt, &n.DeletedAt))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

func pgNoteIDs(ns []pgNote) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.ID)
	}
	slices.Sort(out)
	return out
}

func TestSkipUnchanged_Postgres(t *testing.T) {
	r, err := parse.NewResource[pgNote]("pg_notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.ReadColumns(pgNoteColumns...))
	require.NoError(t, err)
	ctx := context.Background()

	// apply runs Current then Owner in one transaction and splits the rows.
	apply := func(t *testing.T, db *sql.DB, body string, target parse.Target) (changed, unchanged []pgNote) {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, target, parse.Scope{Tenant: "t1"},
			parse.StampSQL("updated_at", "now()"), parse.Increment("version"), parse.SkipUnchanged())
		require.NoError(t, err)
		require.NotNil(t, plan.Current)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		current := queryPGNotes(t, tx, *plan.Current)
		changed = queryPGNotes(t, tx, plan.Owner)
		require.NoError(t, tx.Commit())
		unchanged, err = r.Unchanged(changed, current)
		require.NoError(t, err)
		return changed, unchanged
	}
	stored := func(t *testing.T, db *sql.DB, id string) pgNote {
		t.Helper()
		got := queryPGNotes(t, db, parse.Statement{SQL: `SELECT id, tenant_id, title, rank, version, updated_at, deleted_at FROM pg_notes WHERE id = $1`, Args: []any{id}})
		require.Len(t, got, 1)
		return got[0]
	}

	t.Run("a patch of what the row holds writes nothing", func(t *testing.T) {
		db := pgNoteDB(t)
		before := stored(t, db, "n1")
		changed, unchanged := apply(t, db, `{"title":"one","rank":1,"labels":["a","b"],"attrs":{"a":1,"b":[2]}}`, parse.ByID("n1"))
		assert.Empty(t, changed, "no row is updated")
		assert.Equal(t, []string{"n1"}, pgNoteIDs(unchanged), "the read-back finds the row")
		after := stored(t, db, "n1")
		assert.Equal(t, before.Version, after.Version, "the version stays")
		assert.Nil(t, after.UpdatedAt, "the stamp stays")
	})

	t.Run("jsonb compares canonical text: key order does not count, a number's form does; arrays in order", func(t *testing.T) {
		db := pgNoteDB(t)
		for _, tc := range []struct {
			body    string
			changed bool
		}{
			{`{"attrs":{"b":[2],"a":1}}`, false},
			{`{"attrs":{"a":1.0}}`, true},
			{`{"attrs":{"a":2}}`, true},
			{`{"labels":["a","b"]}`, false},
			{`{"labels":["b","a"]}`, true},
		} {
			changed, _ := apply(t, db, tc.body, parse.ByID("n1"))
			assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
		}
	})

	t.Run("a partial change writes, stamps and counts", func(t *testing.T) {
		db := pgNoteDB(t)
		before := stored(t, db, "n1")
		changed, unchanged := apply(t, db, `{"title":"one","rank":5}`, parse.ByID("n1"))
		require.Equal(t, []string{"n1"}, pgNoteIDs(changed))
		assert.Empty(t, unchanged)
		assert.Equal(t, before.Version+1, changed[0].Version)
		require.NotNil(t, changed[0].UpdatedAt)
	})

	t.Run("NULL to NULL, NULL to a value, a value to NULL", func(t *testing.T) {
		db := pgNoteDB(t)
		for _, tc := range []struct {
			body    string
			changed bool
		}{
			{`{"title":null}`, true},
			{`{"title":null}`, false},
			{`{"title":"back"}`, true},
		} {
			changed, unchanged := apply(t, db, tc.body, parse.ByID("n1"))
			assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
			assert.Equal(t, !tc.changed, len(unchanged) == 1, tc.body)
		}
		assert.Equal(t, int64(3), stored(t, db, "n1").Version)
	})

	t.Run("a batch writes the rows that change, within the scope", func(t *testing.T) {
		db := pgNoteDB(t)
		changed, unchanged := apply(t, db, `{"title":"two","attrs":{"a":1}}`, parse.ByIDs("n1", "n2", "n3", "m1"))
		assert.Equal(t, []string{"n1"}, pgNoteIDs(changed))
		assert.Equal(t, []string{"n2"}, pgNoteIDs(unchanged), "n3 is deleted, m1 is t2's")
		assert.Equal(t, int64(1), stored(t, db, "n2").Version)
		assert.Equal(t, int64(1), stored(t, db, "m1").Version)
	})

	t.Run("not found", func(t *testing.T) {
		db := pgNoteDB(t)
		changed, unchanged := apply(t, db, `{"title":"x"}`, parse.ByID("m1"))
		assert.Empty(t, changed)
		assert.Empty(t, unchanged)
	})
}
