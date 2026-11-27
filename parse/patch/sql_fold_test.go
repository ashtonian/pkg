//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/patchtest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestBuildPatchSQL_RefusesServerColumns(t *testing.T) {
	parsed, err := patch.NewParser(patchtest.FoldSchema(), nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"n"}]`))
	require.NoError(t, err)
	for name, opts := range map[string][]patch.PatchSQLOption{
		"a counter that is not an identifier": {patch.WithIncrement("version; DROP TABLE items")},
		"a counter named twice":               {patch.WithIncrement("version"), patch.WithIncrement("version")},
		"a counter that is the timestamp":     {patch.WithAutoTimestamp("updated_at"), patch.WithIncrement("updated_at")},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{}, opts...)
			require.Error(t, err)
			assert.Nil(t, res)
		})
	}
}

func TestBuildPatchSQL_RejectsUncompilableOps(t *testing.T) {
	parser := patch.NewParser(patchtest.FoldSchema(), nil)
	cases := map[string]struct {
		ops  string
		code string
	}{
		"move":                                  {ops: `[{"op":"move","from":"/name","path":"/name"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"copy":                                  {ops: `[{"op":"copy","from":"/name","path":"/name"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"nested append":                         {ops: `[{"op":"array_append","path":"/meta/color","value":"red"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"a member that reads as an array index": {ops: `[{"op":"replace","path":"/meta/0","value":"red"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"the end of an array":                   {ops: `[{"op":"add","path":"/meta/-","value":"red"}]`, code: patch.ECodeOpNotSQLCompilable.String()},
		"test":                                  {ops: `[{"op":"test","path":"/name","value":"orig"}]`, code: patch.ECodeTestOpSQL.String()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			parsed, err := parser.Parse([]byte(tc.ops))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.Error(t, err)
			assert.Nil(t, res, "a rejected op must not yield a result a caller could treat as an empty patch")
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}
}
