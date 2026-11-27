//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsesql"
)

// execFixture: t1 holds e1 ("one", tagged red), e2 ("two") and a deleted
// e3; t2 holds f1. A junction row may not name the tag "forbidden".
func execFixture(timestamp string) []string {
	stmts := []string{
		`CREATE TABLE exec_items (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, version BIGINT NOT NULL DEFAULT 1, deleted_at ` + timestamp + `)`,
		`CREATE TABLE exec_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL CHECK (tag_id <> 'forbidden'), tenant_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id, tenant_id))`,
		`INSERT INTO exec_items (id, tenant_id, name, deleted_at) VALUES ('e1','t1','one',NULL), ('e2','t1','two',NULL), ('e3','t1','gone','2026-01-01T00:00:00Z'), ('f1','t2','other',NULL)`,
		`INSERT INTO exec_tags VALUES ('e1','red','t1')`,
	}
	return stmts
}

func execItemID(item execItem) string { return item.ID }

// execDoc's counts decode into a map of integers, so a stored document of
// another shape is a row that fails to scan.
type execDoc struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Name   string         `json:"name"   db:"name"   parse:"filter,patch"`
	Counts map[string]int `json:"counts" db:"counts"`
}

// execPanicked is what an execPanicDoc panics with.
const execPanicked = "execPanicDoc: a document that panics as it decodes"

// execPanicDoc is a caller's type whose decoder panics on {"panic":true}: a
// stored document that panics as an update reads its row back.
type execPanicDoc map[string]bool

func (d *execPanicDoc) UnmarshalJSON(raw []byte) error {
	var m map[string]bool
	err := json.Unmarshal(raw, &m)
	if err != nil {
		return err
	}
	if m["panic"] {
		panic(execPanicked)
	}
	*d = m
	return nil
}

type execPanicItem struct {
	ID   string       `json:"id"   db:"id"   parse:"filter,sort"`
	Name string       `json:"name" db:"name" parse:"filter,patch"`
	Doc  execPanicDoc `json:"doc"  db:"doc"`
}

func execPanics() *parse.Resource[execPanicItem] {
	return parse.MustResource[execPanicItem]("exec_panics")
}

// sqlRowReader reads one row: *sql.DB, *sql.Conn and *sql.Tx.
type sqlRowReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// pgxRowReader reads one row: *pgx.Conn and pgx.Tx.
type pgxRowReader interface {
	QueryRow(ctx context.Context, query string, args ...any) pgx.Row
}

// pgxAnnounced is a caller's pgx.Tx that reports each statement it sends to
// the hook dbtest.OnStatement put in the statement's context, as the
// database dbtest.SQLiteHooked opens does on SQLite.
type pgxAnnounced struct {
	pgx.Tx
}

func (c pgxAnnounced) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, q := range b.QueuedQueries {
		// pgx refuses the batch itself once a hook has ended ctx.
		_ = dbtest.Announce(ctx, q.SQL)
	}
	return c.Tx.SendBatch(ctx, b)
}

func (c pgxAnnounced) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	err := dbtest.Announce(ctx, query)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return c.Tx.Exec(ctx, query, args...)
}

// pgxUpdateAlone runs plan through parsepgx on a connection of db's its
// own, under the settings scan holds (the planner's scan methods), sending
// the connection's backend PID to pids before the update (0 when it has
// none), so a test can watch the update wait for a lock.
func pgxUpdateAlone(ctx context.Context, db *sql.DB, plan *parse.UpdatePlan, scan []string, pids chan<- uint32) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		pids <- 0
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	// The connection goes back to the pool, so no setting outlives the call.
	defer func() {
		_, reset := conn.ExecContext(context.WithoutCancel(ctx), "RESET ALL")
		err = errors.Join(err, reset)
	}()
	for _, setting := range scan {
		_, err = conn.ExecContext(ctx, setting)
		if err != nil {
			pids <- 0
			return err
		}
	}
	err = conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			pids <- 0
			return fmt.Errorf("db is not a pgx stdlib pool: %T", driverConn)
		}
		pids <- c.Conn().PgConn().PID()
		_, err := parsepgx.Update(ctx, c.Conn(), execItems, plan)
		return err
	})
	return err
}

