//go:build unit

package parse

import (
	"testing"

	"github.com/stretchr/testify/require"

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
