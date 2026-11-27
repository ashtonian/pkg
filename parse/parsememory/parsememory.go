// Package parsememory is parse's in-memory provider: it holds every table
// in the process, needs no database, file or driver, and runs on any
// platform Go builds for, wasip1 included. Open opens an empty database
// (Database) and the provider on it. Each resource built on the provider
// (parse.NewResource) declares its table to the database, with its key,
// every column of its type and the pair table of each many-to-many
// relation; the application writes rows with Put and Delete, alone or in a
// transaction (Begin), and the resources list, facet and update them as the
// SQL providers do, evaluating every request in Go.
//
// A table is a B-tree of rows ordered by key (github.com/tidwall/btree),
// each column's value held as SQLite would bind it. The set of trees is
// published as one immutable snapshot: a read loads it once, so a list's
// page, count, facets and includes come from one state, and never waits.
// One writer at a time copies the trees it changes, which is O(1) until it
// changes them, and publishes them together when it commits; a write that
// fails publishes nothing. A table holds at most Config.MaxRows rows
// (ErrRowLimit).
//
// The provider compiles no SQL: SQL builders, Query, search, computed
// columns and StampSQL are refused with parse.ErrUnsupported before
// anything runs.
package parsememory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// Name is the provider's name in configuration (parse.Provider.Name).
const Name = "memory"

// Config sets up the database Open opens. Its tags are
// github.com/ashtonian/pkg/config's: loaded through config, a zero setting
// takes its default. Open checks every setting whichever way the Config
// was built.
type Config struct {
	MaxRows int `koanf:"max_rows" default:"100000" validate:"min=1" desc:"Rows one table may hold, pair tables included; a write past it fails with ErrRowLimit and keeps nothing its transaction wrote"`
}

// Row is one row to write: a value per column, by column name, each a Go
// value of the column's type as the application would bind it to SQL. A
// column the row leaves out is NULL.
type Row map[string]any

// Database is the provider Open opened and the tables under it. It is safe
// for concurrent use.
type Database struct {
	provider *parse.Provider
	catalog  *catalog
	store    *store
	closed   atomic.Bool
}

// Open opens an empty database and the provider on it, its tables holding
// at most cfg.MaxRows rows each. It refuses a setting out of its range.
func Open(_ context.Context, cfg Config) (*Database, error) {
	if cfg.MaxRows < 1 {
		return nil, fmt.Errorf("parsememory: max_rows: %d: must be at least 1", cfg.MaxRows)
	}
	db := &Database{catalog: newCatalog(), store: newStore(cfg.MaxRows)}
	db.provider = parse.NewProvider(backend.Provider{Name: Name, Storage: storage{}, DB: newDatabase(db)})
	return db, nil
}

// Choice is the memory provider opened from cfg, for parseprovider.Open to
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

// Provider is the provider to build resources on (parse.NewResource).
func (d *Database) Provider() *parse.Provider { return d.provider }

// Ping reports ErrClosed once the database is closed, and nil before: the
// database is in the process, so it always answers.
func (d *Database) Ping(context.Context) error {
	return d.open()
}

// Close drops every row; every later operation is ErrClosed. A transaction
// still open commits nothing.
func (d *Database) Close() error {
	d.closed.Store(true)
	d.store.close()
	return nil
}

func (d *Database) open() error {
	if d.closed.Load() {
		return ErrClosed
	}
	return nil
}

// Put writes rows to table, each replacing the row of its key, in one
// write: all of them or, on an error, none. It waits for the writer under
// ctx. A table or column no resource declared, a row without a value for
// a key column, and a value its column cannot hold are errors.
func (d *Database) Put(ctx context.Context, table string, rows ...Row) error {
	return d.writeAlone(ctx, func(w *write) error { return d.put(w, table, rows) })
}

// Delete deletes the rows of keys from table, in one write; a key no row
// holds deletes nothing. A key has a value per key column, in key order: a
// pair table's key is every column of its relation, sources, targets, then
// the tenant.
func (d *Database) Delete(ctx context.Context, table string, keys ...parse.Key) error {
	return d.writeAlone(ctx, func(w *write) error { return d.delete(w, table, keys) })
}

// writeAlone runs apply in a write of its own, committed when apply
// succeeds and dropped otherwise.
func (d *Database) writeAlone(ctx context.Context, apply func(*write) error) error {
	err := d.open()
	if err != nil {
		return err
	}
	w, err := d.store.begin(ctx)
	if err != nil {
		return err
	}
	err = apply(w)
	if err != nil {
		w.abort()
		return err
	}
	return w.commit()
}

