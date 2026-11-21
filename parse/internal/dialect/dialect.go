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

const (
	supportedDSNs       = "want a postgres:// or postgresql:// URL, a sqlite://, sqlite3:// or file: URI, a .db, .sqlite or .sqlite3 path, or :memory:"
	ambiguousSQLiteName = "a relative .db, .sqlite or .sqlite3 name holding =, @ or ( can be another driver's DSN; write a SQLite path as ./name, an absolute path or a file: URI"
)

// FromDSN is the dialect of a connection string, decided by its form alone.
// A postgres:// or postgresql:// URL is PostgreSQL. A sqlite://, sqlite3://
// or file: URI, :memory:, and a path ending in .db, .sqlite or .sqlite3
// before any ?query are SQLite. Schemes and extensions match
// case-insensitively; file: is lower case, as SQLite reads it.
//
// CockroachDB's cockroach:// and crdb:// are refused: the PostgreSQL
// dialect's SQL has never been verified there. A postgres:// URL to a
// CockroachDB cluster cannot be told apart and selects PostgreSQL. A
// keyword/value string (host=… dbname=…) is refused too: other drivers
// share its grammar, so it does not say which database it names. Construct
// the dialect for one instead.
//
// Other drivers' DSNs can end in .db (host=h dbname=orders.db,
// u:pw@tcp(h)/app.db, tcp(h)/app.db), so a relative name holding =, @ or (
// is refused whatever its extension. Neither a keyword/value string nor a
// MySQL DSN naming a host or credentials starts as an absolute or ./ path
// does, so such a path is SQLite whatever its directories hold.
//
// Errors name the scheme and never the DSN, which may carry a password.
func FromDSN(dsn string) (Dialect, error) {
	if scheme, _, ok := strings.Cut(dsn, "://"); ok && isURIScheme(scheme) {
		switch strings.ToLower(scheme) {
		case "postgres", "postgresql":
			return Postgres{}, nil
		case "sqlite", "sqlite3", "file":
			return SQLite{}, nil
		case "cockroach", "crdb":
			return nil, fmt.Errorf("%w: scheme %q: CockroachDB is not supported", ErrUnknownDSN, scheme)
		default:
			return nil, fmt.Errorf("%w: scheme %q: %s", ErrUnknownDSN, scheme, supportedDSNs)
		}
	}
	// A query, such as the modernc driver's ?_pragma=, follows the name. A
	// bare file: is SQLite's temporary database.
	name, _, _ := strings.Cut(dsn, "?")
	switch {
	case strings.HasPrefix(dsn, "file:"), name == ":memory:":
		return SQLite{}, nil
	case !hasSQLiteExtension(name):
		return nil, fmt.Errorf("%w: %s", ErrUnknownDSN, supportedDSNs)
	case isExplicitPath(name), !strings.ContainsAny(name, "=@("):
		return SQLite{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownDSN, ambiguousSQLiteName)
	}
}

// isURIScheme reports whether s is an RFC 3986 scheme of two or more
// characters. A single letter before :// is a Windows drive, and text that
// is no scheme at all is part of a path, or of a malformed DSN that an
// error must not quote.
func isURIScheme(s string) bool {
	if len(s) < 2 {
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

func hasSQLiteExtension(name string) bool {
	name = strings.ToLower(name)
	for _, ext := range []string{".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// isExplicitPath reports whether name is an absolute path (/…, C:\…, C:/…,
// \\server\…) or one relative to the working directory through ./ or ../.
func isExplicitPath(name string) bool {
	for _, prefix := range []string{"/", "./", "../", `\\`, `.\`, `..\`} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	if len(name) < 3 {
		return false
	}
	drive := name[0]
	return ('a' <= drive && drive <= 'z' || 'A' <= drive && drive <= 'Z') &&
		name[1] == ':' && (name[2] == '\\' || name[2] == '/')
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
