//go:build unit

package core_test

import (
	"reflect"
	"testing"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type md5Sum [16]byte

// parse imports no UUID package. A UUID type is a named
// 16-byte array that marshals to text — the standard library's and
// github.com/google/uuid's alike; a bare 16-byte array is binary.
func TestUUIDTypes_AreRecognizedByShape(t *testing.T) {
	for name, tc := range map[string]struct {
		typ  reflect.Type
		uuid bool
	}{
		"standard library":      {reflect.TypeFor[uuid.UUID](), true},
		"google/uuid":           {reflect.TypeFor[googleuuid.UUID](), true},
		"pointer":               {reflect.TypeFor[*uuid.UUID](), true},
		"[16]byte":              {reflect.TypeFor[[16]byte](), false},
		"named, no MarshalText": {reflect.TypeFor[md5Sum](), false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.uuid, core.TypeOf(tc.typ).Type == core.FieldTypeUUID)
		})
	}
	elem := core.TypeOf(reflect.TypeFor[[]uuid.UUID]())
	assert.Equal(t, core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeUUID}, elem)
}

func TestUUIDValues_NormalizeToCanonicalText(t *testing.T) {
	const canonical = "0198a3c2-7a1e-7c00-8000-000000000001"
	for name, v := range map[string]any{
		"standard library": uuid.MustParse(canonical),
		"google/uuid":      googleuuid.MustParse(canonical),
		"upper-case text":  "0198A3C2-7A1E-7C00-8000-000000000001",
		"braced text":      "{" + canonical + "}",
		"urn text":         "urn:uuid:" + canonical,
		"undashed text":    "0198a3c27a1e7c008000000000000001",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := core.NormalizeScalar(core.FieldTypeUUID, reflect.Invalid, v)
			require.NoError(t, err)
			assert.Equal(t, canonical, got)
		})
	}
	_, err := core.NormalizeScalar(core.FieldTypeUUID, reflect.Invalid, "not-a-uuid")
	assert.Error(t, err)

	text, ok := core.UUIDText(googleuuid.MustParse(canonical))
	assert.True(t, ok)
	assert.Equal(t, canonical, text)
	_, ok = core.UUIDText(md5Sum{})
	assert.False(t, ok)
}

func BenchmarkUUIDText(b *testing.B) {
	for name, v := range map[string]any{
		"standard library": uuid.MustParse("0198a3c2-7a1e-7c00-8000-000000000001"),
		"google/uuid":      googleuuid.MustParse("0198a3c2-7a1e-7c00-8000-000000000001"),
		"string":           "0198a3c2-7a1e-7c00-8000-000000000001",
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				core.UUIDText(v)
			}
		})
	}
}
