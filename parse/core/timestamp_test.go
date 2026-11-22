//go:build unit

package core_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Every timestamp normalizes to text of one width that normalizes to
// itself, so where timestamps are compared as text, text order is time
// order.
func TestNormalizeScalar_TimestampsAreCanonical(t *testing.T) {
	for _, in := range []any{
		"2026-04-01T02:00:00+02:00",
		"2026-04-01T00:00:00.5Z",
		"2026-04-01T00:00:00.123456789-23:59",
		"0001-01-01T00:00:00Z",
		"0001-01-01T00:00:00-23:59",
		"9999-12-31T23:59:59.999999999Z",
		"9999-12-31T00:00:00+23:59",
		time.Date(2026, 4, 1, 2, 0, 0, 500, time.FixedZone("x", 2*3600)),
		time.Time{},
	} {
		got, err := core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, in)
		require.NoError(t, err, "%v", in)
		text, _ := got.(string)
		assert.Len(t, text, 30, "%v", in)
		again, err := core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, text)
		require.NoError(t, err, "%s", text)
		assert.Equal(t, text, again)
	}
}

func TestNormalizeScalar_RefusesTimestampsOutsideRFC3339AndTheFourDigitYears(t *testing.T) {
	inputs := []any{
		"9999-12-31T23:59:59-01:00",       // UTC year 10000
		"0000-01-01T00:00:00Z",            // year zero
		"0001-01-01T00:00:00+14:00",       // UTC year zero
		"2025-01-01T00:00:00,5Z",          // a comma fraction
		"2025-01-01T3:04:05Z",             // a one-digit hour
		"2025-01-01T00:00:00+24:00",       // an offset of a day
		"2025-01-01T00:00:00.1234567891Z", // a tenth fractional digit
		"2025-01-01T00:00:00+01:60",       // an offset minute of 60
		"2025-01-01T00:00:00.Z",
		"2025-01-01T00:00:00",
		"2025-01-01t00:00:00z",
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(0, 12, 31, 23, 0, 0, 0, time.UTC),
	}
	for _, in := range inputs {
		_, err := core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, in)
		assert.Error(t, err, "%v", in)
	}

	type event struct {
		ID string    `json:"id" db:"id"`
		At time.Time `json:"at" db:"at" parse:"filter,patch"`
	}
	schema, err := filter.GenerateFilterSchema[event](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := filter.NewFilterParser(schema, nil)
	for _, in := range inputs[:8] {
		_, err := parser.ParseMap(map[string]any{"at": map[string]any{"$gte": in}})
		var perr *core.ParseError
		require.True(t, errors.As(err, &perr), "%v: %v", in, err)
		assert.Equal(t, core.CodeValueTypeMismatch.String(), perr.Code, "%v: %s", in, perr.Message)
	}

	events, err := parse.NewResource[event]("events")
	require.NoError(t, err)
	for _, in := range inputs[:8] {
		_, err := events.MergePatchMap(map[string]any{"at": in})
		var perr *core.ParseError
		require.True(t, errors.As(err, &perr), "%v: %v", in, err)
		assert.Equal(t, core.CodePatchValueInvalid.String(), perr.Code, "%v: %s", in, perr.Message)
	}
}
