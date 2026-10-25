//go:build unit

package parse_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type shapeEntity struct {
	ID    string  `json:"id,omitempty" db:"id"`
	Pair  [2]int  `json:"pair"  db:"pair"  parse:"patch"`
	Note  *string `json:"note"  db:"note"  parse:"patch" patch_ops:"replace,add,remove,test"`
	From  *string `json:"from"  db:"from"  parse:"patch" patch_ops:"replace,remove,move"`
	To    *string `json:"to"    db:"to"    parse:"patch" patch_ops:"replace,move,copy"`
	Num   *int64  `json:"num"   db:"num"   parse:"patch" patch_ops:"replace,move,copy"`
	Fixed string  `json:"fixed" db:"fixed" parse:"patch" patch_ops:"replace,move,copy"`
}

func shapeParser(t *testing.T) *parse.Resource[shapeEntity] {
	t.Helper()
	p, err := parse.NewResource[shapeEntity]("shape_entities")
	require.NoError(t, err)
	return p
}

func applyShape(t *testing.T, p *parse.Resource[shapeEntity], body string, target shapeEntity) (shapeEntity, error) {
	t.Helper()
	parsed, err := p.ParsePatch([]byte(body))
	if err != nil {
		return shapeEntity{}, err
	}
	return p.ApplyValidatedPatch(parsed, target)
}

// A fixed-size array takes exactly its length: anything else would be cut
// or zero-filled in memory while SQL stored what was sent.
func TestPatchShapes_FixedArray(t *testing.T) {
	p := shapeParser(t)
	for _, body := range []string{
		`[{"op":"replace","path":"/pair","value":[1,2,3]}]`,
		`[{"op":"replace","path":"/pair","value":[1]}]`,
	} {
		_, err := p.ParsePatch([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}
	_, err := p.ParseMergePatch([]byte(`{"pair":[1,2,3]}`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	got, err := applyShape(t, p, `[{"op":"replace","path":"/pair","value":[7,8]}]`, shapeEntity{})
	require.NoError(t, err)
	assert.Equal(t, [2]int{7, 8}, got.Pair)

	// It cannot grow, so append is neither advertised nor accepted.
	assert.NotContains(t, p.PatchSchema().AllowedPaths["/pair"].AllowedOps, patch.PatchOpArrayAppend)
	_, err = p.ParsePatch([]byte(`[{"op":"array_append","path":"/pair","value":3}]`))
	requireCode(t, err, patch.ECodeOpNotAllowed.String())

	type declared struct {
		ID   string `json:"id,omitempty" db:"id"`
		Pair [2]int `json:"pair" db:"pair" parse:"patch" patch_ops:"replace,array_append"`
	}
	_, err = parse.NewResource[declared]("declareds")
	require.ErrorContains(t, err, "fixed-size array")
}

// RFC 6902 requires a value member on add, replace and test; a missing one
// is a malformed operation, while an explicit null is a value.
func TestPatchShapes_ValueMemberRequired(t *testing.T) {
	p := shapeParser(t)
	for _, op := range []string{"replace", "add", "test", "array_append"} {
		_, err := p.ParsePatch([]byte(`[{"op":"` + op + `","path":"/note"}]`))
		requireCode(t, err, patch.ECodeInvalidPatchOp.String())
	}

	note := "kept"
	got, err := applyShape(t, p, `[{"op":"replace","path":"/note","value":null}]`, shapeEntity{Note: &note})
	require.NoError(t, err)
	assert.Nil(t, got.Note)

	// Pre-decoded operations say so with HasValue.
	parser := patch.NewParser(p.PatchSchema(), core.DefaultConfig())
	_, err = parser.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/note"}})
	requireCode(t, err, patch.ECodeInvalidPatchOp.String())
	_, err = parser.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/note", HasValue: true}})
	require.NoError(t, err)

	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":7,"value":1}]`))
	requireCode(t, err, patch.ECodeInvalidJSON.String())
}

// * is exactly one reference token, and an empty key is a token.
func TestPatchShapes_WildcardTokens(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"/a/*", "/a//b", false},
		{"/a/*", "/a/", true},
		{"/a/*/c", "/a//c", true},
		{"/a/**", "/a//b", true},
		{"/*", "/", true},
		{"/*", "//", false},
		{"/a~1b/*", "/a~1b/c", true},
		{"/a/*", "/a~1b", false},
	} {
		assert.Equal(t, tc.want, core.MatchWildcard(tc.pattern, tc.path), "MatchWildcard(%q, %q)", tc.pattern, tc.path)
	}

	p := docParser(t) // allows /meta/*
	_, err := p.ParsePatch([]byte(`[{"op":"add","path":"/meta//b","value":1}]`))
	requireCode(t, err, patch.ECodePatchFieldNotFound.String())
	parsed, err := p.ParsePatch([]byte(`[{"op":"add","path":"/meta/","value":1}]`))
	require.NoError(t, err)
	assert.True(t, slices.Contains(parsed.AffectedPaths, "/meta/"), parsed.AffectedPaths)
}

// RFC 6902 §4.6 compares numbers by value: 1.0 equals 1, inside documents
// too, and integers beyond float64 stay exact.
func TestPatchShapes_TestComparesJSONValues(t *testing.T) {
	p := docParser(t) // /meta/* allows test
	target := docEntity{Meta: map[string]any{"n": 1, "doc": map[string]any{"x": 2, "s": "a"}, "big": int64(9007199254740993), "even": int64(9007199254740992)}}
	for _, body := range []string{
		`[{"op":"test","path":"/meta/n","value":1.0}]`,
		`[{"op":"test","path":"/meta/doc","value":{"s":"a","x":2.0}}]`,
		`[{"op":"test","path":"/meta/big","value":9007199254740993}]`,
		`[{"op":"test","path":"/meta/big","value":9007199254740993.0}]`,
	} {
		_, err := apply(t, p, body, target)
		require.NoError(t, err, body)
	}
	for _, body := range []string{
		`[{"op":"test","path":"/meta/n","value":2}]`,
		`[{"op":"test","path":"/meta/big","value":9007199254740992}]`,
		`[{"op":"test","path":"/meta/big","value":9007199254740992.0}]`,
		`[{"op":"test","path":"/meta/n","value":1.0000000000000000001}]`,
		`[{"op":"test","path":"/meta/even","value":9007199254740993.0}]`,
		`[{"op":"test","path":"/meta/missing","value":null}]`,
	} {
		_, err := apply(t, p, body, target)
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}
}

// A move removes its source, so the source's removal rules apply; move and
// copy write the source's value into the destination, so the two fields
// must hold one type.
func TestPatchShapes_TransferSourceRules(t *testing.T) {
	p := shapeParser(t)
	_, err := p.ParsePatch([]byte(`[{"op":"move","from":"/fixed","path":"/to"}]`))
	requireCode(t, err, patch.ECodeOpNotAllowed.String())
	_, err = p.ParsePatch([]byte(`[{"op":"move","from":"/from","path":"/num"}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	v := "carried"
	got, err := applyShape(t, p, `[{"op":"move","from":"/from","path":"/to"}]`, shapeEntity{From: &v})
	require.NoError(t, err)
	assert.Nil(t, got.From)
	require.NotNil(t, got.To)
	assert.Equal(t, "carried", *got.To)

	got, err = applyShape(t, p, `[{"op":"copy","from":"/fixed","path":"/to"}]`, shapeEntity{Fixed: "kept"})
	require.NoError(t, err, "copy does not remove its source")
	assert.Equal(t, "kept", got.Fixed)
	require.NotNil(t, got.To)
	assert.Equal(t, "kept", *got.To)
}