// put writes rows to table in w.
func (d *Database) put(w *write, table string, rows []Row) error {
	def, err := d.declared(table)
	if err != nil {
		return fmt.Errorf("parsememory: putting into %q: %w", table, err)
	}
	records := make([]*record, len(rows))
	for i, row := range rows {
		records[i], err = def.record(row)
		if err != nil {
			return fmt.Errorf("parsememory: putting into %q: row %d: %w", table, i, err)
		}
	}
	t := w.mutable(def)
	for _, rec := range records {
		err = w.put(def, t, rec)
		if err != nil {
			return err
		}
	}
	return nil
}

// delete deletes the rows of keys from table in w.
func (d *Database) delete(w *write, table string, keys []parse.Key) error {
	def, err := d.declared(table)
	if err != nil {
		return fmt.Errorf("parsememory: deleting from %q: %w", table, err)
	}
	probes := make([]*record, len(keys))
	for i, k := range keys {
		probes[i], err = def.probe(k)
		if err != nil {
			return fmt.Errorf("parsememory: deleting from %q: key %d: %w", table, i, err)
		}
	}
	if w.rows(table) == nil {
		return nil
	}
	t := w.mutable(def)
	for _, p := range probes {
		t.Delete(p)
	}
	return nil
}

// declared is the named table's definition: an error for a table no
// resource declared.
func (d *Database) declared(table string) (*tableDef, error) {
	def, ok := d.catalog.table(table)
	if !ok {
		return nil, errors.New("no resource declares the table")
	}
	return def, nil
}

// record is row as def's table holds it: each value in its column's
// storage form, by position.
func (def *tableDef) record(row Row) (*record, error) {
	var unknown []string
	for name := range row {
		_, ok := def.index[name]
		if !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, fmt.Errorf("no resource declares the column %s", strings.Join(unknown, ", "))
	}
	values := make([]any, len(def.columns))
	for i, c := range def.columns {
		v, ok := row[c.name]
		if !ok {
			continue
		}
		stored, err := core.StorageValue(c.typ, v)
		if err == nil {
			err = checkHolds(c.typ, stored)
		}
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c.name, err)
		}
		values[i] = stored
	}
	rec := &record{values: values}
	for _, at := range def.key {
		c, v := def.columns[at], rec.value(at)
		if isNull(v) {
			return nil, fmt.Errorf("key column %s: NULL", c.name)
		}
		_, err := core.CompareCanonical(c.typ, v, v)
		if err != nil {
			return nil, fmt.Errorf("key column %s: %w", c.name, err)
		}
	}
	return rec, nil
}

// checkHolds refuses v, a stored value, when its column's type t orders
// values and v is none of them, so every comparison a request makes of
// the column (core.CompareCanonical) can read it. A document's, a geo
// shape's or an array's value is its JSON, which core.StorageValue wrote,
// and a value of no declared type is held as it is.
func checkHolds(t core.TypeInfo, v any) error {
	switch t.Type {
	case core.FieldTypeJSON, core.FieldTypeObject, core.FieldTypeArray, core.FieldTypeGeo, core.FieldTypeAny:
		return nil
	}
	_, err := core.CompareCanonical(t, v, v)
	return err
}

// probe is a record holding key at def's key positions, for a lookup by
// key: each value normalized as its column's type (core.NormalizeKey) and
// in its storage form.
func (def *tableDef) probe(key parse.Key) (*record, error) {
	normalized, err := core.NormalizeKey(def.keyTypes(), key)
	if err != nil {
		return nil, err
	}
	rec := &record{values: make([]any, len(def.columns))}
	for i, at := range def.key {
		rec.values[at], err = core.StorageValue(def.columns[at].typ, normalized[i])
		if err != nil {
			return nil, fmt.Errorf("key column %s: %w", def.columns[at].name, err)
		}
	}
	return rec, nil
}

