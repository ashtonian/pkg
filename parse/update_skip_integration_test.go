//go:build integration

package parse_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

func noteTable() sqlTable {
	table := sqlTable{name: "notes", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[string]("tenant_id").required(), column[string]("title"), column[int64]("rank").required(),
		column[[]string]("labels").required(), column[map[string]any]("attrs").required(), column[int64]("version").required(),
		column[time.Time]("updated_at"), column[time.Time]("deleted_at"),
	}}
	return table
}

func noteTagTable() sqlTable {
	table := sqlTable{name: "note_tags", key: []string{"note_id", "tag_id"}, columns: []sqlColumn{
		column[string]("note_id"), column[string]("tag_id"), column[string]("tenant_id").required(),
	}}
	return table
}

// loadNotes seeds a fresh database of b: t1 holds n1 and n2 (title "one"
// and "two", rank 1, labels a and b) and a deleted n3, t2 holds m1, and n1
// is tagged red. n1's attrs hold their keys out of order.
func loadNotes(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.open(t)
	ab := []string{"a", "b"}
	b.load(t, db, noteTable(), [][]any{
		{"n1", "t1", "one", 1, ab, `{"b":[2],"a":1}`, 1, nil, nil},
		{"n2", "t1", "two", 1, ab, `{"a":1}`, 1, nil, nil},
		{"n3", "t1", "gone", 1, []string{}, `{}`, 1, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"m1", "t2", "other", 1, []string{}, `{}`, 1, nil, nil},
	})
	b.load(t, db, noteTagTable(), [][]any{{"n1", "red", "t1"}})
	return db
}

func noteID(n note) string { return n.ID }

// applied runs a guarded plan through the executor and splits the target
// rows.
func applied(t *testing.T, b sqlBackend, r *parse.Resource[note], db *sql.DB, plan *parse.UpdatePlan) (changed, unchanged []note) {
	t.Helper()
	require.NotNil(t, plan.Current)
	res, err := applyUpdate(t, db, r, b.dialect, plan, false)
	require.NoError(t, err)
	return res.Changed, res.Unchanged
}

func storedNote(t *testing.T, b sqlBackend, db *sql.DB, id string) note {
	t.Helper()
	r := notes(t)
	columns, err := r.Columns(b.dialect)
	require.NoError(t, err)
	got := resourceRows(t, db, r, b.dialect, parse.Statement{SQL: `SELECT ` + joinColumns(columns) + ` FROM notes WHERE id = ` + b.dialect.Placeholder(1), Args: []any{id}})
	require.Len(t, got, 1)
	return got[0]
}

