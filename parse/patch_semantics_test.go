//go:build unit

package parse_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type docConfig struct {
	Threshold int `json:"threshold"`
}

// docEntity exercises every RFC 6902 behaviour the 2026-09-19/28 reviews
// found broken in the hand-written engine.
type docEntity struct {
	ID      string         `json:"id,omitempty" db:"id"`
	Name    string         `json:"name"    db:"name"    parse:"patch" patch_ops:"replace,test"`
	Alias   *string        `json:"alias"   db:"alias"   parse:"patch" patch_ops:"replace,remove,move,copy"`
	Slashed string         `json:"a/b"     db:"a_b"     parse:"patch"`
	Tags    []string       `json:"tags"    db:"tags"    parse:"patch"`
	Owner   uuid.UUID      `json:"owner"   db:"owner"   parse:"patch"`
	Meta    map[string]any `json:"meta"    db:"meta"    parse:"patch" patch_ops:"replace,add,remove,test"`
	Counter *int64         `json:"counter" db:"counter" parse:"patch" patch_ops:"replace,copy,test"`
	Copied  *int64         `json:"copied"  db:"copied"  parse:"patch" patch_ops:"replace,copy"`
	Config  *docConfig     `json:"config"  db:"config"  parse:"patch"`
	Secret  string         `json:"secret"  db:"secret"`
}

func docParser(t *testing.T, opts ...core.Option) *parse.Resource[docEntity] {
	t.Helper()
	p, err := parse.NewResource[docEntity]("doc_entities", parse.ParserOptions(opts...))
	require.NoError(t, err)
	// Nested members are reachable only through explicit wildcard paths.
	p.PatchSchema().AllowedPaths["/meta/*"] = &patch.PathSchema{
		AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpTest, patch.PatchOpMove},
		DataType:   core.FieldTypeAny,
	}
	p.PatchSchema().AllowedPaths["/tags/*"] = &patch.PathSchema{
		AllowedOps: []patch.PatchOp{patch.PatchOpAdd},
		DataType:   core.FieldTypeString,
	}
	return p
}

func apply(t *testing.T, p *parse.Resource[docEntity], body string, target docEntity) (docEntity, error) {
	t.Helper()
	parsed, err := p.ParsePatch([]byte(body))
	if err != nil {
		return docEntity{}, err
	}
	return p.ApplyValidatedPatch(parsed, target)
}

