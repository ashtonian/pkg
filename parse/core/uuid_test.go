//go:build unit

package core_test

import (
	"encoding/hex"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type md5Sum [16]byte

// hexSum writes its bytes in hexadecimal: 32 characters a UUID parser
// would read as an undashed UUID.
type hexSum [16]byte

func (h hexSum) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(h[:])), nil }

// pointerUUID writes UUID text through its pointer.
type pointerUUID [16]byte

func (u *pointerUUID) MarshalText() ([]byte, error) { return []byte(uuid.UUID(*u).String()), nil }

type failingText [16]byte

func (failingText) MarshalText() ([]byte, error) { return nil, errors.New("no text") }

// parse imports no UUID package. A UUID type is a named 16-byte array that
// writes a UUID's text — the standard library's and github.com/google/uuid's
// alike; one that writes other text is that text, and a bare 16-byte array
// is binary.
func TestUUIDTypes_AreRecognizedByWhatTheyWrite(t *testing.T) {
	for name, tc := range map[string]struct {
		typ  reflect.Type
		want core.FieldType
	}{
		"standard library":              {reflect.TypeFor[uuid.UUID](), core.FieldTypeUUID},
		"google/uuid":                   {reflect.TypeFor[googleuuid.UUID](), core.FieldTypeUUID},
		"pointer":                       {reflect.TypeFor[*uuid.UUID](), core.FieldTypeUUID},
		"UUID text through the pointer": {reflect.TypeFor[pointerUUID](), core.FieldTypeUUID},
		"hexadecimal hash":              {reflect.TypeFor[hexSum](), core.FieldTypeString},
		"ULID":                          {reflect.TypeFor[ulid](), core.FieldTypeString},
		"text that fails":               {reflect.TypeFor[failingText](), core.FieldTypeAny},
		"[16]byte":                      {reflect.TypeFor[[16]byte](), core.FieldTypeAny},
		"named, no MarshalText":         {reflect.TypeFor[md5Sum](), core.FieldTypeAny},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, core.TypeOf(tc.typ).Type)
			base := tc.typ
			for base.Kind() == reflect.Pointer {
				base = base.Elem()
			}
			assert.Equal(t, tc.want == core.FieldTypeUUID, core.IsUUIDType(base))
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

// ulid is a 16-byte identifier written as 26 Crockford base32 characters:
// the shape of a UUID type, and not one.
type ulid [16]byte

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func (u ulid) MarshalText() ([]byte, error) {
	out := make([]byte, 26)
	var acc uint64
	bits, n := 0, 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(u[i]) << bits
		for bits += 8; bits >= 5 && n > 0; bits -= 5 {
			out[n] = crockford[acc&31]
			acc >>= 5
			n--
		}
	}
	out[0] = crockford[acc&31]
	return out, nil
}

func (u *ulid) UnmarshalText(text []byte) error {
	if len(text) != 26 {
		return errors.New("a ULID is 26 characters")
	}
	var acc uint64
	bits, n := 0, 15
	for i := 25; i >= 0; i-- {
		d := strings.IndexByte(crockford, text[i])
		if d < 0 {
			return errors.New("a ULID is Crockford base32")
		}
		acc |= uint64(d) << bits
		for bits += 5; bits >= 8 && n >= 0; bits -= 8 {
			u[n] = byte(acc)
			acc >>= 8
			n--
		}
	}
	return nil
}

type ulidEvent struct {
	ID   ulid   `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter"`
}

// A UUID type is one whose text is a UUID's; a 16-byte identifier with
// text of its own filters, sorts and pages by that text.
func TestUUIDTypes_AreRecognizedByBehaviour(t *testing.T) {
	assert.False(t, core.IsUUIDType(reflect.TypeFor[ulid]()))
	assert.Equal(t, core.FieldTypeString, core.TypeOf(reflect.TypeFor[ulid]()).Type)

	var first, second ulid
	require.NoError(t, first.UnmarshalText([]byte("01ARZ3NDEKTSV4RRFFQ69G5FAV")))
	require.NoError(t, second.UnmarshalText([]byte("01ARZ3NDEKTSV4RRFFQ69G5FAW")))
	text, _ := first.MarshalText()
	require.Equal(t, "01ARZ3NDEKTSV4RRFFQ69G5FAV", string(text))

	events, err := parse.NewResource[ulidEvent]("events")
	require.NoError(t, err)
	req, err := events.ParseList(url.Values{"id": {"01ARZ3NDEKTSV4RRFFQ69G5FAV"}})
	require.NoError(t, err)
	plan, err := events.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.Args, "01ARZ3NDEKTSV4RRFFQ69G5FAV")

	req, err = events.ParseList(url.Values{filter.ParamPageSize: {"1"}})
	require.NoError(t, err)
	plan, err = events.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)
	page, err := events.Page([]ulidEvent{{ID: first}, {ID: second}}, plan)
	require.NoError(t, err)
	require.True(t, page.PageInfo.HasNextPage)
	next, err := events.ParseList(url.Values{filter.ParamCursor: {string(page.PageInfo.EndCursor)}})
	require.NoError(t, err)
	plan, err = events.BuildList(filter.SQLiteDialect{}, next, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.Args, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "the seek is past the first row's text")
}
