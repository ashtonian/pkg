//go:build unit

package patch_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestBuildPatchSQL_ChangePredicateSQLite(t *testing.T) {
	runChangeCases(t, changeDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		setup: []string{`CREATE TABLE things (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE, email TEXT COLLATE NOCASE, mood TEXT,
			rank INTEGER, price NUMERIC, amount REAL, ratio REAL, span TEXT, at TEXT, doc TEXT, raw TEXT, tags TEXT NOT NULL DEFAULT '[]',
			version INTEGER NOT NULL DEFAULT 1, updated_at TEXT)`},
		// quote() keeps the storage class: 1 and 1.0 read apart.
		readBack: `SELECT quote(name) || '|' || quote(email) || '|' || quote(mood) || '|' || quote(rank) || '|' || quote(price) || '|' ||
			quote(amount) || '|' || quote(ratio) || '|' || quote(span) || '|' || quote(at) || '|' || quote(doc) || '|' || quote(raw) || '|' ||
			quote(tags) FROM things WHERE id = 1`,
	})
}

func TestBuildPatchSQL_ChangePredicateSQL(t *testing.T) {
	stamps := []patch.PatchSQLOption{patch.WithAutoTimestampSQL("updated_at", "NOW()"), patch.WithIncrement("version"), patch.WithChangePredicate()}
	for _, tc := range []struct {
		name     string
		dialect  patch.SQLDialect
		patch    string
		set, cmp string
	}{
		{"text and an integer, postgres", filter.PostgresDialect{}, `{"name":"n","rank":2}`,
			`name = $1, rank = $2, updated_at = NOW(), version = version + 1`,
			`(name IS DISTINCT FROM $1 OR name::text COLLATE "C" IS DISTINCT FROM ($1)::text OR rank IS DISTINCT FROM $2 OR rank::text COLLATE "C" IS DISTINCT FROM ($2)::text)`},
		{"text and an integer, sqlite", filter.SQLiteDialect{}, `{"name":"n","rank":2}`,
			`name = ?1, rank = ?2, updated_at = NOW(), version = version + 1`,
			`(name COLLATE BINARY IS NOT ?1 OR rank COLLATE BINARY IS NOT ?2)`},
		{"a clear to NULL", filter.PostgresDialect{}, `{"name":null}`,
			`name = NULL, updated_at = NOW(), version = version + 1`,
			`(name IS NOT NULL)`},
		{"a decimal, postgres", filter.PostgresDialect{}, `{"price":"1.50"}`,
			`price = $1, updated_at = NOW(), version = version + 1`,
			`(price IS DISTINCT FROM $1)`},
		{"a declared json column, postgres", filter.PostgresDialect{}, `[{"op":"replace","path":"/raw","value":{"a":1}}]`,
			`raw = $1, updated_at = NOW(), version = version + 1`,
			`(raw::jsonb::text COLLATE "C" IS DISTINCT FROM ($1)::json::jsonb::text)`},
		{"an append, postgres", filter.PostgresDialect{}, `[{"op":"array_append","path":"/tags","value":"x"}]`,
			`tags = array_append(COALESCE(tags, '{}'), $1), updated_at = NOW(), version = version + 1`,
			`(tags IS DISTINCT FROM array_append(COALESCE(tags, '{}'), $1) OR tags::text COLLATE "C" IS DISTINCT FROM (array_append(COALESCE(tags, '{}'), $1))::text)`},
		{"a merge, sqlite", filter.SQLiteDialect{}, `{"doc":{"a":1}}`,
			`doc = json_patch(COALESCE(doc, '{}'), ?1), updated_at = NOW(), version = version + 1`,
			`(doc COLLATE BINARY IS NOT json_patch(COALESCE(doc, '{}'), ?1))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := patch.BuildPatchSQL(patchOf(t, tc.patch), tc.dialect, stamps...)
			require.NoError(t, err)
			assert.Equal(t, tc.set, res.SetClause)
			assert.Equal(t, tc.cmp, res.ChangePredicate)
		})
	}
}

var placeholderRe = regexp.MustCompile(`[$?][0-9]+`)

// The predicate reuses the SET clause's placeholders, offset or not, and
// binds no argument of its own.
func TestBuildPatchSQL_ChangePredicateReusesPlaceholders(t *testing.T) {
	p := patchOf(t, `{"name":"n","rank":2,"doc":{"a":{"b":1},"c":null},"tags":["x"]}`)
	for _, d := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		for _, start := range []int{0, 4} {
			plain, err := patch.BuildPatchSQL(p, d, patch.WithStartParam(start))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(p, d, patch.WithStartParam(start), patch.WithChangePredicate())
			require.NoError(t, err)
			assert.Equal(t, plain.SetClause, res.SetClause)
			assert.Equal(t, plain.Args, res.Args)
			assert.Equal(t, plain.ParamCount, res.ParamCount)

			set := map[string]bool{}
			for _, ph := range placeholderRe.FindAllString(res.SetClause, -1) {
				set[ph] = true
			}
			used := placeholderRe.FindAllString(res.ChangePredicate, -1)
			require.NotEmpty(t, used, res.ChangePredicate)
			for _, ph := range used {
				assert.True(t, set[ph], "%s %s is not one of the SET clause's: %s", d.Name(), ph, res.SetClause)
			}
		}
	}
}

func TestBuildPatchSQL_ChangePredicateIsOptIn(t *testing.T) {
	p := patchOf(t, `{"name":"n"}`)
	res, err := patch.BuildPatchSQL(p, filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Empty(t, res.ChangePredicate)

	server := patchOf(t, `{"version":3}`)
	res, err = patch.BuildPatchSQL(server, filter.PostgresDialect{}, patch.WithIncrement("version"), patch.WithChangePredicate())
	require.NoError(t, err)
	assert.True(t, res.Empty)
	assert.Empty(t, res.ChangePredicate, "a patch of server columns alone assigns nothing to compare")
}

type otherDialect struct{ filter.SQLiteDialect }

func (otherDialect) Name() string { return "other" }

func TestBuildPatchSQL_ChangePredicateRefusesAnUnknownDialect(t *testing.T) {
	p := patchOf(t, `{"name":"n"}`)
	_, err := patch.BuildPatchSQL(p, otherDialect{})
	require.NoError(t, err, "the SET clause alone compiles for any dialect")
	res, err := patch.BuildPatchSQL(p, otherDialect{}, patch.WithChangePredicate())
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorContains(t, err, `dialect "other"`)
}
