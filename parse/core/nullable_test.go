//go:build unit

package core_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	"time"
	stduuid "uuid"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
)

// nilPanicScanner panics scanning NULL, as a Scan that asserts its source's
// type does.
type nilPanicScanner struct{ n int64 }

func (s *nilPanicScanner) Scan(src any) error {
	s.n = src.(int64)
	return nil
}

func (s nilPanicScanner) Value() (driver.Value, error) { return s.n, nil }

// readOnlyScanner scans NULL but writes nothing.
type readOnlyScanner struct{ valid bool }

func (s *readOnlyScanner) Scan(src any) error {
	s.valid = src != nil
	return nil
}

// refusingValuer scans NULL and then refuses to write it.
type refusingValuer struct{ valid bool }

func (s *refusingValuer) Scan(src any) error {
	s.valid = src != nil
	return nil
}

func (s refusingValuer) Value() (driver.Value, error) {
	if !s.valid {
		return nil, errors.New("refusingValuer: no value")
	}
	return "x", nil
}

// flaggedNull's zero value writes 0, and only a NULL scan marks it NULL: a
// field cleared to its zero value would write 0 where SQL wrote NULL.
type flaggedNull struct {
	n    int64
	null bool
}

func (f *flaggedNull) Scan(src any) error {
	f.null = src == nil
	if n, ok := src.(int64); ok {
		f.n = n
	}
	return nil
}

func (f flaggedNull) Value() (driver.Value, error) {
	if f.null {
		return nil, nil
	}
	return f.n, nil
}

func TestIsNullableType(t *testing.T) {
	tests := []struct {
		name string
		t    reflect.Type
		want bool
	}{
		{"pointer", reflect.TypeFor[*int64](), true},
		{"slice", reflect.TypeFor[[]string](), true},
		{"map", reflect.TypeFor[map[string]any](), true},
		{"interface", reflect.TypeFor[any](), true},
		{"sql.NullInt64", reflect.TypeFor[sql.NullInt64](), true},
		{"sql.NullTime", reflect.TypeFor[sql.NullTime](), true},
		{"sql.Null[string]", reflect.TypeFor[sql.Null[string]](), true},
		{"google uuid.NullUUID", reflect.TypeFor[uuid.NullUUID](), true},
		{"decimal.NullDecimal", reflect.TypeFor[decimal.NullDecimal](), true},
		{"int64", reflect.TypeFor[int64](), false},
		{"string", reflect.TypeFor[string](), false},
		{"time.Time", reflect.TypeFor[time.Time](), false},
		// Scans NULL as the nil UUID, which it writes back.
		{"google uuid.UUID", reflect.TypeFor[uuid.UUID](), false},
		{"standard uuid.UUID", reflect.TypeFor[stduuid.UUID](), false},
		{"decimal.Decimal", reflect.TypeFor[decimal.Decimal](), false},
		{"core.Date", reflect.TypeFor[core.Date](), false},
		{"core.LocalTime", reflect.TypeFor[core.LocalTime](), false},
		{"core.Interval", reflect.TypeFor[core.Interval](), false},
		{"core.Inet", reflect.TypeFor[core.Inet](), false},
		{"core.CIDR", reflect.TypeFor[core.CIDR](), false},
		{"core.MACAddr", reflect.TypeFor[core.MACAddr](), false},
		{"a Scan that panics on NULL", reflect.TypeFor[nilPanicScanner](), false},
		{"a Scanner without a Valuer", reflect.TypeFor[readOnlyScanner](), false},
		{"a Valuer that refuses NULL", reflect.TypeFor[refusingValuer](), false},
		{"a zero value that writes a value", reflect.TypeFor[flaggedNull](), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.IsNullableType(tt.t))
			assert.Equal(t, tt.want, core.IsNullableType(tt.t), "cached")
		})
	}
}
