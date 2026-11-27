//go:build unit

package parse_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

var errStopScan = errors.New("stop")

// TestRowScanner_DecodesWhatTheDialectStoresAsText: a row scan decodes a
// column itself, rather than scanning it into its field through the
// driver, only where the dialect stores the field's value as text and the
// driver cannot scan that text into the field.
func TestRowScanner_DecodesWhatTheDialectStoresAsText(t *testing.T) {
	fields := map[string]reflect.Type{}
	row := reflect.TypeFor[storedRow]()
	for i := range row.NumField() {
		fields[row.Field(i).Tag.Get("db")] = row.Field(i).Type
	}
	for _, tc := range []struct {
		name    string
		p       *parse.Provider
		decoded []string
	}{
		{"sqlite", onSQLite, []string{"at", "until", "counts", "settings", "tags", "spans", "raw", "dynamic"}},
		{"postgres", onPostgres, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parse.MustResource[storedRow](tc.p, "stored")
			scanner, err := r.RowScanner()
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
