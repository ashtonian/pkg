//go:build integration

package parsesql_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsesql"
)

// entry is a row of entries, a table on a database file.
type entry struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Amount decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort,patch"`
	Note   string          `json:"note"   db:"note"   parse:"patch"`
}

// createEntries creates entries on db with four rows and an index on
// parse_decimal_key, whose entries are results of parse's functions the
// database file stores: by amount the rows are a (2.50), d (7), b (10) and
// c (1e2).
func createEntries(t *testing.T, db *sql.DB) {
	t.Helper()
	dbtest.Exec(t, db,
		`CREATE TABLE entries (id TEXT PRIMARY KEY, amount TEXT NOT NULL, note TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX entries_amount ON entries (parse_decimal_key(amount), id)`,
		`INSERT INTO entries (id, amount) VALUES ('a', '2.50'), ('b', '10'), ('c', '1e2'), ('d', '7')`,
	)
}

// databaseFile is the path of a database file in a directory of t's.
func databaseFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "app.db")
}

// openFile opens dsn, a database file, through Open on one connection with
// busy as the busy timeout, closed at test cleanup if the test has not
// closed it.
func openFile(t *testing.T, dsn string, busy time.Duration) *parsesql.Database {
	t.Helper()
	db, err := parsesql.Open(t.Context(), parsesql.Config{DSN: dsn, MaxOpenConns: 1, BusyTimeout: busy, SettleTimeout: parse.DefaultSettleTimeout})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// otherHandle is another handle on the database file path, as another
// process holds one, opened with params.
func otherHandle(t *testing.T, path, params string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?"+params)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// noteOf is row id's note, read on q.
func noteOf(t *testing.T, q dbtest.Queryer, id string) string {
	t.Helper()
	notes := dbtest.QueryRows[string](t, q, `SELECT note FROM entries WHERE id = ?`, id)
	require.Len(t, notes, 1)
	return notes[0]
}

// writeNote is a merge patch of an entry's note.
func writeNote(t *testing.T, entries *parse.Resource[entry], note string) *parse.RowUpdate {
	t.Helper()
	patch, err := entries.ParseMergePatch([]byte(`{"note":"` + note + `"}`))
	require.NoError(t, err)
	update := &parse.RowUpdate{Key: parse.Key{"a"}, Patch: patch}
	return update
}

// pageByAmount lists entries by amount two to a page, from cursor ("" for
// the first page): the page's ids, and the cursor after it ("" on the last
// page).
func pageByAmount(t *testing.T, entries *parse.Resource[entry], cursor string) ([]string, string) {
	t.Helper()
	q := url.Values{filter.ParamSort: {"amount"}, filter.ParamPageSize: {"2"}}
	if cursor != "" {
		q.Set(filter.ParamCursor, cursor)
	}
	req, err := entries.ParseList(q)
	require.NoError(t, err)
	page, err := entries.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	ids := make([]string, 0, len(page.Items))
	for _, e := range page.Items {
		ids = append(ids, e.ID)
	}
	next := ""
	if page.PageInfo.HasNextPage {
		next = string(page.PageInfo.EndCursor)
	}
	return ids, next
}

// checkStoredResults checks that entries_amount on db holds what parse's
// functions compute for the rows: integrity_check computes each row's
// index entry and compares it with the stored one, and a query the index
// alone answers (INDEXED BY) reads the stored entries.
func checkStoredResults(t *testing.T, db *sql.DB) {
	t.Helper()
	assert.Equal(t, []string{"ok"}, dbtest.QueryRows[string](t, db, `PRAGMA integrity_check`))
	assert.Equal(t, []string{"d", "b", "c"}, dbtest.QueryRows[string](t, db,
		`SELECT id FROM entries INDEXED BY entries_amount WHERE parse_decimal_key(amount) > parse_decimal_key('5') ORDER BY parse_decimal_key(amount), id`))
}

// A database file closed and opened again holds its rows, the stored
// results of parse's functions in its indexes, and its journal mode, which
// WAL makes a setting of the file; a keyset cursor from before the close
// resumes on it.
func TestOnDisk_AReopenKeepsRowsStoredResultsAndCursors(t *testing.T) {
	path := databaseFile(t)
	db := openFile(t, path+"?_journal_mode=WAL", time.Second)
	createEntries(t, db.DB())
	entries := parse.MustResource[entry](db.Provider(), "entries")
	first, cursor := pageByAmount(t, entries, "")
	assert.Equal(t, []string{"a", "d"}, first)
	require.NotEmpty(t, cursor)
	require.NoError(t, db.Close())

	db = openFile(t, path, time.Second)
	entries = parse.MustResource[entry](db.Provider(), "entries")
	assert.Equal(t, []string{"wal"}, dbtest.QueryRows[string](t, db.DB(), `PRAGMA journal_mode`), "the file keeps its journal mode")
	checkStoredResults(t, db.DB())
	again, _ := pageByAmount(t, entries, "")
	assert.Equal(t, first, again)
	second, last := pageByAmount(t, entries, cursor)
	assert.Equal(t, []string{"b", "c"}, second, "the cursor resumes after the page it ended")
	assert.Empty(t, last)
}

// A reader's transaction holds a snapshot in WAL mode, beside which a
// writer commits without waiting: the provider's busy timeout is 0, so any
// wait would fail. In rollback-journal mode, SQLite's default, the
// reader's lock holds the writer's COMMIT until the reader ends, past the
// busy timeout here, and the update fails at its commit having kept
// nothing; the commit's failure leaves its connection, the provider's only
// one, outside any transaction, so the same update succeeds once the
// reader is done.
func TestOnDisk_AWriterBesideAReader(t *testing.T) {
	open := func(t *testing.T, journal string) (*parse.Resource[entry], *sql.Tx, *sql.DB) {
		t.Helper()
		path := databaseFile(t)
		db := openFile(t, path+"?_journal_mode="+journal, 0)
		createEntries(t, db.DB())
		reader := otherHandle(t, path, "_busy_timeout=0")
		snapshot, err := reader.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = snapshot.Rollback() })
		assert.Empty(t, noteOf(t, snapshot, "a"), "the reader's first read takes its snapshot")
		return parse.MustResource[entry](db.Provider(), "entries"), snapshot, reader
	}

	t.Run("in WAL mode the writer commits while the reader keeps its snapshot", func(t *testing.T) {
		entries, snapshot, reader := open(t, "WAL")
		_, err := entries.Update(t.Context(), writeNote(t, entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
		require.NoError(t, err)
		assert.Empty(t, noteOf(t, snapshot, "a"), "the reader still reads its snapshot")
		require.NoError(t, snapshot.Commit())
		assert.Equal(t, "mine", noteOf(t, reader, "a"))
	})

	t.Run("in rollback-journal mode the writer's commit waits for the reader", func(t *testing.T) {
		entries, snapshot, reader := open(t, "DELETE")
		_, err := entries.Update(t.Context(), writeNote(t, entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
		require.ErrorContains(t, err, "parse: entries: commit: database is locked")
		require.NoError(t, snapshot.Commit())
		assert.Empty(t, noteOf(t, reader, "a"), "the failed commit kept nothing")
		_, err = entries.Update(t.Context(), writeNote(t, entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
		require.NoError(t, err)
		assert.Equal(t, "mine", noteOf(t, reader, "a"))
	})
}

// A writer begins by taking SQLite's write lock, waiting for another
// writer to release it up to the connection's busy timeout (_busy_timeout,
// Open's busy_timeout): without one it fails at once, having written
// nothing; with one it writes once the other writer commits.
func TestOnDisk_AWriterWaitsForTheWriteLockUpToTheBusyTimeout(t *testing.T) {
	open := func(t *testing.T, busy string) (*parse.Resource[entry], *sql.DB, *sql.Tx) {
		t.Helper()
		path := databaseFile(t)
		db := parsesql.HookedFileDB(t, path+"?_journal_mode=WAL&_busy_timeout="+busy, 1)
		createEntries(t, db)
		p, err := parsesql.New(t.Context(), db)
		require.NoError(t, err)
		holder := otherHandle(t, path, "_txlock=immediate&_busy_timeout=0")
		held, err := holder.BeginTx(t.Context(), nil)
		require.NoError(t, err, "the other writer takes the write lock")
		t.Cleanup(func() { _ = held.Rollback() })
		return parse.MustResource[entry](p, "entries"), db, held
	}

	t.Run("with no busy timeout the writer fails at once", func(t *testing.T) {
		entries, db, held := open(t, "0")
		_, err := entries.Update(t.Context(), writeNote(t, entries, "mine").Patch, parse.ByID("a"), parse.Scope{})
		require.ErrorContains(t, err, "parse: entries: begin: database is locked")
		require.NoError(t, held.Rollback())
		assert.Empty(t, noteOf(t, db, "a"))
	})

	t.Run("with a busy timeout the writer waits for the other to commit", func(t *testing.T) {
		entries, db, held := open(t, "60000")
		// The other writer commits once the update is about to begin, so
		// the update's BEGIN IMMEDIATE meets the lock held or just freed;
		// without the wait, the first fails.
		beginning := make(chan struct{})
		var once sync.Once
		ctx := dbtest.OnStatement(t.Context(), func(_ context.Context, query string) {
			if query == "BEGIN IMMEDIATE" {
				once.Do(func() { close(beginning) })
			}
		})
		mine := writeNote(t, entries, "mine").Patch
		done := make(chan error, 1)
		go func() {
			_, err := entries.Update(ctx, mine, parse.ByID("a"), parse.Scope{})
			done <- err
		}()
		select {
		case <-beginning:
		case err := <-done:
			t.Fatalf("the update returned before it began its transaction: %v", err)
		}
		_, err := held.ExecContext(t.Context(), `UPDATE entries SET note = 'other' WHERE id = 'b'`)
		require.NoError(t, err)
		require.NoError(t, held.Commit())
		require.NoError(t, <-done)
		assert.Equal(t, "mine", noteOf(t, db, "a"))
		assert.Equal(t, "other", noteOf(t, db, "b"))
	})
}

// lockFixture is the entries resource on a database file in WAL mode, its
// statements observable (dbtest.OnStatement), and a second writer on the
// same file with no busy timeout: a write while another connection holds
// the write lock fails at once with SQLITE_BUSY rather than waiting.
type lockFixture struct {
	db      *sql.DB
	p       *parse.Provider
	entries *parse.Resource[entry]
	other   *sql.DB
}

// newLockFixture opens the fixture on a file whose DSN begins the
// application's transactions in the mode txlock names, "" for the
// driver's default, DEFERRED.
func newLockFixture(t *testing.T, txlock string) lockFixture {
	t.Helper()
	path := databaseFile(t)
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000"
	if txlock != "" {
		dsn += "&_txlock=" + txlock
	}
	db := parsesql.HookedFileDB(t, dsn, 2)
	createEntries(t, db)
	p, err := parsesql.New(t.Context(), db)
	require.NoError(t, err)
	f := lockFixture{db: db, p: p, entries: parse.MustResource[entry](p, "entries"), other: otherHandle(t, path, "_busy_timeout=0")}
	return f
}

// interleave is ctx with a hook that, at the first UPDATE an update sends
// — its write, after any read — runs the other writer's UPDATE of row a to
// its end, and the other writer's error.
func (f lockFixture) interleave(t *testing.T) (context.Context, *error) {
	t.Helper()
	var otherErr error
	var once sync.Once
	ctx := dbtest.OnStatement(t.Context(), func(ctx context.Context, query string) {
		if !strings.HasPrefix(query, "UPDATE") {
			return
		}
		once.Do(func() {
			_, otherErr = f.other.ExecContext(ctx, `UPDATE entries SET note = 'other' WHERE id = 'a'`)
		})
	})
	return ctx, &otherErr
}

// An update that reads before it writes (SkipUnchanged's Current, a
// batch's preimages) holds SQLite's write lock from its read to its write,
// so no other writer commits between them: the other writer, with no busy
// timeout, fails with SQLITE_BUSY at the update's write, and the update
// completes. parse's own transactions begin IMMEDIATE whatever _txlock the
// DSN gives (here none, so DEFERRED); a transaction of the application's
// own does when its connection begins transactions IMMEDIATE
// (_txlock=immediate).
func TestOnDisk_UpdatesHoldTheWriteLockFromTheirReadToTheirWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		txlock string
		update func(ctx context.Context, t *testing.T, f lockFixture) error
	}{
		{"an update skipping unchanged rows", "", func(ctx context.Context, t *testing.T, f lockFixture) error {
			_, err := f.entries.Update(ctx, writeNote(t, f.entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
			return err
		}},
		{"a batch", "", func(ctx context.Context, t *testing.T, f lockFixture) error {
			_, err := f.entries.UpdateBatch(ctx, []parse.RowUpdate{*writeNote(t, f.entries, "mine")}, parse.Scope{}, parse.UpdateBatchLimits{})
			return err
		}},
		{"an update in a transaction the provider began", "", func(ctx context.Context, t *testing.T, f lockFixture) error {
			tx, err := f.p.Begin(ctx)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(ctx)) }()
			scoped, err := f.entries.In(tx)
			require.NoError(t, err)
			_, err = scoped.Update(ctx, writeNote(t, f.entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		}},
		{"an update in the application's transaction, begun IMMEDIATE", "immediate", func(ctx context.Context, t *testing.T, f lockFixture) error {
			sqlTx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = sqlTx.Rollback() }()
			scoped, err := f.entries.In(parsesql.Tx(sqlTx))
			require.NoError(t, err)
			_, err = scoped.Update(ctx, writeNote(t, f.entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, parse.SkipUnchanged())
			if err != nil {
				return err
			}
			return sqlTx.Commit()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLockFixture(t, tc.txlock)
			ctx, otherErr := f.interleave(t)
			require.NoError(t, tc.update(ctx, t, f))
			require.ErrorContains(t, *otherErr, "database is locked", "the update held the write lock at its write")
			assert.Equal(t, "mine", noteOf(t, f.other, "a"))
		})
	}
}

// A transaction of the application's own begun DEFERRED, the driver's
// default, holds only a read snapshot once an update in it has read: in
// WAL mode another writer commits between the update's read and its write,
// and the write then fails with SQLITE_BUSY_SNAPSHOT at once, whatever the
// busy timeout, since SQLite does not write from a snapshot a later commit
// superseded. The update keeps nothing, and the application rolls the
// transaction back. An update that only writes reads nothing first: it
// waits for the lock and writes over the other writer's commit.
func TestOnDisk_AnUpdateInADeferredTransactionFailsWhenAnotherWriterCommitsBetweenItsReadAndWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   []parse.UpdateOption
		failed bool
	}{
		{"an update skipping unchanged rows, which reads first", []parse.UpdateOption{parse.SkipUnchanged()}, true},
		{"an update that only writes", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLockFixture(t, "")
			ctx, otherErr := f.interleave(t)
			sqlTx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = sqlTx.Rollback() }()
			scoped, err := f.entries.In(parsesql.Tx(sqlTx))
			require.NoError(t, err)
			_, err = scoped.Update(ctx, writeNote(t, f.entries, "mine").Patch, parse.ByID("a"), parse.Scope{}, tc.opts...)
			require.NoError(t, *otherErr, "the other writer commits: the transaction holds no write lock")
			if tc.failed {
				require.ErrorContains(t, err, "parse: entries: owner: database is locked (517)")
				require.NoError(t, sqlTx.Rollback())
				assert.Equal(t, "other", noteOf(t, f.other, "a"))
				return
			}
			require.NoError(t, err)
			require.NoError(t, sqlTx.Commit())
			assert.Equal(t, "mine", noteOf(t, f.other, "a"))
		})
	}
}

// onlineBackup copies db's database to dst through SQLite's online backup
// API, which modernc.org/sqlite's connection offers (NewBackup).
func onlineBackup(t *testing.T, db *sql.DB, dst string) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	err = conn.Raw(func(driverConn any) error {
		source, ok := driverConn.(interface {
			NewBackup(dstURI string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("the driver's connection %T has no online backup", driverConn)
		}
		backup, err := source.NewBackup(dst)
		if err != nil {
			return err
		}
		more, err := backup.Step(-1)
		if err == nil && more {
			err = errors.New("pages left after a step that copies them all")
		}
		return errors.Join(err, backup.Finish())
	})
	require.NoError(t, err)
}

// vacuumInto copies db's database to dst with VACUUM INTO.
func vacuumInto(t *testing.T, db *sql.DB, dst string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `VACUUM INTO ?`, dst)
	require.NoError(t, err)
}

// withoutFunctions is a handle on the database file path through a
// modernc.org/sqlite driver of its own, which parse's functions do not
// reach (Register registers them on the package's driver): the database as
// the sqlite3 shell or another tool opens it.
func withoutFunctions(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := sql.OpenDB(connector{driver: &sqlite.Driver{}, dsn: path})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A backup taken while another writer holds the write lock with a change
// not yet committed — VACUUM INTO, or the online backup API — reads beside
// it in WAL mode and copies what is committed: the rows, and index entries
// that agree with parse's functions. Opened as the original was, the copy
// resumes a keyset cursor the original handed out. VACUUM INTO writes its
// copy in rollback-journal mode; the online backup copies the pages, WAL
// mode's mark included, and calls no function, so it runs on a connection
// without parse's.
func TestOnDisk_ABackupCopiesRowsStoredResultsAndCursors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		backup    func(t *testing.T, db *sql.DB, dst string)
		functions bool
		journal   string
	}{
		{"VACUUM INTO, whose copy is in rollback-journal mode", vacuumInto, true, "delete"},
		{"the online backup API, whose copy keeps WAL mode", onlineBackup, true, "wal"},
		{"the online backup API on a connection without parse's functions", onlineBackup, false, "wal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := databaseFile(t)
			source := openFile(t, path+"?_journal_mode=WAL", 0)
			createEntries(t, source.DB())
			_, cursor := pageByAmount(t, parse.MustResource[entry](source.Provider(), "entries"), "")
			holder := otherHandle(t, path, "_txlock=immediate&_busy_timeout=0")
			held, err := holder.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { _ = held.Rollback() }()
			_, err = held.ExecContext(t.Context(), `UPDATE entries SET note = 'uncommitted' WHERE id = 'a'`)
			require.NoError(t, err)

			from := source.DB()
			if !tc.functions {
				from = withoutFunctions(t, path)
			}
			dst := filepath.Join(t.TempDir(), "copy.db")
			tc.backup(t, from, dst)
			require.NoError(t, held.Rollback())

			copied := openFile(t, dst, 0)
			assert.Equal(t, []string{tc.journal}, dbtest.QueryRows[string](t, copied.DB(), `PRAGMA journal_mode`))
			checkStoredResults(t, copied.DB())
			assert.Empty(t, noteOf(t, copied.DB(), "a"), "the copy holds what was committed")
			second, _ := pageByAmount(t, parse.MustResource[entry](copied.Provider(), "entries"), cursor)
			assert.Equal(t, []string{"b", "c"}, second, "the original's cursor resumes on the copy")
		})
	}
}

// VACUUM INTO rebuilds each index of its copy, so an index on parse's
// functions needs them on the connection that runs it.
func TestOnDisk_VacuumIntoNeedsParsesFunctions(t *testing.T) {
	path := databaseFile(t)
	db := openFile(t, path, 0)
	createEntries(t, db.DB())
	_, err := withoutFunctions(t, path).ExecContext(t.Context(), `VACUUM INTO ?`, filepath.Join(t.TempDir(), "copy.db"))
	require.ErrorContains(t, err, "no such function: parse_decimal_key")
}
