//go:build unit

package parsesql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestCheckVersion(t *testing.T) {
	for _, tt := range []struct {
		name, version string
		older         bool
		malformed     bool
	}{
		{name: "the minimum", version: MinSQLiteVersion},
		{name: "a later patch", version: "3.47.2"},
		{name: "a later minor, compared as a number", version: "3.100.0"},
		{name: "a later major", version: "4.0.0"},
		{name: "the release before the minimum", version: "3.46.1", older: true},
		{name: "decoded labels without escaped quotes", version: "3.45.0", older: true},
		{name: "an older major", version: "2.99.99", older: true},
		{name: "two numbers", version: "3.47", malformed: true},
		{name: "not numbers", version: "3.x.0", malformed: true},
		{name: "a negative number", version: "3.-1.0", malformed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckVersion(tt.version)
			switch {
			case tt.older:
				var vErr *VersionError
				require.ErrorAs(t, err, &vErr)
				assert.Equal(t, VersionError{Have: tt.version, Need: MinSQLiteVersion}, *vErr)
			case tt.malformed:
				require.ErrorContains(t, err, "is not major.minor.patch")
			default:
				require.NoError(t, err)
			}
		})
	}
}

// TestOpen_RefusesAnOlderSQLite opens the provider over the bundled SQLite
// with the release it needs raised past the bundled one, so the bundled
// library stands in for an older one.
func TestOpen_RefusesAnOlderSQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	var bundled string
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT sqlite_version()").Scan(&bundled))

	for _, tt := range []struct {
		name, need string
		refused    bool
	}{
		{name: "the minimum", need: MinSQLiteVersion},
		{name: "the bundled release", need: bundled},
		{name: "a later release", need: "99.0.0", refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := open(t.Context(), db, tt.need, nil)
			if !tt.refused {
				require.NoError(t, err)
				assert.Equal(t, Name, p.Name())
				return
			}
			var vErr *VersionError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, VersionError{Have: bundled, Need: tt.need}, *vErr)
			assert.Nil(t, p)
		})
	}
}

func TestNew_RefusesItsArguments(t *testing.T) {
	_, err := New(t.Context(), nil)
	require.ErrorContains(t, err, "parsesql: nil database")
	_, err = New(t.Context(), dbtest.SQLite(t), SettleTimeout(0))
	require.ErrorContains(t, err, "parsesql: settle timeout 0s: must be positive")
}
