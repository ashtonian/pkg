// Package dialect is the SQL dialect kind parse compiles for: the
// placeholder, identifier and literal rules of PostgreSQL and SQLite.
// filter's dialects embed a kind and add their SQL as methods, which
// filter.Dialect and the interfaces patch consumes name: SQL differs by
// database only there, and a dialect's Kind decides nothing but which type
// filter.DialectFromDSN builds.
//
// The set is closed. Dialect has an unexported method, so only this
// package's Postgres and SQLite, and the types embedding them, implement
// it. A new SQL provider is a kind here and a type in filter implementing
// filter.Dialect.
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

// Dialect is the dialect-specific part of a statement: its kind, its
// placeholders and identifier quoting.
type Dialect interface {
	// Name is the dialect's name for messages: "postgres" or "sqlite".
	// SQL is chosen by the dialect's methods, never by name.
	Name() string
	// Placeholder is the nth parameter's placeholder (1-based): "$1" on
	// PostgreSQL, "?1" on SQLite.
	Placeholder(n int) string
	// QuoteIdent quotes an identifier with double quotes, doubling any
	// quote inside it.
	QuoteIdent(name string) string
	// QuoteLiteral quotes s as a string literal that reads as s whatever
	// the session's settings, for text a statement writes rather than
	// binds: a JSON path's member names.
	QuoteLiteral(s string) string
	// MaxParams is the most parameters one statement binds.
	MaxParams() int

	kind() Kind
}

// Kind is a database parse compiles SQL for.
type Kind uint8

const (
	KindPostgres Kind = iota + 1
	KindSQLite
)

// KindOf is d's kind, which filter.DialectFromDSN maps to its type.
func KindOf(d Dialect) Kind { return d.kind() }

// String is the kind's dialect name.
func (k Kind) String() string {
	switch k {
	case KindPostgres:
		return "postgres"
	case KindSQLite:
		return "sqlite"
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Unsupported is the error of a switch on a kind that has no case for k: a
// bug, since every Dialect has one of the kinds the switch names.
func Unsupported(k Kind, what string) error {
	return fmt.Errorf("parse: %s has no form for dialect %s", what, k)
}

const supportedDSNs = "want a postgres:// or postgresql:// URL, a sqlite:// or sqlite3:// URL, a file: URI, :memory:, or an absolute, ./ or ../ path to a .db, .sqlite or .sqlite3 file"

// FromDSN is the dialect of a connection string, decided by its form alone.
// A postgres:// or postgresql:// URL is PostgreSQL. SQLite is a sqlite:// or
// sqlite3:// URL, a file: URI (lower case, as SQLite reads it), :memory:,
// or a path to a .db, .sqlite or .sqlite3 file, before any ?query, that is
// absolute (/…, C:\…, C:/…, \\server\…, \\?\C:\…) or relative through ./ or ../ (or
// .\ and ..\). Schemes and extensions match case-insensitively.
//
// Anything else is refused, because another driver's DSN takes its form:
//   - CockroachDB's cockroach:// and crdb://, where the PostgreSQL dialect's
//     SQL has never been verified (a postgres:// URL to a CockroachDB
//     cluster cannot be told apart and selects PostgreSQL);
//   - a bare name such as app.db or data/app.db, which a keyword/value
//     string (host=h dbname=app.db), a MySQL DSN (u:pw@tcp(h)/app.db) or an
//     Oracle easy connect (dbhost:1521/app.db) also ends as;
//   - //host/…, Oracle's easy connect, and a one-segment /name.db, which is
//     go-sql-driver/mysql's /dbname.
//
// The accepted forms are SQLite's own and filesystem paths, but any string
// is a SQLite file name, so a form is evidence, not proof: another driver's
// DSN that takes one, such as a MySQL user named file (file:pw@tcp(h)/db),
// selects SQLite, as a postgres:// URL to CockroachDB selects PostgreSQL.
//
// Construct the dialect for a refused DSN. Errors name the scheme and never
// the DSN, which may carry a password.
func FromDSN(dsn string) (Dialect, error) {
	// A bare file: is SQLite's temporary database.
	if strings.HasPrefix(dsn, "file:") {
		return SQLite{}, nil
	}
	if scheme, _, ok := strings.Cut(dsn, "://"); ok && isURIScheme(scheme) {
		switch strings.ToLower(scheme) {
		case "postgres", "postgresql":
			return Postgres{}, nil
		case "sqlite", "sqlite3":
			return SQLite{}, nil
		case "cockroach", "crdb":
			return nil, fmt.Errorf("%w: scheme %q: CockroachDB is not supported", ErrUnknownDSN, scheme)
		default:
			return nil, fmt.Errorf("%w: scheme %q: %s", ErrUnknownDSN, scheme, supportedDSNs)
		}
	}
	// A query, such as the modernc driver's ?_pragma=, follows the name; the
	// ? of a Windows extended-length prefix (\\?\C:\…) is part of it.
	start := 0
	if strings.HasPrefix(dsn, `\\?\`) {
		start = len(`\\?\`)
	}
	name := dsn
	if i := strings.IndexByte(dsn[start:], '?'); i >= 0 {
		name = dsn[:start+i]
	}
	if name == ":memory:" || isSQLitePath(name) {
		return SQLite{}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownDSN, supportedDSNs)
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

// isSQLitePath reports whether name is a path to a SQLite file in a form no
// other driver's DSN takes.
func isSQLitePath(name string) bool {
	if !hasSQLiteExtension(name) {
		return false
	}
	switch {
	case strings.HasPrefix(name, "//"):
		return false
	case strings.HasPrefix(name, "/"):
		return strings.Contains(name[1:], "/")
	}
	for _, prefix := range []string{"./", "../", `.\`, `..\`, `\\`} {
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

func hasSQLiteExtension(name string) bool {
	name = strings.ToLower(name)
	for _, ext := range []string{".db", ".sqlite", ".sqlite3"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// Postgres is PostgreSQL: $n placeholders.
type Postgres struct{}

// Name returns "postgres".
func (Postgres) Name() string { return KindPostgres.String() }

func (Postgres) kind() Kind { return KindPostgres }

// MaxParams is 65535: a Bind message counts its parameters in 16 bits.
func (Postgres) MaxParams() int { return 65535 }

// Placeholder returns "$n". A non-positive n is a statement builder's bug
// and panics: it would render invalid SQL.
func (Postgres) Placeholder(n int) string { return placeholder('$', n) }

// QuoteIdent quotes name per the SQL standard.
func (Postgres) QuoteIdent(name string) string { return quoteIdent(name) }

// SQLite is SQLite: ?n placeholders.
type SQLite struct{}

// Name returns "sqlite".
func (SQLite) Name() string { return KindSQLite.String() }

func (SQLite) kind() Kind { return KindSQLite }

// MaxParams is 999, SQLITE_MAX_VARIABLE_NUMBER before SQLite 3.32: a build
// may keep that limit, and a statement cannot ask which it has.
func (SQLite) MaxParams() int { return 999 }

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
