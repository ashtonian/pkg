//go:build unit

package parse_test

import (
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type note struct {
	ID        string   `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string   `json:"tenant_id"  db:"tenant_id"`
	Title     *string  `json:"title"      db:"title"      parse:"filter,patch"`
	Rank      int64    `json:"rank"       db:"rank"       parse:"filter,patch"`
	Version   int64    `json:"version"    db:"version"`
	UpdatedAt string   `json:"updated_at" db:"updated_at"`
	DeletedAt *string  `json:"deleted_at" db:"deleted_at"`
	Tags      []string `json:"tags"       db:"-"          parse:"filter,patch" relation:"note_tags,note_id,tag_id,id,tenant_id" indexed:"true"`
}

func notes(t *testing.T) *parse.Resource[note] {
	t.Helper()
	r, err := parse.NewResource[note]("notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

// noteDB: t1 holds n1 and n2 (title "one" and "two", rank 1) and a deleted
// n3; t2 holds m1. n1 is tagged red.
func noteDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE notes (id TEXT PRIMARY KEY, tenant_id TEXT, title TEXT, rank INTEGER, version INTEGER NOT NULL DEFAULT 1, updated_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE note_tags (note_id TEXT, tag_id TEXT, tenant_id TEXT, UNIQUE (note_id, tag_id))`,
		`INSERT INTO notes (id, tenant_id, title, rank, updated_at, deleted_at) VALUES ('n1','t1','one',1,'old',NULL), ('n2','t1','two',1,'old',NULL),
			('n3','t1','gone',1,'old','2026-01-01T00:00:00Z'), ('m1','t2','other',1,'old',NULL)`,
		`INSERT INTO note_tags VALUES ('n1','red','t1')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

var skipAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// skipUpdate builds a SkipUnchanged plan that stamps and counts.
func skipUpdate(t *testing.T, r *parse.Resource[note], d parse.Dialect, body string, target parse.Target) *parse.UpdatePlan {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(d, p, target, parse.Scope{Tenant: "t1"},
		parse.Stamp("updated_at", skipAt), parse.Increment("version"), parse.SkipUnchanged())
	require.NoError(t, err)
	return plan
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func scanNotes(t *testing.T, q queryer, st parse.Statement) []note {
	t.Helper()
	rows, err := q.Query(st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []note
	for rows.Next() {
		var n note
		require.NoError(t, rows.Scan(&n.ID, &n.TenantID, &n.Title, &n.Rank, &n.Version, &n.UpdatedAt, &n.DeletedAt))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

// noteIDs are the rows' keys, sorted; nil for none.
func noteIDs(ns []note) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.ID)
	}
	slices.Sort(out)
	return out
}

// applied runs a guarded plan the way a consumer does — Current, then Owner,
// in one transaction — and splits the target rows.
func applied(t *testing.T, r *parse.Resource[note], db *sql.DB, plan *parse.UpdatePlan) (changed, unchanged []note) {
	t.Helper()
	require.NotNil(t, plan.Current)
	tx, err := db.Begin()
	require.NoError(t, err)
	current := scanNotes(t, tx, *plan.Current)
	changed = scanNotes(t, tx, plan.Owner)
	require.NoError(t, tx.Commit())
	unchanged, err = r.Unchanged(changed, current)
	require.NoError(t, err)
	return changed, unchanged
}

func storedNote(t *testing.T, db *sql.DB, id string) note {
	t.Helper()
	got := scanNotes(t, db, parse.Statement{SQL: `SELECT id, tenant_id, title, rank, version, updated_at, deleted_at FROM notes WHERE id = ?1`, Args: []any{id}})
	require.Len(t, got, 1)
	return got[0]
}

func TestSkipUnchanged_WritesOnlyRowsThePatchChanges(t *testing.T) {
	r := notes(t)
	d := filter.SQLiteDialect{}
	stamp := core.FormatTimestamp(skipAt)

	t.Run("a patch of what the row holds writes nothing", func(t *testing.T) {
		db := noteDB(t)
		changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"title":"one","rank":1}`, parse.ByID("n1")))
		assert.Empty(t, changed, "no row is updated")
		assert.Equal(t, []string{"n1"}, noteIDs(unchanged), "the read-back finds the row")
		n1 := storedNote(t, db, "n1")
		assert.Equal(t, int64(1), n1.Version, "the version stays")
		assert.Equal(t, "old", n1.UpdatedAt, "the stamp stays")
		assert.Equal(t, unchanged[0], n1)
	})

	t.Run("a patch changing one column of two writes, stamps and counts", func(t *testing.T) {
		db := noteDB(t)
		changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"title":"one","rank":5}`, parse.ByID("n1")))
		require.Equal(t, []string{"n1"}, noteIDs(changed))
		assert.Empty(t, unchanged)
		assert.Equal(t, int64(5), changed[0].Rank)
		assert.Equal(t, int64(2), changed[0].Version)
		assert.Equal(t, stamp, changed[0].UpdatedAt)
	})

	t.Run("a value to NULL, then NULL to NULL", func(t *testing.T) {
		db := noteDB(t)
		changed, _ := applied(t, r, db, skipUpdate(t, r, d, `{"title":null}`, parse.ByID("n1")))
		require.Equal(t, []string{"n1"}, noteIDs(changed))
		assert.Nil(t, changed[0].Title)
		changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"title":null}`, parse.ByID("n1")))
		assert.Empty(t, changed)
		assert.Equal(t, []string{"n1"}, noteIDs(unchanged))
		changed, _ = applied(t, r, db, skipUpdate(t, r, d, `{"title":"back"}`, parse.ByID("n1")))
		require.Equal(t, []string{"n1"}, noteIDs(changed), "NULL to a value")
		assert.Equal(t, int64(3), changed[0].Version)
	})

	t.Run("a batch writes the rows that change, within the scope", func(t *testing.T) {
		db := noteDB(t)
		changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"title":"two"}`, parse.ByIDs("n1", "n2", "n3", "m1")))
		assert.Equal(t, []string{"n1"}, noteIDs(changed))
		assert.Equal(t, []string{"n2"}, noteIDs(unchanged), "n3 is deleted, m1 is t2's")
		assert.Equal(t, int64(1), storedNote(t, db, "n2").Version)
		assert.Equal(t, int64(1), storedNote(t, db, "m1").Version)
	})

	t.Run("a filter the patch moves rows out of", func(t *testing.T) {
		db := noteDB(t)
		changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"rank":7}`, byNoteFilter(t, r, `{"rank":1}`)))
		assert.Equal(t, []string{"n1", "n2"}, noteIDs(changed))
		assert.Empty(t, unchanged, "the rows the patch moved out of the filter are changed ones")
		changed, unchanged = applied(t, r, db, skipUpdate(t, r, d, `{"rank":7}`, byNoteFilter(t, r, `{"rank":7}`)))
		assert.Empty(t, changed)
		assert.Equal(t, []string{"n1", "n2"}, noteIDs(unchanged))
	})

	t.Run("a target outside the scope is not found", func(t *testing.T) {
		db := noteDB(t)
		for _, id := range []string{"m1", "n3", "zz"} {
			changed, unchanged := applied(t, r, db, skipUpdate(t, r, d, `{"title":"x"}`, parse.ByID(id)))
			assert.Empty(t, changed, id)
			assert.Empty(t, unchanged, id)
		}
	})
}

