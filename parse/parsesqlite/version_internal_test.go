//go:build unit

package parsesqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
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

type connector struct{ d *sqlite.Driver }

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.d.Open(":memory:") }
func (c connector) Driver() driver.Driver                        { return c.d }

// TestRegisterOn_RefusesAnOlderSQLite opens the bundled SQLite through the
// hook RegisterOn installs, with the release it needs raised past the
// bundled one, so the bundled library stands in for an older one.
func TestRegisterOn_RefusesAnOlderSQLite(t *testing.T) {
	plain := sql.OpenDB(connector{&sqlite.Driver{}})
	t.Cleanup(func() { _ = plain.Close() })
	var bundled string
	require.NoError(t, plain.QueryRowContext(t.Context(), "SELECT sqlite_version()").Scan(&bundled))

	for _, tt := range []struct {
		name, need string
		refused    bool
	}{
		{name: "the minimum", need: MinSQLiteVersion},
		{name: "the bundled release", need: bundled},
		{name: "a later release", need: "99.0.0", refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &sqlite.Driver{}
			require.NoError(t, registerOn(d, tt.need))
			db := sql.OpenDB(connector{d})
			t.Cleanup(func() { _ = db.Close() })

			var got string
			err := db.QueryRowContext(t.Context(), "SELECT parse_decimal('1.50')").Scan(&got)
			if !tt.refused {
				require.NoError(t, err)
				assert.Equal(t, "1.5", got)
				return
			}
			var vErr *VersionError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, VersionError{Have: bundled, Need: tt.need}, *vErr)
			assert.ErrorContains(t, err, "connection hook: parsesqlite: SQLite "+bundled+" is older than 99.0.0")
		})
	}
}
