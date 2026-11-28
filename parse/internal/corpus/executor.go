//go:build integration

package corpus

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
)

// execScope is the executor contract's scope: tenant t1.
func execScope() parse.Scope {
	scope := parse.Scope{Tenant: "t1"}
	return scope
}

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

// loadExecRows writes execFixture's rows to the Memory engine's store,
// whose resources declared the tables. A memory table holds no CHECK
// constraint.
func loadExecRows(t *testing.T, db *sql.DB) {
	t.Helper()
	store := storeOf(t, db)
	gone := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.Put(t.Context(), "exec_items",
		map[string]any{"id": "e1", "tenant_id": "t1", "name": "one", "version": int64(1)},
		map[string]any{"id": "e2", "tenant_id": "t1", "name": "two", "version": int64(1)},
		map[string]any{"id": "e3", "tenant_id": "t1", "name": "gone", "version": int64(1), "deleted_at": gone},
		map[string]any{"id": "f1", "tenant_id": "t2", "name": "other", "version": int64(1)},
	))
	require.NoError(t, store.Put(t.Context(), "exec_tags", map[string]any{"item_id": "e1", "tag_id": "red", "tenant_id": "t1"}))
}

// loadExecDocs loads exec_docs: d1, named "one", holds a document execDoc
// cannot decode.
func loadExecDocs(t *testing.T, b sqlBackend, db *sql.DB) {
	t.Helper()
	b.load(t, db, sqlTable{name: "exec_docs", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id").required(), column[string]("name").required(), column[map[string]int]("counts"),
	}}, [][]any{{"d1", "one", `{"x":"not a number"}`}})
}

// execPanicRow is exec_panics' row p1, named "one", whose document panics
// as it decodes.
func execPanicRow() []any {
	row := []any{"p1", "one", `{"panic":true}`}
	return row
}

// execPanicTable is exec_panics, as a fixture declares it.
func execPanicTable() sqlTable {
	table := sqlTable{name: "exec_panics", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id").required(), column[string]("name").required(), column[execPanicDoc]("doc"),
	}}
	return table
}

// execFailure is an update that fails once it has changed a row: run runs
// it in tx, statement is the statement it fails at, and name reads the
// row's name, which the failure leaves as it was, "one".
type execFailure struct {
	run       func(t *testing.T, tx *parse.Tx) error
	statement string
	name      func(t *testing.T) string
}

// execFailureOf is the executor contract's failing update on r's database
// db: on a SQL engine, e1's rename with a membership write exec_tags'
// CHECK constraint refuses, which fails the relations statement; on the
// Memory engine, which holds no constraint, the rename of exec_docs' d1,
// whose stored document fails to scan as the changed row is read.
func execFailureOf(t *testing.T, b sqlBackend, r *parse.Resource[parsetest.ExecItem], db *sql.DB) execFailure {
	t.Helper()
	if b.engine != Memory {
		renaming, err := r.ParseMergePatch([]byte(`{"name":"renamed","tags":["forbidden"]}`))
		require.NoError(t, err)
		failure := execFailure{
			run: func(t *testing.T, tx *parse.Tx) error {
				scoped, err := r.In(tx)
				require.NoError(t, err)
				_, err = scoped.Update(t.Context(), renaming, parse.ByID("e1"), execScope(), parse.Increment("version"))
				return err
			},
			statement: "relations",
			name:      func(t *testing.T) string { return columnOf(t, b, db, "exec_items", "name", "id", "e1")[0] },
		}
		return failure
	}
	docs := parse.MustResource[execDoc](r.Provider(), "exec_docs")
	loadExecDocs(t, b, db)
	renaming, err := docs.ParseMergePatch([]byte(`{"name":"renamed"}`))
	require.NoError(t, err)
	failure := execFailure{
		run: func(t *testing.T, tx *parse.Tx) error {
			scoped, err := docs.In(tx)
			require.NoError(t, err)
			_, err = scoped.Update(t.Context(), renaming, parse.ByID("d1"), parse.Scope{})
			return err
		},
		statement: "owner",
		name:      func(t *testing.T) string { return columnOf(t, b, db, "exec_docs", "name", "id", "d1")[0] },
	}
	return failure
}

