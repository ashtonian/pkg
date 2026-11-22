//go:build unit || concurrency

package core_test

import (
	"math/big"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// No other test uses these types, so each cache sees its first use here.
type (
	firstUseID     [16]byte
	firstUseAmount struct{ units int64 }
	firstUseRecord struct {
		ID     firstUseID     `json:"id" db:"id" first_use:"record_id"`
		Amount firstUseAmount `json:"amount" db:"amount" first_use:"record_amount"`
		Name   string         `json:"name" db:"name" first_use:"record_name"`
	}
)

func (firstUseID) MarshalText() ([]byte, error) {
	return []byte("00000000-0000-0000-0000-000000000000"), nil
}
func (a *firstUseAmount) Coefficient() *big.Int { return big.NewInt(a.units) }
func (a *firstUseAmount) Exponent() int32       { return -2 }

// Schemas are built on whichever goroutines first ask for a type, so the
// per-type caches (field walks per tag pair, UUID and decimal method sets)
// must give concurrent first callers one answer.
func TestTypeCaches_ConcurrentFirstUse(t *testing.T) {
	const callers = 32
	// The second column tag creates its walk cache under the same contention.
	columnTags := []string{"db", "first_use"}
	walks := make([]*core.StructFields, callers)
	errs := make([]error, callers)
	uuids := make([]bool, callers)
	decimals := make([]bool, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-start
			walks[i], errs[i] = core.StructFieldsOf(reflect.TypeFor[firstUseRecord](), "json", columnTags[i%len(columnTags)])
			uuids[i] = core.IsUUIDType(reflect.TypeFor[firstUseID]())
			decimals[i] = core.IsDecimalType(reflect.TypeFor[firstUseAmount]())
		})
	}
	close(start)
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		assert.Same(t, walks[i%len(columnTags)], walks[i], "caller %d: one walk per type and tag pair", i)
		assert.True(t, uuids[i], "caller %d", i)
		assert.True(t, decimals[i], "caller %d", i)
	}
	assert.NotSame(t, walks[0], walks[1])
	id, ok := walks[1].External("id")
	require.True(t, ok)
	assert.Equal(t, "record_id", id.Tags.Column)
}
