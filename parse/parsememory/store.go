package parsememory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/tidwall/btree"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrRowLimit is a write that would leave a table holding more rows than
// Config.MaxRows. It ends the transaction it happens in: nothing that
// transaction wrote is kept.
var ErrRowLimit = errors.New("parsememory: row limit")

// ErrClosed is an operation on a database after Close.
var ErrClosed = errors.New("parsememory: database closed")

// record is one row: a value per column of its table's definition, by
// position, each in its storage form. A position past the end is NULL: the
// column was declared after the row was written.
type record struct {
	values []any
}

// value is the row's value of the column at position i.
func (r *record) value(i int) any {
	if i >= len(r.values) {
		return nil
	}
	return r.values[i]
}

// tree is one table's rows, ordered by key.
type tree = btree.BTreeG[*record]

// newTree is an empty table of def, ordered by its key: column by column,
// each in its type's order (core.CompareCanonical). The key is fixed by the
// table's first declaration, so every later definition orders alike.
func newTree(def *tableDef) *tree {
	key, types := def.key, def.keyTypes()
	less := func(a, b *record) bool { return compareKeys(key, types, a, b) < 0 }
	return btree.NewBTreeGOptions(less, btree.Options{NoLocks: true})
}

// compareKeys orders two rows of a table by their values at the key
// positions, typed by types. Every key a table holds was checked when it
// was written (tableDef.record), so no comparison of two fails; were one to, the
// rows would order as equal, which a tree cannot hold apart, rather than
// at random.
func compareKeys(key []int, types []core.TypeInfo, a, b *record) int {
	for i, at := range key {
		c, err := core.CompareCanonical(types[i], a.value(at), b.value(at))
		if err != nil || c != 0 {
			return c
		}
	}
	return 0
}

// snapshot is one published state of the database: every table's rows as
// one commit left them. Nothing writes a published snapshot or its trees.
// A table with no tree holds no row.
type snapshot struct {
	tables map[string]*tree
}

func (s *snapshot) rows(table string) *tree { return s.tables[table] }

// view is what a read reads rows from: a published snapshot, or a write in
// progress, whose reads see its own writes.
type view interface {
	rows(table string) *tree
}

// store holds the rows: the published snapshot, which every read loads
// once, and the one writer at a time, which copies the trees it changes
// and publishes them together on commit.
type store struct {
	head   atomic.Pointer[snapshot]
	writer chan struct{}
	// publish orders publishing a commit with closing the database.
	publish sync.Mutex
	closed  bool
	maxRows int
}

func newStore(maxRows int) *store {
	s := &store{writer: make(chan struct{}, 1), maxRows: maxRows}
	s.head.Store(&snapshot{tables: map[string]*tree{}})
	return s
}

// read is the published snapshot: a list reads its page, count, facets and
// includes from one.
func (s *store) read() *snapshot { return s.head.Load() }

// begin takes the writer, waiting under ctx for the write that holds it,
// and starts a write on the published snapshot. A ctx already done takes
// nothing, whether or not the writer is free. The write holds the writer
// until it commits or aborts, whatever happens to ctx after.
func (s *store) begin(ctx context.Context) (*write, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("parsememory: waiting for the writer: %w", context.Cause(ctx))
	}
	select {
	case s.writer <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("parsememory: waiting for the writer: %w", context.Cause(ctx))
	}
	w := &write{store: s, base: s.read(), copied: map[string]*tree{}}
	return w, nil
}

// close publishes an empty snapshot, so the rows are freed, and refuses
// every later commit.
func (s *store) close() {
	s.publish.Lock()
	defer s.publish.Unlock()
	s.closed = true
	s.head.Store(&snapshot{tables: map[string]*tree{}})
}

// write is the one write in progress: the snapshot it began on and the
// tables it has copied to change. Its reads see its own writes.
type write struct {
	store  *store
	base   *snapshot
	copied map[string]*tree
	done   bool
}

func (w *write) rows(table string) *tree {
	t, ok := w.copied[table]
	if ok {
		return t
	}
	return w.base.tables[table]
}

// mutable is def's table for the write to change: a copy of the
// snapshot's, made the first time the write changes it. Copy is O(1): the
// copy shares the snapshot's nodes and copies each as the write changes
// it, so the snapshot's readers never see the write.
func (w *write) mutable(def *tableDef) *tree {
	t, ok := w.copied[def.name]
	if ok {
		return t
	}
	base, ok := w.base.tables[def.name]
	if ok {
		t = base.Copy()
	} else {
		t = newTree(def)
	}
	w.copied[def.name] = t
	return t
}

// put stores rec in t, def's table, replacing the row of its key. A new row
// past the store's limit is ErrRowLimit.
func (w *write) put(def *tableDef, t *tree, rec *record) error {
	_, replaced := t.Set(rec)
	if !replaced && t.Len() > w.store.maxRows {
		return fmt.Errorf("parsememory: table %q: more than %d rows: %w", def.name, w.store.maxRows, ErrRowLimit)
	}
	return nil
}

// savepoint is the write's tables as they stand, for restore to return
// to: an O(1) copy of each table the write has changed.
type savepoint map[string]*tree

func (w *write) savepoint() savepoint {
	saved := make(savepoint, len(w.copied))
	for name, t := range w.copied {
		saved[name] = t.Copy()
	}
	return saved
}

// restore returns the write to saved: tables it copied since read as the
// snapshot does again.
func (w *write) restore(saved savepoint) {
	w.copied = map[string]*tree(saved)
}

// commit publishes the write's tables, with the snapshot's others, as the
// next snapshot, and releases the writer. A database closed meanwhile
// keeps nothing.
func (w *write) commit() error {
	if w.done {
		return nil
	}
	defer w.release()
	if len(w.copied) == 0 {
		return nil
	}
	next := maps.Clone(w.base.tables)
	maps.Copy(next, w.copied)
	w.store.publish.Lock()
	defer w.store.publish.Unlock()
	if w.store.closed {
		return ErrClosed
	}
	w.store.head.Store(&snapshot{tables: next})
	return nil
}

// abort drops the write's copies and releases the writer: nothing it did
// is kept.
func (w *write) abort() {
	if w.done {
		return
	}
	w.release()
}

func (w *write) release() {
	w.done = true
	w.copied = nil
	<-w.store.writer
}
