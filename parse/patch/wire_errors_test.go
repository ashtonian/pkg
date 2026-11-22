//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type errorTextRow struct {
	Name   string   `json:"name" parse:"patch" enum:"a,b"`
	Count  int8     `json:"count" parse:"patch"`
	Tags   []string `json:"tags" parse:"patch" maxitems:"1" enum:"x"`
	Note   *string  `json:"note" parse:"patch" patch_ops:"replace"`
	Things []string `json:"things" parse:"patch" relation:"row_things,row_id,thing_id"`
}

// Both wire formats judge a value by one rule and frame its failures their
// own way: a JSON Patch failure names the operation's index, a merge patch's
// does not, and a merge null reports nullability before the operation.
func TestWireFormatErrorTexts(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[errorTextRow](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, tc := range []struct{ body, want string }{
		{`[{"op":"remove","path":"/note"}]`, "[E2064] operation 'remove' not allowed on path '/note' at index 0"},
		{`{"note":null}`, "[E2064] operation 'remove' not allowed on path '/note'"},
		{`[{"op":"replace","path":"/count","value":null}]`, "[E2077] invalid value for path '/count' at index 0: field is not nullable (int8)"},
		{`{"count":null}`, "[E2077] invalid value for path '/count': field is not nullable (int8) and cannot be removed"},
		{`[{"op":"remove","path":"/count"}]`, "[E2064] operation 'remove' not allowed on path '/count' at index 0"},
		{`[{"op":"replace","path":"/count","value":300}]`, "[E2077] invalid value for path '/count' at index 0: 300 overflows int8"},
		{`{"count":300}`, "[E2077] invalid value for path '/count': 300 overflows int8"},
		{`[{"op":"replace","path":"/name","value":"c"}]`, `[E2077] invalid value for path '/name' at index 0: value "c" is not one of: a, b`},
		{`{"name":"c"}`, `[E2077] invalid value for path '/name': value "c" is not one of: a, b`},
		{`[{"op":"replace","path":"/tags","value":["y","z"]}]`, "[E2077] invalid value for path '/tags' at index 0: value \"y\" is not one of: x\n" +
			"[E2077] invalid value for path '/tags' at index 0: value \"z\" is not one of: x\n" +
			"[E2077] invalid value for path '/tags' at index 0: 2 elements is more than the maximum 1"},
		{`{"tags":["y","z"]}`, "[E2077] invalid value for path '/tags': value \"y\" is not one of: x\n" +
			"[E2077] invalid value for path '/tags': value \"z\" is not one of: x\n" +
			"[E2077] invalid value for path '/tags': 2 elements is more than the maximum 1"},
	} {
		var err error
		if tc.body[0] == '[' {
			_, err = patch.NewParser(schema, nil).Parse([]byte(tc.body))
		} else {
			_, err = patch.ParseMergePatch([]byte(tc.body), schema, nil)
		}
		assert.EqualError(t, err, tc.want, tc.body)
	}
}

// A relation's remove normalizes the members it names like the other
// membership verbs.
func TestRelationRemoveNormalizesItsMembers(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[errorTextRow](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	_, err = parser.Parse([]byte(`[{"op":"remove","path":"/things","value":[1]}]`))
	assert.EqualError(t, err, "[E2077] invalid value for path '/things' at index 0: element 0: expected string, got json.Number")
	parsed, err := parser.Parse([]byte(`[{"op":"remove","path":"/things","value":["a"]}]`))
	require.NoError(t, err)
	assert.Equal(t, []any{"a"}, parsed.Operations[0].Value)
}

// Every parse entry point refuses a nil schema with an error, never a panic.
func TestNilSchemaIsAnError(t *testing.T) {
	parser := patch.NewParser(nil, nil)
	_, err := parser.Parse([]byte(`[{"op":"replace","path":"/a","value":1}]`))
	assert.Error(t, err)
	_, err = parser.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/a", Value: 1}})
	assert.Error(t, err)
	for _, body := range []string{`{"a":1}`, `{}`} {
		_, err = patch.ParseMergePatch([]byte(body), nil, nil)
		assert.Error(t, err, body)
	}
	_, err = patch.ParseMergePatchMap(map[string]any{"a": 1}, nil, nil)
	assert.Error(t, err)
}
