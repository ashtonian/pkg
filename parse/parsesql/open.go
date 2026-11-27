package parsesql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// Config sets up the SQLite database Open opens. Its tags are
// github.com/ashtonian/pkg/config's: loaded through config, a zero setting
// takes its default, and the defaults open an in-memory database. Open
// checks every setting whichever way the Config was built. Open begins every
// transaction IMMEDIATE (_txlock=immediate), the write transaction an update
// that reads before it writes needs, and sets the busy timeout, so the DSN
// sets neither.
type Config struct {
	DSN           string        `koanf:"dsn" default:":memory:" validate:"required" sensitive:"true" desc:"SQLite database: a file path (absolute, ./ or ../, ending .db, .sqlite or .sqlite3), a file: URI or :memory:"`
	MaxOpenConns  int           `koanf:"max_open_conns" default:"1" validate:"min=1" desc:"Connections the database holds open, all kept idle and never recycled; an in-memory database without cache=shared is one database per connection and needs 1"`
	BusyTimeout   time.Duration `koanf:"busy_timeout" default:"5s" validate:"gte=0" desc:"How long a statement waits for another connection's write lock before failing with SQLITE_BUSY"`
	SettleTimeout time.Duration `koanf:"settle_timeout" default:"10s" validate:"gt=0" desc:"Bound on each statement that settles an update in a transaction the application holds (savepoint release and rollback)"`
}

// Database is the provider Open opened and the SQLite database under it.
type Database struct {
	provider *parse.Provider
	db       *sql.DB
}

// Provider is the provider to build resources on (parse.NewResource).
func (d *Database) Provider() *parse.Provider { return d.provider }

// DB is the database, for the application's own SQL on it: its schema, its
// own statements, a transaction it shares with parse (Tx).
func (d *Database) DB() *sql.DB { return d.db }

// Ping checks that the database answers, for a readiness probe.
func (d *Database) Ping(ctx context.Context) error {
	err := d.db.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("parsesql: pinging the database: %w", err)
	}
	return nil
}

// Close closes the database once the work on it is done.
func (d *Database) Close() error {
	err := d.db.Close()
	if err != nil {
		return fmt.Errorf("parsesql: closing the database: %w", err)
	}
	return nil
}

// Choice is the SQLite provider opened from cfg, for parseprovider.Open to
// choose among the providers a binary links.
func Choice(cfg Config) parseprovider.Choice {
	choice := parseprovider.Choice{Name: Name, Open: func(ctx context.Context) (parseprovider.Database, error) {
		db, err := Open(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return db, nil
	}}
	return choice
}

// ownedParam is a DSN parameter Open sets, and what sets it.
type ownedParam struct {
	name  string
	setBy string
}

// ownedParams are the DSN parameters Open sets, in the order it refuses
// them.
func ownedParams() []ownedParam {
	params := []ownedParam{
		{"_txlock", "every transaction begins IMMEDIATE"},
		{"_busy_timeout", "the busy_timeout setting"},
		{"_timeout", "the busy_timeout setting"},
	}
	return params
}

// Open opens the SQLite database cfg.DSN names through modernc.org/sqlite,
// with parse's SQL functions registered (Register) and the connection
// limits cfg sets, and the provider on it (New) under ctx. It refuses,
// naming the setting, a DSN of another database or of a form the driver
// does not open, a DSN that sets what a setting owns, and a setting out of
// its range. Errors never quote the DSN.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	dsn, err := cfg.dsn()
	if err != nil {
		return nil, err
	}
	err = Register()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("parsesql: opening the database: %w", err)
	}
	// SQLite's connections are in-process handles on a file, with no server
	// session or network path to refresh, and an in-memory database lives
	// only while a connection to it is open: every connection stays.
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxOpenConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	p, err := New(ctx, db, SettleTimeout(cfg.SettleTimeout))
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	opened := &Database{provider: p, db: db}
	return opened, nil
}

// dsn checks cfg and returns the DSN the driver opens: cfg.DSN with the
// transaction mode and busy timeout Open sets.
func (cfg Config) dsn() (string, error) {
	switch {
	case cfg.DSN == "":
		return "", errors.New("parsesql: dsn: empty")
	case cfg.MaxOpenConns < 1:
		return "", fmt.Errorf("parsesql: max_open_conns: %d: must be at least 1", cfg.MaxOpenConns)
	case cfg.BusyTimeout < 0:
		return "", fmt.Errorf("parsesql: busy_timeout: %s: must not be negative", cfg.BusyTimeout)
	case cfg.SettleTimeout <= 0:
		return "", fmt.Errorf("parsesql: settle_timeout: %s: must be positive", cfg.SettleTimeout)
	}
	kind, err := dialect.KindOfDSN(cfg.DSN)
	if err != nil {
		return "", fmt.Errorf("parsesql: dsn: %w", err)
	}
	if kind != dialect.KindSQLite {
		return "", fmt.Errorf("parsesql: dsn: a %s connection string", kind)
	}
	// The driver splits the name from its parameters at the first ?, and
	// opens anything but a file: URI as a file name.
	if strings.HasPrefix(cfg.DSN, `\\?\`) {
		return "", errors.New(`parsesql: dsn: an extended-length path (\\?\), which the SQLite driver splits at its ?; write the path without the prefix`)
	}
	name, query, _ := strings.Cut(cfg.DSN, "?")
	scheme, _, isURL := strings.Cut(name, "://")
	if isURL && (strings.EqualFold(scheme, "sqlite") || strings.EqualFold(scheme, "sqlite3")) {
		return "", fmt.Errorf("parsesql: dsn: a %s:// URL, which the SQLite driver opens as a file name; write the path or a file: URI", scheme)
	}
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", errors.New("parsesql: dsn: its parameters are no URL query")
	}
	for _, owned := range ownedParams() {
		if params.Has(owned.name) {
			return "", fmt.Errorf("parsesql: dsn: %s: Open sets it (%s)", owned.name, owned.setBy)
		}
	}
	if cfg.MaxOpenConns > 1 && privateMemory(name, params) {
		return "", fmt.Errorf("parsesql: max_open_conns: %d: the DSN names an in-memory database without cache=shared, a database per connection; keep 1", cfg.MaxOpenConns)
	}
	params.Set("_txlock", "immediate")
	params.Set("_busy_timeout", strconv.FormatInt(cfg.BusyTimeout.Milliseconds(), 10))
	return name + "?" + params.Encode(), nil
}

// privateMemory reports whether a SQLite DSN, split into its name and its
// parameters, names an in-memory database each connection opens a database
// of its own for: :memory: or a file: URI with mode=memory, without
// cache=shared (SQLite's in-memory databases).
func privateMemory(name string, params url.Values) bool {
	memory := name == ":memory:" || name == "file::memory:" ||
		(strings.HasPrefix(name, "file:") && params.Get("mode") == "memory")
	return memory && params.Get("cache") != "shared"
}
