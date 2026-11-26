//go:build unit

package parsehttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

type stockRow struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id"`
	SKU         string `json:"sku"          db:"sku"`
	Bin         int32  `json:"bin"          db:"bin"`
	Qty         int    `json:"qty"          db:"qty" parse:"patch"`
}

// keyColumns is a Keyed of any column names, such as a column mapper's,
// which a resource built in a test cannot declare.
type keyColumns []string

func (k keyColumns) PrimaryKeyColumns() []string { return k }

func (keyColumns) ParseKey(texts ...string) (parse.Key, error) {
	key := make(parse.Key, len(texts))
	for i, text := range texts {
		key[i] = text
	}
	return key, nil
}

func TestItemPattern(t *testing.T) {
	tests := []struct {
		name    string
		res     parsehttp.Keyed
		want    string
		wantErr string
	}{
		{name: "one-column key", res: parse.MustResource[stockRow]("stock", parse.PrimaryKey("sku")), want: "{sku}"},
		{name: "key of several columns", res: parse.MustResource[stockRow]("stock", parse.PrimaryKey("warehouse_id", "sku")), want: "{warehouse_id}/{sku}"},
		{name: "column that is no wildcard name", res: keyColumns{"warehouse_id", "order-id"}, wantErr: `key column "order-id" is no ServeMux wildcard name`},
		{name: "no key column", res: keyColumns{}, wantErr: "the resource has no key column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsehttp.ItemPattern(tt.res)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ItemKey reads the key a ServeMux route registered with ItemPattern
// matched: a segment per key column, each unescaped on its own, read as its
// column's text.
func TestItemKey(t *testing.T) {
	stock := parse.MustResource[stockRow]("stock", parse.PrimaryKey("warehouse_id", "sku"))
	bins := parse.MustResource[stockRow]("bins", parse.PrimaryKey("warehouse_id", "bin"))
	skus := parse.MustResource[stockRow]("skus", parse.PrimaryKey("sku"))

	type result struct {
		key parse.Key
		err error
	}
	var got *result
	mux := http.NewServeMux()
	route := func(collection string, res parsehttp.Keyed) {
		pattern, err := parsehttp.ItemPattern(res)
		require.NoError(t, err)
		mux.HandleFunc("PATCH /"+collection+"/"+pattern, func(_ http.ResponseWriter, r *http.Request) {
			key, err := parsehttp.ItemKey(r, res)
			got = &result{key, err}
		})
	}
	route("stock", stock)
	route("bins", bins)
	route("skus", skus)
	// A route without the key's wildcards: only a server defect reaches it.
	mux.HandleFunc("PATCH /stale/{warehouse_id}", func(_ http.ResponseWriter, r *http.Request) {
		key, err := parsehttp.ItemKey(r, stock)
		got = &result{key, err}
	})

	tests := []struct {
		name      string
		target    string
		want      parse.Key
		wantErr   error
		errColumn string
		unrouted  bool
	}{
		{name: "a segment per column", target: "/stock/w1/a", want: parse.Key{"w1", "a"}},
		{name: "an escaped slash stays in its segment", target: "/stock/w1/a%2Fb", want: parse.Key{"w1", "a/b"}},
		{name: "each segment unescaped on its own", target: "/stock/w%201/%25", want: parse.Key{"w 1", "%"}},
		{name: "one-column key", target: "/skus/a%2Fb", want: parse.Key{"a/b"}},
		{name: "integer column read from its digits", target: "/bins/w1/42", want: parse.Key{"w1", int64(42)}},
		{name: "integer column that cannot hold its text", target: "/bins/w1/x", wantErr: parse.ErrNoSuchKey, errColumn: "key column bin"},
		{name: "integer column out of its kind's range", target: "/bins/w1/2147483648", wantErr: parse.ErrNoSuchKey, errColumn: "key column bin"},
		{name: "a segment holding U+0000", target: "/stock/w1/a%00b", wantErr: parse.ErrNoSuchKey, errColumn: "key column sku"},
		{name: "a segment of invalid UTF-8", target: "/stock/w%FF/a", wantErr: parse.ErrNoSuchKey, errColumn: "key column warehouse_id"},
		{name: "route without a key column's wildcard", target: "/stale/w1", wantErr: parsehttp.ErrNoKeyValue, errColumn: "key column sku"},
		{name: "fewer segments than key columns", target: "/stock/w1", unrouted: true},
		{name: "more segments than key columns", target: "/stock/w1/a/b", unrouted: true},
		{name: "an empty last segment", target: "/stock/w1/", unrouted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got = nil
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPatch, tt.target, nil))
			if tt.unrouted {
				assert.Nil(t, got, "the item route matches no path of another shape")
				assert.Equal(t, http.StatusNotFound, rec.Code)
				return
			}
			require.NotNil(t, got, "routed")
			if tt.wantErr != nil {
				require.ErrorIs(t, got.err, tt.wantErr)
				require.ErrorContains(t, got.err, tt.errColumn)
				assert.Nil(t, got.key)
				return
			}
			require.NoError(t, got.err)
			assert.Equal(t, tt.want, got.key)
		})
	}
}
