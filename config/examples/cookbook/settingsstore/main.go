// Command settingsstore keeps the settings an admin API writes in a table:
// one row per key, spelled as config.CanonicalKey spells it, holding a JSON
// value. The service loads the table over config.yaml and the environment
// through config.WithOverrideEntries. A write is checked by the load that
// will read it: the table the write would leave is loaded with the
// service's own options, and only a table that loads is committed, so a
// reload never meets a table the writer accepted.
//
//	PUT /admin/settings {"limits.max": 200, "upstreams[\"api.internal\"].timeout": "2s", "routes": null}
//
// A key's write replaces the rows stored beneath it; a key set to null is
// deleted, with the rows beneath it.
//
//	settingsstore migrate   # once per release, with a role that may create tables
//	settingsstore           # each replica: serves, running no DDL
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver

	"github.com/ashtonian/pkg/config"
)

// Schema creates the tables, as `settingsstore migrate` applies it:
// settings holds a row per key, and settings_lock the one row every write
// updates first, so that writes from every replica take turns.
const Schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings_lock (id INTEGER PRIMARY KEY, writes BIGINT NOT NULL);
INSERT INTO settings_lock (id, writes) VALUES (1, 0) ON CONFLICT (id) DO NOTHING;
`

// BootConfig is what the process needs before it can read the table.
type BootConfig struct {
	DatabaseURL       string        `koanf:"database_url" validate:"required" sensitive:"true"`
	ConfigFile        string        `koanf:"config_file" default:"config.yaml"`
	Addr              string        `koanf:"addr" default:":8080"`
	LogLevel          string        `koanf:"log_level" default:"warn" validate:"oneof=debug info warn error"`
	MaxOpenConns      int           `koanf:"max_open_conns" default:"4" validate:"min=1"`
	MaxIdleConns      int           `koanf:"max_idle_conns" default:"2" validate:"min=0,ltefield=MaxOpenConns"`
	ConnMaxIdleTime   time.Duration `koanf:"conn_max_idle_time" default:"5m" validate:"min=1s"`
	ConnMaxLifetime   time.Duration `koanf:"conn_max_lifetime" default:"30m" validate:"min=1s"`
	QueryTimeout      time.Duration `koanf:"query_timeout" default:"5s" validate:"min=1ms"`
	PollInterval      time.Duration `koanf:"poll_interval" default:"30s" validate:"min=1s"`
	MaxRows           int           `koanf:"max_rows" default:"1000" validate:"min=1"`
	MaxBodyBytes      int64         `koanf:"max_body_bytes" default:"1048576" validate:"min=1"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"min=1ms"`
	ReadTimeout       time.Duration `koanf:"read_timeout" default:"10s" validate:"min=1ms"`
	WriteTimeout      time.Duration `koanf:"write_timeout" default:"15s" validate:"gtfield=QueryTimeout"`
	IdleTimeout       time.Duration `koanf:"idle_timeout" default:"60s" validate:"min=1s"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout" default:"10s" validate:"min=1s"`
}

type Limits struct {
	Min   int   `koanf:"min" default:"1" validate:"min=1"`
	Max   int   `koanf:"max" alias:"ceiling" default:"100"`
	Bytes int64 `koanf:"bytes" default:"1048576" validate:"min=1"`
}

type Upstream struct {
	URL     string        `koanf:"url" validate:"required,url"`
	Timeout time.Duration `koanf:"timeout" default:"1s" validate:"min=1ms"`
}

type Route struct {
	Path     string `koanf:"path" validate:"required,startswith=/"`
	Upstream string `koanf:"upstream" validate:"required"`
}

type AppConfig struct {
	Limits    Limits              `koanf:"limits"`
	Upstreams map[string]Upstream `koanf:"upstreams" validate:"dive"`
	Routes    []Route             `koanf:"routes" validate:"dive"`
}

