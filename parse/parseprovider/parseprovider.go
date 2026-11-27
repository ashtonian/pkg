// Package parseprovider opens the parse provider an application's
// configuration names: SQLite (parsesql) or PostgreSQL (parsepgx), chosen
// once at startup. Config is the setting block, tagged for
// github.com/ashtonian/pkg/config; Open checks it, opens the database with
// the connection limits it sets, and returns the provider with the database
// handle under it, which the application uses for its own SQL and closes at
// shutdown. The two providers are alternatives: nothing here moves data
// between them or runs one beside the other.
package parseprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/parsesqlite"
)

// Config selects the provider and sets up its database. Its tags are
// github.com/ashtonian/pkg/config's: loaded through config, a zero setting
// takes its default, and the defaults open an in-memory SQLite database.
// Open checks every setting whichever way the Config was built.
type Config struct {
	Provider      string         `koanf:"provider" default:"sqlite" validate:"oneof=sqlite postgres" desc:"Database parse runs on: sqlite or postgres"`
	DSN           string         `koanf:"dsn" default:":memory:" validate:"required" sensitive:"true" desc:"The provider's connection string: for sqlite a file path (absolute, ./ or ../, ending .db, .sqlite or .sqlite3), a file: URI or :memory:; for postgres a postgres:// URL or a keyword/value string"`
	SettleTimeout time.Duration  `koanf:"settle_timeout" default:"10s" validate:"gt=0" desc:"Bound on each statement that settles an update's transaction (commit, rollback, savepoint release)"`
	SQLite        SQLiteConfig   `koanf:"sqlite"`
	Postgres      PostgresConfig `koanf:"postgres"`
}

// SQLiteConfig sets up a SQLite database (provider sqlite). Open begins
// every transaction IMMEDIATE (_txlock=immediate), the write transaction
// an update that reads before it writes needs, and sets the busy timeout,
// so the DSN sets neither.
type SQLiteConfig struct {
	MaxOpenConns int           `koanf:"max_open_conns" default:"1" validate:"min=1" desc:"Connections the database holds open, all kept idle and never recycled; an in-memory database without cache=shared is one database per connection and needs 1"`
	BusyTimeout  time.Duration `koanf:"busy_timeout" default:"5s" validate:"gte=0" desc:"How long a statement waits for another connection's write lock before failing with SQLITE_BUSY"`
}

// PostgresConfig sets up a PostgreSQL pool (provider postgres). The pool's
// limits are these settings, so the DSN sets no pool_* parameter.
type PostgresConfig struct {
	Collation            string        `koanf:"collation" default:"pg_c_utf8" validate:"oneof=pg_c_utf8 C" desc:"Code-point collation string sorts and ranges compile under: pg_c_utf8 (PostgreSQL 17+) or C (PostgreSQL 14 to 16)"`
	ContainmentMaxValues int           `koanf:"containment_max_values" default:"0" desc:"Largest $in set a jsonb path compares by containment; 0 is filter.DefaultContainmentMaxValues, negative compares none"`
	MaxConns             int32         `koanf:"max_conns" default:"10" validate:"min=1" desc:"Most connections the pool opens"`
	MinConns             int32         `koanf:"min_conns" default:"0" validate:"min=0" desc:"Connections the pool keeps open while idle; at most max_conns"`
	MaxConnLifetime      time.Duration `koanf:"max_conn_lifetime" default:"1h" validate:"gt=0" desc:"Age at which the pool closes a connection once it is released"`
	MaxConnIdleTime      time.Duration `koanf:"max_conn_idle_time" default:"30m" validate:"gt=0" desc:"Idle time after which the pool closes a connection"`
	HealthCheckPeriod    time.Duration `koanf:"health_check_period" default:"1m" validate:"gt=0" desc:"How often the pool checks idle connections and closes expired ones"`
	ConnectTimeout       time.Duration `koanf:"connect_timeout" default:"5s" validate:"gt=0" desc:"Bound on opening one connection, its TLS and authentication included"`
}

// Database is an opened provider and the database it runs on. Exactly one
// of SQLite and Postgres holds the handle, per the provider's name.
type Database struct {
	provider *parse.Provider
	sqlite   *sql.DB
	pool     *pgxpool.Pool
}

// Provider is the provider to build resources on (parse.NewResource).
func (d *Database) Provider() *parse.Provider { return d.provider }

// SQLite is the database of provider sqlite, for the application's own SQL
// on it: its schema, its own statements, a transaction it shares with
// parse (parsesql.Tx).
func (d *Database) SQLite() (*sql.DB, bool) { return d.sqlite, d.sqlite != nil }

