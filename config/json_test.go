package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Values decode as the YAML parser types them, integers exactly, and the
// escapes and documents only JSON allows parse.
func TestJSONParser_Values(t *testing.T) {
	got, err := jsonParser{}.Unmarshal([]byte(`{
		"int": 9007199254740993,
		"neg": -3,
		"uint": 18446744073709551615,
		"float": 1.5,
		"exp": 1e3,
		"str": "a\/bé",
		"yes": true,
		"none": null,
		"list": [1, "two", {"three": 3}],
		"nested": {"empty": {}, "list": []}
	}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"int":    9007199254740993,
		"neg":    -3,
		"uint":   uint64(18446744073709551615),
		"float":  1.5,
		"exp":    float64(1000),
		"str":    "a/bé",
		"yes":    true,
		"none":   nil,
		"list":   []any{1, "two", map[string]any{"three": 3}},
		"nested": map[string]any{"empty": map[string]any{}, "list": []any{}},
	}, got)

	deep := `{"a": ` + strings.Repeat("[", jsonMaxDepth-1) + strings.Repeat("]", jsonMaxDepth-1) + `}`
	_, err = jsonParser{}.Unmarshal([]byte(deep))
	require.NoError(t, err, "nesting up to the limit parses")
}

// Every rejection opens with the line it is on.
func TestJSONParser_Rejects(t *testing.T) {
	for _, tt := range []struct {
		name, doc string
		line      string
		msg       string
	}{
		{"syntax", "{\n  \"a\": 1,\n  \"b\": oops\n}", "line 3:", "invalid character"},
		{"syntax on the next line", "{\"a\": 1,\n\n  oops}", "line 3:", "invalid character"},
		{"unterminated string", "{\n  \"a\": \"text\n}", "line 2:", "invalid character"},
		{"key set twice", "{\n  \"a\": 1,\n  \"a\": 2\n}", "line 3:", `key "a" is set twice`},
		{"key set twice nested", `{"a": {"b": 1, "b": 2}}`, "line 1:", `key "b" is set twice`},
		{"not an object", `[1, 2]`, "line 1:", "holds a JSON object"},
		{"scalar", `"text"`, "line 1:", "holds a JSON object"},
		{"trailing data", "{}\n{}", "line 2:", "data after the JSON object"},
		{"trailing garbage", "{}\n]", "line 2:", "invalid character"},
		{"empty", "", "line 1:", "unexpected end of JSON input"},
		{"truncated", "{\n  \"a\": [1,", "line 2:", "unexpected end of JSON input"},
		{"unclosed", "{\n  \"a\": [1]\n", "line 3:", "unexpected end of JSON input"},
		{"out of range", `{"a": 1e400}`, "line 1:", "out of range"},
		{"too deep", `{"a": ` + strings.Repeat("[", jsonMaxDepth) + strings.Repeat("]", jsonMaxDepth) + `}`, "line 1:", "nested more than 1000 deep"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := jsonParser{}.Unmarshal([]byte(tt.doc))
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.line), "%q opens with %q", err, tt.line)
			assert.Contains(t, err.Error(), tt.msg)
		})
	}
}

func TestJSONParser_MarshalRoundTrips(t *testing.T) {
	in := map[string]any{"a": 1, "b": []any{"x"}}
	b, err := jsonParser{}.Marshal(in)
	require.NoError(t, err)
	out, err := jsonParser{}.Unmarshal(b)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}