// Validate relates settings no single key can: a write that moves both
// limits is judged with both moved.
func (c *AppConfig) Validate() error {
	if c.Limits.Min > c.Limits.Max {
		return errors.New("limits.min exceeds limits.max")
	}
	for i, r := range c.Routes {
		if _, ok := c.Upstreams[r.Upstream]; !ok {
			return fmt.Errorf("routes[%d] names no upstream", i)
		}
	}
	return nil
}

// Entries reads a settings table for config.WithOverrideEntries.
type Entries = func(ctx context.Context) (map[string]any, error)

// Sources are how the service loads AppConfig: Options builds the options,
// the table read through entries, and Beneath names the sources beneath the
// table, for an operator to check when the settings fail to load.
type Sources struct {
	Options func(entries Entries) []config.Option
	Beneath string
}

// appSources layers the table over the file and APP_* variables.
func appSources(file string, logger *slog.Logger) Sources {
	return Sources{
		Options: func(entries Entries) []config.Option {
			return []config.Option{
				config.WithFile(file),
				config.WithEnvPrefix("APP"),
				config.WithOverrideEntries(entries),
				config.WithLogger(logger),
			}
		},
		Beneath: file + " and the APP_* variables",
	}
}

// Reasons a write is refused.
const (
	ReasonUnknown  = "unknown"  // a key names nothing the service reads
	ReasonRepeated = "repeated" // one key is spelled two ways in one write
	ReasonInvalid  = "invalid"  // the settings the write leaves do not load
	ReasonFull     = "full"     // the table would hold more rows than its cap
)

// ErrBusy is a write that timed out waiting for its turn: behind another
// write of this replica's, for a connection, or for the lock row another
// replica's write holds. It is contention, not a failure; the client
// retries.
var ErrBusy = errors.New("another settings write holds the lock")

// RefusedError is a write the service refuses, and the write's keys at
// fault. It names no value and no key the write did not name, so it is
// safe to return to the client; Err is the load's cause, for the log.
type RefusedError struct {
	Reason string   `json:"reason"`
	Keys   []string `json:"keys"`
	Err    error    `json:"-"`
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("settings write refused (%s): %s", e.Reason, strings.Join(e.Keys, ", "))
}

func (e *RefusedError) Unwrap() error { return e.Err }

// Settings is the settings table. Its methods are safe for concurrent use.
type Settings struct {
	db      *sql.DB
	sources Sources
	timeout time.Duration
	maxRows int
	// writing admits one of this replica's writes at a time, so writers
	// waiting for the lock row hold no pooled connection the reloader needs.
	writing chan struct{}
}

// NewSettings returns the table in db, loaded with sources' options. Each
// read, and each write with its wait for its turn, takes at most timeout,
// and the table holds at most maxRows rows.
func NewSettings(db *sql.DB, sources Sources, timeout time.Duration, maxRows int) *Settings {
	return &Settings{db: db, sources: sources, timeout: timeout, maxRows: maxRows, writing: make(chan struct{}, 1)}
}

// Options are the options the service loads AppConfig with, the table read
// on every load. Write checks a write with the same options, the table
// replaced by the one the write would leave.
func (s *Settings) Options() []config.Option {
	return s.sources.Options(s.read)
}

func (s *Settings) read(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return readRows(ctx, s.db, s.maxRows)
}

