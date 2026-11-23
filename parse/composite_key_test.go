//go:build unit

package parse_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsesql"
)

type stockRow struct {
	WarehouseID string     `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string     `json:"sku"          db:"sku"          parse:"filter,sort"`
	TenantID    string     `json:"tenant_id"    db:"tenant_id"`
	Qty         int64      `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
	CountedAt   time.Time  `json:"counted_at"   db:"counted_at"`
	DeletedAt   *time.Time `json:"deleted_at"   db:"deleted_at"`
}

func stockRows(t *testing.T) *parse.Resource[stockRow] {
	t.Helper()
	r, err := parse.NewResource[stockRow]("stock_rows", parse.PrimaryKey("warehouse_id", "sku"),
		parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

// The key's columns are declared once, each a column T reads as it is
// stored; the tenant column may be one of them.
func TestPrimaryKey_RefusesBadKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []parse.ResourceOption
		want string
	}{
		{"no column", []parse.ResourceOption{parse.PrimaryKey()}, "the key names no column"},
		{"repeated", []parse.ResourceOption{parse.PrimaryKey("sku", "sku")}, `key column "sku" is listed twice`},
		{"unknown", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "bin")}, `key column "bin" is not a column of parse_test.stockRow`},
		{"computed", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.ReadExpr("sku", parse.SQL("'x'"))}, `computed column "sku"`},
		{"unread", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.ReadColumns("warehouse_id", "qty")}, `the read columns omit the key column "sku"`},
		{"soft-delete column", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.DeletedColumn("sku")}, `deleted column "sku" is a key column`},
		{"timestamp in a key of two", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "counted_at")},
			`key column "counted_at" is timestamp; a key of several columns holds string, UUID and integer columns`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[stockRow]("stock_rows", tc.opts...)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	r, err := parse.NewResource[stockRow]("stock_rows", parse.PrimaryKey("tenant_id", "sku"), parse.TenantColumn("tenant_id"))
	require.NoError(t, err, "the tenant column is a key column")
	assert.Equal(t, []string{"tenant_id", "sku"}, r.PrimaryKeyColumns())
}

func TestCompositeKey_ReadsAndDeclaresTheKey(t *testing.T) {
	r := stockRows(t)
	assert.Equal(t, []string{"warehouse_id", "sku"}, r.PrimaryKeyColumns())
	assert.Equal(t, []string{"warehouse_id", "sku"}, r.FilterSchema().PrimaryKey)
	for _, item := range []any{stockRow{WarehouseID: "w1", SKU: "a"}, &stockRow{WarehouseID: "w1", SKU: "a"}} {
		key, err := r.Key(item)
		require.NoError(t, err)
		assert.Equal(t, parse.Key{"w1", "a"}, key)
	}
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, r))
	assert.Equal(t, []string{"warehouse_id", "sku"}, reg.SchemaFor("stock_rows").Key)
	for _, name := range []string{"warehouse_id", "sku"} {
		assert.Contains(t, reg.SchemaFor("stock_rows").Filter.Fields, name, "every key column is a request field")
	}
}

// One key is an AND of the columns' equalities, several one bound set; a
// key of another length than the key's is ErrKeyInvalid, stating both.
func TestCompositeKey_UpdateTargets(t *testing.T) {
	r := stockRows(t)
	p, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}

	plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByKey(parse.Key{"w1", "a"}), scope)
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `WHERE ("stock_rows"."tenant_id" = $2 AND "stock_rows"."deleted_at" IS NULL AND ("stock_rows"."warehouse_id" = $3 AND "stock_rows"."sku" = $4))`)
	assert.Equal(t, []any{int64(7), "t1", "w1", "a"}, plan.Owner.Args)

	plan, err = r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByKeys(parse.Key{"w1", "a"}, parse.Key{"w2", "b"}), scope)
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `("stock_rows"."warehouse_id", "stock_rows"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock_rows", $3) AS "k")`)
	assert.Len(t, plan.Owner.Args, 3, "the set binds one parameter however many keys it holds")

	for _, tc := range []struct {
		name   string
		target parse.Target
		want   string
	}{
		{"ByID", parse.ByID("w1"), "1 values for the 2 key columns (warehouse_id, sku)"},
		{"ByIDs", parse.ByIDs("w1", "w2"), "target key 0: not a value of the key column's type: 1 values for the 2 key columns (warehouse_id, sku)"},
		{"long key", parse.ByKey(parse.Key{"w1", "a", "x"}), "3 values for the 2 key columns"},
		{"mistyped column", parse.ByKey(parse.Key{"w1", 5}), "key column sku: expected string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.BuildUpdate(filter.PostgresDialect{}, p, tc.target, scope)
			require.ErrorIs(t, err, parse.ErrKeyInvalid)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	_, err = r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByKeys(), scope)
	assert.ErrorIs(t, err, parse.ErrNoTarget)
}

func stockDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE stock_rows (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, tenant_id TEXT NOT NULL,
		qty INTEGER NOT NULL, counted_at TEXT, deleted_at TEXT, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	// Every warehouse holds every SKU, so either column alone matches two
	// rows and only the pair names one.
	for _, row := range [][2]string{{"w1", "a"}, {"w1", "b"}, {"w2", "a"}, {"w2", "b"}} {
		_, err := db.Exec(`INSERT INTO stock_rows (warehouse_id, sku, tenant_id, qty, counted_at) VALUES (?, ?, 't1', 0, '2026-01-02T03:04:05Z')`, row[0], row[1])
		require.NoError(t, err)
	}
	return db
}

func stockQuantities(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT warehouse_id, sku, qty FROM stock_rows`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]int64{}
	for rows.Next() {
		var w, sku string
		var qty int64
		require.NoError(t, rows.Scan(&w, &sku, &qty))
		out[w+"/"+sku] = qty
	}
	require.NoError(t, rows.Err())
	return out
}

// An update by key, by keys and a batch each change exactly the rows their
// keys name.
func TestCompositeKey_UpdatesReachTheirRows(t *testing.T) {
	ctx := context.Background()
	r := stockRows(t)
	d := filter.SQLiteDialect{}
	scope := parse.Scope{Tenant: "t1"}
	seven, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)

	db := stockDB(t)
	plan, err := r.BuildUpdate(d, seven, parse.ByKey(parse.Key{"w1", "b"}), scope)
	require.NoError(t, err)
	res, err := parsesql.Update(ctx, db, r, plan)
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 0, "w2/b": 0}, stockQuantities(t, db))

	db = stockDB(t)
	plan, err = r.BuildUpdate(d, seven, parse.ByKeys(parse.Key{"w1", "b"}, parse.Key{"w2", "a"}, parse.Key{"w9", "a"}), scope)
	require.NoError(t, err)
	res, err = parsesql.Update(ctx, db, r, plan)
	require.NoError(t, err)
	assert.Len(t, res.Changed, 2)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 7, "w2/b": 0}, stockQuantities(t, db))

	db = stockDB(t)
	one, err := r.ParseMergePatch([]byte(`{"qty":1}`))
	require.NoError(t, err)
	batch, err := r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{"w2", "a"}, Patch: one}, {Key: parse.Key{"w1", "b"}, Patch: seven}}, scope, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Len(t, batch.Chunks, 1)
	assert.Equal(t, []parse.Key{{"w1", "b"}, {"w2", "a"}}, batch.Chunks[0].Keys, "rows lock in key order")
	assert.Contains(t, batch.Chunks[0].Current.SQL, `ORDER BY "stock_rows"."warehouse_id" COLLATE BINARY, "stock_rows"."sku" COLLATE BINARY`)
	res, err = parsesql.UpdateBatch(ctx, db, r, batch)
	require.NoError(t, err)
	assert.Len(t, res.Changed, 2)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 1, "w2/b": 0}, stockQuantities(t, db))

	assert.NoError(t, batch.Chunks[0].CheckCurrent([]parse.Key{{"w2", "a"}, {"w1", "b"}}))
	assert.ErrorIs(t, batch.Chunks[0].CheckCurrent([]parse.Key{{"w2", "b"}, {"w1", "a"}}), parse.ErrUpdateBatchTargets, "the columns of two keys crossed are neither")
	_, err = r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{"w1", "b"}, Patch: one}, {Key: parse.Key{"w1", "b"}, Patch: seven}}, scope, parse.UpdateBatchLimits{})
	assert.ErrorContains(t, err, "update batch row 1 key: duplicate key")
}