func execItemID(item parsetest.ExecItem) string { return item.ID }

// execMissing has a facet and a relation over a junction table no fixture
// creates: on exec_items its include is the statement that fails, and on a
// table no fixture creates every statement is.
type execMissing struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch,facet" facet:"terms:10"`
	Version   int64      `json:"version"    db:"version"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include" relation:"exec_missing_tags,item_id,tag_id,id,tenant_id" indexed:"true"`
}

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

func execPanics(p *parse.Provider) *parse.Resource[execPanicItem] {
	return parse.MustResource[execPanicItem](p, "exec_panics")
}

// callerTx runs fn in a transaction of the application's own: on a SQL
// engine, on one of db's connections, as the engine's provider takes one
// (Engine.CallerTx); on the Memory engine, one its store begins
// (Store.Begin). x reads and writes in that transaction beside the
// resources fn runs there. The transaction is rolled back after fn unless
// fn settled it.
func callerTx(t *testing.T, b sqlBackend, db *sql.DB, fn func(tx *parse.Tx, x inTx)) {
	t.Helper()
	if b.engine == Memory {
		txn, err := storeOf(t, db).Begin(t.Context())
		require.NoError(t, err)
		tx := txn.Tx()
		defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
		fn(tx, inTx{t: t, store: txn})
		return
	}
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	err = b.opener.CallerTx(t.Context(), conn, func(tx *parse.Tx, query func(stmt string) ([]string, error)) error {
		fn(tx, inTx{t: t, query: query})
		return nil
	})
	require.NoError(t, err)
}

// inTx is what a case reads and writes in the application's own
// transaction beside the resources it runs there: on a SQL engine through
// the statements it sends there, on the Memory engine through the store's
// transaction.
type inTx struct {
	t     *testing.T
	query func(stmt string) ([]string, error)
	store StoreTx
}

// values is column, as text, of each of table's rows whose by column holds
// key, in text order, read under ctx on the Memory engine; a SQL engine
// reads it with the statements the transaction runs under its own.
func (x inTx) values(ctx context.Context, table, column, by, key string) []string {
	x.t.Helper()
	if x.store == nil {
		return x.run("SELECT " + column + " FROM " + table + " WHERE " + by + " = '" + key + "' ORDER BY " + column)
	}
	rows, err := x.store.Rows(ctx, table)
	require.NoError(x.t, err)
	var out []string
	for _, row := range rows {
		if fmt.Sprint(row[by]) == key {
			out = append(out, fmt.Sprint(row[column]))
		}
	}
	slices.Sort(out)
	return out
}

// set writes value to column of table's row id, as the application's own
// write, under ctx on the Memory engine, and returns the ids of the rows it
// wrote.
func (x inTx) set(ctx context.Context, table, column, id, value string) []string {
	x.t.Helper()
	if x.store == nil {
		return x.run("UPDATE " + table + " SET " + column + " = '" + value + "' WHERE id = '" + id + "' RETURNING id")
	}
	rows, err := x.store.Rows(ctx, table)
	require.NoError(x.t, err)
	for _, row := range rows {
		if row["id"] == id {
			row[column] = value
			require.NoError(x.t, x.store.Put(ctx, table, row))
			written := []string{id}
			return written
		}
	}
	return nil
}

func (x inTx) run(stmt string) []string {
	x.t.Helper()
	out, err := x.query(stmt)
	require.NoError(x.t, err, stmt)
	return out
}

