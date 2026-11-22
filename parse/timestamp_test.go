//go:build unit

package parse_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Timestamps on SQLite: a column holding each row's canonical text,
// declared TEXT or DATETIME. modernc.org/sqlite scans the DATETIME column's
// text into a time.Time of the same instant.
func TestTimestamps_SQLite(t *testing.T) {
	for _, atType := range []string{"TEXT", "DATETIME"} {
		t.Run(atType, func(t *testing.T) {
			db := dbtest.SQLite(t)
			runTimestamps(t, timestampDB{db: db, dialect: filter.SQLiteDialect{}, atType: atType})
			if atType != "DATETIME" {
				return
			}
			schema, err := filtertest.TimestampSchema()
			require.NoError(t, err)
			var at time.Time
			require.NoError(t, db.QueryRow("SELECT at FROM "+schema.Name+" WHERE id = 'a'").Scan(&at))
			assert.True(t, filtertest.TimestampRecords()[0].At.Equal(at), "scanned %v", at)
		})
	}
}

// The same instant written through SQLiteDialect.MarshalValue in any form
// is one canonical text, so SQLite stores and compares instants.
func TestSQLiteDialect_MarshalValueWritesCanonicalTimestamps(t *testing.T) {
	instant := time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.FixedZone("", 2*60*60))
	const canonical = "2026-01-02T01:04:05.500000000Z"
	info := core.TypeInfo{Type: core.FieldTypeTimestamp}
	for _, tc := range []struct {
		name  string
		value any
		want  any
	}{
		{"a time", instant, canonical},
		{"a pointer", &instant, canonical},
		{"a valid NullTime", sql.NullTime{Time: instant, Valid: true}, canonical},
		{"RFC 3339 text in an offset", "2026-01-02T03:04:05.5+02:00", canonical},
		{"nil", nil, nil},
		{"a nil pointer", (*time.Time)(nil), nil},
		{"a NULL NullTime", sql.NullTime{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filter.SQLiteDialect{}.MarshalValue(tc.value, info)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := filter.SQLiteDialect{}.MarshalValue(instant.String(), info)
	assert.Error(t, err, "the driver's own text is no RFC 3339 timestamp")

	list := core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeTimestamp}
	got, err := filter.SQLiteDialect{}.MarshalValue([]time.Time{instant, instant.UTC()}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`","`+canonical+`"]`, got, "each element in UTC, as an element reads")
	got, err = filter.SQLiteDialect{}.MarshalValue([]*time.Time{&instant, nil}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`",null]`, got)
	got, err = filter.SQLiteDialect{}.MarshalValue([]time.Time(nil), list)
	require.NoError(t, err)
	assert.Equal(t, `null`, got, "a nil list is JSON null")
	got, err = filter.SQLiteDialect{}.MarshalValue(&[]time.Time{instant}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`"]`, got, "a pointer to a list")
}