func TestPatchSemantics_Apply(t *testing.T) {
	p := docParser(t)
	big := int64(1) << 60
	owner := uuid.MustParse("0190a000-0000-7000-8000-000000000001")

	t.Run("RFC 6901 escapes address the unescaped key", func(t *testing.T) {
		got, err := apply(t, p, `[{"op":"replace","path":"/a~1b","value":"x"}]`, docEntity{})
		require.NoError(t, err)
		assert.Equal(t, "x", got.Slashed)
		merged, err := p.ParseMergePatch([]byte(`{"a/b":"y"}`))
		require.NoError(t, err)
		got, err = p.ApplyValidatedPatch(merged, docEntity{})
		require.NoError(t, err)
		assert.Equal(t, "y", got.Slashed)
	})

	t.Run("array_append creates then extends", func(t *testing.T) {
		got, err := apply(t, p, `[{"op":"array_append","path":"/tags","value":"x"},{"op":"array_append","path":"/tags","value":"y"}]`, docEntity{})
		require.NoError(t, err)
		assert.Equal(t, []string{"x", "y"}, got.Tags)
	})

	t.Run("dash appends to an array", func(t *testing.T) {
		got, err := apply(t, p, `[{"op":"add","path":"/tags/-","value":"z"}]`, docEntity{Tags: []string{"a"}})
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "z"}, got.Tags)
	})

	t.Run("typed replacements apply", func(t *testing.T) {
		got, err := apply(t, p, `[{"op":"replace","path":"/tags","value":["p","q"]},{"op":"replace","path":"/owner","value":"`+owner.String()+`"}]`, docEntity{})
		require.NoError(t, err)
		assert.Equal(t, []string{"p", "q"}, got.Tags)
		assert.Equal(t, owner, got.Owner)
	})

	t.Run("copy keeps exact integers and does not alias", func(t *testing.T) {
		got, err := apply(t, p, `[{"op":"copy","from":"/counter","path":"/copied"}]`, docEntity{Counter: &big})
		require.NoError(t, err)
		require.NotNil(t, got.Copied)
		assert.Equal(t, big, *got.Copied)
		*got.Copied = 0
		assert.Equal(t, big, *got.Counter)
	})

	t.Run("test on a missing member fails", func(t *testing.T) {
		_, err := apply(t, p, `[{"op":"test","path":"/meta/missing","value":null}]`, docEntity{Meta: map[string]any{}})
		require.Error(t, err)
	})

	t.Run("test compares exact integers", func(t *testing.T) {
		_, err := apply(t, p, `[{"op":"test","path":"/counter","value":1152921504606846976},{"op":"replace","path":"/name","value":"ok"}]`, docEntity{Counter: &big})
		require.NoError(t, err)
		_, err = apply(t, p, `[{"op":"test","path":"/counter","value":1152921504606846977}]`, docEntity{Counter: &big})
		require.Error(t, err)
	})

	t.Run("copy amplification is bounded across the patch", func(t *testing.T) {
		small := docParser(t, core.WithMaxPatchCopyBytes(64))
		long := int64(1)
		_, err := apply(t, small, `[{"op":"copy","from":"/counter","path":"/copied"}]`, docEntity{Counter: &long})
		require.NoError(t, err)
		wide := docEntity{Meta: map[string]any{"blob": string(make([]byte, 200))}}
		_, err = apply(t, small, `[{"op":"move","from":"/meta/blob","path":"/meta/other"}]`, wide)
		require.NoError(t, err, "move does not copy")
		small.PatchSchema().AllowedPaths["/meta/*"].AllowedOps = append(small.PatchSchema().AllowedPaths["/meta/*"].AllowedOps, patch.PatchOpCopy)
		_, err = apply(t, small, `[{"op":"copy","from":"/meta/blob","path":"/meta/c1"}]`, wide)
		require.Error(t, err)
	})

	t.Run("failed move leaves the source in place", func(t *testing.T) {
		alias := "keep"
		original := docEntity{Alias: &alias, Meta: map[string]any{"a": "b"}}
		_, err := apply(t, p, `[{"op":"move","from":"/meta/a","path":"/meta/x/y"}]`, original)
		require.Error(t, err)
		assert.Equal(t, map[string]any{"a": "b"}, original.Meta)
	})
}

func TestPatchSemantics_ParseRejects(t *testing.T) {
	p := docParser(t)
	cases := map[string]struct {
		body string
		code string
	}{
		"null into non-nullable": {body: `[{"op":"replace","path":"/name","value":null}]`, code: patch.ECodePatchValueInvalid.String()},
		"remove not advertised":  {body: `[{"op":"remove","path":"/owner"}]`, code: patch.ECodeOpNotAllowed.String()},
		"struct shape":           {body: `[{"op":"replace","path":"/config","value":"not an object"}]`, code: patch.ECodePatchValueInvalid.String()},
		"array element type":     {body: `[{"op":"replace","path":"/tags","value":["a",5]}]`, code: patch.ECodePatchValueInvalid.String()},
		"append element type":    {body: `[{"op":"array_append","path":"/tags","value":{"a":1}}]`, code: patch.ECodePatchValueInvalid.String()},
		"uuid malformed":         {body: `[{"op":"replace","path":"/owner","value":"nope"}]`, code: patch.ECodePatchValueInvalid.String()},
		"from outside schema":    {body: `[{"op":"copy","from":"/secret","path":"/alias"}]`, code: patch.ECodePatchFieldNotFound.String()},
		"malformed pointer":      {body: `[{"op":"replace","path":"name","value":"x"}]`, code: patch.ECodeInvalidPatchOp.String()},
		"bad escape":             {body: `[{"op":"replace","path":"/a~2b","value":"x"}]`, code: patch.ECodeInvalidPatchOp.String()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := p.ParsePatch([]byte(tc.body))
			requireCode(t, err, tc.code)
		})
	}

	t.Run("nullable null and remove accepted", func(t *testing.T) {
		alias := "a"
		got, err := apply(t, p, `[{"op":"remove","path":"/alias"},{"op":"replace","path":"/config","value":null}]`, docEntity{Alias: &alias, Config: &docConfig{Threshold: 1}})
		require.NoError(t, err)
		assert.Nil(t, got.Alias)
		assert.Nil(t, got.Config)
	})

	t.Run("merge null on non-nullable rejected", func(t *testing.T) {
		_, err := p.ParseMergePatch([]byte(`{"name": null}`))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	})

	t.Run("pointer depth honours MaxDepth", func(t *testing.T) {
		shallow := docParser(t, core.WithMaxDepth(1))
		_, err := shallow.ParsePatch([]byte(`[{"op":"add","path":"/meta/a","value":1}]`))
		requireCode(t, err, patch.ECodePathTooDeep.String())
	})
}

