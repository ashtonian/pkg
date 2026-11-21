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

// ErrUnknownDSN is returned by FromDSN for a DSN that names no database
// parse compiles SQL for.
var ErrUnknownDSN = errors.New("parse: unsupported DSN")

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

const supportedDSNs = "want postgres://, postgresql://, sqlite://, file:, a .db path or :memory:"

// FromDSN is the dialect of a connection string: postgres:// and
// postgresql:// are PostgreSQL; sqlite://, file:, a path ending in .db and
// :memory: are SQLite. CockroachDB's cockroach:// and crdb:// are refused:
// the PostgreSQL dialect's SQL has never been verified there.
//
// Errors name the scheme and never the DSN, which may carry a password.
func FromDSN(dsn string) (Dialect, error) {
	if scheme, _, ok := strings.Cut(dsn, "://"); ok {
		switch scheme {
		case "postgres", "postgresql":
			return Postgres{}, nil
		case "sqlite", "file":
			return SQLite{}, nil
		case "cockroach", "crdb":
			return nil, fmt.Errorf("%w: scheme %q: CockroachDB is not supported", ErrUnknownDSN, scheme)
		}
		if !isURIScheme(scheme) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownDSN, supportedDSNs)
		}
		return nil, fmt.Errorf("%w: scheme %q: %s", ErrUnknownDSN, scheme, supportedDSNs)
	}
	if strings.HasPrefix(dsn, "file:") || strings.HasSuffix(dsn, ".db") || dsn == ":memory:" {
		return SQLite{}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownDSN, supportedDSNs)
}

// isURIScheme reports whether s is an RFC 3986 scheme, so an error can
// quote it without quoting part of a malformed DSN's credentials.
func isURIScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// Postgres is PostgreSQL: $n placeholders.
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