// Write applies edits to the table, all or nothing, and returns the keys it
// wrote: each key, spelled canonically, set to its value and replacing the
// rows stored beneath it, or, set to nil, deleted with them. Unless the
// table the write leaves loads with the service's options and fits the row
// cap, the write is refused with a *RefusedError and nothing changes.
func (s *Settings) Write(ctx context.Context, edits map[string]any) ([]string, error) {
	writes, err := canonical(edits)
	if err != nil || len(writes) == 0 {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case s.writing <- struct{}{}:
		defer func() { <-s.writing }()
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for this replica's other settings write: %w: %w", ErrBusy, context.Cause(ctx))
	}
	// At READ COMMITTED each statement reads what committed before it, so a
	// write that waited for the lock row reads the table the write before it
	// left; at REPEATABLE READ or SERIALIZABLE, a server default some
	// deployments set, Postgres refuses the waiting write's update instead.
	// SQLite's driver ignores the level: its transactions are serializable,
	// and updating the lock row takes the database's write lock.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("beginning a settings write: %w", busy(ctx, err))
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op
	// Updating the lock row is the write's first statement: the database
	// holds the row until the write ends, so writes from every replica take
	// turns, and each reads the table the one before it left.
	locked, err := tx.ExecContext(ctx, "UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", busy(ctx, err))
	}
	n, err := locked.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", err)
	}
	if n != 1 {
		return nil, errors.New("taking the settings lock: settings_lock has no row 1; run `settingsstore migrate`")
	}
	stored, err := readRows(ctx, tx, s.maxRows)
	if err != nil {
		return nil, err
	}
	keys := slices.Sorted(maps.Keys(writes))
	table, deleted := apply(stored, writes)
	if len(table) > s.maxRows {
		return nil, &RefusedError{Reason: ReasonFull, Keys: keys}
	}
	if err := s.check(ctx, stored, table, keys); err != nil {
		return nil, err
	}
	for _, key := range deleted {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", key); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", key, err)
		}
	}
	for _, key := range keys {
		if writes[key] == nil {
			continue
		}
		text, err := json.Marshal(writes[key])
		if err != nil {
			return nil, fmt.Errorf("encoding the value of %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, string(text)); err != nil {
			return nil, fmt.Errorf("storing %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing a settings write: %w", err)
	}
	return keys, nil
}

// busy is err, from waiting for a connection or the lock row, as ErrBusy
// when the write's time ran out while it waited.
func busy(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}

// retryAfter is how long a client whose write met contention waits before
// it retries: the most one write takes, its wait for its turn included.
func (s *Settings) retryAfter() time.Duration {
	return s.timeout
}

// check loads table, the one a write of keys leaves, with the service's
// options. A table that does not load refuses the write, unless the
// settings do not load without the write either: then the fault lies in a
// source beneath it or in a row already stored, and no client can fix it by
// retrying.
func (s *Settings) check(ctx context.Context, stored, table map[string]any, keys []string) error {
	_, err := config.Load[AppConfig](ctx, s.sources.Options(fixed(table))...)
	if err == nil || !errors.Is(err, config.ErrValidation) && !errors.Is(err, config.ErrKey) {
		return err
	}
	if _, storedErr := config.Load[AppConfig](ctx, s.sources.Options(fixed(stored))...); storedErr != nil {
		return fmt.Errorf("the settings fail to load without this write; check %s and the settings table: %w", s.sources.Beneath, storedErr)
	}
	reason := ReasonInvalid
	if unknown, ok := errors.AsType[*config.UnknownKeysError](err); ok && len(unknown.Keys) > 0 {
		reason = ReasonUnknown // a key inside a value the write sets
	}
	return &RefusedError{Reason: reason, Keys: keys, Err: err}
}

func fixed(table map[string]any) Entries {
	return func(context.Context) (map[string]any, error) { return table, nil }
}

// canonical is edits keyed as CanonicalKey spells their keys. A key naming
// nothing the service reads, or one key spelled two ways, refuses the
// write.
func canonical(edits map[string]any) (map[string]any, error) {
	writes := make(map[string]any, len(edits))
	var unknown, repeated []string
	for key, value := range edits {
		c, err := config.CanonicalKey[AppConfig](key)
		if errors.Is(err, config.ErrKey) {
			unknown = append(unknown, key)
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, ok := writes[c]; ok {
			repeated = append(repeated, c)
		}
		writes[c] = value
	}
	switch {
	case len(unknown) > 0:
		slices.Sort(unknown)
		return nil, &RefusedError{Reason: ReasonUnknown, Keys: unknown}
	case len(repeated) > 0:
		slices.Sort(repeated)
		return nil, &RefusedError{Reason: ReasonRepeated, Keys: slices.Compact(repeated)}
	}
	return writes, nil
}

// apply is the table stored makes with writes applied, and the stored keys
// the write deletes: each one within a key written, unless the write sets
// that key again.
func apply(stored, writes map[string]any) (table map[string]any, deleted []string) {
	written := make([]config.Key, 0, len(writes))
	for key := range writes {
		k, err := config.ParseKey(key)
		if err == nil {
			written = append(written, k)
		}
	}
	table = map[string]any{}
	for key, value := range stored {
		switch {
		case !covered(key, written):
			table[key] = value
		case writes[key] == nil: // not set again under this spelling, where the upsert would replace it
			deleted = append(deleted, key)
		}
	}
	for key, value := range writes {
		if value != nil {
			table[key] = value
		}
	}
	slices.Sort(deleted)
	return table, deleted
}

// covered reports whether a stored key is within one of written. A row
// some other writer stored under another spelling compares canonically.
func covered(key string, written []config.Key) bool {
	if c, err := config.CanonicalKey[AppConfig](key); err == nil {
		key = c
	}
	k, err := config.ParseKey(key)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(written, func(w config.Key) bool { return k.Within(w) })
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readRows reads the table, failing when it holds more than limit rows.
// Values decode with UseNumber, so an integer keeps every digit.
func readRows(ctx context.Context, q querier, limit int) (map[string]any, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM settings ORDER BY key LIMIT $1", limit+1)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	defer rows.Close()
	table := map[string]any{}
	for rows.Next() {
		if len(table) == limit {
			return nil, fmt.Errorf("reading settings: the table holds more than %d rows", limit)
		}
		var key, text string
		if err := rows.Scan(&key, &text); err != nil {
			return nil, fmt.Errorf("reading settings: %w", err)
		}
		value, err := decodeJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading the value of %s: %w", key, err)
		}
		table[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	return table, nil
}

// decodeJSON decodes the one JSON value r holds, numbers as json.Number.
func decodeJSON(r io.Reader) (any, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}

// settingsHandler is PUT /admin/settings: a JSON object of keys and values,
// written all or nothing, a key set to null deleted. Mount it behind the
// service's admin authentication. changed tells the reloader the table
// changed.
func settingsHandler(s *Settings, changed func(), maxBody int64, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := decodeJSON(http.MaxBytesReader(w, r.Body, maxBody))
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			reply(w, http.StatusRequestEntityTooLarge, map[string]string{"reason": "too large"})
			return
		}
		edits, isObject := body.(map[string]any)
		if err != nil || !isObject {
			reply(w, http.StatusBadRequest, map[string]string{"reason": "the body is not one JSON object"})
			return
		}
		written, err := s.Write(r.Context(), edits)
		refused, isRefused := errors.AsType[*RefusedError](err)
		switch {
		case err == nil:
			if len(written) > 0 {
				changed()
			}
			logger.InfoContext(r.Context(), "settings written", slog.Any("keys", written))
			w.WriteHeader(http.StatusNoContent)
		case isRefused:
			logger.InfoContext(r.Context(), "settings write refused",
				slog.String("reason", refused.Reason), slog.Any("keys", refused.Keys), slog.Any("error", refused.Err))
			reply(w, http.StatusUnprocessableEntity, refused)
		case r.Context().Err() != nil:
			logger.WarnContext(r.Context(), "settings write abandoned", slog.Any("error", err))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "unavailable"})
		case errors.Is(err, ErrBusy):
			logger.WarnContext(r.Context(), "settings write timed out waiting for its turn", slog.Any("error", err))
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(s.retryAfter().Seconds()))))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "busy"})
		default:
			logger.ErrorContext(r.Context(), "writing settings", slog.Any("error", err))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "unavailable"})
		}
	}
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body) // the client has gone when this fails
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	level := new(slog.LevelVar)
	level.Set(slog.LevelWarn)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	open := func(url string) (*sql.DB, error) { return sql.Open("pgx", url) }
	err := run(ctx, os.Args[1:], os.Environ, open, logger, level)
	stop()
	if err != nil {
		logger.Error("settings store", slog.Any("error", err))
		os.Exit(1)
	}
}

