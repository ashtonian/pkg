//go:build unit

package patch_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// nestedMerge is a merge patch for /doc whose member chain is depth objects
// deep, each level also deleting and setting a member.
func nestedMerge(depth int) string {
	var b strings.Builder
	b.WriteString(`{"doc":`)
	for range depth {
		b.WriteString(`{"gone":null,"set":1,"a":`)
	}
	b.WriteString(`{}`)
	b.WriteString(strings.Repeat("}", depth+1))
	return b.String()
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "error %v is not a ParseError", err)
	assert.Equal(t, code, pe.Code, pe.Message)
}

// A member nested d objects deep is a d-segment pointer, bounded like one.
func TestParseMergePatch_BoundsDepth(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.MaxDepth = 3
	_, err := patch.ParseMergePatch([]byte(`{"doc":{"a":{"b":1}}}`), mergeSchema(), cfg)
	require.NoError(t, err)
	_, err = patch.ParseMergePatch([]byte(`{"doc":{"a":{"b":{"c":1}}}}`), mergeSchema(), cfg)
	requireCode(t, err, patch.ECodePathTooDeep.String())

	// Arrays replace whole; objects inside them do not merge.
	_, err = patch.ParseMergePatch([]byte(`{"doc":{"a":[[{"x":{"y":{"z":1}}}]]}}`), mergeSchema(), cfg)
	require.NoError(t, err)
}

// Every nested object that merges is an operation; scalar members are not.
func TestParseMergePatch_BoundsNestedObjects(t *testing.T) {
	objects := func(n int) string {
		members := make([]string, n)
		for i := range n {
			members[i] = fmt.Sprintf(`"o%d":{"x":%d}`, i, i)
		}
		return `{"doc":{` + strings.Join(members, ",") + `}}`
	}
	_, err := patch.ParseMergePatch([]byte(objects(9)), mergeSchema(), nil) // 1 + 9 = MaxOperations
	require.NoError(t, err)
	_, err = patch.ParseMergePatch([]byte(objects(10)), mergeSchema(), nil)
	requireCode(t, err, patch.ECodeTooManyOps.String())

	scalars := make([]string, 1000)
	for i := range scalars {
		scalars[i] = fmt.Sprintf(`"k%d":%d`, i, i)
	}
	_, err = patch.ParseMergePatch([]byte(`{"doc":{`+strings.Join(scalars, ",")+`}}`), mergeSchema(), nil)
	require.NoError(t, err)
}

// PostgreSQL merge SQL and its arguments grow linearly with depth.
func TestBuildPatchSQL_PostgresMergeGrowsLinearly(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.MaxDepth = 0
	schema := mergeSchema()
	schema.MaxOperations = 0
	size := func(depth int) (int, int) {
		parsed, err := patch.ParseMergePatch([]byte(nestedMerge(depth)), schema, cfg)
		require.NoError(t, err)
		res, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
		require.NoError(t, err)
		n := len(res.SetClause)
		for _, a := range res.Args {
			n += len(fmt.Sprint(a))
		}
		return n, res.ParamCount
	}
	s10, p10 := size(10)
	s20, p20 := size(20)
	s40, p40 := size(40)
	assert.Less(t, float64(s20)/float64(s10), 2.2, "depth 10→20: %d→%d bytes", s10, s20)
	assert.Less(t, float64(s40)/float64(s20), 2.2, "depth 20→40: %d→%d bytes", s20, s40)
	for depth, params := range map[int]int{10: p10, 20: p20, 40: p40} {
		assert.LessOrEqual(t, params, 3*(depth+1), "depth %d binds", depth)
	}
}

// However wide a level is, its members bind as one value.
func TestBuildPatchSQL_PostgresWideMergeBindsOnce(t *testing.T) {
	members := make([]string, 500)
	for i := range members {
		members[i] = fmt.Sprintf(`"k%d":%d`, i, i)
	}
	parsed, err := patch.ParseMergePatch([]byte(`{"doc":{`+strings.Join(members, ",")+`}}`), mergeSchema(), nil)
	require.NoError(t, err)
	res, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.ParamCount, res.SetClause)
}
