//go:build unit

package core

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upper is a codec whose stored value is not its Go value.
type upper string

func (u upper) Value() (driver.Value, error) { return "UP:" + string(u), nil }

// pointerCodec has its Valuer on the pointer, as sort schema generation
// admits.
type pointerCodec struct{ n int64 }

func (p *pointerCodec) Value() (driver.Value, error) { return p.n, nil }

// chained is a Valuer whose value is another Valuer.
type chained struct{ next driver.Valuer }

func (c chained) Value() (driver.Value, error) { return c.next, nil }

// looping is a Valuer whose value is itself.
type looping struct{}

func (l looping) Value() (driver.Value, error) { return l, nil }

type failing struct{}

func (failing) Value() (driver.Value, error) { return nil, errors.New("boom") }

func TestStoredValue(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	google := googleuuid.MustParse("0198a3c2-7a1e-7c00-8000-00000000000a")
	std := uuid.MustParse("0198a3c2-7a1e-7c00-8000-00000000000a")
	price := decimal.RequireFromString("1.50")
	text := "a"
	var scanned any = &text
	for name, tc := range map[string]struct {
		in   any
		want any
		null bool
	}{
		"nil":                         {in: nil, null: true},
		"a nil pointer":               {in: (*string)(nil), null: true},
		"a pointer":                   {in: &text, want: "a"},
		"an interface pointer":        {in: &scanned, want: "a"},
		"a nil interface pointer":     {in: new(any), null: true},
		"a Valuer":                    {in: sql.NullString{String: "a", Valid: true}, want: "a"},
		"a NULL Valuer":               {in: sql.NullInt64{}, null: true},
		"a pointer to a Valuer":       {in: &sql.NullInt64{Int64: 7, Valid: true}, want: int64(7)},
		"a named string's Valuer":     {in: upper("a"), want: "UP:a"},
		"a pointer-receiver Valuer":   {in: pointerCodec{n: 3}, want: int64(3)},
		"a Valuer's Valuer":           {in: chained{next: upper("b")}, want: "UP:b"},
		"a time is kept":              {in: at, want: at},
		"a Valuer's time is kept":     {in: sql.NullTime{Time: at, Valid: true}, want: at},
		"a decimal is kept":           {in: price, want: price},
		"a Valuer UUID is kept":       {in: google, want: google},
		"a UUID is kept":              {in: std, want: std},
		"a date is kept":              {in: Date("2026-01-02"), want: Date("2026-01-02")},
		"a network value is kept":     {in: inetOf(t, "10.0.0.1"), want: inetOf(t, "10.0.0.1")},
		"a plain value is itself":     {in: int32(4), want: int32(4)},
		"a pointer to a kept decimal": {in: &price, want: price},
	} {
		t.Run(name, func(t *testing.T) {
			got, null, err := StoredValue(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.null, null)
			assert.Equal(t, tc.want, got)
		})
	}

	_, _, err := StoredValue(failing{})
	require.ErrorContains(t, err, "reading the value of core.failing: boom")
	_, _, err = StoredValue(looping{})
	require.ErrorContains(t, err, "more than 8 Valuers in a chain", "a Valuer returning itself cannot loop")
}

func inetOf(t *testing.T, s string) Inet {
	t.Helper()
	v, err := ParseInet(s)
	require.NoError(t, err)
	return v
}

// A pointer or interface that holds itself is refused by everything that
// follows one, past a bounded depth, instead of following it forever.
func TestDeref_RefusesASelfReference(t *testing.T) {
	self := new(any)
	*self = self
	for name, read := range map[string]func() error{
		"Deref":           func() error { _, err := Deref(self); return err },
		"StoredValue":     func() error { _, _, err := StoredValue(self); return err },
		"NormalizeScalar": func() error { _, err := NormalizeScalar(FieldTypeString, 0, self); return err },
		"NormalizeStored": func() error { _, err := NormalizeStored(FieldTypeFloat, 0, self); return err },
		"DecimalText":     func() error { _, err := DecimalText(self); return err },
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, read(), "more than 32 pointers and interfaces")
		})
	}
}

// Deref follows a chain up to its bound.
func TestDeref_FollowsAChain(t *testing.T) {
	text := "a"
	v := any(&text)
	for range 15 {
		held := v
		v = &held
	}
	got, err := Deref(v)
	require.NoError(t, err)
	assert.Equal(t, "a", got)
}

// A row's value reads as its column's stored value: a driver's spelling of
// it (a DATE's time.Time, a numeric's text, SQLite's 0 and 1 for a
// boolean) as the value, NULL as absent, and a value the column cannot
// hold as an error.
func TestStoredScalar(t *testing.T) {
	for _, tc := range []struct {
		name string
		t    FieldType
		kind reflect.Kind
		v    any
		want any
	}{
		{"a DATE's time.Time", FieldTypeDate, reflect.Invalid, time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC), "2026-01-02"},
		{"a TIME's time.Time", FieldTypeTime, reflect.Invalid, time.Date(0, time.January, 1, 3, 4, 5, 500000000, time.UTC), "03:04:05.5"},
		{"a numeric's text", FieldTypeInt, reflect.Int64, "42", int64(42)},
		{"SQLite's true", FieldTypeBool, reflect.Invalid, int64(1), true},
		{"SQLite's false", FieldTypeBool, reflect.Invalid, int64(0), false},
		{"a Valuer", FieldTypeString, reflect.Invalid, sql.NullString{String: "a", Valid: true}, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, present, err := StoredScalar(tc.t, tc.kind, tc.v)
			require.NoError(t, err)
			assert.True(t, present)
			assert.Equal(t, tc.want, got)
		})
	}
	_, present, err := StoredScalar(FieldTypeDate, reflect.Invalid, sql.NullTime{})
	require.NoError(t, err)
	assert.False(t, present, "NULL")
	_, _, err = StoredScalar(FieldTypeBool, reflect.Invalid, int64(2))
	require.ErrorContains(t, err, "expected boolean, got int64")
}