// run runs the command args name: none serves the admin API, and migrate
// applies Schema and exits. Serving runs no DDL, so replicas need no
// privilege to create tables and never race one another creating them.
func run(ctx context.Context, args []string, environ func() []string, open func(url string) (*sql.DB, error), logger *slog.Logger, level *slog.LevelVar) error {
	var command string
	switch {
	case len(args) == 0:
		command = "serve"
	case len(args) == 1 && args[0] == "migrate":
		command = "migrate"
	default:
		return fmt.Errorf("usage: settingsstore [migrate], not %q", args)
	}
	boot, err := config.Load[BootConfig](ctx, config.WithEnvPrefix("SETTINGS"), config.WithEnviron(environ), config.WithLogger(logger)) // SETTINGS_DATABASE_URL, ...
	if err != nil {
		return err
	}
	if err := level.UnmarshalText([]byte(boot.LogLevel)); err != nil {
		return err
	}
	if err := logStarting(ctx, logger, level, command, boot); err != nil {
		return err
	}

	db, err := open(boot.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(boot.MaxOpenConns)
	db.SetMaxIdleConns(boot.MaxIdleConns)
	db.SetConnMaxIdleTime(boot.ConnMaxIdleTime)
	db.SetConnMaxLifetime(boot.ConnMaxLifetime)
	if command == "migrate" {
		if err := migrate(ctx, db, boot.QueryTimeout); err != nil {
			return err
		}
		logger.InfoContext(ctx, "settings schema applied")
		return nil
	}
	if err := checkSchema(ctx, db, boot.QueryTimeout); err != nil {
		return err
	}
	return serve(ctx, db, boot, logger)
}

// migrate applies Schema, which creates only what is missing. Run it once
// per release, before the replicas start: Postgres can refuse concurrent
// CREATE TABLE IF NOT EXISTS of one table with a unique violation.
func migrate(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, Schema); err != nil {
		return fmt.Errorf("applying the settings schema: %w", err)
	}
	return nil
}

