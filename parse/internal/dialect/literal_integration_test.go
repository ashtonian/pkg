//go:build integration

package dialect_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// literals hold what a string literal must escape somewhere: single quotes,
// which every literal doubles, and backslashes, which PostgreSQL reads as
// escapes in a plain literal when standard_conforming_strings is off.
var literals = []string{"", "plain", "it's ''", `back\slash`, `\`, `\'`, `a\'b''c\\`, `q"u\o'te`}

// TestQuoteLiteral_ReadsAsItself selects each literal as the database reads
// it: PostgreSQL whatever standard_conforming_strings is, so a label that
// a filter writes into a statement is its own whatever the session sets.
func TestQuoteLiteral_ReadsAsItself(t *testing.T) {
	selects := func(t *testing.T, conn *sql.Conn, quote func(string) string) {
		t.Helper()
		for _, s := range literals {
			var got string
			require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT "+quote(s)).Scan(&got), s)
			assert.Equal(t, s, got, quote(s))
		}
	}
	for _, setting := range []string{"on", "off"} {
		t.Run("postgres, standard_conforming_strings "+setting, func(t *testing.T) {
			conn, err := dbtest.Postgres(t).Conn(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			_, err = conn.ExecContext(t.Context(), "SET standard_conforming_strings = "+setting)
			require.NoError(t, err)
			selects(t, conn, pgLiteral)
		})
	}
	t.Run("sqlite", func(t *testing.T) {
		conn, err := dbtest.SQLite(t).Conn(t.Context())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		selects(t, conn, sqliteLiteral)
	})
}

var (
	pgLiteral     = dialect.Postgres{}.QuoteLiteral
	sqliteLiteral = dialect.SQLite{}.QuoteLiteral
)
