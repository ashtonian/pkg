//go:build unit

package filter_test

import (
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// day is a codec that writes a date as its text.
type day struct{ year, month, day int }

func (d day) Value() (driver.Value, error) {
	return time.Date(d.year, time.Month(d.month), d.day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), nil
}

// A date binds as its column holds it, as a timestamp does: a Valuer as
// the date it writes, a NULL one and a nil pointer as NULL.
func TestMarshalValue_BindsADateAsItsColumnHoldsIt(t *testing.T) {
	midnight := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	date := core.TypeInfo{Type: core.FieldTypeDate}
	for _, tc := range []struct {
		name  string
		d     filter.Dialect
		bound any
	}{
		{"SQLite", filter.SQLiteDialect{}, "2026-01-02"},
		{"PostgreSQL", filter.PostgresDialect{}, midnight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range []any{core.Date("2026-01-02"), midnight, sql.NullTime{Time: midnight, Valid: true}, day{2026, 1, 2}, new(core.Date("2026-01-02"))} {
				got, err := tc.d.MarshalValue(v, date)
				require.NoError(t, err, "%#v", v)
				assert.Equal(t, tc.bound, got, "%#v", v)
			}
			for _, v := range []any{nil, sql.NullTime{}, (*core.Date)(nil)} {
				got, err := tc.d.MarshalValue(v, date)
				require.NoError(t, err, "%#v", v)
				assert.Nil(t, got, "%#v", v)
			}
		})
	}
}
