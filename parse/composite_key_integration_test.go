//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type ckStock struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
	Name        string `json:"name"         db:"name"         parse:"filter,sort"`
}

func ckStockResource(t *testing.T) *parse.Resource[ckStock] {
	t.Helper()
	r, err := parse.NewResource[ckStock]("ck_stock", parse.PrimaryKey("warehouse_id", "sku"))
	require.NoError(t, err)
	return r
}

// ckStockDB holds every warehouse's every SKU: either key column alone, the
// quantity and the name all tie across rows, so only the whole key orders
// and names one.
func ckStockDB(t *testing.T, db *sql.DB, d filter.Dialect) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `CREATE TABLE ck_stock (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty BIGINT NOT NULL, name TEXT NOT NULL, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	insert := "INSERT INTO ck_stock VALUES (" + d.Placeholder(1) + ", " + d.Placeholder(2) + ", " + d.Placeholder(3) + ", " + d.Placeholder(4) + ")"
	i := 0
	for _, w := range []string{"w1", "w2", "w3"} {
		for _, sku := range []string{"a", "b", "c"} {
			_, err := db.ExecContext(t.Context(), insert, w, sku, int64(i%2), []string{"x", "y"}[i%3%2])
			require.NoError(t, err)
			i++
		}
	}
}

func TestCompositeKeyPaging(t *testing.T) {
	runSQLBackends(t, testCompositeKeyPaging)
}

// Every sort, ascending and descending, pages every row once at every page
// size, forwards and back.
func testCompositeKeyPaging(t *testing.T, b sqlBackend) {
	db := b.open(t)
	d := b.dialect
	r := ckStockResource(t)
	ckStockDB(t, db, d)
	const rows = 9
	page := func(t *testing.T, sort string, size int, cursor string) *filter.Page[ckStock] {
		t.Helper()
		q := url.Values{filter.ParamPageSize: {strconv.Itoa(size)}}
		if sort != "" {
			q.Set(filter.ParamSort, sort)
		}
		if cursor != "" {
			q.Set(filter.ParamCursor, cursor)
		}
		req, err := r.ParseList(q)
		require.NoError(t, err)
		plan, err := r.BuildList(d, req, parse.Scope{})
		require.NoError(t, err)
		return listPage(t, db, r, d, plan)
	}
	keyOf := func(s ckStock) string { return s.WarehouseID + "/" + s.SKU }
	for _, field := range []string{"", "warehouse_id", "sku", "qty", "name"} {
		for _, sort := range []string{field, "-" + field} {
			if sort == "-" {
				continue
			}
			want := page(t, sort, rows, "").Items
			require.Len(t, want, rows)
			distinct := map[string]bool{}
			for _, s := range want {
				distinct[keyOf(s)] = true
			}
			require.Len(t, distinct, rows, "sort %q", sort)
			for size := 1; size <= rows; size++ {
				t.Run(fmt.Sprintf("sort=%q/size=%d", sort, size), func(t *testing.T) {
					var forward []ckStock
					var last *filter.Page[ckStock]
					cursor := ""
					for range rows + 1 {
						last = page(t, sort, size, cursor)
						forward = append(forward, last.Items...)
						if !last.PageInfo.HasNextPage {
							break
						}
						cursor = string(last.PageInfo.EndCursor)
					}
					assert.Equal(t, want, forward)

					backward := []ckStock{}
					for cursor = string(last.PageInfo.StartCursor); cursor != ""; {
						prev := page(t, sort, size, cursor)
						backward = append(append([]ckStock{}, prev.Items...), backward...)
						cursor = ""
						if prev.PageInfo.HasPrevPage {
							cursor = string(prev.PageInfo.StartCursor)
						}
					}
					assert.Equal(t, want[:rows-len(last.Items)], backward, "walking back from the last page")
				})
			}
		}
	}
}

func TestCompositeKeyUpdates(t *testing.T) {
	runSQLBackends(t, testCompositeKeyUpdates)
}

// An update by key, by keys and a batch each write exactly the rows their
// keys name, though every row shares each key column with others.
func testCompositeKeyUpdates(t *testing.T, b sqlBackend) {
	open := b.open
	d := b.dialect
	r := ckStockResource(t)
	quantities := func(t *testing.T, db *sql.DB) map[string]int64 {
		t.Helper()
		out := map[string]int64{}
		for _, row := range queryRows[[]any](t, db, "SELECT warehouse_id, sku, qty FROM ck_stock") {
			out[fmt.Sprint(row[0])+"/"+fmt.Sprint(row[1])] = asInt64(t, row[2])
		}
		return out
	}
	written := func(t *testing.T, db *sql.DB, before map[string]int64) []string {
		t.Helper()
		var keys []string
		for k, v := range quantities(t, db) {
			if v != before[k] {
				keys = append(keys, k)
			}
		}
		return keys
	}
	setQty, err := r.ParseMergePatch([]byte(`{"qty":70}`))
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		target parse.Target
		want   []string
	}{
		{"by key", parse.ByKey(parse.Key{"w2", "b"}), []string{"w2/b"}},
		{"by keys", parse.ByKeys(parse.Key{"w1", "c"}, parse.Key{"w3", "a"}, parse.Key{"w9", "a"}), []string{"w1/c", "w3/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := open(t)
			ckStockDB(t, db, d)
			before := quantities(t, db)
			plan, err := r.BuildUpdate(d, setQty, tc.target, parse.Scope{})
			require.NoError(t, err)
			res, err := applyUpdate(t, db, r, d, plan, false)
			require.NoError(t, err)
			assert.Len(t, res.Changed, len(tc.want))
			assert.ElementsMatch(t, tc.want, written(t, db, before))
		})
	}

	t.Run("batch", func(t *testing.T) {
		db := open(t)
		ckStockDB(t, db, d)
		before := quantities(t, db)
		other, err := r.ParseMergePatch([]byte(`{"qty":80}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{
			{Key: parse.Key{"w3", "b"}, Patch: other},
			{Key: parse.Key{"w1", "b"}, Patch: setQty},
		}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		res, err := applyUpdateBatch(t, db, r, d, plan)
		require.NoError(t, err)
		assert.Len(t, res.Changed, 2)
		after := quantities(t, db)
		assert.ElementsMatch(t, []string{"w1/b", "w3/b"}, written(t, db, before))
		assert.Equal(t, int64(70), after["w1/b"])
		assert.Equal(t, int64(80), after["w3/b"])
	})
}
