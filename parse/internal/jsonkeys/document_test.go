//go:build unit

package jsonkeys

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cyclePointer points to itself: no document decodes into one past null.
type cyclePointer *cyclePointer

type cycleDoc struct {
	P cyclePointer `json:"p"`
	N int          `json:"n"`
}

// A pointer type that leads back to itself holds no member, so a document
// of it, or one holding it, is checked without following it forever.
func TestValidate_PointerTypeCycle(t *testing.T) {
	var p cyclePointer
	require.NoError(t, Validate([]byte(`null`), &p))
	assert.NoError(t, decodeAndValidate(t, `{"p":null,"n":1}`, reflect.TypeFor[cycleDoc]()))
	require.EqualError(t, decodeAndValidate(t, `{"p":null,"N":1}`, reflect.TypeFor[cycleDoc]()), `JSON member "/N" is noncanonical for jsonkeys.cycleDoc; use "n"`)
	assert.NoError(t, CheckDecodable(reflect.TypeFor[cyclePointer]()))
	assert.NoError(t, CheckDecodable(reflect.TypeFor[cycleDoc]()))
}

// Decoding replaces each byte of invalid UTF-8 in a member name with
// U+FFFD, so two names that differ only there name one map key, and a
// struct member is matched by its decoded name, as the implementation
// Validate replaced judged them.
func TestValidate_InvalidUTF8MemberNames(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		typ  reflect.Type
		err  string
	}{
		{name: "one invalid name", doc: "{\"\xff\":1}", typ: reflect.TypeFor[map[string]int]()},
		{name: "two invalid names decoding alike", doc: "{\"\xff\":1,\"\xfe\":2}", typ: reflect.TypeFor[map[string]int](), err: "duplicate JSON map member"},
		{name: "an invalid name and its decoding", doc: "{\"\xff\":1,\"�\":2}", typ: reflect.TypeFor[map[string]int](), err: "duplicate JSON map member"},
		{name: "struct member named once", doc: "{\"a\xff\":1}", typ: reflect.TypeFor[struct {
			A int `json:"a�"`
		}]()},
		{name: "struct member named twice", doc: "{\"a\xff\":1,\"a\xfe\":2}", typ: reflect.TypeFor[struct {
			A int `json:"a�"`
		}](), err: "duplicate JSON member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fresh := reflect.New(tt.typ)
			require.NoError(t, json.Unmarshal([]byte(tt.doc), fresh.Interface()))
			err := Validate([]byte(tt.doc), fresh.Interface())
			reference := referenceValidate([]byte(tt.doc), fresh.Interface())
			if tt.err == "" {
				require.NoError(t, reference, "the reference")
				assert.NoError(t, err)
				return
			}
			require.Error(t, reference, "the reference")
			assert.ErrorContains(t, err, tt.err)
		})
	}
}
