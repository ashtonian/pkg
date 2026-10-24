// Package dialect is the SQL dialect kind parse compiles for: the
// placeholder and identifier rules of PostgreSQL and SQLite. The filter
// and patch dialects embed it and add their SQL.
package dialect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnknownDSN is returned by FromDSN for a DSN whose scheme names no
// supported database.
var ErrUnknownDSN = errors.New("parse: unknown DSN scheme")

// Dialect is the dialect-specific part of a statement: its name, its
// placeholders and identifier quoting.
type Dialect interface {
	// Name is "postgres" or "sqlite".
	Name() string
	// Placeholder is the nth parameter's placeholder (1-based): "$1" on
	// PostgreSQL, "?1" on SQLite.
	Placeholder(n int) string
	// QuoteIdent quotes an identifier with double quotes, doubling any
	// quote inside it.
	QuoteIdent(name string) string
}

// FromDSN is the dialect of a connection string.
func FromDSN(dsn string) (Dialect, error) {
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"),
		strings.HasPrefix(dsn, "cockroach://"), strings.HasPrefix(dsn, "crdb://"):
		return Postgres{}, nil
	case strings.HasSuffix(dsn, ".db"), strings.HasPrefix(dsn, "file:"),
		strings.HasPrefix(dsn, "sqlite://"), dsn == ":memory:":
		return SQLite{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDSN, dsn)
	}
}

// Postgres is PostgreSQL (and CockroachDB): $n placeholders.
type Postgres struct{}

// Name returns "postgres".
func (Postgres) Name() string { return "postgres" }

// Placeholder returns "$n". A non-positive n is a statement builder's bug
// and panics: it would render invalid SQL.
func (Postgres) Placeholder(n int) string { return placeholder('$', n) }

// QuoteIdent quotes name per the SQL standard.
func (Postgres) QuoteIdent(name string) string { return quoteIdent(name) }

// SQLite is SQLite: ?n placeholders.
type SQLite struct{}

// Name returns "sqlite".
func (SQLite) Name() string { return "sqlite" }

// Placeholder returns "?n"; a non-positive n panics, as for Postgres.
func (SQLite) Placeholder(n int) string { return placeholder('?', n) }

// QuoteIdent quotes name per the SQL standard.
func (SQLite) QuoteIdent(name string) string { return quoteIdent(name) }

func placeholder(sigil byte, n int) string {
	if n < 1 {
		panic("parse: placeholder index must be >= 1, got " + strconv.Itoa(n))
	}
	return string(sigil) + strconv.Itoa(n)
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