// updateAlone applies p to target with r on a connection of db's its own,
// in a transaction of the application's own that r runs in (Resource.In),
// under the settings scan holds (the planner's scan methods), sending the
// connection's PostgreSQL backend PID to pids before the update (0 when it
// has none), so a test can watch the update wait for a lock.
func updateAlone(ctx context.Context, b sqlBackend, db *sql.DB, r *parse.Resource[parsetest.ExecItem], p *patch.ParsedPatch, target parse.Target, opts []parse.UpdateOption, scan []string, pids chan<- uint32) (err error) {
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
	var pid uint32
	err = conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)
	pids <- pid
	if err != nil {
		return err
	}
	return b.opener.CallerTx(ctx, conn, func(tx *parse.Tx, _ func(string) ([]string, error)) error {
		scoped, err := r.In(tx)
		if err != nil {
			return err
		}
		_, err = scoped.Update(ctx, p, target, execScope(), opts...)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

// Every provider holds to one contract: what an update reports as changed
// and unchanged, what its membership changes count, what a failure leaves
// and names, how it runs in a transaction the application holds, and what
// a list page carries. SQLite's database reports each statement to
// dbtest.OnStatement's hook; on PostgreSQL a caller's transaction does
// (Engine.CallerTx).
func executorContract(t *testing.T, engine Engine) {
	runOn(t, engine, runExecutorContract, hooked())
}

// runExecutorContract runs every case on a fresh database of b.
func runExecutorContract(t *testing.T, b sqlBackend) {
	d := b.dialect
	timestamp := b.columnType(core.TypeInfo{Type: core.FieldTypeTimestamp})
	// setup opens a fresh database of b holding execFixture, and the
	// contract's resource on its provider.
	setup := func(t *testing.T) (*parse.Resource[parsetest.ExecItem], *sql.DB) {
		t.Helper()
		p, db := b.open(t)
		if b.engine == Memory {
			r := parsetest.ExecItems(p)
			loadExecRows(t, db)
			return r, db
		}
		dbtest.Exec(t, db, execFixture(timestamp)...)
		return parsetest.ExecItems(p), db
	}
	parsed := func(t *testing.T, r *parse.Resource[parsetest.ExecItem], body string) *patch.ParsedPatch {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		return p
	}
	// update applies body to target in t1, counting versions.
	update := func(ctx context.Context, t *testing.T, r *parse.Resource[parsetest.ExecItem], body string, target parse.Target, opts ...parse.UpdateOption) (parse.UpdateResult[parsetest.ExecItem], error) {
		t.Helper()
		return r.Update(ctx, parsed(t, r, body), target, execScope(), append([]parse.UpdateOption{parse.Increment("version")}, opts...)...)
	}
	// stored is the row of id as the table holds it, whatever its tenant
	// or deletion.
	stored := func(t *testing.T, r *parse.Resource[parsetest.ExecItem], db *sql.DB, id string) parsetest.ExecItem {
		t.Helper()
		if b.engine == Memory {
			var got []parsetest.ExecItem
			for _, row := range b.tableRows(t, db, "exec_items", "id", "tenant_id", "name", "version") {
				if row[0] == id {
					got = append(got, parsetest.ExecItem{ID: id, TenantID: columnText(t, row[1]), Name: columnText(t, row[2]), Version: asInt64(t, row[3])})
				}
			}
			require.Len(t, got, 1)
			return got[0]
		}
		got := parsetest.ResourceRows(t, r, parse.Statement{
			SQL:  `SELECT ` + parsetest.JoinColumns(r.Columns()) + ` FROM "exec_items" WHERE "exec_items"."id" = ` + d.Placeholder(1),
			Args: []any{id},
		})
		require.Len(t, got, 1)
		return got[0]
	}
	tags := func(t *testing.T, db *sql.DB, id string) []string {
		t.Helper()
		return columnOf(t, b, db, "exec_tags", "tag_id", "item_id", id)
	}
	statementOf := func(t *testing.T, err error) string {
		t.Helper()
		failed, ok := errors.AsType[*parse.StatementError](err)
		require.True(t, ok, "a *parse.StatementError: %v", err)
		return failed.Statement
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
			r, db := setup(t)
			res, err := update(t.Context(), t, r, tc.body, tc.target, tc.opts...)
			require.NoError(t, err)
			assert.Equal(t, tc.changed, sortedIDs(res.Changed, execItemID), "changed")
			assert.Equal(t, tc.unchanged, sortedIDs(res.Unchanged, execItemID), "unchanged")
			assert.Equal(t, tc.relationRows, res.RelationRows)
			e1 := stored(t, r, db, "e1")
			assert.Equal(t, tc.e1Name, e1.Name)
			assert.Equal(t, tc.e1Version, e1.Version)
			assert.Equal(t, tc.e1Tags, tags(t, db, "e1"))
			assert.Equal(t, "other", stored(t, r, db, "f1").Name, "another tenant's row stays")
		})
	}

	t.Run("a membership change that fails undoes the row change", func(t *testing.T) {
		b.sqlOnly(t, "refuses a membership write by a CHECK constraint; a memory relation write fails only past MaxRows (parsememory's ErrRowLimit tests)")
		r, db := setup(t)
		res, err := update(t.Context(), t, r, `{"name":"renamed","tags":["forbidden"]}`, parse.ByID("e1"))
		assert.Equal(t, "relations", statementOf(t, err), "every provider names the statement that failed")
		assert.Zero(t, res)
		assert.Equal(t, "one", stored(t, r, db, "e1").Name)
		assert.Equal(t, []string{"red"}, tags(t, db, "e1"))
	})

	// The resource on a missing table fails at its first statement, and a
	// list's include over a missing junction table once the page has read.
	t.Run("a statement that fails is named", func(t *testing.T) {
		b.sqlOnly(t, "fails statements on tables no fixture created, where a memory resource declares its own")
		r, _ := setup(t)
		on := func(table string) *parse.Resource[execMissing] {
			return parse.MustResource[execMissing](r.Provider(), table, parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
		}
		missing, orphans := on("exec_missing"), on("exec_items")
		rename, err := missing.ParseMergePatch([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		include, err := orphans.ParseList(url.Values{filter.ParamInclude: {"tags"}})
		require.NoError(t, err)
		facets, err := missing.ParseFacets(url.Values{filter.ParamFacets: {"name"}})
		require.NoError(t, err)
		lock := "owner"
		if b.engine == Postgres {
			lock = "lock"
		}
		for _, tc := range []struct {
			name, want string
			run        func(ctx context.Context) error
		}{
			{"a list's page", "page", func(ctx context.Context) error {
				_, err := missing.List(ctx, nil, execScope())
				return err
			}},
			{"a list's include", "include tags", func(ctx context.Context) error {
				_, err := orphans.List(ctx, include, execScope())
				return err
			}},
			{"a facets request's count", "facets", func(ctx context.Context) error {
				_, err := missing.Facets(ctx, facets, execScope())
				return err
			}},
			{"a query", "query", func(ctx context.Context) error {
				_, err := missing.Query(ctx, parse.Statement{SQL: `SELECT ` + parsetest.JoinColumns(missing.Columns()) + ` FROM "exec_missing"`})
				return err
			}},
			{"an update's owner", "owner", func(ctx context.Context) error {
				_, err := missing.Update(ctx, rename, parse.ByID("e1"), execScope())
				return err
			}},
			{"an update's lock of several rows, where the dialect locks rows", lock, func(ctx context.Context) error {
				_, err := missing.Update(ctx, rename, parse.ByIDs("e1", "e2"), execScope())
				return err
			}},
			{"a guarded update's current", "current", func(ctx context.Context) error {
				_, err := missing.Update(ctx, rename, parse.ByID("e1"), execScope(), parse.SkipUnchanged())
				return err
			}},
			{"a batch's current", "current", func(ctx context.Context) error {
				_, err := missing.UpdateBatch(ctx, []parse.RowUpdate{{Key: parse.Key{"e1"}, Patch: rename}}, execScope(), parse.UpdateBatchLimits{})
				return err
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := tc.run(t.Context())
				assert.Equal(t, tc.want, statementOf(t, err))
			})
		}
	})

	t.Run("in the caller's transaction a failure leaves it usable and its rollback undoes the rest", func(t *testing.T) {
		r, db := setup(t)
		failing := execFailureOf(t, b, r, db)
		callerTx(t, b, db, func(tx *parse.Tx, _ inTx) {
			scoped, err := r.In(tx)
			require.NoError(t, err)
			assert.Equal(t, failing.statement, statementOf(t, failing.run(t, tx)))
			res, err := update(t.Context(), t, scoped, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e2"))
			require.NoError(t, err, "the transaction survives the failed update")
			assert.Equal(t, []string{"e2"}, sortedIDs(res.Changed, execItemID))
			assert.Equal(t, int64(1), res.RelationRows)
		})
		assert.Equal(t, "two", stored(t, r, db, "e2").Name, "the caller rolled back")
		assert.Empty(t, tags(t, db, "e2"))
		assert.Equal(t, "one", failing.name(t))
	})

	t.Run("in the caller's transaction an update commits with the caller's own statements", func(t *testing.T) {
		r, db := setup(t)
		callerTx(t, b, db, func(tx *parse.Tx, x inTx) {
			assert.Equal(t, []string{"e2"}, x.set(t.Context(), "exec_items", "name", "e2", "by the application"))
			scoped, err := r.In(tx)
			require.NoError(t, err)
			res, err := update(t.Context(), t, scoped, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e1"))
			require.NoError(t, err)
			assert.Equal(t, []string{"e1"}, sortedIDs(res.Changed, execItemID))
			assert.Equal(t, []string{"renamed"}, x.values(t.Context(), "exec_items", "name", "id", "e1"), "the update is visible in the transaction")
			require.NoError(t, tx.Commit(t.Context()))
		})
		assert.Equal(t, "renamed", stored(t, r, db, "e1").Name)
		assert.Equal(t, []string{"blue"}, tags(t, db, "e1"))
		assert.Equal(t, "by the application", stored(t, r, db, "e2").Name)
	})

	t.Run("in a transaction the provider began", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			commit bool
		}{
			{"an update commits with it", true},
			{"an update rolls back with it", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r, db := setup(t)
				tx, err := r.Provider().Begin(t.Context())
				require.NoError(t, err)
				scoped, err := r.In(tx)
				require.NoError(t, err)
				res, err := update(t.Context(), t, scoped, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e1"))
				require.NoError(t, err)
				assert.Equal(t, []string{"e1"}, sortedIDs(res.Changed, execItemID))
				assert.Equal(t, int64(2), res.RelationRows)
				if tc.commit {
					require.NoError(t, tx.Commit(t.Context()))
				} else {
					require.NoError(t, tx.Rollback(t.Context()))
				}
				require.NoError(t, tx.Rollback(t.Context()), "a rollback of a settled transaction does nothing")
				want, wantTags := "one", []string{"red"}
				if tc.commit {
					want, wantTags = "renamed", []string{"blue"}
				}
				assert.Equal(t, want, stored(t, r, db, "e1").Name)
				assert.Equal(t, wantTags, tags(t, db, "e1"))
			})
		}

		t.Run("a failed update leaves it usable, and its commit keeps the rest", func(t *testing.T) {
			r, db := setup(t)
			failing := execFailureOf(t, b, r, db)
			tx, err := r.Provider().Begin(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
			scoped, err := r.In(tx)
			require.NoError(t, err)
			assert.Equal(t, failing.statement, statementOf(t, failing.run(t, tx)))
			_, err = update(t.Context(), t, scoped, `{"name":"renamed","tags":["blue"]}`, parse.ByID("e2"))
			require.NoError(t, err, "the savepoint the failed update ran under kept the transaction usable")
			require.NoError(t, tx.Commit(t.Context()))
			assert.Equal(t, "one", failing.name(t), "the failed update kept nothing")
			assert.Equal(t, []string{"red"}, tags(t, db, "e1"))
			assert.Equal(t, "renamed", stored(t, r, db, "e2").Name)
			assert.Equal(t, []string{"blue"}, tags(t, db, "e2"))
		})
	})

	// An error means nothing the update wrote is kept: a returned row that
	// fails to scan comes after the database applied the statement, so only
	// the transaction the provider settles after scanning can undo it.
	t.Run("a row that fails to scan writes nothing", func(t *testing.T) {
		r, db := setup(t)
		execDocs := parse.MustResource[execDoc](r.Provider(), "exec_docs")
		loadExecDocs(t, b, db)
		renaming, err := execDocs.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)

		_, err = execDocs.Update(t.Context(), renaming, parse.ByID("d1"), parse.Scope{})
		assert.Equal(t, "owner", statementOf(t, err))
		assert.Equal(t, []string{"one"}, columnOf(t, b, db, "exec_docs", "name", "id", "d1"))

		callerTx(t, b, db, func(tx *parse.Tx, x inTx) {
			scoped, err := execDocs.In(tx)
			require.NoError(t, err)
			_, err = scoped.Update(t.Context(), renaming, parse.ByID("d1"), parse.Scope{})
			assert.Equal(t, "owner", statementOf(t, err))
			assert.Equal(t, []string{"one"}, x.values(t.Context(), "exec_docs", "name", "id", "d1"), "the caller's transaction is usable and keeps nothing")
		})
	})

	// A context that ends while the update runs undoes it in the caller's
	// transaction, which stays usable; one that ends once every statement has
	// run leaves the update finished and reported so.
	t.Run("a cancellation in the caller's transaction is reported as what it left", func(t *testing.T) {
		b.sqlOnly(t, "cancels as a statement hook sees each SQL statement sent (dbtest.OnStatement)")
		for _, tc := range []struct {
			name    string
			trigger string
			applied bool
		}{
			{"as the membership changes are sent", "exec_tags", false},
			{"as the savepoint is released", "RELEASE SAVEPOINT", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r, db := setup(t)
				renaming := parsed(t, r, `{"name":"renamed","tags":["blue"]}`)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx = dbtest.OnStatement(ctx, func(_ context.Context, query string) {
					if strings.Contains(query, tc.trigger) {
						cancel()
					}
				})
				callerTx(t, b, db, func(tx *parse.Tx, x inTx) {
					scoped, err := r.In(tx)
					require.NoError(t, err)
					_, err = scoped.Update(ctx, renaming, parse.ByID("e1"), execScope())
					// The update's context is cancelled: what it left is read without
					// its cancellation.
					read := context.WithoutCancel(ctx)
					name := x.values(read, "exec_items", "name", "id", "e1")
					tags := x.values(read, "exec_tags", "tag_id", "item_id", "e1")
					if tc.applied {
						require.NoError(t, err)
						assert.Equal(t, []string{"renamed"}, name)
						assert.Equal(t, []string{"blue"}, tags)
						return
					}
					require.ErrorIs(t, err, context.Canceled)
					assert.Equal(t, []string{"one"}, name, "the caller's transaction is usable and keeps nothing")
					assert.Equal(t, []string{"red"}, tags)
				})
			})
		}
	})

	// Each statement that settles an update runs under the provider's settle
	// timeout, from when it is sent; every other statement runs under the
	// caller's context. SQLite's COMMIT and ROLLBACK of a transaction the
	// provider begins take no deadline, so the savepoint path is the one
	// every provider shows.
	t.Run("each statement that settles the update runs under the settle timeout", func(t *testing.T) {
		b.sqlOnly(t, "times the SQL statements that settle an update (the savepoint's release)")
		const bound = time.Hour
		p, db := b.openSettling(t, bound)
		dbtest.Exec(t, db, execFixture(timestamp)...)
		r := parsetest.ExecItems(p)
		renaming := parsed(t, r, `{"name":"renamed","tags":["blue"]}`)
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
		callerTx(t, b, db, func(tx *parse.Tx, _ inTx) {
			scoped, err := r.In(tx)
			require.NoError(t, err)
			_, err = scoped.Update(ctx, renaming, parse.ByID("e1"), execScope())
			require.NoError(t, err)
		})
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
	// the provider, which settles on the way out as it does for an error:
	// the owner statement's write is undone, the connection is free, and a
	// caller's transaction is still usable.
	t.Run("a panic as a row is read settles the update on its way out", func(t *testing.T) {
		r, db := setup(t)
		panics := execPanics(r.Provider())
		b.load(t, db, execPanicTable(), [][]any{execPanicRow()})
		renaming, err := panics.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		updating := func(r *parse.Resource[execPanicItem]) func() {
			return func() { _, _ = r.Update(t.Context(), renaming, parse.ByID("p1"), parse.Scope{}) }
		}

		assert.PanicsWithValue(t, execPanicked, updating(panics))
		// Bounded: a connection the update kept would hold SQLite's only
		// one, and a transaction it left open PostgreSQL's row lock, so the
		// write would wait for it.
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		if b.engine == Memory {
			rows := storeRows(t, execPanicTable(), [][]any{execPanicRow()})
			require.NoError(t, storeOf(t, db).Put(ctx, "exec_panics", rows...), "nothing holds the writer")
		} else {
			_, err = db.ExecContext(ctx, `UPDATE exec_panics SET name = name WHERE id = 'p1'`)
			require.NoError(t, err, "nothing holds the connection or the row")
		}
		assert.Equal(t, []string{"one"}, columnOf(t, b, db, "exec_panics", "name", "id", "p1"))

		callerTx(t, b, db, func(tx *parse.Tx, x inTx) {
			scoped, err := panics.In(tx)
			require.NoError(t, err)
			assert.PanicsWithValue(t, execPanicked, updating(scoped))
			assert.Equal(t, []string{"one"}, x.values(t.Context(), "exec_panics", "name", "id", "p1"), "the caller's transaction is usable and keeps nothing")
		})
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
		if b.engine != Postgres {
			t.Skip("only PostgreSQL locks rows: SQLite's write transactions and the memory engine's writer serialize")
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
				r, db := setup(t)
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
				// Each update parses its own patch, as two requests would.
				patches := []*patch.ParsedPatch{parsed(t, r, tc.body), parsed(t, r, tc.body)}
				opts := append([]parse.UpdateOption{parse.Increment("version")}, tc.opts...)
				targets := []parse.Target{parse.ByIDs(keys...), parse.ByIDs(reversed...)}
				scans := [][]string{
					{"SET enable_indexscan = off", "SET enable_bitmapscan = off", "SET enable_indexonlyscan = off"},
					{"SET enable_seqscan = off", "SET enable_bitmapscan = off"},
				}
				holder, err := db.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				defer func() { _ = holder.Rollback() }()
				_, err = holder.ExecContext(t.Context(), `SELECT id FROM exec_items WHERE id LIKE 'n%' FOR UPDATE`)
				require.NoError(t, err)

				pids := make(chan uint32, len(targets))
				results := make(chan error, len(targets))
				for i, target := range targets {
					go func() { results <- updateAlone(t.Context(), b, db, r, patches[i], target, opts, scans[i], pids) }()
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
				for range targets {
					require.NoError(t, <-results, "neither update is aborted as a deadlock")
				}
			})
		}
	})

	t.Run("a batch reports changed and unchanged rows and writes nothing when a target is missing", func(t *testing.T) {
		r, db := setup(t)
		batch := func(t *testing.T, rows map[string]string) (parse.UpdateResult[parsetest.ExecItem], error) {
			t.Helper()
			var updates []parse.RowUpdate
			for _, id := range slices.Sorted(func(yield func(string) bool) {
				for id := range rows {
					if !yield(id) {
						return
					}
				}
			}) {
				updates = append(updates, parse.RowUpdate{Key: parse.Key{id}, Patch: parsed(t, r, rows[id])})
			}
			return r.UpdateBatch(t.Context(), updates, execScope(), parse.UpdateBatchLimits{}, parse.SkipUnchanged(), parse.Increment("version"))
		}
		res, err := batch(t, map[string]string{"e1": `{"name":"one"}`, "e2": `{"name":"new"}`})
		require.NoError(t, err)
		assert.Equal(t, []string{"e2"}, sortedIDs(res.Changed, execItemID))
		assert.Equal(t, []string{"e1"}, sortedIDs(res.Unchanged, execItemID))
		assert.Equal(t, int64(2), stored(t, r, db, "e2").Version)

		res, err = batch(t, map[string]string{"e1": `{"name":"must-not-write"}`, "f1": `{"name":"x"}`})
		require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
		assert.Zero(t, res)
		assert.Equal(t, "one", stored(t, r, db, "e1").Name)
	})

	t.Run("a page carries its total and its includes", func(t *testing.T) {
		r, _ := setup(t)
		req, err := r.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamInclude: {"tags"}, filter.ParamSort: {"id"}})
		require.NoError(t, err)
		page := listPage(t, r, req, execScope())
		assert.Equal(t, []string{"e1", "e2"}, sortedIDs(page.Items, execItemID))
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
		assert.Equal(t, map[string]map[string][]string{"tags": {"e1": {"red"}}}, page.Relations, "an owner without pairs is omitted")
	})

	t.Run("a page of no rows carries an empty items array and its total", func(t *testing.T) {
		r, _ := setup(t)
		req, err := r.ParseList(url.Values{"name": {"none"}, filter.ParamIncludeTotal: {"true"}, filter.ParamInclude: {"tags"}})
		require.NoError(t, err)
		page := listPage(t, r, req, execScope())
		require.NotNil(t, page.Items)
		assert.Empty(t, page.Items)
		require.NotNil(t, page.PageInfo.TotalCount)
		assert.Zero(t, *page.PageInfo.TotalCount)
		got, err := json.Marshal(page)
		require.NoError(t, err)
		assert.JSONEq(t, `{"items":[],"page_info":{"has_next_page":false,"has_prev_page":false,"total_count":0}}`, string(got))
	})

	// The other resource's table does not exist, and the contract's does:
	// had any statement run, its error would be a statement's, or the update
	// would have written e1.
	t.Run("a value another resource parsed is refused before any statement", func(t *testing.T) {
		r, db := setup(t)
		elsewhere := parse.MustResource[parsetest.ExecItem](r.Provider(), "exec_items_elsewhere", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
		foreign := parsed(t, elsewhere, `{"name":"x"}`)
		req, err := elsewhere.ParseList(url.Values{})
		require.NoError(t, err)
		for _, run := range []struct {
			kind string
			run  func(ctx context.Context) error
		}{
			{"update", func(ctx context.Context) error {
				_, err := r.Update(ctx, foreign, parse.ByID("e1"), execScope())
				return err
			}},
			{"update batch", func(ctx context.Context) error {
				_, err := r.UpdateBatch(ctx, []parse.RowUpdate{{Key: parse.Key{"e1"}, Patch: foreign}}, execScope(), parse.UpdateBatchLimits{})
				return err
			}},
			{"list", func(ctx context.Context) error {
				_, err := r.List(ctx, req, execScope())
				return err
			}},
			{"facets", func(ctx context.Context) error {
				_, err := r.Facets(ctx, req, execScope())
				return err
			}},
		} {
			err := run.run(t.Context())
			require.ErrorIs(t, err, parse.ErrForeign, run.kind)
			_, ran := errors.AsType[*parse.StatementError](err)
			assert.False(t, ran, run.kind)
		}
		assert.Equal(t, "one", stored(t, r, db, "e1").Name)
	})
}
