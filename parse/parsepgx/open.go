package parsepgx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// Config sets up the PostgreSQL pool Open opens. Its tags are
// github.com/ashtonian/pkg/config's: loaded through config, a zero setting
// takes its default. The DSN has none and no validate tag, so an
// application that links this provider beside another loads without one
// when configuration chooses the other; Open refuses it empty. The pool's
// limits are these settings, so the DSN sets no pool_* parameter. Open
// checks every setting whichever way the Config was built.
type Config struct {
	DSN                  string        `koanf:"dsn" sensitive:"true" desc:"PostgreSQL connection string: a postgres:// URL or a keyword/value string; required when the application runs on PostgreSQL"`
	SettleTimeout        time.Duration `koanf:"settle_timeout" default:"10s" validate:"gt=0" desc:"Bound on each statement that settles an update's transaction (commit, rollback, savepoint release)"`
	Collation            string        `koanf:"collation" default:"pg_c_utf8" validate:"oneof=pg_c_utf8 C" desc:"Code-point collation string sorts and ranges compile under: pg_c_utf8 (PostgreSQL 17+) or C (PostgreSQL 14 to 16)"`
	ContainmentMaxValues int           `koanf:"containment_max_values" default:"0" desc:"Largest $in set a jsonb path compares by containment; 0 is filter.DefaultContainmentMaxValues, negative compares none"`
	MaxConns             int32         `koanf:"max_conns" default:"10" validate:"min=1" desc:"Most connections the pool opens"`
	MinConns             int32         `koanf:"min_conns" default:"0" validate:"min=0" desc:"Connections the pool keeps open while idle; at most max_conns"`
	MaxConnLifetime      time.Duration `koanf:"max_conn_lifetime" default:"1h" validate:"gt=0" desc:"Age at which the pool closes a connection once it is released"`
	MaxConnIdleTime      time.Duration `koanf:"max_conn_idle_time" default:"30m" validate:"gt=0" desc:"Idle time after which the pool closes a connection"`
	HealthCheckPeriod    time.Duration `koanf:"health_check_period" default:"1m" validate:"gt=0" desc:"How often the pool checks idle connections and closes expired ones"`
	ConnectTimeout       time.Duration `koanf:"connect_timeout" default:"5s" validate:"gt=0" desc:"Bound on opening one connection, its TLS and authentication included"`
}

// Database is the provider Open opened and the pool under it.
type Database struct {
	provider *parse.Provider
	pool     *pgxpool.Pool
}

// Provider is the provider to build resources on (parse.NewResource).
func (d *Database) Provider() *parse.Provider { return d.provider }

// Pool is the pool, for the application's own SQL on it: its schema, its
// own statements, a transaction it shares with parse (Tx).
func (d *Database) Pool() *pgxpool.Pool { return d.pool }

// Ping checks that the database answers, for a readiness probe.
func (d *Database) Ping(ctx context.Context) error {
	err := d.pool.Ping(ctx)
	if err != nil {
		return fmt.Errorf("parsepgx: pinging the database: %w", err)
	}
	return nil
}

// Close closes the pool once the work on it is done; it waits for every
// connection in use to be released.
func (d *Database) Close() error {
	d.pool.Close()
	return nil
}

// Choice is the PostgreSQL provider opened from cfg, for parseprovider.Open
// to choose among the providers a binary links.
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

// Open opens a pool on the database cfg.DSN names, with the limits cfg
// sets, and the provider on it (New) under ctx. It refuses, naming the
// setting, an empty DSN, one of another database or one pgx does not read,
// a DSN that sets the pool's limits, and a setting out of its range. Errors
// never quote the DSN.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	collation, err := collationNamed(cfg.Collation)
	if err != nil {
		return nil, err
	}
	poolConfig, err := cfg.pool()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("parsepgx: opening the pool: %w", err)
	}
	p, err := New(ctx, pool,
		Collation(collation),
		ContainmentMaxValues(cfg.ContainmentMaxValues),
		SettleTimeout(cfg.SettleTimeout))
	if err != nil {
		pool.Close()
		return nil, err
	}
	opened := &Database{provider: p, pool: pool}
	return opened, nil
}

// collationNamed is the collation Config.Collation names.
func collationNamed(name string) (filter.PostgresCollation, error) {
	switch name {
	case "pg_c_utf8":
		return filter.CollationPgCUTF8, nil
	case "C":
		return filter.CollationC, nil
	}
	return 0, fmt.Errorf("parsepgx: collation: %q: want pg_c_utf8 or C", name)
}

// pool is the pool configuration for cfg.DSN with cfg's limits. A DSN pgx
// cannot read is refused without pgx's error, which quotes it.
func (cfg Config) pool() (*pgxpool.Config, error) {
	switch {
	case cfg.DSN == "":
		return nil, errors.New("parsepgx: dsn: empty")
	case cfg.SettleTimeout <= 0:
		return nil, fmt.Errorf("parsepgx: settle_timeout: %s: must be positive", cfg.SettleTimeout)
	case cfg.MaxConns < 1:
		return nil, fmt.Errorf("parsepgx: max_conns: %d: must be at least 1", cfg.MaxConns)
	case cfg.MinConns < 0 || cfg.MinConns > cfg.MaxConns:
		return nil, fmt.Errorf("parsepgx: min_conns: %d: must be from 0 to max_conns (%d)", cfg.MinConns, cfg.MaxConns)
	case cfg.MaxConnLifetime <= 0:
		return nil, fmt.Errorf("parsepgx: max_conn_lifetime: %s: must be positive", cfg.MaxConnLifetime)
	case cfg.MaxConnIdleTime <= 0:
		return nil, fmt.Errorf("parsepgx: max_conn_idle_time: %s: must be positive", cfg.MaxConnIdleTime)
	case cfg.HealthCheckPeriod <= 0:
		return nil, fmt.Errorf("parsepgx: health_check_period: %s: must be positive", cfg.HealthCheckPeriod)
	case cfg.ConnectTimeout <= 0:
		return nil, fmt.Errorf("parsepgx: connect_timeout: %s: must be positive", cfg.ConnectTimeout)
	}
	kind, kindErr := dialect.KindOfDSN(cfg.DSN)
	if kindErr == nil && kind != dialect.KindPostgres {
		return nil, fmt.Errorf("parsepgx: dsn: a %s connection string", kind)
	}
	// A URL of another scheme is refused as KindOfDSN names it; pgx reads
	// the keyword/value form, which KindOfDSN does not judge.
	_, isURL := dialect.URLScheme(cfg.DSN)
	if isURL && kindErr != nil {
		return nil, fmt.Errorf("parsepgx: dsn: %w", kindErr)
	}
	connConfig, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("parsepgx: dsn: not a PostgreSQL connection string pgx reads")
	}
	for param := range connConfig.RuntimeParams {
		if strings.HasPrefix(param, "pool_") {
			return nil, fmt.Errorf("parsepgx: dsn: %s: the pool is set by the max_conns, min_conns and other pool settings", param)
		}
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.New("parsepgx: dsn: not a PostgreSQL connection string pgx reads")
	}
	poolConfig.MaxConns = cfg.MaxConns
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConnLifetime = cfg.MaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolConfig.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolConfig.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	return poolConfig, nil
}
