//go:build unit

package parse_test

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestSkipUnchanged_SQL(t *testing.T) {
	r := notes(t)
	at := core.FormatTimestamp(skipAt())
	const columns = `"notes"."id", "notes"."tenant_id", "notes"."title", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at"`
	for _, tc := range []struct {
		dialect        parse.Dialect
		owner, current string
	}{
		{filter.PostgresDialect{},
			`UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = $3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = $4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $5) ` +
				`AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text ` +
				`OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $2) FOR UPDATE`},
		{filter.SQLiteDialect{},
			`UPDATE "notes" SET "rank" = ?1, "title" = ?2, "updated_at" = ?3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = ?4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?5) ` +
				`AND ("rank" COLLATE BINARY IS NOT ?1 OR "title" COLLATE BINARY IS NOT ?2) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = ?1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?2)`},
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

// A statement that locks the rows of a target that can hold several takes
// them in key order, so two updates over overlapping rows lock in one order
// and cannot deadlock on each other: Current, a relation-only patch's Owner,
// and Lock, which an unguarded UPDATE of several rows runs first, since the
// UPDATE takes its own locks in the order its plan reads the rows. One row
// needs no order, and SQLite locks no rows. TestExecutorContract/postgres
// runs the deadlock itself.
func TestBuildUpdate_LocksSeveralRowsInKeyOrder(t *testing.T) {
	r := notes(t)
	byTitle, err := r.ParseFilterMap(map[string]any{"title": "one"})
	require.NoError(t, err)
	const ordered = ` ORDER BY "notes"."id" COLLATE pg_c_utf8 FOR UPDATE`
	build := func(t *testing.T, d parse.Dialect, body string, target parse.Target, opts ...parse.UpdateOption) *parse.UpdatePlan {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(d, p, target, parse.Scope{Tenant: "t1"}, opts...)
		require.NoError(t, err)
		return plan
	}
	pg := filter.PostgresDialect{}
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
		current := build(t, filter.SQLiteDialect{}, `{"title":"x"}`, parse.ByIDs("n2", "n1"), parse.SkipUnchanged()).Current
		require.NotNil(t, current)
		assert.NotContains(t, current.SQL, "ORDER BY")
		assert.Nil(t, build(t, filter.SQLiteDialect{}, `{"title":"x"}`, parse.ByIDs("n2", "n1")).Lock)
	})
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

// optionalCode is a key codec that writes NULL for its zero value.
type optionalCode string

func (c optionalCode) Value() (driver.Value, error) {
	if c == "" {
		return nil, nil
	}
	return string(c), nil
}

type valuerKeyed struct {
	ID   optionalCode `json:"id"   db:"id"   parse:"filter"`
	Name string       `json:"name" db:"name" parse:"patch"`
}

// A key column the database holds as NULL — a Valuer that writes nil, as
// a nil pointer — is a row without a key.
func TestUnchanged_RefusesARowWhoseKeyIsNull(t *testing.T) {
	r, err := parse.NewResource[valuerKeyed]("keyed")
	require.NoError(t, err)
	unchanged, err := r.Unchanged([]valuerKeyed{{ID: "a"}}, []valuerKeyed{{ID: "a"}, {ID: "b"}})
	require.NoError(t, err)
	assert.Equal(t, []valuerKeyed{{ID: "b"}}, unchanged)

	_, err = r.Unchanged(nil, []valuerKeyed{{}})
	require.ErrorContains(t, err, "parse: keyed: a row has no key: key column id holds no value")
}