// Postgres is the pool of provider postgres, for the application's own SQL
// on it: its schema, its own statements, a transaction it shares with
// parse (parsepgx.Tx).
func (d *Database) Postgres() (*pgxpool.Pool, bool) { return d.pool, d.pool != nil }

// Ping checks that the database answers, for a readiness probe.
func (d *Database) Ping(ctx context.Context) error {
	var err error
	if d.pool != nil {
		err = d.pool.Ping(ctx)
	} else {
		err = d.sqlite.PingContext(ctx)
	}
	if err != nil {
		return fmt.Errorf("parseprovider: pinging %s: %w", d.provider.Name(), err)
	}
	return nil
}

// Close closes the database once the work on it is done. A pool's Close
// waits for every connection in use to be released.
func (d *Database) Close() error {
	if d.pool != nil {
		d.pool.Close()
		return nil
	}
	err := d.sqlite.Close()
	if err != nil {
		return fmt.Errorf("parseprovider: closing sqlite: %w", err)
	}
	return nil
}

// Open opens the provider cfg names on the database cfg.DSN names, under
// ctx: it connects, and the provider checks the database (parsesql.New,
// parsepgx.New). It refuses, naming the setting, a provider it does not
// know, a DSN of another database or of a form the provider's driver does
// not open, a DSN that sets what a setting owns, and a setting out of its
// range. Errors never quote the DSN.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	if cfg.DSN == "" {
		return nil, errors.New("parseprovider: dsn: empty")
	}
	if cfg.SettleTimeout <= 0 {
		return nil, fmt.Errorf("parseprovider: settle_timeout: %s: must be positive", cfg.SettleTimeout)
	}
	switch cfg.Provider {
	case parsesql.Name:
		return openSQLite(ctx, cfg)
	case parsepgx.Name:
		return openPostgres(ctx, cfg)
	}
	return nil, fmt.Errorf("parseprovider: provider: %q: want %s or %s", cfg.Provider, parsesql.Name, parsepgx.Name)
}

// ownedParam is a DSN parameter Open sets, and what sets it.
type ownedParam struct {
	name  string
	setBy string
}

// sqliteOwned are the DSN parameters Open sets, in the order it refuses
// them.
func sqliteOwned() []ownedParam {
	params := []ownedParam{
		{"_txlock", "every transaction begins IMMEDIATE"},
		{"_busy_timeout", "the sqlite.busy_timeout setting"},
		{"_timeout", "the sqlite.busy_timeout setting"},
	}
	return params
}

