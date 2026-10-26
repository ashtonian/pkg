package parse_test

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type civilTask struct {
	ID    string     `json:"id" db:"id" parse:"filter,sort"`
	DueOn *core.Date `json:"due_on" db:"due_on" parse:"filter,patch,sort"`
}

func TestCivilDateRequestContract(t *testing.T) {
	resource := parse.MustResource[civilTask]("tasks")
	_, err := parse.NewResource[civilTask]("tasks", parse.ColumnType("due_on", core.SQLDate))
	require.NoError(t, err)
	_, err = parse.NewResource[struct {
		ID  string `db:"id"`
		Day string `db:"day"`
	}]("bad", parse.ColumnType("day", core.SQLDate))
	require.Error(t, err)

	for _, day := range []string{"2026-10-26", "2028-02-29"} {
		query, err := resource.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"` + day + `"}}`}, "sort": {"due_on,id"}})
		require.NoError(t, err)
		_, err = resource.BuildList(filter.PostgresDialect{}, query, parse.Scope{})
		require.NoError(t, err)
		_, err = resource.BuildList(filter.SQLiteDialect{}, query, parse.Scope{})
		require.NoError(t, err)
		patch, err := resource.ParseMergePatch([]byte(`{"due_on":"` + day + `"}`))
		require.NoError(t, err)
		target, err := resource.ApplyValidatedPatch(patch, civilTask{ID: "one"})
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
	for _, dialect := range []parse.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		var value *core.Date
		normalized, err := dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
		assert.Nil(t, normalized)
		value = new(core.Date("2028-02-29"))
		_, err = dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
	}
}
