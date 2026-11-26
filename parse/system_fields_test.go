//go:build unit

package parse_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

type keyedDevice struct {
	ID   uuid.UUID `json:"id"   db:"id"`
	Name string    `json:"name" db:"name" parse:"filter"`
}

// A primary key is an index by construction, so a request filtering on
// the key's system field costs as an indexed condition: three key
// equalities under $or fit the default MaxCost.
func TestKeyFilter_CostsAsIndexed(t *testing.T) {
	r := parse.MustResource[keyedDevice]("keyed_devices")
	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := r.ParseListInput(parse.ListInput{Filter: `{"$or":[{"id":"` + a + `"},{"id":"` + b + `"},{"id":"` + c + `"}]}`})
	require.NoError(t, err)
}