// Each schema owns its operator slices: configuring one resource must not
// change another's accepted operations.
func TestSchemaIsolation_DefaultOperatorSlices(t *testing.T) {
	type entity struct {
		ID    string            `json:"id,omitempty" db:"id"`
		Name  string            `json:"name" parse:"filter,patch"`
		Attrs map[string]string `json:"attrs" parse:"patch"`
	}
	a, err := parse.NewResource[entity]("entities")
	require.NoError(t, err)
	b, err := parse.NewResource[entity]("entities")
	require.NoError(t, err)

	a.PatchSchema().AllowedPaths["/attrs"].AllowedOps[0] = patch.PatchOpTest
	a.FilterSchema().Fields["name"].AllowedOperators[0] = filter.OpGt

	assert.Equal(t, patch.PatchOpReplace, b.PatchSchema().AllowedPaths["/attrs"].AllowedOps[0])
	assert.Equal(t, filter.OpEq, b.FilterSchema().Fields["name"].AllowedOperators[0])

	c, err := parse.NewResource[entity]("entities")
	require.NoError(t, err)
	assert.Equal(t, patch.PatchOpReplace, c.PatchSchema().AllowedPaths["/attrs"].AllowedOps[0])
	assert.Equal(t, filter.OpEq, c.FilterSchema().Fields["name"].AllowedOperators[0])
}

// Default remove follows nullability: dropped for non-nullable fields
// (and rejected when tagged), offered for nullable ones — including
// nullable scalars, which a merge-patch null clears.
func TestGeneratePatchSchema_RemoveFollowsNullability(t *testing.T) {
	type defaults struct {
		ID     string          `json:"id,omitempty" db:"id"`
		Config struct{ A int } `json:"config" parse:"patch"`
		Meta   map[string]any  `json:"meta"   parse:"patch"`
		Note   string          `json:"note"   parse:"patch"`
		Alias  *string         `json:"alias"  parse:"patch"`
		Hidden *bool           `json:"hidden" parse:"patch"`
	}
	p, err := parse.NewResource[defaults]("defaults")
	require.NoError(t, err)
	paths := p.PatchSchema().AllowedPaths
	assert.NotContains(t, paths["/config"].AllowedOps, patch.PatchOpRemove)
	assert.Contains(t, paths["/meta"].AllowedOps, patch.PatchOpRemove)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace}, paths["/note"].AllowedOps)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}, paths["/alias"].AllowedOps)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}, paths["/hidden"].AllowedOps)

	alias, hidden := "a", true
	merged, err := p.ParseMergePatch([]byte(`{"alias":null,"hidden":null}`))
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(merged, defaults{Alias: &alias, Hidden: &hidden})
	require.NoError(t, err)
	assert.Nil(t, got.Alias)
	assert.Nil(t, got.Hidden)

	// Configured defaults are taken as given.
	q, err := parse.NewResource[defaults]("defaults", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"replace"}}
	})))
	require.NoError(t, err)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace}, q.PatchSchema().AllowedPaths["/alias"].AllowedOps)

	type tagged struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `json:"name" parse:"patch" patch_ops:"replace,remove"`
	}
	_, err = parse.NewResource[tagged]("taggeds")
	assert.ErrorContains(t, err, "nullable")
}

func TestGeneratorOptions_DefaultPatchOpsHonoured(t *testing.T) {
	type entity struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `json:"name" parse:"patch"`
	}
	p, err := parse.NewResource[entity]("entities", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"replace", "test"}}
	})))
	require.NoError(t, err)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpTest}, p.PatchSchema().AllowedPaths["/name"].AllowedOps)

	_, err = parse.NewResource[entity]("entities", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"bogus"}}
	})))
	assert.Error(t, err)
}
