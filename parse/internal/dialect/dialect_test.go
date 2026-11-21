//go:build unit

package dialect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialects(t *testing.T) {
	for _, tc := range []struct {
		d     Dialect
		name  string
		first string
	}{
		{Postgres{}, "postgres", "$1"},
		{SQLite{}, "sqlite", "?1"},
	} {
		assert.Equal(t, tc.name, tc.d.Name())
		assert.Equal(t, tc.first, tc.d.Placeholder(1))
		assert.Equal(t, `"a""b"`, tc.d.QuoteIdent(`a"b`), "an embedded quote doubles")
		assert.Panics(t, func() { tc.d.Placeholder(0) })
	}
}

func TestFromDSN(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://u:p@h/db":                   "postgres",
		"postgresql://h/db":                     "postgres",
		"POSTGRES://h/db":                       "postgres",
		"PostgreSQL://h/db":                     "postgres",
		"postgres://h/app.db":                   "postgres",
		"sqlite://app":                          "sqlite",
		"file:x.db":                             "sqlite",
		"file:x.db?mode=ro":                     "sqlite",
		"file:///srv/app.db":                    "sqlite",
		"app.db":                                "sqlite",
		"/var/lib/app/state.db":                 "sqlite",
		"app.db?_pragma=busy_timeout(5000)":     "sqlite",
		"/data/app.sqlite":                      "sqlite",
		"state.sqlite3?_pragma=foreign_keys(1)": "sqlite",
		":memory:":                              "sqlite",
		":memory:?_pragma=foreign_keys(1)":      "sqlite",
		"/srv/a://b/app.db":                     "sqlite",
		"C://data/app.db":                       "sqlite",
	} {
		d, err := FromDSN(dsn)
		require.NoError(t, err, dsn)
		assert.Equal(t, want, d.Name(), dsn)
	}
}

func TestFromDSNRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		dsn  string
		want string
	}{
		{"cockroach", "cockroach://root:secret@h:26257/db", `parse: unsupported DSN: scheme "cockroach": CockroachDB is not supported`},
		{"crdb", "crdb://root:secret@h/db", `parse: unsupported DSN: scheme "crdb": CockroachDB is not supported`},
		{"crdb with a .db database", "crdb://h/app.db", `parse: unsupported DSN: scheme "crdb": CockroachDB is not supported`},
		{"crdb in capitals", "CRDB://h/db", `parse: unsupported DSN: scheme "CRDB": CockroachDB is not supported`},
		{"another database", "mysql://u:secret@h/db", `parse: unsupported DSN: scheme "mysql": ` + supportedDSNs},
		{"another scheme ending in .db", "mysql://h/app.db", `parse: unsupported DSN: scheme "mysql": ` + supportedDSNs},
		{"misspelled postgres", "postgress://u:secret@h/db", `parse: unsupported DSN: scheme "postgress": ` + supportedDSNs},
		{"not a scheme before ://", "u:secret@h://db", "parse: unsupported DSN: " + supportedDSNs},
		{"keyword/value", "host=h user=u password=secret dbname=db", "parse: unsupported DSN: " + supportedDSNs},
		{"another extension", "app.dbx", "parse: unsupported DSN: " + supportedDSNs},
		{"empty", "", "parse: unsupported DSN: " + supportedDSNs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := FromDSN(tc.dsn)
			require.ErrorIs(t, err, ErrUnknownDSN)
			assert.Nil(t, d)
			assert.Equal(t, tc.want, err.Error())
		})
	}
}
