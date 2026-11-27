//go:build unit

package parse

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// textStoring is a dialect that stores the values of the types it names as
// text a row scan decodes, and every other value as the driver's own.
type textStoring map[core.FieldType]bool

func (textStoring) Name() string { return "provider" }

func (s textStoring) StoresAsText(t core.FieldType) bool { return s[t] }

// TestRowDecodeOf_DecodesOnlyTheTextItReads: a scan decodes the text a
// dialect stores a timestamp, a document or an array as, leaves every
// other value to the driver where the dialect stores it as its own, and
// refuses a dialect that stores one of another type as text, which no
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
				require.ErrorContains(t, err, "dialect provider stores")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