// A value is stored in one form, whatever Go value spells it: a timestamp
// as its canonical text, a document, a geo shape and an array as their JSON text, a
// value type as its canonical text, anything else as its driver value.
func TestStorageValue(t *testing.T) {
	instant := time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.FixedZone("", 2*60*60))
	const canonical = "2026-01-02T01:04:05.500000000Z"
	timestamps := TypeInfo{Type: FieldTypeArray, Elem: FieldTypeTimestamp}
	type named string
	for _, tc := range []struct {
		name  string
		t     TypeInfo
		value any
		want  driver.Value
	}{
		{"nil", TypeInfo{Type: FieldTypeString}, nil, nil},
		{"a nil pointer", TypeInfo{Type: FieldTypeInt}, (*int)(nil), nil},
		{"a NULL wrapper", TypeInfo{Type: FieldTypeInt}, sql.NullInt64{}, nil},
		{"a wrapper's value", TypeInfo{Type: FieldTypeInt}, sql.NullInt64{Int64: 3, Valid: true}, int64(3)},
		{"an int32", TypeInfo{Type: FieldTypeInt}, int32(7), int64(7)},
		{"a uint64 within int64", TypeInfo{Type: FieldTypeInt}, uint64(7), int64(7)},
		{"a float32", TypeInfo{Type: FieldTypeFloat}, float32(0.1), float64(float32(0.1))},
		{"a named string", TypeInfo{Type: FieldTypeString}, named("x"), "x"},
		{"a pointer to a string", TypeInfo{Type: FieldTypeString}, new("x"), "x"},
		{"a decimal", TypeInfo{Type: FieldTypeDecimal}, decimal.RequireFromString("1.50"), "1.5"},
		{"a UUID", TypeInfo{Type: FieldTypeUUID}, googleuuid.MustParse("0190A5B4-7C1E-7D8A-9B2C-3D4E5F6A7B8C"), "0190a5b4-7c1e-7d8a-9b2c-3d4e5f6a7b8c"},
		{"a timestamp in an offset", TypeInfo{Type: FieldTypeTimestamp}, instant, canonical},
		{"a timestamp's RFC 3339 text", TypeInfo{Type: FieldTypeTimestamp}, "2026-01-02T03:04:05.5+02:00", canonical},
		{"a valid NullTime", TypeInfo{Type: FieldTypeTimestamp}, sql.NullTime{Time: instant, Valid: true}, canonical},
		{"a date", TypeInfo{Type: FieldTypeDate}, Date("2026-01-02"), "2026-01-02"},
		{"an interval", TypeInfo{Type: FieldTypeInterval}, Interval{Microseconds: 5_400_000_000}, "PT1H30M"},
		{"a map", TypeInfo{Type: FieldTypeJSON}, map[string]any{"b": 1, "a": "<"}, `{"a":"\u003c","b":1}`},
		{"a nil map", TypeInfo{Type: FieldTypeJSON}, map[string]any(nil), `null`},
		{"raw JSON, compacted", TypeInfo{Type: FieldTypeJSON}, json.RawMessage(`{"a": 1}`), `{"a":1}`},
		{"a struct", TypeInfo{Type: FieldTypeObject}, struct{ W int }{W: 2}, `{"W":2}`},
		{"a geo point", TypeInfo{Type: FieldTypeGeo}, map[string]float64{"lon": 2, "lat": 1}, `{"lat":1,"lon":2}`},
		{"a geo point's raw JSON, compacted", TypeInfo{Type: FieldTypeGeo}, json.RawMessage(`{"lat": 1, "lon": 2}`), `{"lat":1,"lon":2}`},
		{"a declared json Valuer, as its JSON", TypeInfo{Type: FieldTypeJSON, SQLType: SQLJSON}, upper("a"), `"a"`},
		{"an array", TypeInfo{Type: FieldTypeArray, Elem: FieldTypeString}, []string{"a", "b"}, `["a","b"]`},
		{"an empty array", TypeInfo{Type: FieldTypeArray, Elem: FieldTypeString}, []string{}, `[]`},
		{"a nil array", TypeInfo{Type: FieldTypeArray, Elem: FieldTypeString}, []string(nil), nil},
		{"timestamps, each in UTC", timestamps, []time.Time{instant, instant.UTC()}, `["` + canonical + `","` + canonical + `"]`},
		{"timestamps holding NULL", timestamps, []*time.Time{&instant, nil}, `["` + canonical + `",null]`},
		{"a pointer to timestamps", timestamps, &[]time.Time{instant}, `["` + canonical + `"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StorageValue(tc.t, tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := StorageValue(TypeInfo{Type: FieldTypeInt}, uint64(1<<63))
	require.Error(t, err, "an unsigned integer above int64 has no driver value")
	_, err = StorageValue(TypeInfo{Type: FieldTypeTimestamp}, instant.String())
	require.Error(t, err, "the driver's own text is no RFC 3339 timestamp")
	_, err = StorageValue(timestamps, []string{"yesterday"})
	require.ErrorContains(t, err, "timestamp element 0")
}
