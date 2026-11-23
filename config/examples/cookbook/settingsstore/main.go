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
	MaxTableBytes     int64         `koanf:"max_table_bytes" default:"1048576" validate:"min=1"`
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
	ReasonFull     = "full"     // the table would grow past a cap
	ReasonEnclosed = "enclosed" // a key lies inside a list or a scalar a stored row sets
	ReasonOverlap  = "overlap"  // one write names a key and a key within it
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
	limits  TableLimits
	logger  *slog.Logger
	// writing admits one of this replica's writes at a time, so writers
	// waiting for the lock row hold no pooled connection the reloader needs.
	writing chan struct{}
}

// readSlack is how far past its caps a read accepts the table. A cap
// lowered beneath the table leaves the table readable, so loads go on and
// writes can bring it back under, while what a read holds stays bounded.
const readSlack = 2

// TableLimits are the table's caps. A write may not grow the table past
// either; a read fails past readSlack times either.
type TableLimits struct {
	Rows  int
	Bytes int64 // every row's key and its value's JSON text
}

func (l TableLimits) read() TableLimits {
	return TableLimits{Rows: readSlack * l.Rows, Bytes: readSlack * l.Bytes}
}

func (l TableLimits) over(size tableSize) bool {
	return size.rows > l.Rows || size.bytes > l.Bytes
}

// refuses reports whether a write that takes the table from before to after
// passes a cap and grows the table on it. A table over a cap, the cap
// lowered beneath it, takes the writes that do not grow it, so the API can
// bring it back under.
func (l TableLimits) refuses(before, after tableSize) bool {
	return after.rows > l.Rows && after.rows > before.rows ||
		after.bytes > l.Bytes && after.bytes > before.bytes
}

// tableSize is a table's size, as TableLimits bound it.
type tableSize struct {
	rows  int
	bytes int64
}

// NewSettings returns the table in db, loaded with sources' options. Each
// read, and each write with its wait for its turn, takes at most timeout,
// and limits cap the table.
func NewSettings(db *sql.DB, sources Sources, timeout time.Duration, limits TableLimits, logger *slog.Logger) *Settings {
	return &Settings{db: db, sources: sources, timeout: timeout, limits: limits, logger: logger, writing: make(chan struct{}, 1)}
}

// Options are the options the service loads AppConfig with, the table read
// on every load. Write checks a write with the same options, the table
// replaced by the one the write would leave.
func (s *Settings) Options() []config.Option {
	return s.sources.Options(s.read)
}

func (s *Settings) read(ctx context.Context) (map[string]any, error) {
	readCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	stored, err := readRows(readCtx, s.db, s.limits.read())
	if err != nil {
		return nil, err
	}
	if size := stored.size(); s.limits.over(size) {
		s.logger.WarnContext(ctx, "settings table over its cap",
			slog.Int("rows", size.rows), slog.Int("max_rows", s.limits.Rows),
			slog.Int64("bytes", size.bytes), slog.Int64("max_table_bytes", s.limits.Bytes),
			slog.String("remedy", "delete rows through the API, or raise SETTINGS_MAX_ROWS or SETTINGS_MAX_TABLE_BYTES"))
	}
	table, left := readable(stored.values)
	if len(left) > 0 {
		s.logger.WarnContext(ctx, "settings the schema does not read left out of the load",
			slog.Any("keys", left), slog.String("remedy", "write each key as null, or write the key of the row holding it whole"))
	}
	return table, nil
}