func byNoteFilter(t *testing.T, r *parse.Resource[note], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

func noteTags(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryRows[string](t, db, `SELECT note_id || ':' || tag_id FROM note_tags ORDER BY 1`)
}

// SkipUnchanged writes the rows a patch changes, on each backend: columns,
// arrays and documents compared as the backend holds them.
func TestSkipUnchanged_WritesOnlyRowsThePatchChanges(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		r := notes(t)
		d := b.dialect

		t.Run("a patch of what the row holds writes nothing", func(t *testing.T) {
			db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":"one","rank":1,"labels":["a","b"],"attrs":{"a":1,"b":[2]}}`, parse.ByID("n1")))
			assert.Empty(t, changed, "no row is updated")
			assert.Equal(t, []string{"n1"}, sortedIDs(unchanged, noteID), "the read-back finds the row")
			n1 := storedNote(t, b, db, "n1")
			assert.Equal(t, int64(1), n1.Version, "the version stays")
			assert.Nil(t, n1.UpdatedAt, "the stamp stays")
			assert.Equal(t, unchanged[0], n1)
		})

		// A merge keeps the stored document's key order, so a patch in
		// another order reads as unchanged; a number's form counts, and an
		// array's order.
		t.Run("documents and arrays: key order does not count, a number's form does; arrays in order", func(t *testing.T) {
			db := loadNotes(t, b)
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
				changed, _ := applied(t, b, r, db, skipUpdate(t, r, d, tc.body, parse.ByID("n1")))
				assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
			}
		})

		t.Run("a patch changing one column of two writes, stamps and counts", func(t *testing.T) {
			db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":"one","rank":5}`, parse.ByID("n1")))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Empty(t, unchanged)
			assert.Equal(t, int64(5), changed[0].Rank)
			assert.Equal(t, int64(2), changed[0].Version)
			require.NotNil(t, changed[0].UpdatedAt)
			assert.True(t, skipAt().Equal(*changed[0].UpdatedAt), "stamped %v", *changed[0].UpdatedAt)
		})

		t.Run("a stamp the database clock sets moves with the rows that change", func(t *testing.T) {
			db := loadNotes(t, b)
			for _, tc := range []struct {
				body    string
				changed bool
			}{
				{`{"rank":1}`, false},
				{`{"rank":2}`, true},
			} {
				p, err := r.ParseMergePatch([]byte(tc.body))
				require.NoError(t, err)
				plan, err := r.BuildUpdate(d, p, parse.ByID("n1"), parse.Scope{Tenant: "t1"},
					parse.StampSQL("updated_at", b.clock()), parse.Increment("version"), parse.SkipUnchanged())
				require.NoError(t, err)
				changed, unchanged := applied(t, b, r, db, plan)
				assert.Equal(t, tc.changed, len(changed) == 1, tc.body)
				assert.Equal(t, !tc.changed, len(unchanged) == 1, tc.body)
			}
			n1 := storedNote(t, b, db, "n1")
			assert.Equal(t, int64(2), n1.Version)
			require.NotNil(t, n1.UpdatedAt)
			assert.WithinDuration(t, time.Now(), *n1.UpdatedAt, time.Minute)
		})

		t.Run("a value to NULL, then NULL to NULL", func(t *testing.T) {
			db := loadNotes(t, b)
			changed, _ := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":null}`, parse.ByID("n1")))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Nil(t, changed[0].Title)
			changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":null}`, parse.ByID("n1")))
			assert.Empty(t, changed)
			assert.Equal(t, []string{"n1"}, sortedIDs(unchanged, noteID))
			changed, _ = applied(t, b, r, db, skipUpdate(t, r, d, `{"title":"back"}`, parse.ByID("n1")))
			require.Equal(t, []string{"n1"}, sortedIDs(changed, noteID), "NULL to a value")
			assert.Equal(t, int64(3), changed[0].Version)
		})

		t.Run("a batch writes the rows that change, within the scope", func(t *testing.T) {
			db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":"two","attrs":{"a":1}}`, parse.ByIDs("n1", "n2", "n3", "m1")))
			assert.Equal(t, []string{"n1"}, sortedIDs(changed, noteID))
			assert.Equal(t, []string{"n2"}, sortedIDs(unchanged, noteID), "n3 is deleted, m1 is t2's")
			assert.Equal(t, int64(1), storedNote(t, b, db, "n2").Version)
			assert.Equal(t, int64(1), storedNote(t, b, db, "m1").Version)
		})

		t.Run("a filter the patch moves rows out of", func(t *testing.T) {
			db := loadNotes(t, b)
			changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"rank":7}`, byNoteFilter(t, r, `{"rank":1}`)))
			assert.Equal(t, []string{"n1", "n2"}, sortedIDs(changed, noteID))
			assert.Empty(t, unchanged, "the rows the patch moved out of the filter are changed ones")
			changed, unchanged = applied(t, b, r, db, skipUpdate(t, r, d, `{"rank":7}`, byNoteFilter(t, r, `{"rank":7}`)))
			assert.Empty(t, changed)
			assert.Equal(t, []string{"n1", "n2"}, sortedIDs(unchanged, noteID))
		})

		t.Run("a target outside the scope is not found", func(t *testing.T) {
			db := loadNotes(t, b)
			for _, id := range []string{"m1", "n3", "zz"} {
				changed, unchanged := applied(t, b, r, db, skipUpdate(t, r, d, `{"title":"x"}`, parse.ByID(id)))
				assert.Empty(t, changed, id)
				assert.Empty(t, unchanged, id)
			}
		})
	})
}

// SkipUnchanged with relations, on each backend: the junction says what a
// relation patch changed.
func TestSkipUnchanged_Relations(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		r := notes(t)
		d := b.dialect

		t.Run("a relation-only patch selects every owner, and the junction says what changed", func(t *testing.T) {
			db := loadNotes(t, b)
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
				res, err := applyUpdate(t, db, r, d, plan, false)
				require.NoError(t, err)
				assert.Empty(t, res.Changed, "a relation change moves no column")
				require.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
				assert.Equal(t, tc.changes, res.RelationRows, tc.body)
				assert.Equal(t, tc.tags, noteTags(t, db))
				assert.Equal(t, int64(1), storedNote(t, b, db, "n1").Version, "a relation change moves no column")
			}
		})

		t.Run("unchanged columns still change relations, for the rows Current reads", func(t *testing.T) {
			db := loadNotes(t, b)
			plan := skipUpdate(t, r, d, `{"title":"one","tags":["blue"]}`, parse.ByIDs("n1", "n2"))
			require.NotNil(t, plan.Current)
			res, err := applyUpdate(t, db, r, d, plan, false)
			require.NoError(t, err)
			assert.Equal(t, []string{"n2"}, sortedIDs(res.Changed, noteID), "n1 already reads one")
			assert.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
			assert.Equal(t, int64(3), res.RelationRows, "red out of n1, blue into both")
			assert.Equal(t, []string{"n1:blue", "n2:blue"}, noteTags(t, db))
			assert.Equal(t, int64(1), storedNote(t, b, db, "n1").Version)
			assert.Equal(t, int64(2), storedNote(t, b, db, "n2").Version)
		})

		t.Run("an empty patch is a scoped read", func(t *testing.T) {
			plan := skipUpdate(t, r, d, `{}`, parse.ByID("n1"))
			assert.False(t, plan.Writes)
			assert.Nil(t, plan.Current)
			res, err := applyUpdate(t, loadNotes(t, b), r, d, plan, false)
			require.NoError(t, err)
			assert.Empty(t, res.Changed)
			assert.Equal(t, []string{"n1"}, sortedIDs(res.Unchanged, noteID))
		})
	})
}
