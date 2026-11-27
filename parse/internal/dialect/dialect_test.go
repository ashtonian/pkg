//go:build unit

package dialect

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialects(t *testing.T) {
	for _, tc := range []struct {
		d     Dialect
		kind  Kind
		name  string
		first string
	}{
		{Postgres{}, KindPostgres, "postgres", "$1"},
		{SQLite{}, KindSQLite, "sqlite", "?1"},
	} {
		assert.Equal(t, tc.kind, KindOf(tc.d))
		assert.Equal(t, tc.name, tc.d.Name())
		assert.Equal(t, tc.first, tc.d.Placeholder(1))
		assert.Positive(t, tc.d.MaxParams())
		assert.Equal(t, `"a""b"`, tc.d.QuoteIdent(`a"b`), "an embedded quote doubles")
		assert.Panics(t, func() { tc.d.Placeholder(0) })
	}
}

func TestKindOfDSN(t *testing.T) {
	for _, tc := range []struct{ dsn, want string }{
		{"postgres://u:p@h/db", "postgres"},
		{"postgresql://h/db", "postgres"},
		{"POSTGRES://h/db", "postgres"},
		{"PostgreSQL://h/db", "postgres"},
		{"postgres://h/app.db", "postgres"},
		{"sqlite://app", "sqlite"},
		{"sqlite3:///srv/app.db", "sqlite"},
		{"file:x.db", "sqlite"},
		{"file:x.db?mode=ro", "sqlite"},
		{"file:///srv/app.db", "sqlite"},
		{"file:", "sqlite"},
		{":memory:", "sqlite"},
		{":memory:?_pragma=foreign_keys(1)", "sqlite"},
		{"/var/lib/app/state.db", "sqlite"},
		{"/Users/me/My Data/app.db", "sqlite"},
		{"/Users/me/Library/CloudStorage/GoogleDrive-me@example.com/My Drive/app.db", "sqlite"},
		{"/data/tenant=42/app.db", "sqlite"},
		{"/data/app.db?_pragma=busy_timeout(5000)", "sqlite"},
		{"/data/app.sqlite", "sqlite"},
		{"/data/state.SQLITE3", "sqlite"},
		{"/srv/a://b/app.db", "sqlite"},
		{"./app.db", "sqlite"},
		{"./tenant=42/app.db", "sqlite"},
		{"../state.sqlite3?_pragma=foreign_keys(1)", "sqlite"},
		{`.\app.db`, "sqlite"},
		{`..\backups (old)\app.db`, "sqlite"},
		{`\\server\share\app.db`, "sqlite"},
		{`\\?\C:\data\app.db`, "sqlite"},
		{`\\?\C:\data\app.db?_pragma=busy_timeout(5000)`, "sqlite"},
		{`\\?\UNC\server\share\app.db`, "sqlite"},
		{`C:\Users\me@corp\APP.DB`, "sqlite"},
		{"C:/data/app.db", "sqlite"},
		{"C://data/app.db", "sqlite"},
	} {
		t.Run(tc.dsn, func(t *testing.T) {
			k, err := KindOfDSN(tc.dsn)
			require.NoError(t, err)
			assert.Equal(t, tc.want, k.String())
		})
	}
}

func TestKindOfDSNRefuses(t *testing.T) {
	const cockroach = "parse: unsupported DSN: scheme %q: CockroachDB is not supported"
	for _, tc := range []struct {
		name string
		dsn  string
		want string
	}{
		{"cockroach", "cockroach://root:secret@h:26257/db", fmt.Sprintf(cockroach, "cockroach")},
		{"crdb", "crdb://root:secret@h/db", fmt.Sprintf(cockroach, "crdb")},
		{"crdb with a .db database", "crdb://h/app.db", fmt.Sprintf(cockroach, "crdb")},
		{"crdb in capitals", "CRDB://h/db", fmt.Sprintf(cockroach, "CRDB")},
		{"another database", "mysql://u:secret@h/db", `parse: unsupported DSN: scheme "mysql": ` + supportedDSNs},
		{"another scheme ending in .db", "mysql://h/app.db", `parse: unsupported DSN: scheme "mysql": ` + supportedDSNs},
		{"misspelled postgres", "postgress://u:secret@h/db", `parse: unsupported DSN: scheme "postgress": ` + supportedDSNs},
		{"upper-case FILE is no URI", "FILE:///srv/app.db", `parse: unsupported DSN: scheme "FILE": ` + supportedDSNs},
		{"not a scheme before ://", "u:secret@h://db", "parse: unsupported DSN: " + supportedDSNs},
		{"keyword/value", "host=h user=u password=secret dbname=db", "parse: unsupported DSN: " + supportedDSNs},
		{"keyword/value naming a .db database", "host=h user=u password=secret dbname=orders.db", "parse: unsupported DSN: " + supportedDSNs},
		{"mysql naming a .db database", "u:secret@tcp(h:3306)/app.db", "parse: unsupported DSN: " + supportedDSNs},
		{"mysql over tcp without credentials", "tcp(db:3306)/orders.db", "parse: unsupported DSN: " + supportedDSNs},
		{"mysql over a socket", "unix(/var/run/mysqld.sock)/app.db", "parse: unsupported DSN: " + supportedDSNs},
		{"mysql with a ? in its password", "u:s.db?x@tcp(h:3306)/app", "parse: unsupported DSN: " + supportedDSNs},
		{"mysql /dbname", "/app.db?parseTime=true", "parse: unsupported DSN: " + supportedDSNs},
		{"oracle easy connect", "dbhost:1521/sales.db", "parse: unsupported DSN: " + supportedDSNs},
		{"oracle easy connect with //", "//dbhost:1521/sales.db", "parse: unsupported DSN: " + supportedDSNs},
		{"bare name", "app.db", "parse: unsupported DSN: " + supportedDSNs},
		{"bare name with a directory", "data/app.db", "parse: unsupported DSN: " + supportedDSNs},
		{"upper-case FILE: is a file name", "FILE:app.db", "parse: unsupported DSN: " + supportedDSNs},
		{"another extension", "./app.dbx", "parse: unsupported DSN: " + supportedDSNs},
		{"extended-length path to another extension", `\\?\C:\data\app.dbx?x.db`, "parse: unsupported DSN: " + supportedDSNs},
		{"empty", "", "parse: unsupported DSN: " + supportedDSNs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := KindOfDSN(tc.dsn)
			require.ErrorIs(t, err, ErrUnknownDSN)
			assert.Zero(t, k)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}