// Write applies edits to the table, all or nothing, and returns the keys it
// wrote: each key, spelled canonically, set to its value and replacing the
// rows stored beneath it, or, set to nil, deleted with them, and in either
// case cut out of the values of the rows stored above it. A key the
// schema does not read may be set only to nil, to delete the rows a release
// that removed its field left. Unless the table the write leaves loads with
// the service's options, and fits the caps or is no larger than before,
// the write is refused with a *RefusedError and nothing changes.
func (s *Settings) Write(ctx context.Context, edits map[string]any) ([]string, error) {
	writes, unread, err := canonical(edits)
	if err != nil || len(writes) == 0 && len(unread) == 0 {
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
	stored, err := readRows(ctx, tx, s.limits.read())
	if err != nil {
		return nil, err
	}
	if missing := unstored(unread, stored.values); len(missing) > 0 {
		return nil, &RefusedError{Reason: ReasonUnknown, Keys: missing}
	}
	keys := append(slices.Collect(maps.Keys(writes)), unread...)
	slices.Sort(keys)
	c, err := apply(stored.values, writes, unread)
	if err != nil {
		return nil, err
	}
	texts, err := encode(writes, c.rewritten)
	if err != nil {
		return nil, err
	}
	after := tableSize{rows: len(c.table)}
	for key := range c.table {
		if text, ok := texts[key]; ok {
			after.bytes += int64(len(key) + len(text))
		} else {
			after.bytes += stored.sizes[key]
		}
	}
	if s.limits.refuses(stored.size(), after) {
		return nil, &RefusedError{Reason: ReasonFull, Keys: keys}
	}
	before, _ := readable(stored.values)
	if err := s.check(ctx, before, loadable(c.table, writes), keys); err != nil {
		return nil, err
	}
	for _, key := range c.deleted {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", key); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", key, err)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(texts)) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, texts[key]); err != nil {
			return nil, fmt.Errorf("storing %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing a settings write: %w", err)
	}
	return keys, nil
}

// encode is the JSON text of each row a write stores: each key it sets,
// and each stored row it rewrites.
func encode(writes, rewritten map[string]any) (map[string]string, error) {
	texts := make(map[string]string, len(writes)+len(rewritten))
	for _, rows := range []map[string]any{writes, rewritten} {
		for key, value := range rows {
			if value == nil {
				continue
			}
			text, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("encoding the value of %s: %w", key, err)
			}
			texts[key] = string(text)
		}
	}
	return texts, nil
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

// check loads after, the table loads will read once a write of keys
// commits, with the service's options. A table that does not load refuses
// the write, unless before, the table loads read now, does not load either:
// then the fault lies in a source beneath the table or in a row already
// stored, and no client can fix it by retrying.
func (s *Settings) check(ctx context.Context, before, after map[string]any, keys []string) error {
	_, err := config.Load[AppConfig](ctx, s.sources.Options(fixed(after))...)
	if err == nil || !errors.Is(err, config.ErrValidation) && !errors.Is(err, config.ErrKey) {
		return err
	}
	if _, storedErr := config.Load[AppConfig](ctx, s.sources.Options(fixed(before))...); storedErr != nil {
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

// readable is a stored table as loads read it. A release that removes a
// field leaves rows, and parts of rows' values, at keys the schema no
// longer reads; left out, they stop no load, and left names them.
func readable(stored map[string]any) (table map[string]any, left []string) {
	table = make(map[string]any, len(stored))
	for key, value := range stored {
		k, ok := schemaKey(key)
		if !ok {
			left = append(left, key)
			continue
		}
		table[key] = readableValue(k, value, &left)
	}
	slices.Sort(left)
	return table, left
}

// readableValue is value, stored at the key at, without its parts at keys
// the schema does not read, which it adds to left.
func readableValue(at config.Key, value any, left *[]string) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for name, child := range v {
			k, text, ok := childKey(at, name)
			if !ok {
				*left = append(*left, text)
				continue
			}
			out[name] = readableValue(k, child, left)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = readableValue(append(slices.Clip(at), config.Segment{Kind: config.SegmentIndex, Index: i}), child, left)
		}
		return out
	}
	return value
}

// loadable is table, the one a write of writes leaves, as loads will read
// it: what was stored as readable leaves it, and what the write sets as
// written, so a key the schema refuses inside a written value refuses the
// write.
func loadable(table, writes map[string]any) map[string]any {
	stored := maps.Clone(table)
	for key := range writes {
		delete(stored, key)
	}
	out, _ := readable(stored)
	for key, value := range writes {
		if value != nil {
			out[key] = value
		}
	}
	return out
}

