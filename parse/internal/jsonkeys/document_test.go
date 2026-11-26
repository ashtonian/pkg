//go:build unit

package jsonkeys

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/shapes"
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

// Validate reads a document strictly first and again as decoding reads it
// when the strict read refuses it; its answer is the permissive read's,
// whatever the document holds and wherever its invalid UTF-8 sits.
func TestValidate_StrictReadAnswersAsThePermissiveRead(t *testing.T) {
	docs := []string{
		`{"x":1,"ints":{"7":1},"Names":{"a":{"y":1}}}`,
		`{"X":1}`,
		`{"ints":{"07":1}}`,
		`{"ints":{"7":1,"7":2}}`,
		`{"x":1,"x":2}`,
		`{"Names":{"\u0061":null,"a":null}}`,
		"{\"Names\":{\"\xff\":null}}",
		"{\"Names\":{\"\xff\":null,\"\xfe\":null}}",
		"{\"X\":1,\"Names\":{\"\xff\":null}}",
		"{\"Names\":{\"\xff\":null},\"X\":1}",
		"{\"raw\":\"\xff\",\"X\":1}",
		"{\"x\":\"\xff\"}",
		`{"Names":{"\ud800":null,"\udc00":null}}`,
		`{"Names":{"\ud800":null},"X":1}`,
		`{"x":1`,
		`{"x":1}{}`,
		"{\"x\":1,\"\xff",
	}
	for _, doc := range docs {
		decoded := new(keyDoc)
		want := permissiveValidate(t, []byte(doc), decoded)
		got := Validate([]byte(doc), decoded)
		if want == nil {
			require.NoError(t, got, "%q", doc)
			continue
		}
		require.EqualError(t, got, want.Error(), "%q", doc)
	}
}

// permissiveValidate is Validate reading only as decoding reads.
func permissiveValidate(t *testing.T, raw []byte, decoded any) error {
	t.Helper()
	shape, err := shapes.Document(reflect.TypeOf(decoded).Elem())
	require.NoError(t, err)
	v, _ := keyValidators.Get().(*keyValidator)
	defer v.release()
	return v.validate(raw, shape, false)
}

// textInner reaches textDecoded's field V only through an unexported
// embedded pointer, which decoding into a fresh value cannot allocate.
type textInner struct {
	V string `json:"v"`
}

// textDecoded decodes itself from a JSON string through UnmarshalText, so
// encoding/json never reads its fields.
type textDecoded struct {
	*textInner
}

func (d *textDecoded) UnmarshalText(text []byte) error {
	d.textInner = &textInner{V: string(text)}
	return nil
}

type textDoc struct {
	T textDecoded `json:"t"`
	N int         `json:"n"`
}

// A type that decodes itself through UnmarshalText is opaque, as one with
// JSON methods is: encoding/json reads it from a string and never sets the
// field the unexported pointer hides.
func TestCheckDecodable_TextUnmarshalerDecodesItself(t *testing.T) {
	var decoded textDoc
	require.NoError(t, json.Unmarshal([]byte(`{"t":"x","n":1}`), &decoded))
	assert.Equal(t, "x", decoded.T.V)
	require.NoError(t, CheckDecodable(reflect.TypeFor[textDoc]()))
	require.NoError(t, Validate([]byte(`{"t":"x","n":1}`), &decoded))
}
