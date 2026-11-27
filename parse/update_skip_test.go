//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

func TestSkipUnchanged_SQL(t *testing.T) {
	at := core.FormatTimestamp(parsetest.SkipAt())
	const columns = `"notes"."id", "notes"."tenant_id", "notes"."title", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at"`
	for _, tc := range []struct {
		provider       *parse.Provider
		owner, current string
	}{
		{onPostgres,
			`UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = $3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = $4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $5) ` +
				`AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text ` +
				`OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $2) FOR UPDATE`},
		{onSQLite,
			`UPDATE "notes" SET "rank" = ?1, "title" = ?2, "updated_at" = ?3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = ?4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?5) ` +
				`AND ("rank" COLLATE BINARY IS NOT ?1 OR "title" COLLATE BINARY IS NOT ?2) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = ?1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?2)`},
	} {
		t.Run(tc.provider.Name(), func(t *testing.T) {
			plan := parsetest.SkipUpdate(t, parsetest.Notes(t, tc.provider), `{"title":"n","rank":2}`, parse.ByID("n1"))
			assert.Equal(t, tc.owner, plan.Owner.SQL)
			assert.Equal(t, []any{int64(2), "n", at, "t1", "n1"}, plan.Owner.Args, "the predicate binds nothing of its own")
			require.NotNil(t, plan.Current)
			assert.Equal(t, tc.current, plan.Current.SQL)
			assert.Equal(t, []any{"t1", "n1"}, plan.Current.Args)
		})
	}
}

// A statement that locks the rows of a target that can hold several takes
// them in key order, so two updates over overlapping rows lock in one order
// and cannot deadlock on each other: Current, a relation-only patch's Owner,
// and Lock, which an unguarded UPDATE of several rows runs first, since the
// UPDATE takes its own locks in the order its plan reads the rows. One row
// needs no order, and SQLite locks no rows. The corpus's ExecutorContract
// case runs the deadlock itself on PostgreSQL (parsepgx's TestCorpus).
func TestBuildUpdate_LocksSeveralRowsInKeyOrder(t *testing.T) {
	pg := parsetest.Notes(t, onPostgres)
	byTitle, err := pg.ParseFilterMap(map[string]any{"title": "one"})
	require.NoError(t, err)
	const ordered = ` ORDER BY "notes"."id" COLLATE pg_c_utf8 FOR UPDATE`
	build := func(t *testing.T, r *parse.Resource[parsetest.Note], body string, target parse.Target, opts ...parse.UpdateOption) *parse.UpdatePlan {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(p, target, parse.Scope{Tenant: "t1"}, opts...)
		require.NoError(t, err)
		return plan
	}
	for _, tc := range []struct {
		name   string
		target parse.Target
		suffix string
	}{
		{"several keys", parse.ByIDs("n2", "n1"), ordered},
		{"a filter", parse.ByFilter(byTitle), ordered},
		{"one key", parse.ByID("n1"), ") FOR UPDATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := build(t, pg, `{"title":"x"}`, tc.target, parse.SkipUnchanged()).Current
			require.NotNil(t, current)
			assert.True(t, strings.HasSuffix(current.SQL, tc.suffix), current.SQL)
			owner := build(t, pg, `{"tags":["blue"]}`, tc.target).Owner
			assert.True(t, strings.HasSuffix(owner.SQL, tc.suffix), owner.SQL)

			unguarded := build(t, pg, `{"title":"x"}`, tc.target)
			assert.Nil(t, unguarded.Current)
			if tc.suffix != ordered {
				assert.Nil(t, unguarded.Lock, "one row's UPDATE locks it alone")
				return
			}
			require.NotNil(t, unguarded.Lock)
			assert.True(t, strings.HasPrefix(unguarded.Lock.SQL, `SELECT 1 FROM "notes" WHERE `), unguarded.Lock.SQL)
			assert.True(t, strings.HasSuffix(unguarded.Lock.SQL, tc.suffix), unguarded.Lock.SQL)
			assert.Nil(t, build(t, pg, `{"title":"x"}`, tc.target, parse.SkipUnchanged()).Lock, "Current locks the rows of a guarded update")
		})
	}
	t.Run("sqlite", func(t *testing.T) {
		lite := parsetest.Notes(t, onSQLite)
		current := build(t, lite, `{"title":"x"}`, parse.ByIDs("n2", "n1"), parse.SkipUnchanged()).Current
		require.NotNil(t, current)
		assert.NotContains(t, current.SQL, "ORDER BY")
		assert.Nil(t, build(t, lite, `{"title":"x"}`, parse.ByIDs("n2", "n1")).Lock)
	})
}

