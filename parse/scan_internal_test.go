//go:build unit

package parse

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type decodedJSON struct {
	Value string
	Calls int
}

var errJSONDecode = errors.New("custom JSON decoder rejected value")

func (v *decodedJSON) UnmarshalJSON(raw []byte) error {
	v.Calls++
	if err := json.Unmarshal(raw, &v.Value); err != nil {
		return err
	}
	if v.Value == "reject" {
		return errJSONDecode
	}
	return nil
}

func columnOf(target any) jsonColumn { return jsonColumn{target: reflect.ValueOf(target).Elem()} }

func TestJSONColumn_ReplacesOnlyAfterDecodeAndKeyValidation(t *testing.T) {
	value := map[int]string{9: "retained"}
	scan := columnOf(&value)
	require.NoError(t, scan.Scan(`{"1":"one"}`))
	assert.Equal(t, map[int]string{1: "one"}, value)
	for _, input := range []string{`{"01":"lossy"}`, `{"1":"one","01":"collision"}`, `{"1":"one","1":"duplicate"}`, `{"2":`} {
		require.Error(t, scan.Scan(input))
		assert.Equal(t, map[int]string{1: "one"}, value)
	}
	require.Error(t, scan.Scan(`null`))
	assert.Equal(t, map[int]string{1: "one"}, value)
	require.NoError(t, scan.Scan(nil))
	assert.Nil(t, value)
	assert.ErrorContains(t, scan.Scan(7), "JSON scan source must be string or []byte, got int")
}

func TestJSONColumn_CustomDecoderAndPresence(t *testing.T) {
	value := decodedJSON{Value: "old", Calls: 7}
	scan := columnOf(&value)
	require.NoError(t, scan.Scan([]byte(`"accepted"`)))
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)
	require.ErrorIs(t, scan.Scan(`"reject"`), errJSONDecode)
	assert.Equal(t, decodedJSON{Value: "accepted", Calls: 1}, value)

	var raw json.RawMessage
	require.NoError(t, columnOf(&raw).Scan(`null`))
	assert.Equal(t, json.RawMessage(`null`), raw)
	var dynamic any = "retained"
	require.Error(t, columnOf(&dynamic).Scan(`null`))
	assert.Equal(t, "retained", dynamic)
	var pointer = new("retained")
	require.Error(t, columnOf(&pointer).Scan(`null`))
	assert.Equal(t, "retained", *pointer)
	require.NoError(t, columnOf(&pointer).Scan(nil))
	assert.Nil(t, pointer)
}

// textStoring is a provider's storage that holds the values of the types it
// names as text a row scan decodes, and every other value as the driver's
// own.
type textStoring map[core.FieldType]bool

func (textStoring) Name() string { return "texty" }

func (s textStoring) StoresAsText(t core.FieldType) bool { return s[t] }

// TestRowDecodeOf_DecodesOnlyTheTextItReads: a scan decodes the text a
// provider stores a timestamp, a document or an array as, leaves every
// other value to the driver where the provider stores it as its own, and
// refuses a provider that stores one of another type as text, which no
// decoder reads.
func TestRowDecodeOf_DecodesOnlyTheTextItReads(t *testing.T) {
	all := textStoring{core.FieldTypeTimestamp: true, core.FieldTypeObject: true, core.FieldTypeJSON: true, core.FieldTypeArray: true, core.FieldTypeInt: true, core.FieldTypeString: true}
	for _, tc := range []struct {
		name    string
		t       reflect.Type
		json    bool
		d       textStoring
		want    rowDecode
		refused bool
	}{
		{name: "a declared JSON column", t: reflect.TypeFor[string](), json: true, d: textStoring{}, want: decodeJSON},
		{name: "a timestamp stored as text", t: reflect.TypeFor[time.Time](), d: all, want: decodeTimestamp},
		{name: "a document stored as text", t: reflect.TypeFor[map[string]int](), d: all, want: decodeJSON},
		{name: "raw JSON stored as text", t: reflect.TypeFor[json.RawMessage](), d: all, want: decodeJSON},
		{name: "an array stored as text", t: reflect.TypeFor[[]string](), d: all, want: decodeJSON},
		{name: "a scanner the driver fills", t: reflect.TypeFor[core.Interval](), d: all, want: decodeDriver},
		{name: "a timestamp stored as itself", t: reflect.TypeFor[time.Time](), d: textStoring{}, want: decodeDriver},
		{name: "a document stored as itself", t: reflect.TypeFor[map[string]int](), d: textStoring{}, want: decodeDriver},
		{name: "an integer stored as text", t: reflect.TypeFor[int](), d: all, refused: true},
		{name: "a string stored as text", t: reflect.TypeFor[string](), d: all, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rowDecodeOf(tc.t, tc.json, tc.d)
			if tc.refused {
				require.ErrorContains(t, err, "provider texty stores")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
