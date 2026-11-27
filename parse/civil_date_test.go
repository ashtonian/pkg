//go:build unit || integration

package parse_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

func TestCivilDateRequestContract(t *testing.T) {
	resource := parse.MustResource[parsetest.CivilTask](onSQLite, "tasks")
	_, err := parse.NewResource[parsetest.CivilTask](onSQLite, "tasks", parse.ColumnType("due_on", core.SQLDate))
	require.NoError(t, err)
	_, err = parse.NewResource[struct {
		ID  string `db:"id"`
		Day string `db:"day"`
	}](onSQLite, "bad", parse.ColumnType("day", core.SQLDate))
	require.Error(t, err)

	for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
		tasks := parse.MustResource[parsetest.CivilTask](provider, "tasks")
		for _, day := range []string{"2026-10-26", "2028-02-29"} {
			query, err := tasks.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"` + day + `"}}`}, "sort": {"due_on,id"}})
			require.NoError(t, err)
			_, err = tasks.BuildList(query, parse.Scope{})
			require.NoError(t, err, provider.Name())
		}
	}
	for _, day := range []string{"2026-10-26", "2028-02-29"} {
		patch, err := resource.ParseMergePatch([]byte(`{"due_on":"` + day + `"}`))
		require.NoError(t, err)
		target, err := resource.ApplyValidatedPatch(patch, parsetest.CivilTask{ID: "one"})
		require.NoError(t, err)
		require.NotNil(t, target.DueOn)
		assert.Equal(t, core.Date(day), *target.DueOn)
		wire, err := json.Marshal(target)
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"one","due_on":"`+day+`"}`, string(wire))
	}
	for _, invalid := range []string{"2026-02-29", "2026-10-26T00:00:00Z", "now-7d"} {
		_, err := resource.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"` + invalid + `"}}`}})
		require.Error(t, err)
		_, err = resource.ParseMergePatch([]byte(`{"due_on":"` + invalid + `"}`))
		require.Error(t, err)
	}
}

func TestCivilDateNullableBindings(t *testing.T) {
	for _, dialect := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		var value *core.Date
		normalized, err := dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
		assert.Nil(t, normalized)
		value = new(core.Date("2028-02-29"))
		_, err = dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
	}
}