func TestSkipUnchanged_LeavesTheDefaultAlone(t *testing.T) {
	r := parsetest.Notes(t, onPostgres)
	p, err := r.ParseMergePatch([]byte(`{"title":"n","rank":2}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("n1"), parse.Scope{Tenant: "t1"}, parse.Increment("version"))
	require.NoError(t, err)
	assert.Nil(t, plan.Current)
	assert.NotContains(t, plan.Owner.SQL, "DISTINCT")
}

type pointerKeyed struct {
	ID   *string `json:"id"   db:"id"   parse:"filter"`
	Name string  `json:"name" db:"name" parse:"filter,patch"`
}

// keyedTable holds rows a and b, both named new, on a fresh SQLite
// database; a key column that is no INTEGER PRIMARY KEY holds NULL there.
func keyedTable(t *testing.T) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := providertest.SQLite(t)
	dbtest.Exec(t, db, `CREATE TABLE keyed (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO keyed VALUES ('a', 'old'), ('b', 'new')`)
	return p, db
}

// renameSkipping applies {"name":"new"} to target under SkipUnchanged.
func renameSkipping[T any](t *testing.T, r *parse.Resource[T], target func(*parse.Resource[T]) parse.Target) (parse.UpdateResult[T], error) {
	t.Helper()
	rename, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	return r.Update(t.Context(), rename, target(r), parse.Scope{}, parse.SkipUnchanged())
}

func byName[T any](t *testing.T) func(*parse.Resource[T]) parse.Target {
	return func(r *parse.Resource[T]) parse.Target {
		f, err := r.ParseFilterMap(map[string]any{"name": "new"})
		require.NoError(t, err)
		return parse.ByFilter(f)
	}
}

// Rows scanned apart hold distinct pointers to one key: the update splits
// the rows it read by their keys' values.
func TestUnchanged_ComparesPointerKeysByValue(t *testing.T) {
	p, db := keyedTable(t)
	r, err := parse.NewResource[pointerKeyed](p, "keyed")
	require.NoError(t, err)
	res, err := renameSkipping(t, r, func(*parse.Resource[pointerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, "a", *res.Changed[0].ID)
	require.Len(t, res.Unchanged, 1)
	assert.Equal(t, "b", *res.Unchanged[0].ID)

	dbtest.Exec(t, db, `INSERT INTO keyed VALUES (NULL, 'new')`)
	_, err = renameSkipping(t, r, byName[pointerKeyed](t))
	assert.ErrorContains(t, err, "has no key")
}

// optionalCode is a key codec that writes NULL for its zero value.
type optionalCode string

func (c optionalCode) Value() (driver.Value, error) {
	if c == "" {
		return nil, nil
	}
	return string(c), nil
}

func (c *optionalCode) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*c = ""
	case string:
		*c = optionalCode(v)
	default:
		return fmt.Errorf("optionalCode: %T", src)
	}
	return nil
}

type valuerKeyed struct {
	ID   optionalCode `json:"id"   db:"id"   parse:"filter"`
	Name string       `json:"name" db:"name" parse:"filter,patch"`
}

// A key column the database holds as NULL — a Valuer that writes nil, as
// a nil pointer — is a row without a key.
func TestUnchanged_RefusesARowWhoseKeyIsNull(t *testing.T) {
	p, db := keyedTable(t)
	r, err := parse.NewResource[valuerKeyed](p, "keyed")
	require.NoError(t, err)
	res, err := renameSkipping(t, r, func(*parse.Resource[valuerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
	require.NoError(t, err)
	assert.Equal(t, []valuerKeyed{{ID: "a", Name: "new"}}, res.Changed)
	assert.Equal(t, []valuerKeyed{{ID: "b", Name: "new"}}, res.Unchanged)

	dbtest.Exec(t, db, `INSERT INTO keyed VALUES (NULL, 'new')`)
	_, err = renameSkipping(t, r, byName[valuerKeyed](t))
	require.ErrorContains(t, err, "keyed: a row has no key: key column id holds no value")
}