// Txn is a transaction of the application's own: it holds the writer from
// Begin until Commit or Rollback, so the database's other writes wait for
// it, and its reads see its own writes. Resources run in it through Tx. A
// Txn is safe for concurrent use: its operations, and the requests
// resources run in it, run one at a time, as a database/sql Tx runs them
// on its one connection.
type Txn struct {
	db *Database
	// mu serializes the transaction's operations: Put, Delete, Commit,
	// Rollback, the rollback its context's end makes, and every request a
	// resource runs in it (executor.hold).
	mu sync.Mutex
	w  *write
	// cause is the cause of the end of the context the transaction is bound
	// to (Database.Begin), nil until it ends, and stop stops the rollback
	// that end makes; both nil for a transaction no context bounds.
	cause func() error
	stop  func() bool
	// ended is why the transaction ended before Commit or Rollback: its
	// context's end; nil for every other end.
	ended error
}

// ErrTxnDone is an operation on a transaction already committed or rolled
// back, including one ErrRowLimit ended and one its context's end rolled
// back.
var ErrTxnDone = errors.New("parsememory: transaction already committed or rolled back")

// Begin begins a transaction, waiting for the writer under ctx, and binds
// it to ctx as database/sql binds a Tx to BeginTx's context: once ctx is
// done, the transaction rolls back, releasing the writer, and every later
// operation returns ErrTxnDone wrapping ctx's cause (context.Cause), Commit
// included. A Rollback deferred past a Commit does nothing.
func (d *Database) Begin(ctx context.Context) (*Txn, error) {
	txn, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	txn.cause = func() error { return context.Cause(ctx) }
	txn.stop = context.AfterFunc(ctx, func() {
		txn.mu.Lock()
		defer txn.mu.Unlock()
		_ = txn.live()
	})
	return txn, nil
}

// begin begins a transaction no context bounds, waiting for the writer
// under ctx: parse.Provider.Begin's, which lasts until Commit or Rollback.
func (d *Database) begin(ctx context.Context) (*Txn, error) {
	err := d.open()
	if err != nil {
		return nil, err
	}
	w, err := d.store.begin(ctx)
	if err != nil {
		return nil, err
	}
	txn := &Txn{db: d, w: w}
	return txn, nil
}

// live reports why the transaction takes no more operations, nil while it
// takes them; a transaction whose context has ended is rolled back here,
// so no operation runs past that end. It is called with mu held.
func (t *Txn) live() error {
	if !t.w.done && t.cause != nil {
		cause := t.cause()
		if cause != nil {
			t.w.abort()
			t.ended = fmt.Errorf("%w: rolled back as its context ended: %w", ErrTxnDone, cause)
		}
	}
	if !t.w.done {
		return nil
	}
	if t.ended != nil {
		return t.ended
	}
	return ErrTxnDone
}

// settled stops the rollback the transaction's context would make, once it
// has committed or rolled back. It is called with mu held.
func (t *Txn) settled() {
	if t.stop != nil {
		t.stop()
	}
}

// Put writes rows to table in the transaction, as Database.Put does: all
// of them or, on an error, none. ErrRowLimit rolls the transaction back.
func (t *Txn) Put(_ context.Context, table string, rows ...Row) error {
	return t.apply(func(w *write) error { return t.db.put(w, table, rows) })
}

// Delete deletes the rows of keys from table in the transaction, as
// Database.Delete does.
func (t *Txn) Delete(_ context.Context, table string, keys ...parse.Key) error {
	return t.apply(func(w *write) error { return t.db.delete(w, table, keys) })
}

// apply runs apply, a write that checks every row before it writes any,
// in the transaction. ErrRowLimit ends the transaction.
func (t *Txn) apply(apply func(*write) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.live()
	if err != nil {
		return err
	}
	err = apply(t.w)
	if errors.Is(err, ErrRowLimit) {
		t.w.abort()
		t.settled()
	}
	return err
}

// Commit publishes what the transaction wrote and releases the writer,
// once any operation in flight has returned.
func (t *Txn) Commit(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.live()
	if err != nil {
		return err
	}
	t.settled()
	return t.w.commit()
}

// Rollback drops what the transaction wrote and releases the writer, once
// any operation in flight has returned. A transaction already settled is
// left as it is, and Rollback returns nil.
func (t *Txn) Rollback(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.w.abort()
	t.settled()
	return nil
}

// Tx is txn as a transaction resources run in (parse.Resource.In), so that
// the application's writes and parse's updates commit together. An update
// in it that fails leaves the transaction as it was. Commit or roll back
// txn itself, or through the returned Tx.
func Tx(txn *Txn) *parse.Tx {
	wrapped := parse.NewTx(backend.Transaction{Provider: Name, Tx: transaction{executor: executor{db: txn.db, txn: txn}}})
	return wrapped
}