// schemaKey is key parsed as CanonicalKey spells it, or false when the
// schema reads nothing at key.
func schemaKey(key string) (config.Key, bool) {
	c, err := config.CanonicalKey[AppConfig](key)
	if err != nil {
		return nil, false
	}
	k, err := config.ParseKey(c)
	return k, err == nil
}

// childKey is the key the schema reads the entry name of a value stored at
// at as: a field, or failing that a map key. text spells it either way, as
// a field where that parses back to the one entry.
func childKey(at config.Key, name string) (key config.Key, text string, ok bool) {
	field, fieldParses := childText(at, config.SegmentField, name)
	mapKey, _ := childText(at, config.SegmentKey, name)
	if fieldParses {
		if k, ok := schemaKey(field); ok {
			return k, field, true
		}
	}
	if k, ok := schemaKey(mapKey); ok {
		return k, mapKey, true
	}
	if fieldParses {
		return nil, field, false
	}
	return nil, mapKey, false
}

// childText spells the entry name beneath at as a segment of kind, and
// reports whether that spelling parses back to it: a field name holding a
// dot or a bracket would parse as more than one segment.
func childText(at config.Key, kind config.SegmentKind, name string) (string, bool) {
	text := append(slices.Clip(at), config.Segment{Kind: kind, Name: name}).String()
	k, err := config.ParseKey(text)
	return text, err == nil && len(k) == len(at)+1
}

