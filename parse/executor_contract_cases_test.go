//go:build unit || integration

package parse_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsesql"
)

type execItem struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include,patch" relation:"exec_tags,item_id,tag_id,id,tenant_id" indexed:"true"`
}

var execItems = parse.MustResource[execItem]("exec_items", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))

var execScope = parse.Scope{Tenant: "t1"}

// execFixture: t1 holds e1 ("one", tagged red), e2 ("two") and a deleted
// e3; t2 holds f1. A junction row may not name the tag "forbidden".
func execFixture(timestamp string) []string {
	return []string{
		`CREATE TABLE exec_items (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, version BIGINT NOT NULL DEFAULT 1, deleted_at ` + timestamp + `)`,
		`CREATE TABLE exec_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL CHECK (tag_id <> 'forbidden'), tenant_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id, tenant_id))`,
		`INSERT INTO exec_items (id, tenant_id, name, deleted_at) VALUES ('e1','t1','one',NULL), ('e2','t1','two',NULL), ('e3','t1','gone','2026-01-01T00:00:00Z'), ('f1','t2','other',NULL)`,
		`INSERT INTO exec_tags VALUES ('e1','red','t1')`,
	}
}

func execIDs(items []execItem) []string {
	var out []string
	for _, item := range items {
		out = append(out, item.ID)
	}
	slices.Sort(out)
	return out
}

// execDoc's counts decode into a map of integers, so a stored document of
// another shape is a row that fails to scan.
type execDoc struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Name   string         `json:"name"   db:"name"   parse:"filter,patch"`
	Counts map[string]int `json:"counts" db:"counts"`
}

var execDocs = parse.MustResource[execDoc]("exec_docs")

// sqlCancelOn is a caller's transaction whose context is cancelled as the
// first statement holding trigger is sent.
type sqlCancelOn struct {
	parsesql.DB
	trigger string
	cancel  context.CancelFunc
}

func (c sqlCancelOn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, c.trigger) {
		c.cancel()
	}
	return c.DB.ExecContext(ctx, query, args...)
}

func (c sqlCancelOn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, c.trigger) {
		c.cancel()
	}
	return c.DB.QueryContext(ctx, query, args...)
}

// pgxCancelOn is sqlCancelOn for a caller's pgx.Tx: the context is cancelled
// as a batch or statement holding trigger is sent.
type pgxCancelOn struct {
	pgx.Tx
	trigger string
	cancel  context.CancelFunc
}

func (c pgxCancelOn) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, q := range b.QueuedQueries {
		if strings.Contains(q.SQL, c.trigger) {
			c.cancel()
		}
	}
	return c.Tx.SendBatch(ctx, b)
}

func (c pgxCancelOn) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, c.trigger) {
		c.cancel()
	}
	return c.Tx.Exec(ctx, query, args...)
}

// runExecutorContract holds the dialect's executor to one contract: what
// an update reports as changed and unchanged, what its membership changes
// count, what a failure leaves, and what a list page carries. open returns
// a fresh database for each case.
func runExecutorContract(t *testing.T, open func(*testing.T) *sql.DB, d filter.Dialect, timestamp string) {
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := open(t)
		for _, stmt := range execFixture(timestamp) {
			_, err := db.Exec(stmt)
			require.NoError(t, err, stmt)
		}
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
		var out []string
		for _, row := range queryRows(t, db, `SELECT tag_id FROM exec_tags WHERE item_id = `+d.Placeholder(1)+` ORDER BY tag_id`, []any{id}) {
			out = append(out, fmt.Sprintf("%s", row[0]))
		}
		return out
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
			assert.Equal(t, tc.changed, execIDs(res.Changed), "changed")
			assert.Equal(t, tc.unchanged, execIDs(res.Unchanged), "unchanged")
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
		require.Error(t, err)
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
			assert.Equal(t, []string{"e2"}, execIDs(res.Changed))
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
		db := setup(t)
		document := "TEXT"
		if isPostgres(d) {
			document = "JSONB"
		}
		for _, stmt := range []string{
			`CREATE TABLE exec_docs (id TEXT PRIMARY KEY, name TEXT NOT NULL, counts ` + document + `)`,
			`INSERT INTO exec_docs VALUES ('d1', 'one', '{"x":"not a number"}')`,
		} {
			_, err := db.Exec(stmt)
			require.NoError(t, err, stmt)
		}
		p, err := execDocs.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		renaming, err := execDocs.BuildUpdate(d, p, parse.ByID("d1"), parse.Scope{})
		require.NoError(t, err)
		for _, inTx := range []bool{false, true} {
			_, err = applyUpdate(t, db, execDocs, d, renaming, inTx)
			require.Error(t, err, "in the caller's transaction: %v", inTx)
			var name string
			require.NoError(t, db.QueryRow(`SELECT name FROM exec_docs WHERE id = 'd1'`).Scan(&name))
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
				require.NoError(t, onExecutor(t, db, d, true, func(tx parsesql.DB) error {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					_, err := parsesql.Update(ctx, sqlCancelOn{DB: tx, trigger: tc.trigger, cancel: cancel}, execItems, renaming)
					var name string
					require.NoError(t, tx.(*sql.Tx).QueryRowContext(t.Context(), `SELECT name FROM exec_items WHERE id = 'e1'`).Scan(&name), "the caller's transaction is usable")
					var tags []string
					rows, qerr := tx.QueryContext(t.Context(), `SELECT tag_id FROM exec_tags WHERE item_id = 'e1' ORDER BY tag_id`)
					require.NoError(t, qerr)
					for rows.Next() {
						var tag string
						require.NoError(t, rows.Scan(&tag))
						tags = append(tags, tag)
					}
					require.NoError(t, rows.Close())
					check(t, err, name, tags)
					return nil
				}, func(tx parsepgx.DB) error {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					_, err := parsepgx.Update(ctx, pgxCancelOn{Tx: tx.(pgx.Tx), trigger: tc.trigger, cancel: cancel}, execItems, renaming)
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
		assert.Equal(t, []string{"e2"}, execIDs(res.Changed))
		assert.Equal(t, []string{"e1"}, execIDs(res.Unchanged))
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
		assert.Equal(t, []string{"e1", "e2"}, execIDs(page.Items))
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
		require.Error(t, err)
		if isPostgres(d) {
			assert.ErrorIs(t, err, parsepgx.ErrDialect)
		} else {
			assert.ErrorIs(t, err, parsesql.ErrDialect)
		}
		_, err = queryPage(t, db, execItems, d, func() *parse.ListPlan {
			list, err := execItems.BuildList(other, nil, execScope)
			require.NoError(t, err)
			return list
		}())
		require.Error(t, err)
		assert.Equal(t, "one", stored(t, db, "e1").Name)
	})
}
