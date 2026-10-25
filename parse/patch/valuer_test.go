//go:build unit

package patch_test

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// amount is a column type in the shape of decimal.Decimal: its JSON form is a
// quoted number and the driver stores Value().
type amount struct{ text string }

func (a *amount) UnmarshalJSON(b []byte) error {
	a.text = strings.Trim(string(b), `"`)
	return nil
}

func (a amount) MarshalJSON() ([]byte, error) { return []byte(`"` + a.text + `"`), nil }

func (a amount) Value() (driver.Value, error) { return a.text, nil }

// A patch to a column whose Go type is a database value binds that value,
// not its JSON form: the quoted "200" a decimal encodes to reached a NUMERIC
// column as the string "\"200\"" (PostgreSQL refused it; SQLite stored it).
func TestPatch_ADatabaseValueColumnBindsTheValue(t *testing.T) {
	type Limit struct {
		ID  string `json:"id"`
		Max amount `json:"max_value" db:"max_value" parse:"patch" patch_ops:"replace"`
	}
	schema, err := patch.GeneratePatchSchema[Limit](core.NewSchemaGenerator())
	require.NoError(t, err)

	parsed, err := patch.ParseMergePatch([]byte(`{"max_value":"200"}`), schema, nil)
	require.NoError(t, err)
	require.Len(t, parsed.Operations, 1)
	assert.Equal(t, amount{text: "200"}, parsed.Operations[0].Value)

	for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		built, err := patch.BuildPatchSQL(parsed, dialect)
		require.NoError(t, err)
		require.Len(t, built.Args, 1, dialect.Name())
		valuer, ok := built.Args[0].(driver.Valuer)
		require.True(t, ok)
		v, err := valuer.Value()
		require.NoError(t, err)
		assert.Equal(t, "200", v, dialect.Name())
	}
}