// canonical is edits keyed as CanonicalKey spells their keys, and unread,
// the keys it refuses that edits set to nil, each a delete of the rows a
// release that removed its field left. Any other key naming nothing the
// service reads, one key spelled two ways, or a key and a key within it
// refuses the write.
func canonical(edits map[string]any) (writes map[string]any, unread []string, err error) {
	writes = make(map[string]any, len(edits))
	var unknown, repeated []string
	for key, value := range edits {
		c, err := config.CanonicalKey[AppConfig](key)
		if errors.Is(err, config.ErrKey) {
			if value == nil {
				unread = append(unread, key)
			} else {
				unknown = append(unknown, key)
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if _, ok := writes[c]; ok {
			repeated = append(repeated, c)
		}
		writes[c] = value
	}
	switch {
	case len(unknown) > 0:
		slices.Sort(unknown)
		return nil, nil, &RefusedError{Reason: ReasonUnknown, Keys: unknown}
	case len(repeated) > 0:
		slices.Sort(repeated)
		return nil, nil, &RefusedError{Reason: ReasonRepeated, Keys: slices.Compact(repeated)}
	}
	// Refused rather than folded together: which wins is the load's merge
	// to decide, and the client can send the parent with the child in it.
	if overlap := overlapping(writes); len(overlap) > 0 {
		return nil, nil, &RefusedError{Reason: ReasonOverlap, Keys: overlap}
	}
	slices.Sort(unread)
	return writes, unread, nil
}

// overlapping is the keys of writes within another of them, and the keys
// they are within. Each key's parents are looked up by their spelling, a
// field spelled as a map key, as Key.Within compares them, so the cost is
// the keys' segments, not their pairs.
func overlapping(writes map[string]any) []string {
	parsed := make(map[string]config.Key, len(writes))
	bySpelling := make(map[string]string, len(writes))
	for key := range writes {
		k, err := config.ParseKey(key)
		if err != nil {
			continue
		}
		parsed[key] = k
		bySpelling[fieldsAsMapKeys(k).String()] = key
	}
	var overlap []string
	for key, k := range parsed {
		for n := len(k) - 1; n > 0; n-- {
			if parent, ok := bySpelling[fieldsAsMapKeys(k[:n]).String()]; ok {
				overlap = append(overlap, key, parent)
			}
		}
	}
	slices.Sort(overlap)
	return slices.Compact(overlap)
}

// fieldsAsMapKeys is k with every field a map key of the same name.
func fieldsAsMapKeys(k config.Key) config.Key {
	out := slices.Clone(k)
	for i := range out {
		if out[i].Kind == config.SegmentField {
			out[i].Kind = config.SegmentKey
		}
	}
	return out
}

// unreadAt reports whether a stored key the schema does not read is at or
// beneath unread, a key written null that the schema does not read either.
// A row the schema reads is never deleted through a spelling it refuses.
func unreadAt(stored, unread string) bool {
	if _, err := config.CanonicalKey[AppConfig](stored); err == nil {
		return false
	}
	if stored == unread {
		return true
	}
	s, err := config.ParseKey(stored)
	if err != nil {
		return false
	}
	u, err := config.ParseKey(unread)
	return err == nil && s.Within(u)
}

// unstored is the keys of unread with no row stored at or beneath them for
// a null write to delete: keys naming nothing at all.
func unstored(unread []string, stored map[string]any) []string {
	var missing []string
	for _, u := range unread {
		found := false
		for key := range stored {
			if unreadAt(key, u) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, u)
		}
	}
	return missing
}

// change is what a write does to the stored rows.
type change struct {
	table     map[string]any // the rows the write leaves
	deleted   []string       // the stored keys it deletes
	rewritten map[string]any // stored rows above a key written, that key cut out of their values
}

// apply is the change writes and unread make to stored. A stored row within
// a key written is deleted, unless the write sets that key again, as is one
// the schema does not read at or beneath a key of unread. A stored row
// above a key written has that key cut out of its value, so the write takes
// effect beneath it, and is deleted when left setting nothing. A key beneath
// a list or a scalar a stored row sets cannot be cut out, and refuses the
// write.
func apply(stored, writes map[string]any, unread []string) (change, error) {
	keys := slices.Sorted(maps.Keys(writes))
	written := make([]config.Key, 0, len(keys))
	for _, key := range keys {
		if k, err := config.ParseKey(key); err == nil {
			written = append(written, k)
		}
	}
	c := change{table: map[string]any{}, rewritten: map[string]any{}}
	var enclosed []string
	var errs []error
	for key, value := range stored {
		switch {
		case slices.ContainsFunc(unread, func(u string) bool { return unreadAt(key, u) }):
			c.deleted = append(c.deleted, key)
		case covered(key, written):
			if writes[key] == nil { // not set again under this spelling, where the upsert replaces it
				c.deleted = append(c.deleted, key)
			}
		default:
			value, changed, refused := cutBeneath(key, value, written)
			if refused != nil {
				enclosed = append(enclosed, refused.Keys...)
				errs = append(errs, refused.Err)
				continue
			}
			if object, ok := value.(map[string]any); changed && ok && len(object) == 0 {
				c.deleted = append(c.deleted, key)
				continue
			}
			c.table[key] = value
			if changed {
				c.rewritten[key] = value
			}
		}
	}
	if len(enclosed) > 0 {
		slices.Sort(enclosed)
		return change{}, &RefusedError{Reason: ReasonEnclosed, Keys: slices.Compact(enclosed), Err: errors.Join(errs...)}
	}
	for key, value := range writes {
		if value != nil {
			c.table[key] = value
		}
	}
	slices.Sort(c.deleted)
	return c, nil
}

// cutBeneath is value, stored at key, with each key of written beneath key
// cut out of it, and whether that changed it. A key it cannot cut out
// refuses the write.
func cutBeneath(key string, value any, written []config.Key) (any, bool, *RefusedError) {
	at, ok := schemaKey(key)
	if !ok {
		return value, false, nil // loads leave the row out
	}
	changed := false
	var refused *RefusedError
	for _, w := range written {
		if len(w) <= len(at) || !w.Within(at) {
			continue
		}
		cut, c, err := cutAt(at, value, w)
		if err != nil {
			if refused == nil {
				refused = &RefusedError{Reason: ReasonEnclosed}
			}
			refused.Keys = append(refused.Keys, w.String())
			refused.Err = errors.Join(refused.Err, fmt.Errorf("the row %s: %w", key, err))
			continue
		}
		value, changed = cut, changed || c
	}
	return value, changed, refused
}

// cutAt is value, stored at the key at, without what it holds at target, a
// key beneath at, under any spelling the schema reads as target, and whether
// it held anything there. A list or a scalar on the way to target cannot be
// cut into.
func cutAt(at config.Key, value any, target config.Key) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		kind := "a scalar"
		if _, isList := value.([]any); isList {
			kind = "a list"
		}
		return nil, false, fmt.Errorf("%s holds %s, which %s cannot be cut out of", at, kind, target)
	}
	next := target[:len(at)+1]
	out := maps.Clone(object)
	changed := false
	for name, child := range object {
		k, _, ok := childKey(at, name)
		if !ok || len(k) != len(next) || !k.Within(next) {
			continue
		}
		if len(next) == len(target) {
			delete(out, name)
			changed = true
			continue
		}
		cut, c, err := cutAt(k, child, target)
		if err != nil {
			return nil, false, err
		}
		if c {
			out[name] = cut
			changed = true
		}
	}
	return out, changed, nil
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

