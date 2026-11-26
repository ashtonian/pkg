//go:build unit

package core_test

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
)

type (
	marshalsV1    struct{}
	unmarshalsV1  struct{}
	marshalsV2    struct{}
	unmarshalsV2  struct{}
	ownFields     struct{ A int }
	marshalsV2Ptr struct{}
)

func (marshalsV1) MarshalJSON() ([]byte, error)                 { return []byte(`""`), nil }
func (*unmarshalsV1) UnmarshalJSON([]byte) error                { return nil }
func (marshalsV2) MarshalJSONTo(*jsontext.Encoder) error        { return nil }
func (*unmarshalsV2) UnmarshalJSONFrom(*jsontext.Decoder) error { return nil }
func (*marshalsV2Ptr) MarshalJSONTo(*jsontext.Encoder) error    { return nil }

// encoding/json honours the methods of both versions, on the value or its
// pointer, and so does the predicate every reader of a JSON document
// consults.
func TestCustomJSON_EitherVersionEitherDirection(t *testing.T) {
	for _, tt := range []struct {
		typ              reflect.Type
		custom, marshals bool
	}{
		{reflect.TypeFor[marshalsV1](), true, true},
		{reflect.TypeFor[unmarshalsV1](), true, false},
		{reflect.TypeFor[marshalsV2](), true, true},
		{reflect.TypeFor[unmarshalsV2](), true, false},
		{reflect.TypeFor[marshalsV2Ptr](), true, true},
		{reflect.TypeFor[ownFields](), false, false},
		{reflect.TypeFor[map[string]any](), false, false},
		{nil, false, false},
	} {
		name := "nil"
		if tt.typ != nil {
			name = tt.typ.String()
		}
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.custom, core.CustomJSON(tt.typ), "CustomJSON")
			assert.Equal(t, tt.marshals, core.MarshalsJSON(tt.typ), "MarshalsJSON")
		})
	}
}
