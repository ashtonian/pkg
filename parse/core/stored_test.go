//go:build unit

package core

import (
	"database/sql"
	"database/sql/driver"
	"errors"
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
