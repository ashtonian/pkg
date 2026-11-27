//go:build unit

package core_test

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/jsonkeys"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

// Document types encoding/json reads and writes, that parse's row walk
// refuses: their names come from the json tag alone.
type (
	lockedDoc struct {
		sync.Mutex `json:"-"`
		A          int `json:"a"`
	}
	foreignTagDoc struct {
		A int `json:"a" min:"abc"`
	}
	tieX1  struct{ X int }
	tieX2  struct{ X int }
	tieDoc struct {
		tieX1
		tieX2
		Y int `json:"y"`
	}
	documentHolder struct {
		ID      string                    `json:"id" db:"id"`
		Locked  map[string]*lockedDoc     `json:"locked" db:"locked" parse:"patch"`
		Foreign map[string]*foreignTagDoc `json:"foreign" db:"foreign" parse:"patch"`
		Ties    map[string]*tieDoc        `json:"ties" db:"ties" parse:"patch"`
	}
)

// A document type is named the same wherever it is stored: as a map value
// it builds, validates as a scan validates it, and takes a merge patch.
func TestDocumentTypes_AsMapValuesScanValidateAndMergePatch(t *testing.T) {
	resource, err := parse.NewResource[documentHolder](providertest.Compile(filter.SQLiteDialect{}), "holders")
	require.NoError(t, err)

	for _, typ := range []reflect.Type{
		reflect.TypeFor[map[string]*lockedDoc](), reflect.TypeFor[map[string]*foreignTagDoc](), reflect.TypeFor[map[string]*tieDoc](),
	} {
		raw := []byte(`{"k":{"a":1,"y":2}}`)
		fresh := reflect.New(typ)
		require.NoError(t, json.Unmarshal(raw, fresh.Interface()))
		assert.NoError(t, jsonkeys.Validate(raw, fresh.Interface()), "%s", typ)
	}

	parsed, err := resource.ParseMergePatch([]byte(`{"locked":{"k":{"a":2}},"foreign":{"k":{"a":3}},"ties":{"k":{"y":4}}}`))
	require.NoError(t, err)
	out, err := resource.ApplyValidatedPatch(parsed, documentHolder{
		Locked:  map[string]*lockedDoc{"k": {A: 1}},
		Foreign: map[string]*foreignTagDoc{"k": {A: 1}},
		Ties:    map[string]*tieDoc{"k": {Y: 1}},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, out.Locked["k"].A)
	assert.Equal(t, 3, out.Foreign["k"].A)
	assert.Equal(t, 4, out.Ties["k"].Y)

	// encoding/json drops both fields that claim "X", so a merge cannot
	// name it.
	_, err = resource.ParseMergePatch([]byte(`{"ties":{"k":{"X":1}}}`))
	assert.ErrorContains(t, err, `unknown field "X"`)
}
