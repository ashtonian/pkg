package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type omissionConfig struct {
	Mode     string   `koanf:"mode" validate:"omitempty,oneof=a b"`
	Limit    int      `koanf:"limit" validate:"omitempty,min=1,max=64"`
	Pointer  *int     `koanf:"pointer" validate:"omitempty,min=1"`
	List     []string `koanf:"list" validate:"omitempty,min=1"`
	Before   string   `koanf:"before" default:"abc" validate:"min=2,omitempty,max=4"`
	Required *int     `koanf:"required" default:"1" validate:"required,omitempty,min=1"`
	After    string   `koanf:"after" validate:"omitempty,required,min=2"`
	Password string   `koanf:"password" validate:"omitempty,min=8,oneof=secret-example"`
	PIN      int      `koanf:"pin" sensitive:"true" validate:"min=123,max=456"`
}

func TestUsageBoundsAxesAndIntegerPrecision(t *testing.T) {
	type config struct {
		Count int            `koanf:"count" validate:"min=10,min=1,gt=2,lte=20,lt=30"`
		Ratio float64        `koanf:"ratio" validate:"gt=0.1,lt=0.9"`
		Text  string         `koanf:"text" validate:"len=3"`
		Items []int          `koanf:"items" validate:"min=1,max=3"`
		Named map[string]int `koanf:"named" validate:"min=2,max=4"`
		Equal int64          `koanf:"equal" validate:"eq=9007199254740993"`
		Large uint64         `koanf:"large" validate:"omitempty,oneof=18446744073709551614 18446744073709551615,max=18446744073709551615"`
	}
	rows, err := Usage[config]("")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	assert.Equal(t, []UsageBounds{{Target: "value", Minimum: "10", Maximum: "20"}}, byKey["count"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "value", Minimum: "0.1", Maximum: "0.9", ExclusiveMinimum: true, ExclusiveMaximum: true}}, byKey["ratio"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "length", Minimum: "3", Maximum: "3"}}, byKey["text"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "items", Minimum: "1", Maximum: "3"}}, byKey["items"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "properties", Minimum: "2", Maximum: "4"}}, byKey["named"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "value", Minimum: "9007199254740993", Maximum: "9007199254740993"}}, byKey["equal"].Bounds)
	assert.Equal(t, []string{"0", "18446744073709551614", "18446744073709551615"}, byKey["large"].Allowed)
	raw, err := JSONSchema[config]()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"const": 9007199254740993`)
	assert.Contains(t, string(raw), `"maximum": 18446744073709551615`)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"minimum":9007199254740993`)
}

func TestCollectionBoundsRejectUnconditionallyInvalidNull(t *testing.T) {
	type config struct {
		Items   []string       `koanf:"items" validate:"min=1"`
		Named   map[string]int `koanf:"named" validate:"min=1"`
		Pointer *int           `koanf:"pointer" validate:"min=1"`
	}
	schema := jsonSchemaOf[config](t)
	for _, key := range []string{"items", "named", "pointer"} {
		assert.False(t, matchesConstraint(prop(t, schema, key), nil), key)
	}
	_, err := Load[config](t.Context())
	require.ErrorIs(t, err, ErrValidation)
}

func TestConditionalConstraintsMatchLoading(t *testing.T) {
	schema := jsonSchemaOf[omissionConfig](t)
	for _, tc := range []struct {
		key   string
		value any
		valid bool
	}{
		{"mode", "", true}, {"mode", "a", true}, {"mode", "wrong", false},
		{"limit", int64(0), true}, {"limit", int64(64), true}, {"limit", int64(65), false},
		{"pointer", nil, true}, {"pointer", int64(0), false}, {"pointer", int64(1), true},
		{"list", nil, true}, {"list", []any{}, false}, {"list", []any{"a"}, true},
		{"before", "", false}, {"before", "abc", true}, {"before", "abcde", false},
		{"required", nil, false}, {"required", int64(1), true},
		{"after", "", true}, {"after", "a", false}, {"after", "ab", true},
	} {
		t.Run(fmt.Sprintf("%s/%v", tc.key, tc.value), func(t *testing.T) {
			_, err := Decode[omissionConfig](t.Context(), map[string]any{tc.key: tc.value, "pin": 123})
			assert.Equal(t, tc.valid, err == nil, "Load: %v", err)
			assert.Equal(t, tc.valid, matchesConstraint(prop(t, schema, tc.key), tc.value), "schema: %v", prop(t, schema, tc.key))
		})
	}
	mode := prop(t, schema, "mode")
	require.Equal(t, []any{map[string]any{"anyOf": []any{map[string]any{"const": ""}, map[string]any{"type": "string", "enum": []any{"a", "b"}}}}}, mode["allOf"])
}

func TestUsageBoundsShareSchemaRules(t *testing.T) {
	rows, err := Usage[omissionConfig]("")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	assert.Equal(t, []string{"", "a", "b"}, byKey["mode"].Allowed)
	assert.Equal(t, []UsageBounds{{Target: "value", Minimum: "1", Maximum: "64", ZeroAllowed: true}}, byKey["limit"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "value", Minimum: "1", ZeroAllowed: true}}, byKey["pointer"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "items", Minimum: "1", ZeroAllowed: true}}, byKey["list"].Bounds)
	assert.Equal(t, []UsageBounds{{Target: "length", Minimum: "2"}, {Target: "length", Maximum: "4", ZeroAllowed: true}}, byKey["before"].Bounds)
	assert.Empty(t, byKey["password"].Allowed)
	assert.Equal(t, []UsageBounds{{Target: "length", Minimum: "8", ZeroAllowed: true}}, byKey["password"].Bounds)
	assert.Empty(t, byKey["pin"].Bounds)
	assert.False(t, byKey["after"].Required)
	var buf bytes.Buffer
	require.NoError(t, PrintUsage[omissionConfig]("", WithFormat(FormatMarkdown), WithWriter(&buf)))
	assert.Contains(t, buf.String(), "| Bounds |")
	assert.Contains(t, buf.String(), "zero or (value &gt;= 1, value &lt;= 64)")
	assert.NotContains(t, buf.String(), "secret-example")
}
