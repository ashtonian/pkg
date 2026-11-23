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

// BootConfig is what the process needs before it can read the table. A
// read takes up to twice each table cap, so the caps' upper bounds keep it
// well within an int; a key's upper bound stays beneath the largest entry
// PostgreSQL's btree index takes, about 2.7 KB. MaxWriteKeys left unset is
// MaxRows (see loadBoot).
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
	MaxRows           int           `koanf:"max_rows" default:"1000" validate:"min=1,max=1000000"`
	MaxTableBytes     int64         `koanf:"max_table_bytes" default:"1048576" validate:"min=1,max=1073741824"`
	MaxWriteKeys      int           `koanf:"max_write_keys" validate:"min=1,max=1000000"`
	MaxKeyBytes       int           `koanf:"max_key_bytes" default:"1024" validate:"min=1,max=2048"`
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
// the table read through entries under the table's source options, and
// Beneath names the sources beneath the table, for an operator to check
// when the settings fail to load.
type Sources struct {
	Options func(entries Entries, table ...config.SourceOption) []config.Option
	Beneath string
}

// appSources layers the table over the file and APP_* variables.
func appSources(file string, logger *slog.Logger) Sources {
	return Sources{
		Options: func(entries Entries, table ...config.SourceOption) []config.Option {
			return []config.Option{
				config.WithFile(file),
				config.WithEnvPrefix("APP"),
				config.WithOverrideEntries(entries, table...),
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
	ReasonTooMany  = "too many" // the write names more keys than one write may
	ReasonTooLong  = "too long" // a key is longer than a key may be
)

// ErrBusy is a write whose time ran out: waiting for its turn — behind
// another write of this replica's, for a connection, or for the lock row
// another replica's write holds — or once it had it, reading the table,
// checking the write or storing it. It is contention, not a failure; the
// client retries. A write sets its keys to their values, so a retry after a
// commit the deadline cut short, which the database may have completed,
// sets them again.
var ErrBusy = errors.New("settings write timed out")

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
	db          *sql.DB
	sources     Sources
	timeout     time.Duration
	limits      TableLimits
	writeLimits WriteLimits
	logger      *slog.Logger
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

// WriteLimits bound what one write names. They are checked before the
// write waits for its turn, so a write past them costs no other write
// time.
type WriteLimits struct {
	Keys     int // keys one write names
	KeyBytes int // bytes of each key, spelled as it is stored
}

// tableSize is a table's size, as TableLimits bound it.
type tableSize struct {
	rows  int
	bytes int64
}

// NewSettings returns the table in db, loaded with sources' options. Each
// read, and each write with its wait for its turn, takes at most timeout;
// limits cap the table and writeLimits each write.
func NewSettings(db *sql.DB, sources Sources, timeout time.Duration, limits TableLimits, writeLimits WriteLimits, logger *slog.Logger) *Settings {
	return &Settings{db: db, sources: sources, timeout: timeout, limits: limits, writeLimits: writeLimits, logger: logger, writing: make(chan struct{}, 1)}
}

// Options are the options the service loads AppConfig with, the table read
// on every load. Write checks a write with the same options, the table
// replaced by the one the write would leave.
//
// A release that removes a field leaves rows, and parts of rows' values, at
// keys no field reads. The table's source claims them, so they stop no
// load; each load that finds any logs them, with the remedy.
func (s *Settings) Options() []config.Option {
	return s.sources.Options(s.read, config.WithUnknownEntries(s.reportUnknown))
}

func (s *Settings) reportUnknown(ctx context.Context, unknown []config.UnknownKey) error {
	keys := make([]string, len(unknown))
	for i, u := range unknown {
		keys[i] = u.Key
	}
	s.logger.WarnContext(ctx, "settings the schema does not read left out of the load",
		slog.Any("keys", keys), slog.String("remedy", "write each key as null, or write the key of the row holding it whole"))
	return nil
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
	return stored.values, nil
}

// Write applies edits to the table, all or nothing, and returns the keys it
// wrote: each key, spelled canonically, set to its value and replacing the
// rows stored beneath it, or, set to nil, deleted with them, and in either
// case cut out of the values of the rows stored above it. A key the
// schema does not read may be set only to nil, to delete the rows a release
// that removed its field left. A write naming more keys than its limits
// allow, or a longer key, is refused before it waits for its turn. Unless
// the table the write leaves loads with the service's options, and fits the
// caps or is no larger than before, the write is refused with a
// *RefusedError and nothing changes. A write whose time runs out, waiting
// for its turn or once it has it, is ErrBusy.
func (s *Settings) Write(ctx context.Context, edits map[string]any) ([]string, error) {
	if len(edits) > s.writeLimits.Keys {
		return nil, &RefusedError{Reason: ReasonTooMany, Keys: []string{}}
	}
	writes, unread, err := canonical[AppConfig](edits, s.writeLimits.KeyBytes)
	if err != nil || len(writes) == 0 && len(unread) == 0 {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	keys, err := s.store(ctx, writes, unread)
	// Whichever step the deadline cut short, the client retries the write,
	// so every one is ErrBusy; a refusal stands, as no retry changes it.
	if _, refused := errors.AsType[*RefusedError](err); err != nil && !refused && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return keys, err
}

// store makes the write in its turn: it waits for this replica's other
// write and for the lock row, then reads the table, checks the table the
// write leaves and commits it.
func (s *Settings) store(ctx context.Context, writes map[string]any, unread []string) ([]string, error) {
	select {
	case s.writing <- struct{}{}:
		defer func() { <-s.writing }()
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for this replica's other settings write: %w", context.Cause(ctx))
	}
	// At READ COMMITTED each statement reads what committed before it, so a
	// write that waited for the lock row reads the table the write before it
	// left; at REPEATABLE READ or SERIALIZABLE, a server default some
	// deployments set, Postgres refuses the waiting write's update instead.
	// SQLite's driver ignores the level: its transactions are serializable,
	// and updating the lock row takes the database's write lock.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("beginning a settings write: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op
	// Updating the lock row is the write's first statement: the database
	// holds the row until the write ends, so writes from every replica take
	// turns, and each reads the table the one before it left.
	locked, err := tx.ExecContext(ctx, "UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", err)
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
	c, err := apply[AppConfig](stored.values, writes, unread)
	if err != nil {
		return nil, err
	}
	keys := append(slices.Collect(maps.Keys(writes)), unread...)
	slices.Sort(keys)
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
	if err := s.check(ctx, stored.values, c.table, writes, keys); err != nil {
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

// retryAfter is how long a client whose write met contention waits before
// it retries: the most one write takes, its wait for its turn included.
func (s *Settings) retryAfter() time.Duration {
	return s.timeout
}

// check loads after, the table a write of writes leaves, with the service's
// options. A table that does not load refuses the write, as does a key no
// field reads inside a value the write sets; one a stored row holds is
// tolerated, as loads tolerate it. The write is not refused when before,
// the table stored now, does not load either: then the fault lies in a
// source beneath the table or in a row already stored, and no client can
// fix it by retrying.
func (s *Settings) check(ctx context.Context, before, after, writes map[string]any, keys []string) error {
	_, err := config.Load[AppConfig](ctx, s.sources.Options(fixed(after), config.WithUnknownEntries(unknownInWrites(writes)))...)
	if err == nil || !errors.Is(err, config.ErrValidation) && !errors.Is(err, config.ErrKey) {
		return err
	}
	if _, storedErr := config.Load[AppConfig](ctx, s.sources.Options(fixed(before), config.WithUnknownEntries(nil))...); storedErr != nil {
		return fmt.Errorf("the settings fail to load without this write; check %s and the settings table: %w", s.sources.Beneath, storedErr)
	}
	reason := ReasonInvalid
	if _, ok := errors.AsType[*writtenUnknownError](err); ok {
		reason = ReasonUnknown
	}
	return &RefusedError{Reason: reason, Keys: keys, Err: err}
}

// writtenUnknownError is the keys no field reads inside the values a write
// sets.
type writtenUnknownError struct {
	keys []config.UnknownKey
}

func (e *writtenUnknownError) Error() string {
	names := make([]string, len(e.keys))
	for i, k := range e.keys {
		names[i] = k.Key
	}
	return "keys no field reads in the values written: " + strings.Join(names, ", ")
}

// unknownInWrites is the table's report for a write's check: it fails the
// load on a key no field reads within a key writes sets, and lets the rest,
// what rows already stored hold, through.
func unknownInWrites(writes map[string]any) func(context.Context, []config.UnknownKey) error {
	set := &keyTree{}
	for key, value := range writes {
		if k, err := config.ParseKey(key); err == nil && value != nil {
			set.add(k, key)
		}
	}
	return func(_ context.Context, unknown []config.UnknownKey) error {
		var written []config.UnknownKey
		for _, u := range unknown {
			if k, err := config.ParseKey(u.Key); err == nil && len(set.within(k)) > 0 {
				written = append(written, u)
			}
		}
		if len(written) > 0 {
			return &writtenUnknownError{keys: written}
		}
		return nil
	}
}

func fixed(table map[string]any) Entries {
	return func(context.Context) (map[string]any, error) { return table, nil }
}

// schemaKey is key parsed as CanonicalKey spells it, or false when the
// schema reads nothing at key.
func schemaKey[T any](key string) (config.Key, bool) {
	c, err := config.CanonicalKey[T](key)
	if err != nil {
		return nil, false
	}
	k, err := config.ParseKey(c)
	return k, err == nil
}

// childKey is the key the schema reads the entry name of a value stored at
// at as: a field, or failing that a map key.
func childKey[T any](at config.Key, name string) (config.Key, bool) {
	for _, kind := range []config.SegmentKind{config.SegmentField, config.SegmentKey} {
		text, parses := childText(at, kind, name)
		if !parses {
			continue
		}
		if k, ok := schemaKey[T](text); ok {
			return k, true
		}
	}
	return nil, false
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
// service reads, a key holding a wildcard, a key longer than maxKeyBytes as
// it is stored, one key spelled two ways, or a key and a key within it
// refuses the write.
func canonical[T any](edits map[string]any, maxKeyBytes int) (writes map[string]any, unread []string, err error) {
	writes = make(map[string]any, len(edits))
	var unknown, long, repeated []string
	for key, value := range edits {
		c, err := config.CanonicalKey[T](key)
		if errors.Is(err, config.ErrKey) {
			switch {
			case value != nil || wildcard(key):
				unknown = append(unknown, key)
			case len(key) > maxKeyBytes:
				long = append(long, key)
			default:
				unread = append(unread, key)
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if len(c) > maxKeyBytes {
			long = append(long, c)
			continue
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
	case len(long) > 0:
		slices.Sort(long)
		return nil, nil, &RefusedError{Reason: ReasonTooLong, Keys: long}
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

// wildcard reports whether key parses to a pattern: a null names the rows
// to delete by their key, and [*] names no one key.
func wildcard(key string) bool {
	k, err := config.ParseKey(key)
	return err == nil && slices.ContainsFunc(k, func(s config.Segment) bool { return s.Kind == config.SegmentAny })
}

// overlapping is the keys of writes within another of them, and the keys
// they are within.
func overlapping(writes map[string]any) []string {
	tree := &keyTree{}
	parsed := make(map[string]config.Key, len(writes))
	for key := range writes {
		if k, err := config.ParseKey(key); err == nil {
			tree.add(k, key)
			parsed[key] = k
		}
	}
	var overlap []string
	for key, k := range parsed {
		for _, parent := range tree.within(k[:len(k)-1]) {
			overlap = append(overlap, key, parent.text)
		}
	}
	slices.Sort(overlap)
	return slices.Compact(overlap)
}

// keyTree holds keys by their segments, so the keys a key is within, and
// the keys beneath it, are found in steps of its depth rather than of the
// keys held. A field and a map key of one name are one segment, as
// Key.Within compares them. The keys it holds are concrete: a wildcard in a
// key looked up matches none of them, as Within matches a wildcard only in
// the key compared against.
type keyTree struct {
	children map[config.Segment]*keyTree
	keys     []treeKey // the keys ending here
}

// treeKey is a key a keyTree holds, as written and parsed.
type treeKey struct {
	text string
	key  config.Key
}

// add holds k, written as text.
func (t *keyTree) add(k config.Key, text string) {
	n := t
	for _, s := range k {
		next := n.children[branch(s)]
		if next == nil {
			if n.children == nil {
				n.children = map[config.Segment]*keyTree{}
			}
			next = &keyTree{}
			n.children[branch(s)] = next
		}
		n = next
	}
	n.keys = append(n.keys, treeKey{text: text, key: k})
}

// within is the keys held that k is within: k and the keys above it.
func (t *keyTree) within(k config.Key) []treeKey {
	var keys []treeKey
	n := t
	for _, s := range k {
		if n = n.children[branch(s)]; n == nil {
			break
		}
		keys = append(keys, n.keys...)
	}
	return keys
}

// beneath is the keys held strictly beneath k, a key of at least one
// segment, sorted.
func (t *keyTree) beneath(k config.Key) []treeKey {
	n := t
	for _, s := range k {
		if n = n.children[branch(s)]; n == nil {
			return nil
		}
	}
	keys := n.below(nil)
	slices.SortFunc(keys, func(a, b treeKey) int { return strings.Compare(a.text, b.text) })
	return keys
}

// below is keys with the keys held beneath t appended.
func (t *keyTree) below(keys []treeKey) []treeKey {
	for _, child := range t.children {
		keys = child.below(append(keys, child.keys...))
	}
	return keys
}

// branch is the segment a keyTree branches on for s: a map key as the
// field of its name.
func branch(s config.Segment) config.Segment {
	if s.Kind == config.SegmentKey {
		s.Kind = config.SegmentField
	}
	return s
}

// storedKey is a stored row's key as a write compares it: spelled as
// CanonicalKey spells it when the schema reads it, as stored otherwise.
type storedKey struct {
	key  config.Key // nil when it does not parse
	read bool       // the schema reads it
}

func readStoredKey[T any](stored string) storedKey {
	c, err := config.CanonicalKey[T](stored)
	if err != nil {
		k, _ := config.ParseKey(stored)
		return storedKey{key: k}
	}
	k, _ := config.ParseKey(c)
	return storedKey{key: k, read: true}
}

// change is what a write does to the stored rows.
type change struct {
	table     map[string]any // the rows the write leaves
	deleted   []string       // the stored keys it deletes
	rewritten map[string]any // stored rows above a key written, that key cut out of their values
}

// apply is the change writes and unread make to stored, keys compared as
// the schema T spells them. A stored row within a key written is deleted,
// unless the write sets that key again, as is one the schema does not read
// at or beneath a key of unread: a row the schema reads is never deleted
// through a spelling it refuses. A stored row above a key written has that
// key cut out of its value, so the write takes effect beneath it, and is
// deleted when left setting nothing. A key of unread with no row to delete
// names nothing, and refuses the write as unknown; a key beneath a list or
// a scalar a stored row sets cannot be cut out, and refuses it as enclosed.
//
// Each stored key is read once, and the written and unread keys are held in
// key trees, so the cost is the rows and the keys, each by its depth, and
// not their pairs: the write holds the lock row while it runs.
func apply[T any](stored, writes map[string]any, unread []string) (change, error) {
	written := &keyTree{}
	for key := range writes {
		if k, err := config.ParseKey(key); err == nil {
			written.add(k, key)
		}
	}
	deleting := &keyTree{}
	unparsed := map[string]bool{} // matched by the row stored at the same text
	for _, key := range unread {
		if k, err := config.ParseKey(key); err == nil {
			deleting.add(k, key)
		} else {
			unparsed[key] = true
		}
	}
	matched := map[string]bool{}
	c := change{table: map[string]any{}, rewritten: map[string]any{}}
	var enclosed []string
	var errs []error
	for key, value := range stored {
		row := readStoredKey[T](key)
		if !row.read {
			deletes := deleting.within(row.key)
			for _, u := range deletes {
				matched[u.text] = true
			}
			if unparsed[key] {
				matched[key] = true
			}
			if len(deletes) > 0 || unparsed[key] {
				c.deleted = append(c.deleted, key)
				continue
			}
		}
		if len(written.within(row.key)) > 0 {
			if writes[key] == nil { // not set again under this spelling, where the upsert replaces it
				c.deleted = append(c.deleted, key)
			}
			continue
		}
		// A row the schema does not read stays as stored: loads leave it out.
		if row.read && len(row.key) > 0 {
			cut, changed, refused := cutBeneath[T](key, row.key, value, written.beneath(row.key))
			if refused != nil {
				enclosed = append(enclosed, refused.Keys...)
				errs = append(errs, refused.Err)
				continue
			}
			if object, ok := cut.(map[string]any); changed && ok && len(object) == 0 {
				c.deleted = append(c.deleted, key)
				continue
			}
			value = cut
			if changed {
				c.rewritten[key] = value
			}
		}
		c.table[key] = value
	}
	var missing []string
	for _, key := range unread {
		if !matched[key] {
			missing = append(missing, key)
		}
	}
	switch {
	case len(missing) > 0:
		return change{}, &RefusedError{Reason: ReasonUnknown, Keys: missing}
	case len(enclosed) > 0:
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

// cutBeneath is value, stored at row, whose key is at, with each key of
// beneath, the written keys beneath at, cut out of it, and whether that
// changed it. A key it cannot cut out refuses the write.
func cutBeneath[T any](row string, at config.Key, value any, beneath []treeKey) (any, bool, *RefusedError) {
	changed := false
	var refused *RefusedError
	for _, w := range beneath {
		cut, c, err := cutAt[T](at, value, w.key)
		if err != nil {
			if refused == nil {
				refused = &RefusedError{Reason: ReasonEnclosed}
			}
			refused.Keys = append(refused.Keys, w.text)
			refused.Err = errors.Join(refused.Err, fmt.Errorf("the row %s: %w", row, err))
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
func cutAt[T any](at config.Key, value any, target config.Key) (any, bool, error) {
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
		k, ok := childKey[T](at, name)
		if !ok || len(k) != len(next) || !k.Within(next) {
			continue
		}
		if len(next) == len(target) {
			delete(out, name)
			changed = true
			continue
		}
		cut, c, err := cutAt[T](k, child, target)
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
// bytes. The database sends a row's key and value only while the running
// total of the rows' bytes, in key order, is within the byte limit, so a
// read holds at most that many bytes of keys and values whatever another
// writer stored. Values decode with UseNumber, so an integer keeps every
// digit.
func readRows(ctx context.Context, q querier, limit TableLimits) (storedTable, error) {
	rows, err := q.QueryContext(ctx, `
SELECT CASE WHEN total <= $2 THEN key END, total, CASE WHEN total <= $2 THEN value END
FROM (
	SELECT key, value, CAST(SUM(CAST(octet_length(key) AS BIGINT) + octet_length(value)) OVER (ORDER BY key) AS BIGINT) AS total
	FROM settings
) AS sized
ORDER BY sized.key LIMIT $1`, limit.Rows+1, limit.Bytes)
	if err != nil {
		return storedTable{}, fmt.Errorf("reading settings: %w", err)
	}
	defer rows.Close()
	table := storedTable{values: map[string]any{}, sizes: map[string]int64{}}
	var before int64
	for rows.Next() {
		if len(table.values) == limit.Rows {
			return storedTable{}, fmt.Errorf("reading settings: the table holds more than %d rows, %d times SETTINGS_MAX_ROWS; raise it to at least half the rows, delete rows through the API, then lower it", limit.Rows, readSlack)
		}
		var key, text sql.NullString
		var total int64
		if err := rows.Scan(&key, &total, &text); err != nil {
			return storedTable{}, fmt.Errorf("reading settings: %w", err)
		}
		if total > limit.Bytes { // the key and the value were not sent
			return storedTable{}, fmt.Errorf("reading settings: the table holds more than %d bytes, %d times SETTINGS_MAX_TABLE_BYTES; raise it to at least half the table's bytes, delete rows through the API, then lower it", limit.Bytes, readSlack)
		}
		value, err := decodeJSON(strings.NewReader(text.String))
		if err != nil {
			return storedTable{}, fmt.Errorf("reading the value of %s: %w", key.String, err)
		}
		table.values[key.String] = value
		table.sizes[key.String] = total - before
		before = total
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
			logger.LogAttrs(r.Context(), slog.LevelInfo, "settings written", keysLogged(r.Context(), logger, written)...)
			w.WriteHeader(http.StatusNoContent)
		case isRefused:
			attrs := append([]slog.Attr{slog.String("reason", refused.Reason)}, keysLogged(r.Context(), logger, refused.Keys)...)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "settings write refused", append(attrs, slog.Any("error", refused.Err))...)
			reply(w, http.StatusUnprocessableEntity, refused)
		case r.Context().Err() != nil:
			logger.WarnContext(r.Context(), "settings write abandoned", slog.Any("error", err))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "unavailable"})
		case errors.Is(err, ErrBusy):
			logger.WarnContext(r.Context(), "settings write timed out", slog.Any("error", err))
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(s.retryAfter().Seconds()))))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "busy"})
		default:
			logger.ErrorContext(r.Context(), "writing settings", slog.Any("error", err))
			reply(w, http.StatusServiceUnavailable, map[string]string{"reason": "unavailable"})
		}
	}
}

// keysLogged is a write's keys as its log entry carries them: their count,
// and at Debug the keys themselves, each as shownKey shows it.
func keysLogged(ctx context.Context, logger *slog.Logger, keys []string) []slog.Attr {
	attrs := []slog.Attr{slog.Int("key_count", len(keys))}
	if logger.Enabled(ctx, slog.LevelDebug) {
		shown := make([]string, len(keys))
		for i, key := range keys {
			shown[i] = shownKey[AppConfig](key)
		}
		attrs = append(attrs, slog.Any("keys", shown))
	}
	return attrs
}

// shownKey is key as a log may show it, judged by the config package: a
// key it reads whose field is sensitive by its field's pattern, every map
// key and index in it as [*], since a sensitive map's keys may themselves
// be secrets; a key it does not read as its own errors name it, which
// withhold what lies beneath a sensitive map or list.
func shownKey[T any](key string) string {
	if _, err := config.CanonicalKey[T](key); err != nil {
		if ke, ok := errors.AsType[*config.KeyError](err); ok {
			return ke.Key
		}
		return withheld
	}
	f, err := config.FieldFor[T](key)
	switch {
	case err != nil: // a key a remain field collects names no field
		return withheld
	case f.Sensitive:
		return f.Path
	}
	return key
}

// withheld stands for a key no log shows.
const withheld = "[REDACTED]"

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
	boot, err := loadBoot(ctx, environ, logger)
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

// loadBoot loads BootConfig from the SETTINGS_* variables. A write's key cap
// left unset is the row cap: a write naming more keys than the table may
// hold rows is refused before it waits for its turn.
func loadBoot(ctx context.Context, environ func() []string, logger *slog.Logger) (*BootConfig, error) {
	return config.Load[BootConfig](ctx, config.WithEnvPrefix("SETTINGS"), config.WithEnviron(environ), config.WithLogger(logger),
		config.WithPostLoad(func(_ context.Context, b *BootConfig) error {
			if b.MaxWriteKeys == 0 {
				b.MaxWriteKeys = b.MaxRows
			}
			return nil
		}))
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
	settings := NewSettings(db, appSources(boot.ConfigFile, logger), boot.QueryTimeout,
		TableLimits{Rows: boot.MaxRows, Bytes: boot.MaxTableBytes},
		WriteLimits{Keys: boot.MaxWriteKeys, KeyBytes: boot.MaxKeyBytes}, logger)
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