func openSQLite(ctx context.Context, cfg Config) (*Database, error) {
	c := cfg.SQLite
	if c.MaxOpenConns < 1 {
		return nil, fmt.Errorf("parseprovider: sqlite.max_open_conns: %d: must be at least 1", c.MaxOpenConns)
	}
	if c.BusyTimeout < 0 {
		return nil, fmt.Errorf("parseprovider: sqlite.busy_timeout: %s: must not be negative", c.BusyTimeout)
	}
	kind, err := dialect.KindOfDSN(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parseprovider: dsn: %w", err)
	}
	if kind != dialect.KindSQLite {
		return nil, fmt.Errorf("parseprovider: dsn: a %s connection string for provider sqlite", kind)
	}
	// The driver splits the name from its parameters at the first ?, and
	// opens anything but a file: URI as a file name.
	if strings.HasPrefix(cfg.DSN, `\\?\`) {
		return nil, errors.New(`parseprovider: dsn: an extended-length path (\\?\), which the SQLite driver splits at its ?; write the path without the prefix`)
	}
	name, query, _ := strings.Cut(cfg.DSN, "?")
	scheme, _, isURL := strings.Cut(name, "://")
	if isURL && (strings.EqualFold(scheme, "sqlite") || strings.EqualFold(scheme, "sqlite3")) {
		return nil, fmt.Errorf("parseprovider: dsn: a %s:// URL, which the SQLite driver opens as a file name; write the path or a file: URI", scheme)
	}
	params, err := url.ParseQuery(query)
	if err != nil {
		return nil, errors.New("parseprovider: dsn: its parameters are no URL query")
	}
	for _, owned := range sqliteOwned() {
		if params.Has(owned.name) {
			return nil, fmt.Errorf("parseprovider: dsn: %s: Open sets it (%s)", owned.name, owned.setBy)
		}
	}
	if c.MaxOpenConns > 1 && privateMemory(name, params) {
		return nil, fmt.Errorf("parseprovider: sqlite.max_open_conns: %d: the DSN names an in-memory database without cache=shared, a database per connection; keep 1", c.MaxOpenConns)
	}
	params.Set("_txlock", "immediate")
	params.Set("_busy_timeout", strconv.FormatInt(c.BusyTimeout.Milliseconds(), 10))
	err = parsesqlite.Register()
	if err != nil {
		return nil, fmt.Errorf("parseprovider: registering parse's SQLite functions: %w", err)
	}
	db, err := sql.Open("sqlite", name+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("parseprovider: opening sqlite: %w", err)
	}
	// SQLite's connections are in-process handles on a file, with no server
	// session or network path to refresh, and an in-memory database lives
	// only while a connection to it is open: every connection stays.
	db.SetMaxOpenConns(c.MaxOpenConns)
	db.SetMaxIdleConns(c.MaxOpenConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	p, err := parsesql.New(ctx, db, parsesql.SettleTimeout(cfg.SettleTimeout))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("parseprovider: %w", err), db.Close())
	}
	opened := &Database{provider: p, sqlite: db}
	return opened, nil
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

func openPostgres(ctx context.Context, cfg Config) (*Database, error) {
	c := cfg.Postgres
	collation, err := postgresCollation(c.Collation)
	if err != nil {
		return nil, err
	}
	poolConfig, err := postgresPool(cfg.DSN, c)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("parseprovider: opening postgres pool: %w", err)
	}
	p, err := parsepgx.New(ctx, pool,
		parsepgx.Collation(collation),
		parsepgx.ContainmentMaxValues(c.ContainmentMaxValues),
		parsepgx.SettleTimeout(cfg.SettleTimeout))
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("parseprovider: %w", err)
	}
	opened := &Database{provider: p, pool: pool}
	return opened, nil
}

func postgresCollation(name string) (filter.PostgresCollation, error) {
	switch name {
	case "pg_c_utf8":
		return filter.CollationPgCUTF8, nil
	case "C":
		return filter.CollationC, nil
	}
	return 0, fmt.Errorf("parseprovider: postgres.collation: %q: want pg_c_utf8 or C", name)
}

// postgresPool is the pool configuration for dsn with c's limits. A DSN
// pgx cannot read is refused without pgx's error, which quotes it.
func postgresPool(dsn string, c PostgresConfig) (*pgxpool.Config, error) {
	switch {
	case c.MaxConns < 1:
		return nil, fmt.Errorf("parseprovider: postgres.max_conns: %d: must be at least 1", c.MaxConns)
	case c.MinConns < 0 || c.MinConns > c.MaxConns:
		return nil, fmt.Errorf("parseprovider: postgres.min_conns: %d: must be from 0 to postgres.max_conns (%d)", c.MinConns, c.MaxConns)
	case c.MaxConnLifetime <= 0:
		return nil, fmt.Errorf("parseprovider: postgres.max_conn_lifetime: %s: must be positive", c.MaxConnLifetime)
	case c.MaxConnIdleTime <= 0:
		return nil, fmt.Errorf("parseprovider: postgres.max_conn_idle_time: %s: must be positive", c.MaxConnIdleTime)
	case c.HealthCheckPeriod <= 0:
		return nil, fmt.Errorf("parseprovider: postgres.health_check_period: %s: must be positive", c.HealthCheckPeriod)
	case c.ConnectTimeout <= 0:
		return nil, fmt.Errorf("parseprovider: postgres.connect_timeout: %s: must be positive", c.ConnectTimeout)
	}
	kind, kindErr := dialect.KindOfDSN(dsn)
	if kindErr == nil && kind != dialect.KindPostgres {
		return nil, fmt.Errorf("parseprovider: dsn: a %s connection string for provider postgres", kind)
	}
	// A URL of another scheme is refused as KindOfDSN names it; pgx reads
	// the keyword/value form, which KindOfDSN does not judge.
	_, isURL := dialect.URLScheme(dsn)
	if isURL && kindErr != nil {
		return nil, fmt.Errorf("parseprovider: dsn: %w", kindErr)
	}
	connConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("parseprovider: dsn: not a PostgreSQL connection string pgx reads")
	}
	for param := range connConfig.RuntimeParams {
		if strings.HasPrefix(param, "pool_") {
			return nil, fmt.Errorf("parseprovider: dsn: %s: the pool is set by the postgres.* settings", param)
		}
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("parseprovider: dsn: not a PostgreSQL connection string pgx reads")
	}
	poolConfig.MaxConns = c.MaxConns
	poolConfig.MinConns = c.MinConns
	poolConfig.MaxConnLifetime = c.MaxConnLifetime
	poolConfig.MaxConnIdleTime = c.MaxConnIdleTime
	poolConfig.HealthCheckPeriod = c.HealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = c.ConnectTimeout
	return poolConfig, nil
}