// storedTable is the table as read: each row's value, and its size, the
// bytes of its key and its value's JSON text.
type storedTable struct {
	values map[string]any
	sizes  map[string]int64
}

func (t storedTable) size() tableSize {
	size := tableSize{rows: len(t.values)}
	for _, n := range t.sizes {
		size.bytes += n
	}
	return size
}

// readRows reads the table, failing when it holds more than limit's rows or
// bytes. The database leaves out a value that alone passes the byte limit,
// so what a read holds stays bounded whatever another writer stored. Values
// decode with UseNumber, so an integer keeps every digit.
func readRows(ctx context.Context, q querier, limit TableLimits) (storedTable, error) {
	rows, err := q.QueryContext(ctx, `
SELECT key, size, CASE WHEN size <= $2 THEN value END
FROM (SELECT key, value, CAST(octet_length(key) AS BIGINT) + octet_length(value) AS size FROM settings) AS sized
ORDER BY key LIMIT $1`, limit.Rows+1, limit.Bytes)
	if err != nil {
		return storedTable{}, fmt.Errorf("reading settings: %w", err)
	}
	defer rows.Close()
	table := storedTable{values: map[string]any{}, sizes: map[string]int64{}}
	var total int64
	for rows.Next() {
		if len(table.values) == limit.Rows {
			return storedTable{}, fmt.Errorf("reading settings: the table holds more than %d rows, %d times SETTINGS_MAX_ROWS; raise it to at least half the rows, delete rows through the API, then lower it", limit.Rows, readSlack)
		}
		var key string
		var size int64
		var text sql.NullString
		if err := rows.Scan(&key, &size, &text); err != nil {
			return storedTable{}, fmt.Errorf("reading settings: %w", err)
		}
		if total += size; total > limit.Bytes {
			return storedTable{}, fmt.Errorf("reading settings: the table holds more than %d bytes, %d times SETTINGS_MAX_TABLE_BYTES; raise it to at least half the table's bytes, delete rows through the API, then lower it", limit.Bytes, readSlack)
		}
		value, err := decodeJSON(strings.NewReader(text.String))
		if err != nil {
			return storedTable{}, fmt.Errorf("reading the value of %s: %w", key, err)
		}
		table.values[key] = value
		table.sizes[key] = size
	}
	if err := rows.Err(); err != nil {
		return storedTable{}, fmt.Errorf("reading settings: %w", err)
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
	settings := NewSettings(db, appSources(boot.ConfigFile, logger), boot.QueryTimeout, TableLimits{Rows: boot.MaxRows, Bytes: boot.MaxTableBytes}, logger)
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
