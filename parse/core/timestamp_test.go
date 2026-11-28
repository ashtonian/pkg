//go:build unit

package core_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
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

// Text already in the canonical form is read without being parsed, so
// each is checked against what parsing and formatting it again gives.
func TestNormalizeScalar_CanonicalTimestampsNormalizeToThemselves(t *testing.T) {
	for _, in := range []string{
		"0001-01-01T00:00:00.000000000Z",
		"9999-12-31T23:59:59.999999999Z",
		"2024-02-29T12:30:45.000000001Z", // a leap year
		"2000-02-29T00:00:00.000000000Z", // a leap year divisible by 400
		"2026-04-30T23:59:59.500000000Z",
		"2026-01-31T00:00:00.000000000Z",
	} {
		parsed, err := time.Parse(time.RFC3339Nano, in)
		require.NoError(t, err, in)
		require.Equal(t, in, core.FormatTimestamp(parsed), in)
		got, err := core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, in)
		require.NoError(t, err, in)
		assert.Equal(t, in, got)
	}
}

// A canonical text's last day of each month agrees with time.Parse's,
// leap years and their century exceptions included.
func TestNormalizeScalar_CanonicalTimestampsEndEachMonthAsTimeParseDoes(t *testing.T) {
	for _, year := range []int{1, 4, 100, 400, 1900, 2000, 2023, 2024, 2100, 9996, 9999} {
		for month := 1; month <= 12; month++ {
			for day := 28; day <= 32; day++ {
				in := fmt.Sprintf("%04d-%02d-%02dT00:00:00.000000000Z", year, month, day)
				_, parseErr := time.Parse(time.RFC3339Nano, in)
				_, err := core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, in)
				assert.Equal(t, parseErr == nil, err == nil, "%s: time.Parse: %v, NormalizeScalar: %v", in, parseErr, err)
			}
		}
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
		"2025-02-29T00:00:00.000000000Z", // the canonical width, a day out of range
		"2025-01-01T24:00:00.000000000Z", // the canonical width, an hour out of range
		"0000-01-01T00:00:00.000000000Z", // the canonical width, year zero
		"2025-00-01T00:00:00.000000000Z", // the canonical width, month zero
		"2025-13-01T00:00:00.000000000Z", // the canonical width, a month out of range
		"2025-01-00T00:00:00.000000000Z", // the canonical width, day zero
		"2025-04-31T00:00:00.000000000Z", // the canonical width, a day April does not have
		"2100-02-29T00:00:00.000000000Z", // the canonical width, a century that is no leap year
		"2025-01-01T00:60:00.000000000Z", // the canonical width, a minute out of range
		"2025-01-01T00:00:60.000000000Z", // the canonical width, a leap second
		"2025-01-01T00:00:00.00000000aZ", // the canonical width, a letter in the fraction
		"2025-01-01T00:00:00.000000000z", // the canonical width, a lowercase zone
		"2025-01-01 00:00:00.000000000Z", // the canonical width, a space for the T
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

	events, err := parse.NewResource[event](providertest.Compile(filter.SQLiteDialect{}), "events")
	require.NoError(t, err)
	for _, in := range inputs[:8] {
		_, err := events.MergePatchMap(map[string]any{"at": in})
		var perr *core.ParseError
		require.True(t, errors.As(err, &perr), "%v: %v", in, err)
		assert.Equal(t, core.CodePatchValueInvalid.String(), perr.Code, "%v: %s", in, perr.Message)
	}
}