// Each dialect's executor holds to one contract: what an update reports as
// changed and unchanged, what its membership changes count, what a failure
// leaves, and what a list page carries. SQLite's database reports each
// statement to dbtest.OnStatement's hook; on PostgreSQL pgxAnnounced does.
func TestExecutorContract(t *testing.T) {
	runSQLBackends(t, runExecutorContract, hooked())
}

// runExecutorContract runs every case on a fresh database of b.
func runExecutorContract(t *testing.T, b sqlBackend) {
	d := b.dialect
	document := b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := b.open(t)
		execStatements(t, db, execFixture(b.columnType(core.TypeInfo{Type: core.FieldTypeTimestamp}))...)
		return db
	}
	plan := func(t *testing.T, body string, target parse.Target, opts ...parse.UpdateOption) *parse.UpdatePlan {
		t.Helper()
		p, err := execItems.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := execItems.BuildUpdate(d, p, target, execScope, append([]parse.UpdateOption{parse.Increment("version")}, opts...)...)
		require.NoError(t, err)
		return plan
	}
	stored := func(t *testing.T, db *sql.DB, id string) execItem {
		t.Helper()
		columns, err := execItems.Columns(d)
		require.NoError(t, err)
		got := resourceRows(t, db, execItems, d, parse.Statement{
			SQL:  `SELECT ` + joinColumns(columns) + ` FROM "exec_items" WHERE "exec_items"."id" = ` + d.Placeholder(1),
			Args: []any{id},
		})
		require.Len(t, got, 1)
		return got[0]
	}
	tags := func(t *testing.T, db *sql.DB, id string) []string {
		t.Helper()
		return queryRows[string](t, db, `SELECT tag_id FROM exec_tags WHERE item_id = `+d.Placeholder(1)+` ORDER BY tag_id`, id)
	}

	for _, tc := range []struct {
		name               string
		body               string
		target             parse.Target
		opts               []parse.UpdateOption
		changed, unchanged []string
		relationRows       int64
		e1Name             string
		e1Version          int64
		e1Tags             []string
	}{
		{name: "columns change every target the scope admits", body: `{"name":"x"}`, target: parse.ByIDs("e1", "e2", "e3", "f1"),
			changed: []string{"e1", "e2"}, e1Name: "x", e1Version: 2, e1Tags: []string{"red"}},
		{name: "a guarded update reports the rows it left", body: `{"name":"one"}`, target: parse.ByIDs("e1", "e2"), opts: []parse.UpdateOption{parse.SkipUnchanged()},
			changed: []string{"e2"}, unchanged: []string{"e1"}, e1Name: "one", e1Version: 1, e1Tags: []string{"red"}},
		{name: "a relation-only patch writes no column and counts its pairs", body: `{"tags":["blue"]}`, target: parse.ByID("e1"),
			unchanged: []string{"e1"}, relationRows: 2, e1Name: "one", e1Version: 1, e1Tags: []string{"blue"}},
		{name: "a relation-only patch of what the row holds counts none", body: `{"tags":["red"]}`, target: parse.ByID("e1"),
			unchanged: []string{"e1"}, e1Name: "one", e1Version: 1, e1Tags: []string{"red"}},
		{name: "columns and relations, guarded: both sides change memberships", body: `{"name":"one","tags":["red","blue"]}`, target: parse.ByIDs("e1", "e2"), opts: []parse.UpdateOption{parse.SkipUnchanged()},
			changed: []string{"e2"}, unchanged: []string{"e1"}, relationRows: 3, e1Name: "one", e1Version: 1, e1Tags: []string{"blue", "red"}},
		{name: "an empty patch is a scoped read", body: `{}`, target: parse.ByIDs("e1", "f1"),
			unchanged: []string{"e1"}, e1Name: "one", e1Version: 1, e1Tags: []string{"red"}},
		{name: "another tenant's row is not found", body: `{"name":"x","tags":["blue"]}`, target: parse.ByID("f1"),
			e1Name: "one", e1Version: 1, e1Tags: []string{"red"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setup(t)
			res, err := applyUpdate(t, db, execItems, d, plan(t, tc.body, tc.target, tc.opts...), false)
			require.NoError(t, err)
			assert.Equal(t, tc.changed, sortedIDs(res.Changed, execItemID), "changed")
			assert.Equal(t, tc.unchanged, sortedIDs(res.Unchanged, execItemID), "unchanged")
			assert.Equal(t, tc.relationRows, res.RelationRows)
			e1 := stored(t, db, "e1")
			assert.Equal(t, tc.e1Name, e1.Name)
			assert.Equal(t, tc.e1Version, e1.Version)
			assert.Equal(t, tc.e1Tags, tags(t, db, "e1"))
			assert.Equal(t, "other", stored(t, db, "f1").Name, "another tenant's row stays")
		})
	}

	t.Run("a membership change that fails undoes the row change", func(t *testing.T) {
		db := setup(t)
		res, err := applyUpdate(t, db, execItems, d, plan(t, `{"name":"renamed","tags":["forbidden"]}`, parse.ByID("e1")), false)
		var failed *parse.StatementError
		require.ErrorAs(t, err, &failed)
		assert.Equal(t, "relations", failed.Statement, "both executors name the statement that failed")
		assert.Zero(t, res)
		assert.Equal(t, "one", stored(t, db, "e1").Name)
		assert.Equal(t, []string{"red"}, tags(t, db, "e1"))
	})

	t.Run("in the caller's transaction a failure leaves it usable and its rollback undoes the rest", func(t *testing.T) {
		db := setup(t)
		failing := plan(t, `{"name":"renamed","tags":["forbidden"]}`, parse.ByID("e1"))
		renaming := plan(t, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e2"))
		check := func(failed error, res parse.UpdateResult[execItem], err error) {
			require.Error(t, failed)
			require.NoError(t, err, "the transaction survives the failed update")
			assert.Equal(t, []string{"e2"}, sortedIDs(res.Changed, execItemID))
			assert.Equal(t, int64(1), res.RelationRows)
		}
		require.NoError(t, onExecutor(t, db, d, true, func(tx parsesql.DB) error {
			_, failed := parsesql.Update(t.Context(), tx, execItems, failing)
			res, err := parsesql.Update(t.Context(), tx, execItems, renaming)
			check(failed, res, err)
			return nil
		}, func(tx parsepgx.DB) error {
			_, failed := parsepgx.Update(t.Context(), tx, execItems, failing)
			res, err := parsepgx.Update(t.Context(), tx, execItems, renaming)
			check(failed, res, err)
			return nil
		}))
		assert.Equal(t, "two", stored(t, db, "e2").Name, "the caller rolled back")
		assert.Empty(t, tags(t, db, "e2"))
		assert.Equal(t, "one", stored(t, db, "e1").Name)
	})

	// An error means nothing the update wrote is kept: a returned row that
	// fails to scan comes after the database applied the statement, so only
	// the transaction the executor settles after scanning can undo it.
	t.Run("a row that fails to scan writes nothing", func(t *testing.T) {
		execDocs := parse.MustResource[execDoc]("exec_docs")
		db := setup(t)
		for _, stmt := range []string{
			`CREATE TABLE exec_docs (id TEXT PRIMARY KEY, name TEXT NOT NULL, counts ` + document + `)`,
			`INSERT INTO exec_docs VALUES ('d1', 'one', '{"x":"not a number"}')`,
		} {
			_, err := db.ExecContext(t.Context(), stmt)
			require.NoError(t, err, stmt)
		}
		p, err := execDocs.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		renaming, err := execDocs.BuildUpdate(d, p, parse.ByID("d1"), parse.Scope{})
		require.NoError(t, err)
		for _, inTx := range []bool{false, true} {
			_, err = applyUpdate(t, db, execDocs, d, renaming, inTx)
			var failed *parse.StatementError
			require.ErrorAs(t, err, &failed, "in the caller's transaction: %v", inTx)
			assert.Equal(t, "owner", failed.Statement, "in the caller's transaction: %v", inTx)
			var name string
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT name FROM exec_docs WHERE id = 'd1'`).Scan(&name))
			assert.Equal(t, "one", name, "in the caller's transaction: %v", inTx)
		}
		require.NoError(t, onExecutor(t, db, d, true, func(tx parsesql.DB) error {
			_, err := parsesql.Update(t.Context(), tx, execDocs, renaming)
			require.Error(t, err)
			var n string
			require.NoError(t, tx.(*sql.Tx).QueryRowContext(t.Context(), `SELECT name FROM exec_docs WHERE id = 'd1'`).Scan(&n), "the caller's transaction is usable")
			assert.Equal(t, "one", n, "the caller's transaction keeps nothing")
			return nil
		}, func(tx parsepgx.DB) error {
			_, err := parsepgx.Update(t.Context(), tx, execDocs, renaming)
			require.Error(t, err)
			var n string
			require.NoError(t, tx.(pgx.Tx).QueryRow(t.Context(), `SELECT name FROM exec_docs WHERE id = 'd1'`).Scan(&n), "the caller's transaction is usable")
			assert.Equal(t, "one", n, "the caller's transaction keeps nothing")
			return nil
		}))
	})

	// A context that ends while the update runs undoes it in the caller's
	// transaction, which stays usable; one that ends once every statement has
	// run leaves the update finished and reported so.
	t.Run("a cancellation in the caller's transaction is reported as what it left", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			trigger string
			applied bool
		}{
			{"as the membership changes are sent", "exec_tags", false},
			{"as the savepoint is released", "RELEASE SAVEPOINT", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db := setup(t)
				renaming := plan(t, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e1"))
				check := func(t *testing.T, err error, name string, tags []string) {
					t.Helper()
					if tc.applied {
						require.NoError(t, err)
						assert.Equal(t, "renamed", name)
						assert.Equal(t, []string{"blue"}, tags)
						return
					}
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, "one", name)
					assert.Equal(t, []string{"red"}, tags)
				}
				cancelling := func(t *testing.T) (context.Context, context.CancelFunc) {
					t.Helper()
					ctx, cancel := context.WithCancel(t.Context())
					ctx = dbtest.OnStatement(ctx, func(_ context.Context, query string) {
						if strings.Contains(query, tc.trigger) {
							cancel()
						}
					})
					return ctx, cancel
				}
				require.NoError(t, onExecutor(t, db, d, true, func(tx parsesql.DB) error {
					ctx, cancel := cancelling(t)
					defer cancel()
					_, err := parsesql.Update(ctx, tx, execItems, renaming)
					var name string
					require.NoError(t, tx.(*sql.Tx).QueryRowContext(t.Context(), `SELECT name FROM exec_items WHERE id = 'e1'`).Scan(&name), "the caller's transaction is usable")
					var tags []string
					rows, qerr := tx.QueryContext(t.Context(), `SELECT tag_id FROM exec_tags WHERE item_id = 'e1' ORDER BY tag_id`)
					require.NoError(t, qerr)
					defer func() { require.NoError(t, rows.Close()) }()
					for rows.Next() {
						var tag string
						require.NoError(t, rows.Scan(&tag))
						tags = append(tags, tag)
					}
					require.NoError(t, rows.Err())
					check(t, err, name, tags)
					return nil
				}, func(tx parsepgx.DB) error {
					ctx, cancel := cancelling(t)
					defer cancel()
					_, err := parsepgx.Update(ctx, pgxAnnounced{Tx: tx.(pgx.Tx)}, execItems, renaming)
					var name string
					require.NoError(t, tx.(pgx.Tx).QueryRow(t.Context(), `SELECT name FROM exec_items WHERE id = 'e1'`).Scan(&name), "the caller's transaction is usable")
					tags, qerr := pgx.CollectRows(func() pgx.Rows {
						rows, _ := tx.(pgx.Tx).Query(t.Context(), `SELECT tag_id FROM exec_tags WHERE item_id = 'e1' ORDER BY tag_id`)
						return rows
					}(), pgx.RowTo[string])
					require.NoError(t, qerr)
					check(t, err, name, tags)
					return nil
				}))
			})
		}
	})

	// Each statement that settles an update runs under the settle timeout,
	// from when it is sent; every other statement runs under the caller's
	// context. Commits and rollbacks of database/sql take no context, so the
	// savepoint path is the one both executors show.
	t.Run("each statement that settles the update runs under the settle timeout", func(t *testing.T) {
		db := setup(t)
		renaming := plan(t, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e1"))
		const bound = time.Hour
		type sent struct {
			query    string
			deadline time.Time
			bounded  bool
		}
		var seen []sent
		ctx := dbtest.OnStatement(t.Context(), func(ctx context.Context, query string) {
			deadline, bounded := ctx.Deadline()
			seen = append(seen, sent{query: query, deadline: deadline, bounded: bounded})
		})
		start := time.Now()
		require.NoError(t, onExecutor(t, db, d, true, func(tx parsesql.DB) error {
			_, err := parsesql.Update(ctx, tx, execItems, renaming, parse.WithSettleTimeout(bound))
			return err
		}, func(tx parsepgx.DB) error {
			_, err := parsepgx.Update(ctx, pgxAnnounced{Tx: tx.(pgx.Tx)}, execItems, renaming, parse.WithSettleTimeout(bound))
			return err
		}))
		end := time.Now()
		settled := 0
		for _, s := range seen {
			if !strings.HasPrefix(s.query, "RELEASE SAVEPOINT") {
				assert.False(t, s.bounded, "%s runs under the caller's context", s.query)
				continue
			}
			settled++
			require.True(t, s.bounded, "the release has a deadline")
			assert.False(t, s.deadline.Before(start.Add(bound)), "the release's deadline is the settle timeout from when it is sent")
			assert.False(t, s.deadline.After(end.Add(bound)), "the release's deadline is the settle timeout from when it is sent")
		}
		assert.Equal(t, 1, settled, "the savepoint is released once")
	})

	// A panic as the update reads its rows — a stored document's own decoder,
	// run as the owner statement's returned row is read — unwinds through
	// the executor, which settles on the way out as it does for an error:
	// the owner statement's write is undone, the connection is free, and a
	// caller's transaction is still usable.
	t.Run("a panic as a row is read settles the update on its way out", func(t *testing.T) {
		db := setup(t)
		for _, stmt := range []string{
			`CREATE TABLE exec_panics (id TEXT PRIMARY KEY, name TEXT NOT NULL, doc ` + document + `)`,
			`INSERT INTO exec_panics VALUES ('p1', 'one', '{"panic":true}')`,
		} {
			_, err := db.ExecContext(t.Context(), stmt)
			require.NoError(t, err, stmt)
		}
		panics := execPanics()
		p, err := panics.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		renaming, err := panics.BuildUpdate(d, p, parse.ByID("p1"), parse.Scope{})
		require.NoError(t, err)
		const read = `SELECT name FROM exec_panics WHERE id = 'p1'`
		for _, inTx := range []bool{false, true} {
			require.NoError(t, onExecutor(t, db, d, inTx, func(sqlDB parsesql.DB) error {
				assert.PanicsWithValue(t, execPanicked, func() { _, _ = parsesql.Update(t.Context(), sqlDB, panics, renaming) }, "in the caller's transaction: %v", inTx)
				// Bounded: a connection the update kept would hold SQLite's
				// only one, and the read would wait for it.
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				var name string
				require.NoError(t, sqlDB.(sqlRowReader).QueryRowContext(ctx, read).Scan(&name), "the connection is free; in the caller's transaction: %v", inTx)
				assert.Equal(t, "one", name, "in the caller's transaction: %v", inTx)
				return nil
			}, func(pgxDB parsepgx.DB) error {
				assert.PanicsWithValue(t, execPanicked, func() { _, _ = parsepgx.Update(t.Context(), pgxDB, panics, renaming) }, "in the caller's transaction: %v", inTx)
				conn, own := pgxDB.(*pgx.Conn)
				if own {
					assert.Equal(t, byte('I'), conn.PgConn().TxStatus(), "no transaction is left open on the connection")
				}
				var name string
				require.NoError(t, pgxDB.(pgxRowReader).QueryRow(t.Context(), read).Scan(&name), "the connection is free; in the caller's transaction: %v", inTx)
				assert.Equal(t, "one", name, "in the caller's transaction: %v", inTx)
				return nil
			}))
		}
	})

	// Two updates naming the same rows lock them in one order, so whichever
	// takes the first row takes them all and the other waits for it, rather
	// than each holding a row the other waits for (40P01). PostgreSQL locks
	// rows in the order its plan reads them, so the two are given plans that
	// read in opposite orders over rows written in descending key order: one
	// scans the heap, the other the key's index. A third transaction holds
	// the rows until both updates wait behind it, so neither has locked a row
	// when the two start. From opposite ends of 1000 rows the two meet before
	// either finishes, which 100 rows, locked faster than the other wakes,
	// do not ensure.
	t.Run("updates naming the same rows in opposite orders do not deadlock", func(t *testing.T) {
		if !isPostgres(d) {
			t.Skip("SQLite has no row locks: its write transactions serialize")
		}
		for _, tc := range []struct {
			name string
			body string
			opts []parse.UpdateOption
		}{
			{"columns alone", `{"name":"x"}`, nil},
			{"columns and relations", `{"name":"x","tags":["blue"]}`, nil},
			{"guarded", `{"name":"x"}`, []parse.UpdateOption{parse.SkipUnchanged()}},
			{"relations alone", `{"tags":["blue"]}`, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db := setup(t)
				keys := make([]any, 1000)
				values := make([]string, len(keys))
				for i := range keys {
					id := fmt.Sprintf("n%04d", len(keys)-i)
					keys[i] = id
					values[i] = "('" + id + "', 't1', 'n', NULL)"
				}
				_, err := db.ExecContext(t.Context(), `INSERT INTO exec_items (id, tenant_id, name, deleted_at) VALUES `+strings.Join(values, ", "))
				require.NoError(t, err)
				reversed := slices.Clone(keys)
				slices.Reverse(reversed)
				plans := []*parse.UpdatePlan{
					plan(t, tc.body, parse.ByIDs(keys...), tc.opts...),
					plan(t, tc.body, parse.ByIDs(reversed...), tc.opts...),
				}
				scans := [][]string{
					{"SET enable_indexscan = off", "SET enable_bitmapscan = off", "SET enable_indexonlyscan = off"},
					{"SET enable_seqscan = off", "SET enable_bitmapscan = off"},
				}
				holder, err := db.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer func() { _ = holder.Rollback() }()
				_, err = holder.ExecContext(t.Context(), `SELECT id FROM exec_items WHERE id LIKE 'n%' FOR UPDATE`)
				require.NoError(t, err)

				pids := make(chan uint32, len(plans))
				results := make(chan error, len(plans))
				for i, p := range plans {
					go func() { results <- pgxUpdateAlone(t.Context(), db, p, scans[i], pids) }()
				}
				first, second := <-pids, <-pids
				require.NotZero(t, first, "the first update has a connection")
				require.NotZero(t, second, "the second update has a connection")
				for {
					var waiting bool
					err := db.QueryRowContext(t.Context(), `SELECT cardinality(pg_blocking_pids($1::int)) > 0 AND cardinality(pg_blocking_pids($2::int)) > 0`, first, second).Scan(&waiting)
					require.NoError(t, err)
					if waiting {
						break
					}
					select {
					case err := <-results:
						require.Fail(t, "an update finished while the holder held its rows", "%v", err)
					case <-time.After(10 * time.Millisecond):
					}
				}
				require.NoError(t, holder.Commit())
				for range plans {
					require.NoError(t, <-results, "neither update is aborted as a deadlock")
				}
			})
		}
	})

	t.Run("a batch reports changed and unchanged rows and writes nothing when a target is missing", func(t *testing.T) {
		db := setup(t)
		batch := func(t *testing.T, rows map[string]string) *parse.UpdateBatchPlan {
			t.Helper()
			var updates []parse.RowUpdate
			for _, id := range slices.Sorted(func(yield func(string) bool) {
				for id := range rows {
					if !yield(id) {
						return
					}
				}
			}) {
				p, err := execItems.ParseMergePatch([]byte(rows[id]))
				require.NoError(t, err)
				updates = append(updates, parse.RowUpdate{Key: parse.Key{id}, Patch: p})
			}
			plan, err := execItems.BuildUpdateBatch(d, updates, execScope, parse.UpdateBatchLimits{}, parse.SkipUnchanged(), parse.Increment("version"))
			require.NoError(t, err)
			return plan
		}
		res, err := applyUpdateBatch(t, db, execItems, d, batch(t, map[string]string{"e1": `{"name":"one"}`, "e2": `{"name":"new"}`}))
		require.NoError(t, err)
		assert.Equal(t, []string{"e2"}, sortedIDs(res.Changed, execItemID))
		assert.Equal(t, []string{"e1"}, sortedIDs(res.Unchanged, execItemID))
		assert.Equal(t, int64(2), stored(t, db, "e2").Version)

		res, err = applyUpdateBatch(t, db, execItems, d, batch(t, map[string]string{"e1": `{"name":"must-not-write"}`, "f1": `{"name":"x"}`}))
		require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
		assert.Zero(t, res)
		assert.Equal(t, "one", stored(t, db, "e1").Name)
	})

	t.Run("a page carries its total and its includes", func(t *testing.T) {
		db := setup(t)
		req, err := execItems.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamInclude: {"tags"}, filter.ParamSort: {"id"}})
		require.NoError(t, err)
		list, err := execItems.BuildList(d, req, execScope)
		require.NoError(t, err)
		page := listPage(t, db, execItems, d, list)
		assert.Equal(t, []string{"e1", "e2"}, sortedIDs(page.Items, execItemID))
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
		assert.Equal(t, map[string]map[string][]string{"tags": {"e1": {"red"}}}, page.Relations, "an owner without pairs is omitted")
	})

	t.Run("a page of no rows carries an empty items array and its total", func(t *testing.T) {
		db := setup(t)
		req, err := execItems.ParseList(url.Values{"name": {"none"}, filter.ParamIncludeTotal: {"true"}, filter.ParamInclude: {"tags"}})
		require.NoError(t, err)
		list, err := execItems.BuildList(d, req, execScope)
		require.NoError(t, err)
		page := listPage(t, db, execItems, d, list)
		require.NotNil(t, page.Items)
		assert.Empty(t, page.Items)
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.Zero(t, *page.PageInfo.TotalCount)
		got, err := json.Marshal(page)
		require.NoError(t, err)
		assert.JSONEq(t, `{"items":[],"page_info":{"has_next_page":false,"has_prev_page":false,"total_count":0}}`, string(got))
	})

	t.Run("a plan for another dialect is refused before any statement", func(t *testing.T) {
		other := filter.Dialect(filter.PostgresDialect{})
		if isPostgres(d) {
			other = filter.SQLiteDialect{}
		}
		p, err := execItems.ParseMergePatch([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		foreign, err := execItems.BuildUpdate(other, p, parse.ByID("e1"), execScope)
		require.NoError(t, err)
		db := setup(t)
		_, err = applyUpdate(t, db, execItems, d, foreign, false)
		require.ErrorIs(t, err, parse.ErrDialect)
		_, err = queryPage(t, db, execItems, d, func() *parse.ListPlan {
			list, err := execItems.BuildList(other, nil, execScope)
			require.NoError(t, err)
			return list
		}())
		require.Error(t, err)
		assert.Equal(t, "one", stored(t, db, "e1").Name)
	})

	// The other resource's table does not exist: had any statement run, the
	// error would be that statement's.
	t.Run("a plan another resource built is refused before any statement", func(t *testing.T) {
		elsewhere := parse.MustResource[execItem]("exec_items_elsewhere", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
		p, err := elsewhere.ParseMergePatch([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		update, err := elsewhere.BuildUpdate(d, p, parse.ByID("e1"), execScope)
		require.NoError(t, err)
		batch, err := elsewhere.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{"e1"}, Patch: p}}, execScope, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		list, err := elsewhere.BuildList(d, nil, execScope)
		require.NoError(t, err)

		db := setup(t)
		for _, run := range []struct {
			kind string
			run  func() error
		}{
			{"update", func() error {
				_, err := applyUpdate(t, db, execItems, d, update, false)
				return err
			}},
			{"update batch", func() error {
				_, err := applyUpdateBatch(t, db, execItems, d, batch)
				return err
			}},
			{"list", func() error {
				_, err := queryPage(t, db, execItems, d, list)
				return err
			}},
		} {
			err := run.run()
			require.ErrorContains(t, err, `parse: exec_items: `+run.kind+` plan built for "exec_items_elsewhere"`)
			_, ran := errors.AsType[*parse.StatementError](err)
			assert.False(t, ran, run.kind)
		}
		assert.Equal(t, "one", stored(t, db, "e1").Name)
	})
}
