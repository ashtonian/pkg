//go:build unit

package parse_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// textlessDialect is a SQL provider that stores every value in a type of
// its own, as PostgreSQL does, though it embeds SQLite's dialect.
type textlessDialect struct{ filter.SQLiteDialect }

func (textlessDialect) StoresAsText(core.FieldType) bool { return false }

var errStopScan = errors.New("stop")

// TestRowScanner_DecodesWhatTheDialectStoresAsText: a row scan decodes a
// column itself, rather than scanning it into its field through the
// driver, only where the dialect stores the field's value as text and the
// driver cannot scan that text into the field.
func TestRowScanner_DecodesWhatTheDialectStoresAsText(t *testing.T) {
	r := parse.MustResource[storedRow]("stored")
	fields := map[string]reflect.Type{}
	row := reflect.TypeFor[storedRow]()
	for i := range row.NumField() {
		fields[row.Field(i).Tag.Get("db")] = row.Field(i).Type
	}
	for _, tc := range []struct {
		name    string
		d       parse.Dialect
		decoded []string
	}{
		{"sqlite", filter.SQLiteDialect{}, []string{"at", "until", "counts", "settings", "tags", "spans", "raw", "dynamic"}},
		{"postgres", filter.PostgresDialect{}, nil},
		{"a provider storing nothing as text", textlessDialect{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner, err := r.RowScanner(tc.d)
			require.NoError(t, err)
			var decoded []string
			_, err = scanner.Scan(func(dest ...any) error {
				for i, name := range r.ColumnNames() {
					if reflect.TypeOf(dest[i]) != reflect.PointerTo(fields[name]) {
						decoded = append(decoded, name)
					}
				}
				return errStopScan
			})
			require.ErrorIs(t, err, errStopScan)
			assert.Equal(t, tc.decoded, decoded)
		})
	}
}
