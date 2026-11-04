//go:build unit

package patch_test

import (
	"errors"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type constrainedPatch struct {
	Mode   string   `json:"mode" parse:"patch" enum:"on,off"`
	Title  string   `json:"title" parse:"patch" maxlen:"3"`
	Values []string `json:"values" parse:"patch" enum:"a,b" maxitems:"2"`
}

func constraintParseErrors(err error) []*core.ParseError {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []*core.ParseError
		for _, child := range joined.Unwrap() {
			out = append(out, constraintParseErrors(child)...)
		}
		return out
	}
	var parsed *core.ParseError
	if errors.As(err, &parsed) {
		return []*core.ParseError{parsed}
	}
	return nil
}

func TestDeclaredConstraintFailures(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[constrainedPatch](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, format := range []string{"merge", "json"} {
		t.Run(format, func(t *testing.T) {
			var err error
			if format == "merge" {
				_, err = patch.ParseMergePatch([]byte(`{"mode":"invalid","title":"long","values":["bad","worse","a"]}`), schema, nil)
			} else {
				_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/mode","value":"invalid"},{"op":"replace","path":"/title","value":"long"},{"op":"replace","path":"/values","value":["bad","worse","a"]}]`))
			}
			require.Error(t, err)
			failures := constraintParseErrors(err)
			require.Len(t, failures, 5)
			var first *core.ParseError
			require.ErrorAs(t, err, &first)
			assert.Same(t, failures[0], first)
			assert.Equal(t, "/mode", first.Path)
			expected := []struct{ name, limit, path string }{{"enum", "on,off", "/mode"}, {"maxlen", "3", "/title"}, {"enum", "a,b", "/values"}, {"enum", "a,b", "/values"}, {"maxitems", "2", "/values"}}
			for i, failure := range failures {
				assert.Equal(t, "E2077", failure.Code)
				assert.Equal(t, expected[i].path, failure.Path)
				var constraint *core.ConstraintError
				require.ErrorAs(t, failure.Cause, &constraint)
				assert.Equal(t, expected[i].name, constraint.Constraint)
				assert.Equal(t, expected[i].limit, constraint.Limit)
			}
		})
	}
}

type constraintLimits struct {
	Short   string   `json:"short" parse:"patch" minlen:"3"`
	Minimum int      `json:"minimum" parse:"patch" min:"5"`
	Maximum int      `json:"maximum" parse:"patch" max:"10"`
	Items   []string `json:"items" parse:"patch" minitems:"1"`
	Rate    float32  `json:"rate" parse:"patch" max:"0.1"`
}

func TestDeclaredConstraintLimits(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[constraintLimits](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, tc := range []struct{ body, name, limit string }{
		{`{"short":"é"}`, "minlen", "3"},
		{`{"minimum":4}`, "min", "5"},
		{`{"maximum":11}`, "max", "10"},
		{`{"items":[]}`, "minitems", "1"},
		{`{"items":null}`, "minitems", "1"},
		{`{"rate":0.2}`, "max", "0.1"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			parsed, err := patch.ParseMergePatch([]byte(tc.body), schema, nil)
			assert.Nil(t, parsed)
			var failure *core.ParseError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, "E2077", failure.Code)
			var constraint *core.ConstraintError
			require.ErrorAs(t, failure.Cause, &constraint)
			assert.Equal(t, tc.name, constraint.Constraint)
			assert.Equal(t, tc.limit, constraint.Limit)
		})
	}
}