// checkSchema fails unless Schema has been applied: both tables, and the
// lock row every write updates.
func checkSchema(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var writes int64
	err := db.QueryRowContext(ctx, "SELECT writes FROM settings_lock WHERE id = 1").Scan(&writes)
	if err == nil {
		_, err = db.ExecContext(ctx, "SELECT 1 FROM settings WHERE 1 = 0")
	}
	if err != nil {
		return fmt.Errorf("checking the settings schema; run `settingsstore migrate` with a role that may create tables: %w", err)
	}
	return nil
}

// serve serves the admin API over the table in db until ctx ends.
func serve(ctx context.Context, db *sql.DB, boot *BootConfig, logger *slog.Logger) error {
	settings := NewSettings(db, appSources(boot.ConfigFile, logger), boot.QueryTimeout, boot.MaxRows)
	reloader, err := config.NewReloader[AppConfig](ctx, settings.Options(), config.WithPollInterval(boot.PollInterval))
	if err != nil {
		return err
	}
	go func() {
		if err := reloader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "config reloader ended", slog.Any("error", err))
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("PUT /admin/settings", settingsHandler(settings, reloader.SourceChanged, boot.MaxBodyBytes, logger))
	server := &http.Server{
		Addr:              boot.Addr,
		Handler:           mux,
		ReadHeaderTimeout: boot.ReadHeaderTimeout,
		ReadTimeout:       boot.ReadTimeout,
		WriteTimeout:      boot.WriteTimeout,
		IdleTimeout:       boot.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), boot.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), reloader.Stop(shutdownCtx))
}

// logStarting writes the one startup entry straight to the handler, so no
// level drops it: the build, the command, the level and the boot config,
// its database URL withheld.
func logStarting(ctx context.Context, logger *slog.Logger, level *slog.LevelVar, command string, boot *BootConfig) error {
	shown, err := config.RedactedMapChecked(boot)
	if err != nil {
		return err
	}
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		version = info.Main.Version
	}
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "starting", 0)
	record.AddAttrs(
		slog.String("service", "settingsstore"),
		slog.String("version", version),
		slog.String("command", command),
		slog.String("log_level", level.Level().String()),
		slog.Any("config", shown),
	)
	return logger.Handler().Handle(ctx, record)
}
