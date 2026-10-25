//go:build unit

package dialect

import (
	"errors"
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
		"postgres://h/db":   "postgres",
		"postgresql://h/db": "postgres",
		"cockroach://h/db":  "postgres",
		"crdb://h/db":       "postgres",
		"file:x.db":         "sqlite",
		"app.db":            "sqlite",
		"sqlite://app":      "sqlite",
		":memory:":          "sqlite",
	} {
		d, err := FromDSN(dsn)
		require.NoError(t, err, dsn)
		assert.Equal(t, want, d.Name(), dsn)
	}
	_, err := FromDSN("mysql://h/db")
	assert.True(t, errors.Is(err, ErrUnknownDSN))
}
