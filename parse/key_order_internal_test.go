//go:build unit

package parse

import (
	"cmp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
)

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