func byNoteFilter(t *testing.T, r *parse.Resource[note], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

// execAll runs statements and reports the rows they changed.
func execAll(t *testing.T, tx *sql.Tx, stmts []string, args [][]any) int64 {
	t.Helper()
	var total int64
	for i, st := range stmts {
		res, err := tx.Exec(st, args[i]...)
		require.NoError(t, err, st)
		n, err := res.RowsAffected()
		require.NoError(t, err)
		total += n
	}
	return total
}

func relationRows(t *testing.T, r *parse.Resource[note], tx *sql.Tx, plan *parse.UpdatePlan, owners []note) int64 {
	t.Helper()
	keys := make([]any, len(owners))
	for i, o := range owners {
		k, err := r.Key(o)
		require.NoError(t, err)
		keys[i] = k
	}
	stmts, err := r.RelationStatements(plan, keys)
	require.NoError(t, err)
	sqls := make([]string, len(stmts))
	args := make([][]any, len(stmts))
	for i, st := range stmts {
		sqls[i], args[i] = st.SQL, st.Args
	}
	return execAll(t, tx, sqls, args)
}

func noteTags(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT note_id || ':' || tag_id FROM note_tags ORDER BY 1`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestSkipUnchanged_Relations(t *testing.T) {
	r := notes(t)
	d := filter.SQLiteDialect{}

	t.Run("a relation-only patch selects every owner, and the junction says what changed", func(t *testing.T) {
		db := noteDB(t)
		for _, tc := range []struct {
			body    string
			changes int64
			tags    []string
		}{
			{`{"tags":["red"]}`, 0, []string{"n1:red"}},
			{`{"tags":["blue"]}`, 2, []string{"n1:blue"}},
		} {
			plan := skipUpdate(t, r, d, tc.body, parse.ByID("n1"))
			assert.Nil(t, plan.Current, "Owner returns every target row")
			assert.True(t, plan.Writes)
			tx, err := db.Begin()
			require.NoError(t, err)
			owners := scanNotes(t, tx, plan.Owner)
			require.Equal(t, []string{"n1"}, noteIDs(owners))
			assert.Equal(t, tc.changes, relationRows(t, r, tx, plan, owners), tc.body)
			require.NoError(t, tx.Commit())
			assert.Equal(t, tc.tags, noteTags(t, db))
			assert.Equal(t, int64(1), storedNote(t, db, "n1").Version, "a relation change moves no column")
		}
	})

	t.Run("unchanged columns still change relations, for the rows Current reads", func(t *testing.T) {
		db := noteDB(t)
		plan := skipUpdate(t, r, d, `{"title":"one","tags":["blue"]}`, parse.ByIDs("n1", "n2"))
		require.NotNil(t, plan.Current)
		tx, err := db.Begin()
		require.NoError(t, err)
		current := scanNotes(t, tx, *plan.Current)
		changed := scanNotes(t, tx, plan.Owner)
		assert.Equal(t, []string{"n2"}, noteIDs(changed), "n1 already reads one")
		unchanged, err := r.Unchanged(changed, current)
		require.NoError(t, err)
		assert.Equal(t, []string{"n1"}, noteIDs(unchanged))
		assert.Equal(t, int64(3), relationRows(t, r, tx, plan, append(changed, unchanged...)), "red out of n1, blue into both")
		require.NoError(t, tx.Commit())
		assert.Equal(t, []string{"n1:blue", "n2:blue"}, noteTags(t, db))
		assert.Equal(t, int64(1), storedNote(t, db, "n1").Version)
		assert.Equal(t, int64(2), storedNote(t, db, "n2").Version)
	})

	t.Run("an empty patch is a scoped read", func(t *testing.T) {
		plan := skipUpdate(t, r, d, `{}`, parse.ByID("n1"))
		assert.False(t, plan.Writes)
		assert.Nil(t, plan.Current)
		assert.Equal(t, []string{"n1"}, noteIDs(scanNotes(t, noteDB(t), plan.Owner)))
	})
}

func TestSkipUnchanged_SQL(t *testing.T) {
	r := notes(t)
	at := core.FormatTimestamp(skipAt)
	const columns = "notes.id, notes.tenant_id, notes.title, notes.rank, notes.version, notes.updated_at, notes.deleted_at"
	for _, tc := range []struct {
		dialect        parse.Dialect
		owner, current string
	}{
		{filter.PostgresDialect{},
			`UPDATE notes SET rank = $1, title = $2, updated_at = $3, version = version + 1 ` +
				`WHERE (notes.tenant_id = $4 AND notes.deleted_at IS NULL AND notes.id = $5) ` +
				`AND (rank IS DISTINCT FROM $1 OR rank::text COLLATE "C" IS DISTINCT FROM ($1)::text ` +
				`OR title IS DISTINCT FROM $2 OR title::text COLLATE "C" IS DISTINCT FROM ($2)::text) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM notes WHERE (notes.tenant_id = $1 AND notes.deleted_at IS NULL AND notes.id = $2) FOR UPDATE`},
		{filter.SQLiteDialect{},
			`UPDATE notes SET rank = ?1, title = ?2, updated_at = ?3, version = version + 1 ` +
				`WHERE (notes.tenant_id = ?4 AND notes.deleted_at IS NULL AND notes.id = ?5) ` +
				`AND (rank COLLATE BINARY IS NOT ?1 OR title COLLATE BINARY IS NOT ?2) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM notes WHERE (notes.tenant_id = ?1 AND notes.deleted_at IS NULL AND notes.id = ?2)`},
	} {
		t.Run(tc.dialect.Name(), func(t *testing.T) {
			plan := skipUpdate(t, r, tc.dialect, `{"title":"n","rank":2}`, parse.ByID("n1"))
			assert.Equal(t, tc.owner, plan.Owner.SQL)
			assert.Equal(t, []any{int64(2), "n", at, "t1", "n1"}, plan.Owner.Args, "the predicate binds nothing of its own")
			require.NotNil(t, plan.Current)
			assert.Equal(t, tc.current, plan.Current.SQL)
			assert.Equal(t, []any{"t1", "n1"}, plan.Current.Args)
		})
	}
}

func TestSkipUnchanged_LeavesTheDefaultAlone(t *testing.T) {
	r := notes(t)
	p, err := r.ParseMergePatch([]byte(`{"title":"n","rank":2}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("n1"), parse.Scope{Tenant: "t1"}, parse.Increment("version"))
	require.NoError(t, err)
	assert.Nil(t, plan.Current)
	assert.NotContains(t, plan.Owner.SQL, "DISTINCT")
}

type pointerKeyed struct {
	ID   *string `json:"id"   db:"id"   parse:"filter"`
	Name string  `json:"name" db:"name" parse:"patch"`
}

// Rows scanned apart hold distinct pointers to one key.
func TestUnchanged_ComparesPointerKeysByValue(t *testing.T) {
	r, err := parse.NewResource[pointerKeyed]("keyed")
	require.NoError(t, err)
	key := func(s string) *string { return &s }
	changed := []pointerKeyed{{ID: key("a"), Name: "new"}}
	current := []pointerKeyed{{ID: key("a")}, {ID: key("b")}}
	unchanged, err := r.Unchanged(changed, current)
	require.NoError(t, err)
	require.Len(t, unchanged, 1)
	assert.Equal(t, "b", *unchanged[0].ID)

	_, err = r.Unchanged(nil, []pointerKeyed{{}})
	assert.ErrorContains(t, err, "has no key")
}
