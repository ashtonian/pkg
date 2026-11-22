//go:build unit

package patch_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// catalogSchema has a string column, a document whose members are paths, a
// path without a column, and a relation that also allows test.
func catalogSchema() *patch.PatchSchema {
	s := newSchema("items", 2, map[string]*patch.PathSchema{
		"/name":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpMove, patch.PatchOpTest}, DataType: core.FieldTypeString, GoType: reflect.TypeFor[string](), SQLColumn: "name"},
		"/note":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove}, DataType: core.FieldTypeString, SQLColumn: "note"},
		"/doc/**": {AllowedOps: []patch.PatchOp{patch.PatchOpAdd}, DataType: core.FieldTypeAny},
		"/loose":  {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
		"/tags": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpTest},
			DataType:   core.FieldTypeArray, GoType: reflect.TypeFor[[]string](),
			Relation: &core.RelationSchema{JoinTable: "item_tags", SourceColumn: "item_id", TargetColumn: "tag_id"},
		},
	})
	s.MaxRelationValues = 2
	return s
}

// Every code patch defines is one a request or a server's use can provoke,
// and has a case here: a code with none is dead, and this test fails for it.
func TestEveryCodeIsProvoked(t *testing.T) {
	jsonPatch := func(body string, cfg *core.ParserConfig) error {
		_, err := patch.NewParser(catalogSchema(), cfg).Parse([]byte(body))
		return err
	}
	compile := func(body string) error {
		parsed, err := patch.NewParser(catalogSchema(), nil).Parse([]byte(body))
		require.NoError(t, err, body)
		_, err = patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
		return err
	}
	policy := func(body string, opt patch.PolicyOption) error {
		parsed, err := patch.NewParser(catalogSchema(), nil).Parse([]byte(body))
		require.NoError(t, err, body)
		return patch.ApplyPatchPolicyConstraints(parsed, mustPolicy(t, opt), nil)
	}
	cases := map[core.Code]func() error{
		patch.ECodeOpNotAllowed: func() error { return jsonPatch(`[{"op":"add","path":"/name","value":"x"}]`, nil) },
		patch.ECodePathTooDeep: func() error {
			cfg := core.DefaultConfig()
			cfg.MaxDepth = 2
			return jsonPatch(`[{"op":"add","path":"/doc/a/b","value":1}]`, cfg)
		},
		patch.ECodeTooManyOps: func() error {
			return jsonPatch(`[{"op":"replace","path":"/name","value":"a"},{"op":"replace","path":"/name","value":"b"},{"op":"replace","path":"/name","value":"c"}]`, nil)
		},
		patch.ECodeInputTooLarge: func() error {
			cfg := core.DefaultConfig()
			cfg.MaxInputSize = 8
			return jsonPatch(`[{"op":"replace","path":"/name","value":"x"}]`, cfg)
		},
		patch.ECodeInvalidPatchOp:     func() error { return jsonPatch(`[{"op":"upsert","path":"/name","value":"x"}]`, nil) },
		patch.ECodePatchFieldNotFound: func() error { return jsonPatch(`[{"op":"replace","path":"/colour","value":"x"}]`, nil) },
		patch.ECodePatchValueInvalid:  func() error { return jsonPatch(`[{"op":"replace","path":"/name","value":5}]`, nil) },
		patch.ECodeInvalidJSON:        func() error { return jsonPatch(`not json`, nil) },
		patch.ECodePatchPolicyForbidden: func() error {
			return policy(`[{"op":"replace","path":"/name","value":"x"}]`, patch.ForbidPaths("/name"))
		},
		patch.ECodePatchPolicyValue: func() error {
			return policy(`[{"op":"replace","path":"/name","value":"x"}]`, patch.ConstrainValues("/name", patch.ValueConstraint{AllowedValues: []any{"y"}}))
		},
		patch.ECodeRelationVerbNotAllowed: func() error { return compile(`[{"op":"test","path":"/tags","value":["a"]}]`) },
		patch.ECodeRelationValueInvalid:   func() error { return jsonPatch(`[{"op":"add","path":"/tags","value":[]}]`, nil) },
		patch.ECodeRelationPatchTooLarge:  func() error { return jsonPatch(`[{"op":"add","path":"/tags","value":["a","b","c"]}]`, nil) },
		patch.ECodeTestOpSQL:              func() error { return compile(`[{"op":"test","path":"/name","value":"x"}]`) },
		patch.ECodeSQLColumnMissing:       func() error { return compile(`[{"op":"replace","path":"/loose","value":"x"}]`) },
		patch.ECodeOpNotSQLCompilable:     func() error { return compile(`[{"op":"move","from":"/note","path":"/name"}]`) },
	}
	for _, code := range patch.Codes() {
		provoke, ok := cases[code]
		if !assert.True(t, ok, "%s has no case that provokes it", code) {
			continue
		}
		var pe *core.ParseError
		if err := provoke(); assert.ErrorAs(t, err, &pe, code.String()) {
			assert.Equal(t, code.String(), pe.Code, pe.Message)
		}
	}
	assert.Len(t, cases, len(patch.Codes()), "a case for a code patch does not define")
}
