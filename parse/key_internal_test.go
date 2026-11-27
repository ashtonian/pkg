//go:build unit

package parse

import (
	"cmp"
	"testing"

	"github.com/ashtonian/pkg/parse/internal/ordertest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A batch chunk's preimage holds exactly its keys: the rows of two keys
// with their columns crossed are neither key.
func TestUpdateBatchChunk_CheckCurrentComparesWholeKeys(t *testing.T) {
	type stock struct {
		WarehouseID string `json:"warehouse_id" db:"warehouse_id"`
		SKU         string `json:"sku"          db:"sku"`
		Qty         int64  `json:"qty"          db:"qty"          parse:"patch"`
	}
	r, err := NewResource[stock](compiling(filter.SQLiteDialect{}), "stock_rows", PrimaryKey("warehouse_id", "sku"))
	require.NoError(t, err)
	qty, err := r.ParseMergePatch([]byte(`{"qty":1}`))
	require.NoError(t, err)
	batch, err := r.BuildUpdateBatch([]RowUpdate{{Key: Key{"w2", "a"}, Patch: qty}, {Key: Key{"w1", "b"}, Patch: qty}}, Scope{}, UpdateBatchLimits{})
	require.NoError(t, err)
	require.Len(t, batch.Chunks, 1)
	chunk := batch.Chunks[0]

	require.NoError(t, chunk.checkCurrent([]Key{{"w2", "a"}, {"w1", "b"}}))
	require.ErrorIs(t, chunk.checkCurrent([]Key{{"w2", "b"}, {"w1", "a"}}), ErrUpdateBatchTargets)
	require.ErrorIs(t, chunk.checkCurrent([]Key{{"w2", "a"}, {"w2", "a"}}), ErrUpdateBatchTargets, "one key twice")
	require.ErrorIs(t, chunk.checkCurrent([]Key{{"w2", "a"}}), ErrUpdateBatchTargets, "a key missing")
}

// A key orders and is identified by its columns' one order: keys sort as
// each type's case orders its values (the order an update batch locks rows
// in), in every spelling, and the spellings of one value are one key.
func TestKeyColumns_CanonicalOrder(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			kc := keyColumns{names: []string{"k"}, types: []core.TypeInfo{c.Type}}
			for i, x := range c.Values {
				for j, y := range c.Values {
					for _, a := range x.Spellings {
						for _, b := range y.Spellings {
							got, err := kc.compare(Key{a}, Key{b})
							require.NoError(t, err, "%#v vs %#v", a, b)
							assert.Equal(t, cmp.Compare(i, j), got, "%#v vs %#v", a, b)

							idA, err := kc.identity(Key{a})
							require.NoError(t, err, "%#v", a)
							idB, err := kc.identity(Key{b})
							require.NoError(t, err, "%#v", b)
							assert.Equal(t, i == j, idA == idB, "%#v and %#v", a, b)
						}
					}
				}
			}
		})
	}
}
