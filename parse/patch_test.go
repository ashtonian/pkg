//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestResource_ParsePatch(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	result, err := tp.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"new"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 {
		t.Errorf("expected 1 op, got %d", len(result.Operations))
	}
}

func TestResource_ParsePatch_UnknownPath(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	_, err := tp.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":"x"}]`))
	if err == nil {
		t.Fatal("expected error for field not in patch schema")
	}
}

func TestResource_ApplyPatch(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"New"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New" {
		t.Errorf("expected New, got %s", updated.Name)
	}
	if updated.ID != "d1" {
		t.Errorf("ID should be unchanged")
	}
}

type TestDeviceMovable struct {
	ID   string  `json:"id"   parse:"filter" filter_ops:"eq"`
	Name *string `json:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace,remove,move,copy"`
	Alt  *string `json:"alt"  parse:"patch" patch_ops:"replace,remove,move,copy"`
}

// --- ApplyValidatedPatch fast path vs slow path ---

func TestApplyPatch_DirectFastPath(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}

	// Merge patch — all top-level replaces → uses direct reflection path.
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"New"},{"op":"replace","path":"/status","value":"offline"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New" {
		t.Errorf("expected New, got %s", updated.Name)
	}
	if updated.Status != "offline" {
		t.Errorf("expected offline, got %s", updated.Status)
	}
	if updated.ID != "d1" {
		t.Error("ID should be unchanged")
	}
}

func TestApplyPatch_MergePatchFastPath(t *testing.T) {
	tp := parse.MustResource[TestDevice](onPostgres, "test_devices")
	original := TestDevice{ID: "d1", Name: "Old", Status: "active"}

	parsed, err := tp.ParseMergePatch([]byte(`{"name":"Merged"}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tp.ApplyValidatedPatch(parsed, original)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Merged" {
		t.Errorf("expected Merged, got %s", updated.Name)
	}
	if updated.ID != "d1" {
		t.Error("ID should be unchanged")
	}
	if updated.Status != "active" {
		t.Error("Status should be unchanged")
	}
}

func TestApplyPatch_SlowPathForMoveOp(t *testing.T) {
	tp := parse.MustResource[TestDeviceMovable](onPostgres, "test_device_movables")
	name := "Original"
	original := TestDeviceMovable{ID: "d1", Name: &name}

	// move op → triggers slow path (JSON round-trip).
	updated, err := tp.ApplyPatch(
		[]byte(`[{"op":"move","from":"/name","path":"/alt"}]`),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Alt == nil || *updated.Alt != "Original" {
		t.Errorf("expected Original in alt, got %v", updated.Alt)
	}
	if updated.Name != nil {
		t.Errorf("expected no name after move, got %q", *updated.Name)
	}
}

type TestDeviceTimestamped struct {
	ID        string     `json:"id"         parse:"filter"       filter_ops:"eq"`
	Name      string     `json:"name"       parse:"filter,patch" filter_ops:"eq" patch_ops:"replace"`
	RevokedAt *time.Time `json:"revoked_at" parse:"filter,patch" patch_ops:"replace"`
	SeenAt    time.Time  `json:"seen_at"    parse:"filter,patch" patch_ops:"replace"`
}

// The direct-apply fast path must coerce JSON RFC 3339 strings into
// time.Time / *time.Time targets — this is how every timestamp column
// arrives from a merge-patch body, and the memory repositories apply
// patches through this path (SQL backends coerce in the database).
func TestApplyPatch_DirectPathCoercesRFC3339Timestamps(t *testing.T) {
	tp, err := parse.NewResource[TestDeviceTimestamped](onPostgres, "test_device_timestampeds")
	if err != nil {
		t.Fatal(err)
	}
	original := TestDeviceTimestamped{ID: "d1", Name: "Old"}

	parsed, err := tp.ParseMergePatch([]byte(`{"revoked_at":"2026-07-09T12:34:56.789Z","seen_at":"2026-07-09T01:02:03Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := tp.ApplyValidatedPatch(parsed, original)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RevokedAt == nil || !updated.RevokedAt.Equal(time.Date(2026, 7, 9, 12, 34, 56, 789_000_000, time.UTC)) {
		t.Errorf("RevokedAt not coerced: %v", updated.RevokedAt)
	}
	if !updated.SeenAt.Equal(time.Date(2026, 7, 9, 1, 2, 3, 0, time.UTC)) {
		t.Errorf("SeenAt not coerced: %v", updated.SeenAt)
	}

	// A non-timestamp string is rejected when the patch is parsed, before
	// any backend sees it.
	_, err = tp.ParseMergePatch([]byte(`{"revoked_at":"not-a-time"}`))
	var pe *core.ParseError
	if !errors.As(err, &pe) || pe.Code != patch.ECodePatchValueInvalid.String() {
		t.Errorf("non-RFC3339 string: err = %v, want %s", err, patch.ECodePatchValueInvalid.String())
	}
}

type sensor struct {
	ID        string         `json:"id"         db:"id"`
	Name      string         `json:"name"       db:"name"       parse:"patch"`
	Rate      int32          `json:"rate"       db:"rate"       parse:"patch"`
	SeenAt    *time.Time     `json:"seen_at"    db:"seen_at"    parse:"patch"`
	Labels    map[string]any `json:"labels"     db:"labels"     parse:"patch"`
	Rotated   *time.Time     `json:"rotated"    db:"rotated"    parse:"server"`
	CreatedAt time.Time      `json:"created_at" db:"created_at"`
}

func setSQL(t *testing.T, p *patch.ParsedPatch) (string, []any) {
	t.Helper()
	res, err := patch.BuildPatchSQL(p, filter.PostgresDialect{})
	require.NoError(t, err)
	return res.SetClause, res.Args
}

// A patch built in code validates as the same patch parsed from JSON does,
// Go values of the paths' types included, without a JSON round trip.
func TestMergePatchMap_IsTheBodysPatch(t *testing.T) {
	p, err := parse.NewResource[sensor](onSQLite, "sensors")
	require.NoError(t, err)
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	fromMap, err := p.MergePatchMap(map[string]any{"name": "s1", "rate": 5, "seen_at": at, "labels": map[string]any{"zone": "a", "old": nil}})
	require.NoError(t, err)
	fromBody, err := p.ParseMergePatch([]byte(`{"name":"s1","rate":5,"seen_at":"2026-10-19T12:00:00Z","labels":{"zone":"a","old":null}}`))
	require.NoError(t, err)
	mapSet, mapArgs := setSQL(t, fromMap)
	bodySet, bodyArgs := setSQL(t, fromBody)
	assert.Equal(t, bodySet, mapSet)
	assert.Equal(t, bodyArgs, mapArgs)

	for name, m := range map[string]map[string]any{
		"unknown field":         {"colour": "red"},
		"not the path's type":   {"rate": "fast"},
		"out of the path range": {"rate": int64(1) << 40},
		"a server field":        {"rotated": at},
	} {
		_, mergeErr := p.MergePatchMap(m)
		var pe *core.ParseError
		require.ErrorAs(t, mergeErr, &pe, name)
	}
	server, err := p.ServerMergePatchMap(map[string]any{"rotated": at, "name": "s2"})
	require.NoError(t, err)
	set, _ := setSQL(t, server)
	assert.Contains(t, set, `"rotated" = $`)
}

type gaugeSettings struct {
	Theme *string `json:"theme"`
	Size  int     `json:"size"`
}

type gauge struct {
	ID       string            `json:"id"       db:"id"`
	Note     *string           `json:"note"     db:"note"     parse:"patch"`
	Tags     []string          `json:"tags"     db:"tags"     parse:"patch"`
	Labels   map[string]string `json:"labels"   db:"labels"   parse:"patch"`
	Settings gaugeSettings     `json:"settings" db:"settings" parse:"patch"`
	Rotated  *time.Time        `json:"-"        db:"rotated"  parse:"server"`
}

// A Go value encoding/json writes as null — a nil pointer, map or slice —
// is the merge patch's null, in a nested object too, so a map holding the
// server's typed values patches as its JSON encoding would.
func TestMergePatchMap_NilIsJSONNull(t *testing.T) {
	p, err := parse.NewResource[gauge](onSQLite, "gauges")
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		m    map[string]any
		body string
	}{
		"nil pointer":      {map[string]any{"note": (*string)(nil)}, `{"note":null}`},
		"nil slice":        {map[string]any{"tags": []string(nil)}, `{"tags":null}`},
		"nil map":          {map[string]any{"labels": map[string]string(nil)}, `{"labels":null}`},
		"nested nil":       {map[string]any{"labels": map[string]any{"a": (*string)(nil), "b": "x"}}, `{"labels":{"a":null,"b":"x"}}`},
		"server nil":       {map[string]any{"rotated": (*time.Time)(nil)}, `{"rotated":null}`},
		"nil merge object": {map[string]any{"labels": map[string]any(nil)}, `{"labels":null}`},
	} {
		t.Run(name, func(t *testing.T) {
			fromMap, mergeErr := p.ServerMergePatchMap(tc.m)
			require.NoError(t, mergeErr)
			fromBody, mergeErr := p.ParseServerMergePatch([]byte(tc.body))
			require.NoError(t, mergeErr)
			mapSet, mapArgs := setSQL(t, fromMap)
			bodySet, bodyArgs := setSQL(t, fromBody)
			assert.Equal(t, bodySet, mapSet)
			assert.Equal(t, bodyArgs, mapArgs)
		})
	}

	nested := map[string]any{"a": (*string)(nil)}
	_, err = p.MergePatchMap(map[string]any{"labels": nested})
	require.NoError(t, err)
	assert.Equal(t, (*string)(nil), nested["a"], "the caller's map is not modified")

	// A struct value replaces the document whole, its nil members included;
	// the same value marshaled to JSON would merge member by member.
	whole, err := p.MergePatchMap(map[string]any{"settings": gaugeSettings{Size: 2}})
	require.NoError(t, err)
	set, args := setSQL(t, whole)
	assert.Equal(t, `"settings" = $1`, set)
	value, ok := args[0].(string)
	require.True(t, ok)
	assert.JSONEq(t, `{"size":2,"theme":null}`, value)
}

// Operations built in code validate like a JSON Patch body.
func TestValidateOperations_IsTheBodysPatch(t *testing.T) {
	p, err := parse.NewResource[sensor](onSQLite, "sensors")
	require.NoError(t, err)
	ops := []patch.RawPatchOp{{Op: "replace", Path: "/rate", Value: 7}, {Op: "remove", Path: "/seen_at"}}
	built, err := p.ValidateOperations(ops)
	require.NoError(t, err)
	parsed, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/rate","value":7},{"op":"remove","path":"/seen_at"}]`))
	require.NoError(t, err)
	builtSet, builtArgs := setSQL(t, built)
	parsedSet, parsedArgs := setSQL(t, parsed)
	assert.Equal(t, parsedSet, builtSet)
	assert.Equal(t, parsedArgs, builtArgs)

	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/created_at", Value: time.Now()}})
	require.Error(t, err, "a path the schema does not admit")
	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: time.Now()}})
	require.Error(t, err, "a server field is not a request's")
	_, err = p.ValidateServerOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: time.Now()}})
	assert.NoError(t, err)
}

// A ParsedPatch assembled by hand skipped validation: applying or compiling
// it is an error, never a panic or a write.
func TestHandBuiltPatchIsRefused(t *testing.T) {
	p, err := parse.NewResource[sensor](onSQLite, "sensors")
	require.NoError(t, err)
	byHand := &patch.ParsedPatch{Operations: []*patch.PatchOperation{{Op: patch.PatchOpReplace, Path: "/name", Value: "x"}}}
	_, err = p.ApplyValidatedPatch(byHand, sensor{})
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = p.BuildUpdate(byHand, parse.ByID("1"), parse.Scope{})
	assert.ErrorIs(t, err, parse.ErrForeign)
}

type docConfig struct {
	Threshold int `json:"threshold"`
}

// docEntity exercises the RFC 6902 behaviours a JSON Patch engine most
// easily gets wrong.
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

func docMetaOps() []patch.PatchOp {
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpTest, patch.PatchOpMove}
	return ops
}

func docParser(t *testing.T, opts ...core.Option) *parse.Resource[docEntity] {
	t.Helper()
	return docResource(t, docMetaOps(), opts...)
}

// docResource declares /meta/* with metaOps.
func docResource(t *testing.T, metaOps []patch.PatchOp, opts ...core.Option) *parse.Resource[docEntity] {
	t.Helper()
	p, err := parse.NewResource[docEntity](onSQLite, "doc_entities", parse.ParserOptions(opts...), parse.CustomizeSchemas(func(s parse.Schemas) error {
		// Nested members are reachable only through explicit wildcard paths.
		return errors.Join(
			s.Patch.AddPath("/meta/*", &patch.PathSchema{AllowedOps: slices.Clone(metaOps), DataType: core.FieldTypeAny}),
			s.Patch.AddPath("/tags/*", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpAdd}, DataType: core.FieldTypeString}),
		)
	}))
	require.NoError(t, err)
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
		small := docResource(t, append(slices.Clone(docMetaOps()), patch.PatchOpCopy), core.WithMaxPatchCopyBytes(64))
		long := int64(1)
		_, err := apply(t, small, `[{"op":"copy","from":"/counter","path":"/copied"}]`, docEntity{Counter: &long})
		require.NoError(t, err)
		wide := docEntity{Meta: map[string]any{"blob": string(make([]byte, 200))}}
		_, err = apply(t, small, `[{"op":"move","from":"/meta/blob","path":"/meta/other"}]`, wide)
		require.NoError(t, err, "move does not copy")
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
// change another's accepted operations, and changing what an accessor hands
// out changes nothing.
func TestSchemaIsolation_DefaultOperatorSlices(t *testing.T) {
	type entity struct {
		ID    string            `json:"id,omitempty" db:"id"`
		Name  string            `json:"name" parse:"filter,patch"`
		Attrs map[string]string `json:"attrs" parse:"patch"`
	}
	a, err := parse.NewResource[entity](onSQLite, "entities", parse.CustomizeSchemas(func(s parse.Schemas) error {
		s.Filter.Fields["name"].AllowedOperators[0] = filter.OpGt
		return s.Patch.UpdatePath("/attrs", func(ps *patch.PathSchema) { ps.AllowedOps[0] = patch.PatchOpTest })
	}))
	require.NoError(t, err)
	b, err := parse.NewResource[entity](onSQLite, "entities")
	require.NoError(t, err)

	assert.Equal(t, patch.PatchOpTest, pathOf(a.PatchSchema(), "/attrs").AllowedOps[0])
	assert.Equal(t, filter.OpGt, a.FilterSchema().Fields["name"].AllowedOperators[0])
	assert.Equal(t, patch.PatchOpReplace, pathOf(b.PatchSchema(), "/attrs").AllowedOps[0])
	assert.Equal(t, filter.OpEq, b.FilterSchema().Fields["name"].AllowedOperators[0])

	pathOf(b.PatchSchema(), "/attrs").AllowedOps[0] = patch.PatchOpTest
	b.FilterSchema().Fields["name"].AllowedOperators[0] = filter.OpGt
	assert.Equal(t, patch.PatchOpReplace, pathOf(b.PatchSchema(), "/attrs").AllowedOps[0], "the accessor hands out a copy")
	assert.Equal(t, filter.OpEq, b.FilterSchema().Fields["name"].AllowedOperators[0], "the accessor hands out a copy")

	c, err := parse.NewResource[entity](onSQLite, "entities")
	require.NoError(t, err)
	assert.Equal(t, patch.PatchOpReplace, pathOf(c.PatchSchema(), "/attrs").AllowedOps[0])
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
	p, err := parse.NewResource[defaults](onSQLite, "defaults")
	require.NoError(t, err)
	paths := maps.Collect(p.PatchSchema().Paths())
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
	q, err := parse.NewResource[defaults](onSQLite, "defaults", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"replace"}}
	})))
	require.NoError(t, err)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace}, pathOf(q.PatchSchema(), "/alias").AllowedOps)

	type tagged struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `json:"name" parse:"patch" patch_ops:"replace,remove"`
	}
	_, err = parse.NewResource[tagged](onSQLite, "taggeds")
	assert.ErrorIs(t, err, patch.ErrRemoveNeedsNull)
}

func TestGeneratorOptions_DefaultPatchOpsHonoured(t *testing.T) {
	type entity struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `json:"name" parse:"patch"`
	}
	p, err := parse.NewResource[entity](onSQLite, "entities", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"replace", "test"}}
	})))
	require.NoError(t, err)
	assert.Equal(t, []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpTest}, pathOf(p.PatchSchema(), "/name").AllowedOps)

	_, err = parse.NewResource[entity](onSQLite, "entities", parse.ParserOptions(core.WithGeneratorOptions(func(o *core.GeneratorOptions) {
		o.DefaultPatchOps = map[core.FieldType][]string{core.FieldTypeString: {"bogus"}}
	})))
	assert.Error(t, err)
}

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
	p, err := parse.NewResource[shapeEntity](onSQLite, "shape_entities")
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
	assert.NotContains(t, pathOf(p.PatchSchema(), "/pair").AllowedOps, patch.PatchOpArrayAppend)
	_, err = p.ParsePatch([]byte(`[{"op":"array_append","path":"/pair","value":3}]`))
	requireCode(t, err, patch.ECodeOpNotAllowed.String())

	type declared struct {
		ID   string `json:"id,omitempty" db:"id"`
		Pair [2]int `json:"pair" db:"pair" parse:"patch" patch_ops:"replace,array_append"`
	}
	_, err = parse.NewResource[declared](onSQLite, "declareds")
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

// A path schema's * is exactly one reference token, and an empty key is a
// token.
func TestPatchShapes_WildcardTokens(t *testing.T) {
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

type policyDoc struct {
	ID     string         `json:"id" db:"id"`
	Attrs  map[string]any `json:"attrs" db:"attrs" parse:"patch"`
	Status *string        `json:"status" db:"status" parse:"patch"`
	Tags   []string       `json:"tags" db:"tags" parse:"patch"`
}

// Policy reaches a forbidden member, a denied element or a cleared field
// however the request reaches it: through its parent, a whole-array write, or
// a remove carrying a stray value.
func TestPatchPolicy_JudgesWhatIsWritten(t *testing.T) {
	docs, err := parse.NewResource[policyDoc](onSQLite, "docs")
	require.NoError(t, err)

	secret := mustPolicy(t, patch.ForbidPaths("/attrs/secret/**"))
	level := mustPolicy(t, patch.ConstrainValues("/attrs/level", patch.ValueConstraint{AllowedValues: []any{"low", "high"}}))
	tags := mustPolicy(t, patch.ConstrainValues("/tags", patch.ValueConstraint{Pattern: "^[a-z]+$", DeniedValues: []any{"admin"}}))
	status := mustPolicy(t, patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}))
	for _, tc := range []struct {
		name   string
		policy *patch.PatchPolicyConstraints
		merge  bool
		body   string
		code   string
	}{
		{"merge into the parent of a forbidden member", secret, true, `{"attrs":{"secret":"x"}}`, patch.ECodePatchPolicyForbidden.String()},
		{"replace the parent of a forbidden member", secret, false, `[{"op":"replace","path":"/attrs","value":{"secret":"x"}}]`, patch.ECodePatchPolicyForbidden.String()},
		{"merge a member outside allowed values", level, true, `{"attrs":{"level":"root"}}`, patch.ECodePatchPolicyValue.String()},
		{"append a denied element", tags, false, `[{"op":"array_append","path":"/tags","value":"admin"}]`, patch.ECodePatchPolicyValue.String()},
		{"replace an array holding a denied element", tags, false, `[{"op":"replace","path":"/tags","value":["admin","BAD!"]}]`, patch.ECodePatchPolicyValue.String()},
		{"merge an array holding a denied element", tags, true, `{"tags":["admin","BAD!"]}`, patch.ECodePatchPolicyValue.String()},
		{"remove a constrained field", status, false, `[{"op":"remove","path":"/status"}]`, patch.ECodePatchPolicyValue.String()},
		{"remove carrying an allowed value", status, false, `[{"op":"remove","path":"/status","value":"active"}]`, patch.ECodePatchPolicyValue.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.merge {
				_, err = docs.ParseMergePatch([]byte(tc.body), parse.WithPatchPolicy(tc.policy, nil))
			} else {
				_, err = docs.ParsePatch([]byte(tc.body), parse.WithPatchPolicy(tc.policy, nil))
			}
			requireCode(t, err, tc.code)
		})
	}
}

func mustPolicy(t *testing.T, opts ...patch.PolicyOption) *patch.PatchPolicyConstraints {
	t.Helper()
	pc, err := patch.NewPatchPolicyConstraints(opts...)
	require.NoError(t, err)
	return pc
}

// A remove clears the field whatever value the client attached.
func TestPatch_RemoveCarriesNoValue(t *testing.T) {
	docs, err := parse.NewResource[policyDoc](onSQLite, "docs")
	require.NoError(t, err)
	parsed, err := docs.ParsePatch([]byte(`[{"op":"remove","path":"/status","value":"active"}]`))
	require.NoError(t, err)
	assert.Nil(t, parsed.Operations[0].Value)

	plan, err := docs.BuildUpdate(parsed, parse.ByID("1"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `SET "status" = NULL`)
}

type transferRow struct {
	ID     string  `json:"id,omitempty" db:"id"`
	Source *string `json:"source" parse:"patch" patch_ops:"replace,remove,copy,move"`
	Status string  `json:"status" parse:"patch" patch_ops:"replace,copy,move" enum:"active,offline"`
}

func TestPatchTransferDestinationValidation(t *testing.T) {
	p, err := parse.NewResource[transferRow](onSQLite, "transfer_rows")
	require.NoError(t, err)
	for _, verb := range []string{"copy", "move"} {
		for _, tc := range []struct {
			name      string
			source    *string
			wantError bool
		}{
			{"enum", new("invalid"), true}, {"null", nil, true}, {"valid", new("active"), false},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				target := transferRow{Source: tc.source, Status: "offline"}
				parsed, err := p.ParsePatch([]byte(fmt.Sprintf(`[{"op":%q,"from":"/source","path":"/status"}]`, verb)))
				require.NoError(t, err)
				got, err := p.ApplyValidatedPatch(parsed, target)
				if tc.wantError {
					var pe *core.ParseError
					require.ErrorAs(t, err, &pe)
					require.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
					require.Equal(t, "/status", pe.Path)
					require.Equal(t, 0, *pe.Index)
				} else {
					require.NoError(t, err)
					require.Equal(t, "active", got.Status)
					if verb == "move" {
						require.Nil(t, got.Source)
					} else {
						require.Equal(t, tc.source, got.Source)
					}
				}
				require.Equal(t, "offline", target.Status)
				require.Equal(t, tc.source, target.Source)
			})
		}
	}
}

func TestPatchTransferUsesCurrentValueAndCustomValidator(t *testing.T) {
	sentinel := errors.New("destination validator")
	p, err := parse.NewResource[transferRow](onSQLite, "transfer_rows", parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.UpdatePath("/status", func(status *patch.PathSchema) {
			status.Validators = append(status.Validators, func(value any) error {
				if value == "offline" {
					return sentinel
				}
				return nil
			})
		})
	}))
	require.NoError(t, err)
	for _, tc := range []struct {
		value string
		fail  bool
	}{{"offline", true}, {"active", false}} {
		target := transferRow{Source: new("invalid"), Status: "offline"}
		body := fmt.Sprintf(`[{"op":"replace","path":"/source","value":%q},{"op":"copy","from":"/source","path":"/status"}]`, tc.value)
		got, applyErr := p.ApplyPatch([]byte(body), target)
		if tc.fail {
			require.ErrorIs(t, applyErr, sentinel)
		} else {
			require.NoError(t, applyErr)
			require.Equal(t, "active", got.Status)
		}
		require.Equal(t, "invalid", *target.Source)
	}
	// A policy-constrained destination remains rejected before document execution.
	_, err = p.ParsePatch([]byte(`[{"op":"copy","from":"/source","path":"/status"}]`), parse.WithPatchPolicy(mustPolicy(t, patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active"}})), nil))
	require.Error(t, err)
}

type emptyKeyRow struct {
	ID   string         `json:"id,omitempty" db:"id"`
	Meta map[string]any `json:"meta" parse:"patch"`
}

func emptyKeyParser(t *testing.T, opts ...core.Option) *parse.Resource[emptyKeyRow] {
	t.Helper()
	p, err := parse.NewResource[emptyKeyRow](onSQLite, "empty_key_rows", parse.ParserOptions(opts...), parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.AddPath("/meta/**", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpCopy, patch.PatchOpMove, patch.PatchOpTest}})
	}))
	require.NoError(t, err)
	return p
}

func TestPatchEmptyObjectKeys(t *testing.T) {
	p := emptyKeyParser(t)
	target := emptyKeyRow{Meta: map[string]any{"": map[string]any{"x": "old"}, "\x00": "collision", "array": []any{map[string]any{"": map[string]any{"z": "old"}}}}}
	body := `[
 {"op":"replace","path":"/meta//x","value":"new"},
 {"op":"copy","from":"/meta//x","path":"/meta/copied"},
 {"op":"move","from":"/meta/copied","path":"/meta//moved"},
 {"op":"remove","path":"/meta//x"},
 {"op":"add","path":"/meta/array/0//z","value":"new"},
 {"op":"add","path":"/meta//payload","value":{"":{"a/b":"ok"},"\u0001":"keep"}},
 {"op":"test","path":"/meta//moved","value":"new"},
 {"op":"test","path":"/meta/array/0//z","value":"new"},
 {"op":"test","path":"/meta//payload//a~1b","value":"ok"}
 ]`
	got, err := p.ApplyPatch([]byte(body), target)
	require.NoError(t, err)
	require.Equal(t, "collision", got.Meta["\x00"])
	nested, ok := got.Meta[""].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "new", nested["moved"])
	require.NotContains(t, nested, "x")
	payload, payloadOK := nested["payload"].(map[string]any)
	require.True(t, payloadOK)
	require.Equal(t, "keep", payload["\x01"])
	original, originalOK := target.Meta[""].(map[string]any)
	require.True(t, originalOK)
	require.Equal(t, "old", original["x"])
	_, err = p.ApplyPatch([]byte(`[{"op":"replace","path":"/meta//missing","value":1}]`), target)
	require.Error(t, err)
	_, err = p.ApplyPatch([]byte(`[{"op":"add","path":"/meta/missing//x","value":1}]`), target)
	require.Error(t, err)
}

func TestPatchCopyBudgetCountsOverwritesAcrossRuns(t *testing.T) {
	for _, source := range []string{"/meta/source", "/meta//source"} {
		t.Run(source, func(t *testing.T) {
			p := emptyKeyParser(t, core.WithMaxPatchCopyBytes(10))
			target := emptyKeyRow{Meta: map[string]any{"source": "abcd", "": map[string]any{"source": "abcd"}, "dest": "existing"}}
			// Each copy costs six encoded bytes even when replacing a longer value.
			body := fmt.Sprintf(`[{"op":"copy","from":%q,"path":"/meta/dest"},{"op":"test","path":"/meta/dest","value":"abcd"},{"op":"copy","from":%q,"path":"/meta/dest"}]`, source, source)
			_, err := p.ApplyPatch([]byte(body), target)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, 2, *pe.Index)
			oneCopy := []byte(fmt.Sprintf(`[{"op":"copy","from":%q,"path":"/meta/dest"}]`, source))
			got, err := emptyKeyParser(t, core.WithMaxPatchCopyBytes(6)).ApplyPatch(oneCopy, target)
			require.NoError(t, err)
			require.Equal(t, "abcd", got.Meta["dest"])
			require.Equal(t, "existing", target.Meta["dest"])
			_, err = emptyKeyParser(t, core.WithMaxPatchCopyBytes(5)).ApplyPatch(oneCopy, target)
			require.Error(t, err)
		})
	}
}

type nullablePatchRow struct {
	ID       string       `json:"id" db:"id"`
	At       *time.Time   `json:"at" db:"at" parse:"patch"`
	Required time.Time    `json:"required" db:"required" parse:"patch"`
	Times    []*time.Time `json:"times" db:"times" parse:"patch" patch_ops:"replace,array_append"`
	Values   []time.Time  `json:"values" db:"values" parse:"patch" patch_ops:"replace,array_append"`
}

func TestValidateOperations_TypedNilTimestamp(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow](onSQLite, "nullable_patch_rows")
	require.NoError(t, err)
	at := time.Date(2026, 10, 25, 1, 2, 3, 123456789, time.UTC)
	var absent *time.Time
	var boxed any = absent
	for name, value := range map[string]any{
		"nil pointer":            absent,
		"pointer to nil pointer": &absent,
		"pointer to interface":   &boxed,
	} {
		t.Run(name, func(t *testing.T) {
			built, parseErr := p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: value}})
			require.NoError(t, parseErr)
			wire, parseErr := p.ParsePatch([]byte(`[{"op":"replace","path":"/at","value":null}]`))
			require.NoError(t, parseErr)
			assert.Nil(t, built.Operations[0].Value)
			assert.Equal(t, wire.AffectedPaths, built.AffectedPaths)
			for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
				builtSQL, sqlErr := patch.BuildPatchSQL(built, dialect)
				require.NoError(t, sqlErr)
				wireSQL, sqlErr := patch.BuildPatchSQL(wire, dialect)
				require.NoError(t, sqlErr)
				assert.Equal(t, wireSQL, builtSQL)
				assert.Contains(t, builtSQL.SetClause, `"at" = NULL`)
			}
			got, applyErr := p.ApplyValidatedPatch(built, nullablePatchRow{At: &at, Required: at})
			require.NoError(t, applyErr)
			assert.Nil(t, got.At)
			assert.Equal(t, at, got.Required)
		})
	}

	nonnull, err := p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: &at}})
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(nonnull, nullablePatchRow{})
	require.NoError(t, err)
	require.NotNil(t, got.At)
	assert.Equal(t, at, *got.At)

	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/required", Value: absent}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
	assert.Contains(t, pe.Message, "not nullable")

	server, err := parse.NewResource[sensor](onSQLite, "sensors")
	require.NoError(t, err)
	cleared, err := server.ValidateServerOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: absent}})
	require.NoError(t, err)
	assert.Nil(t, cleared.Operations[0].Value)
}

func TestValidateOperations_TypedNilArrayElement(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow](onSQLite, "nullable_patch_rows")
	require.NoError(t, err)
	var absent *time.Time
	for _, op := range []string{"replace", "array_append"} {
		t.Run(op, func(t *testing.T) {
			var value any = absent
			if op == "replace" {
				value = []*time.Time{absent}
			}
			built, parseErr := p.ValidateOperations([]patch.RawPatchOp{{Op: op, Path: "/times", Value: value}})
			require.NoError(t, parseErr)
			got, applyErr := p.ApplyValidatedPatch(built, nullablePatchRow{})
			require.NoError(t, applyErr)
			assert.Equal(t, []*time.Time{nil}, got.Times)

			_, parseErr = p.ValidateOperations([]patch.RawPatchOp{{Op: op, Path: "/values", Value: value}})
			require.Error(t, parseErr, "non-nullable array elements reject typed nil")
		})
	}
}

func TestValidateOperations_CyclicPointerIsNotNull(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow](onSQLite, "nullable_patch_rows")
	require.NoError(t, err)
	var cycle any
	cycle = &cycle
	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: cycle}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
}

func TestValidateOperations_TypedNilValidatorValue(t *testing.T) {
	for name, goType := range map[string]reflect.Type{
		"time":    reflect.TypeFor[*time.Time](),
		"string":  reflect.TypeFor[*string](),
		"integer": reflect.TypeFor[*int64](),
		"boolean": reflect.TypeFor[*bool](),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			p := patch.NewParser(newSchema("", 0, map[string]*patch.PathSchema{
				"/value": {
					AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, GoType: goType,
					Validators: []patch.Validator{func(value any) error {
						calls++
						assert.Nil(t, value, "validators receive canonical null")
						return nil
					}},
				},
			}), nil)
			_, err := p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: reflect.Zero(goType).Interface()}})
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
		})
	}
}

type credentialRow struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name" parse:"filter,patch"`
	Fingerprint *string   `json:"fingerprint" db:"fingerprint" parse:"filter,server"`
	HasSecret   bool      `json:"-" db:"has_secret" parse:"server"`
	CreatedAt   time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

// A request body cannot reach a server field; the server's own patch can,
// hidden ones included, and the repositories apply it like any other.
func TestResource_ServerMergePatch(t *testing.T) {
	p, err := parse.NewResource[credentialRow](onPostgres, "credential_rows")
	require.NoError(t, err)

	for _, body := range []string{`{"fingerprint":"abc"}`, `{"has_secret":true}`} {
		_, parseErr := p.ParseMergePatch([]byte(body))
		require.Error(t, parseErr, "a request cannot set a server field: %s", body)
	}
	_, err = p.ParseServerMergePatch([]byte(`{"created_at":"2026-01-01T00:00:00Z"}`))
	require.Error(t, err, "the server patch admits server fields, not every column")
	_, err = p.ParseFilter(`{"has_secret":true}`)
	require.Error(t, err, "a hidden server field is not filterable")

	parsed, err := p.ParseServerMergePatch([]byte(`{"name":"rotated","fingerprint":"abc","has_secret":true}`))
	require.NoError(t, err)

	got, err := p.ApplyValidatedPatch(parsed, credentialRow{ID: "1", Name: "old"})
	require.NoError(t, err)
	require.NotNil(t, got.Fingerprint)
	assert.Equal(t, credentialRow{ID: "1", Name: "rotated", Fingerprint: got.Fingerprint, HasSecret: true}, got)
	assert.Equal(t, "abc", *got.Fingerprint)

	sql, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	require.NoError(t, err)
	for _, col := range []string{"name", "fingerprint", "has_secret"} {
		assert.Contains(t, sql.SetClause, col)
	}
}

// numericEntity covers every integer width, signedness and pointer shape the
//
//	probes corrupted.
type numericEntity struct {
	ID    int64          `json:"id"    db:"id"    parse:"filter,sort"`
	Small int8           `json:"small" db:"small" parse:"filter,patch"`
	U8    uint8          `json:"u8"    db:"u8"    parse:"filter,patch"`
	PU64  *uint64        `json:"pu64"  db:"pu64"  parse:"patch"`
	PI    *int           `json:"pi"    db:"pi"    parse:"patch"`
	Big   int64          `json:"big"   db:"big"   parse:"filter,patch" patch_ops:"replace,test"`
	Ratio float32        `json:"ratio" db:"ratio" parse:"patch"`
	Meta  map[string]any `json:"meta"  db:"meta"  parse:"patch"`
}

func numericParser(t *testing.T) *parse.Resource[numericEntity] {
	t.Helper()
	p, err := parse.NewResource[numericEntity](onSQLite, "numeric_entities")
	require.NoError(t, err)
	return p
}

func requireCode(t *testing.T, err error, want string) {
	t.Helper()
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "want *core.ParseError, got %v", err)
	assert.Equal(t, want, pe.Code, pe.Message)
}

// Out-of-domain values are rejected when the patch is parsed — never wrapped,
// truncated or rounded into a different value by a later backend.
func TestPatch_RejectsOutOfDomainNumbers(t *testing.T) {
	p := numericParser(t)
	bodies := map[string]string{
		"int8 overflow":         `{"small": 128}`,
		"int8 underflow":        `{"small": -129}`,
		"uint8 overflow":        `{"u8": 256}`,
		"unsigned negative":     `{"u8": -1}`,
		"*uint64 negative":      `{"pu64": -1}`,
		"*int fractional":       `{"pi": 3.7}`,
		"int64 overflow":        `{"big": 9223372036854775808}`,
		"float32 overflow":      `{"ratio": 1e39}`,
		"string for an integer": `{"small": "5"}`,
	}
	for name, body := range bodies {
		t.Run("merge/"+name, func(t *testing.T) {
			_, err := p.ParseMergePatch([]byte(body))
			requireCode(t, err, patch.ECodePatchValueInvalid.String())
		})
	}
	_, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/small","value":128}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
}

func TestPatch_AppliesExactIntegers(t *testing.T) {
	p := numericParser(t)

	t.Run("direct path", func(t *testing.T) {
		parsed, err := p.ParseMergePatch([]byte(`{"big": 9007199254740993, "pu64": 18446744073709551615, "small": -128, "pi": 7}`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
		require.NotNil(t, got.PU64)
		assert.Equal(t, uint64(math.MaxUint64), *got.PU64)
		assert.Equal(t, int8(-128), got.Small)
		require.NotNil(t, got.PI)
		assert.Equal(t, 7, *got.PI)
	})

	t.Run("document path", func(t *testing.T) {
		// A test op forces the encode → apply → decode document path.
		parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/big","value":1},{"op":"replace","path":"/big","value":9007199254740993}]`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{Big: 1})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
	})

	t.Run("document path leaves untouched fields exact", func(t *testing.T) {
		parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/big","value":9007199254740993},{"op":"replace","path":"/small","value":1}]`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{Big: 9007199254740993, PU64: new(uint64(math.MaxUint64))})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
		assert.Equal(t, uint64(math.MaxUint64), *got.PU64)
		assert.Equal(t, int8(1), got.Small)
	})

	t.Run("nested JSON keeps exact leaves", func(t *testing.T) {
		parsed, err := p.ParseMergePatch([]byte(`{"meta": {"n": 9007199254740993}}`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{})
		require.NoError(t, err)
		n, ok := core.CompareNumbers(got.Meta["n"], int64(9007199254740993))
		assert.True(t, ok)
		assert.Zero(t, n)
	})
}

func TestFilter_RejectsOutOfDomainNumbers(t *testing.T) {
	p := numericParser(t)
	for name, q := range map[string]string{
		"int8 overflow":        `{"small": 128}`,
		"unsigned negative":    `{"u8": {"$in": [1, -1]}}`,
		"fraction for integer": `{"big": 1.5}`,
		"float lost precision": `{"big": 9007199254740993.0e0}`,
		"string for integer":   `{"big": "5"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.ParseFilter(q)
			if name == "float lost precision" {
				// An exact literal written in exponent form is still exact.
				require.NoError(t, err)
				return
			}
			requireCode(t, err, filter.ECodeValueTypeMismatch.String())
		})
	}
	_, err := queryParams(t, p.FilterSchema()).Parse(map[string][]string{"small": {"128"}})
	requireCode(t, err, filter.ECodeValueTypeMismatch.String())
}

// A patch that fails part-way must not leave a pointer target half-written.
func TestApplyValidatedPatch_PointerTargetUntouchedOnFailure(t *testing.T) {
	p, err := parse.NewResource[*numericEntity](onSQLite, "numeric_entities")
	require.NoError(t, err)
	parsed, err := p.ParseMergePatch([]byte(`{"big": 5, "meta": {"a": 1}}`))
	require.NoError(t, err)
	// Force the second operation to fail at apply by retyping its value.
	for _, op := range parsed.Operations {
		if op.Path == "/meta" {
			op.Value = "not an object"
			op.Merge = false
		}
	}
	original := &numericEntity{Big: 1}
	_, err = p.ApplyValidatedPatch(parsed, original)
	require.Error(t, err)
	assert.Equal(t, int64(1), original.Big, "caller's struct must be unchanged after a failed patch")

	ok, err := p.ParseMergePatch([]byte(`{"big": 5}`))
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(ok, original)
	require.NoError(t, err)
	assert.Equal(t, int64(5), got.Big)
	assert.Equal(t, int64(1), original.Big, "successful apply returns a new value")
}

type productionPatchRow struct {
	ID       string  `json:"id,omitempty" db:"id"`
	Name     string  `json:"name" parse:"patch" patch_ops:"replace,test"`
	Optional *string `json:"optional,omitempty" parse:"patch" patch_ops:"replace,remove"`
	Secret   bool    `json:"-" db:"secret" parse:"server"`
	private  string
}

func TestDocumentPatchPreservesHiddenState(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow](onSQLite, "production_patch_rows")
	require.NoError(t, err)
	original := productionPatchRow{Name: "old", Secret: true, private: "keep"}
	got, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), original)
	require.NoError(t, err)
	require.True(t, got.Secret)
	require.Equal(t, "keep", got.private)
	require.Equal(t, "new", got.Name)
	require.Equal(t, "old", original.Name)
}

func TestDocumentPatchOmittedFieldMatchesDirect(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow](onSQLite, "production_patch_rows")
	require.NoError(t, err)
	for _, operation := range []string{`{"op":"replace","path":"/optional","value":"new"}`, `{"op":"remove","path":"/optional"}`} {
		direct, err := p.ApplyPatch([]byte("["+operation+"]"), productionPatchRow{Name: "old"})
		require.NoError(t, err)
		fallback, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},`+operation+`]`), productionPatchRow{Name: "old"})
		require.NoError(t, err)
		require.Equal(t, direct, fallback)
	}
}

func TestPatchRejectsNullDocument(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow](onSQLite, "production_patch_rows")
	require.NoError(t, err)
	_, err = p.ParsePatch([]byte(`null`))
	require.Error(t, err)
	_, err = p.ParseMergePatch([]byte(`null`))
	require.Error(t, err)
}

func TestMergeNullRunsDestinationValidators(t *testing.T) {
	rejected := errors.New("clearing optional")
	p, err := parse.NewResource[productionPatchRow](onSQLite, "production_patch_rows", parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.UpdatePath("/optional", func(ps *patch.PathSchema) {
			ps.Validators = []patch.Validator{func(value any) error {
				if value == nil {
					return rejected
				}
				return nil
			}}
		})
	}))
	require.NoError(t, err)
	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/optional","value":null}]`))
	require.ErrorIs(t, err, rejected)
	_, err = p.ParseMergePatch([]byte(`{"optional":null}`))
	require.ErrorIs(t, err, rejected)
}

func TestPatchNullableArrayElements(t *testing.T) {
	type row struct {
		ID       string `json:"id,omitempty" db:"id"`
		Numbers  []int  `json:"numbers" parse:"patch" patch_ops:"replace,array_append"`
		Optional []*int `json:"optional" parse:"patch" patch_ops:"replace,array_append"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	require.NoError(t, err)
	_, err = p.ParsePatch([]byte(`[{"op":"array_append","path":"/numbers","value":null}]`))
	require.Error(t, err)
	for _, body := range []string{`[{"op":"replace","path":"/optional","value":[null,1]}]`, `[{"op":"array_append","path":"/optional","value":null},{"op":"array_append","path":"/optional","value":1}]`} {
		got, err := p.ApplyPatch([]byte(body), row{})
		require.NoError(t, err)
		require.Len(t, got.Optional, 2)
		require.Nil(t, got.Optional[0])
		require.Equal(t, 1, *got.Optional[1])
	}
}

func TestDocumentPatchNilTarget(t *testing.T) {
	p, err := parse.NewResource[*productionPatchRow](onSQLite, "production_patch_rows")
	require.NoError(t, err)
	_, err = p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"}]`), nil)
	require.Error(t, err)
	maps, err := parse.NewResource[*emptyKeyRow](onSQLite, "empty_key_rows")
	require.NoError(t, err)
	parsed, err := maps.ParseMergePatch([]byte(`{"meta":{"x":1}}`))
	require.NoError(t, err)
	_, err = maps.ApplyValidatedPatch(parsed, nil)
	require.Error(t, err)
}

func TestDocumentPatchPreservesUnrelatedNestedState(t *testing.T) {
	type details struct {
		Visible string `json:"visible"`
		Hidden  string `json:"-"`
		private string
	}
	type row struct {
		ID      string  `json:"id,omitempty" db:"id"`
		Name    string  `json:"name" parse:"patch" patch_ops:"replace,test"`
		Details details `json:"details"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	require.NoError(t, err)
	target := row{Name: "old", Details: details{Visible: "visible", Hidden: "hidden", private: "private"}}
	got, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), target)
	require.NoError(t, err)
	require.Equal(t, target.Details, got.Details)
	require.Equal(t, "new", got.Name)
}

func TestDocumentPatchRequiresMatchingFieldCodec(t *testing.T) {
	type row struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `bson:"name" json:"-" parse:"patch" patch_ops:"replace,test"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	require.NoError(t, err)
	got, err := p.ApplyPatch([]byte(`[{"op":"replace","path":"/name","value":"new"}]`), row{Name: "old"})
	require.NoError(t, err)
	require.Equal(t, "new", got.Name)
	_, err = p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), row{Name: "old"})
	require.ErrorContains(t, err, "configure a matching codec")
}

// Accepted requests may still fail against current data (for example a missing
// nested path). Neither outcome may mutate the caller or erase hidden state.
func FuzzPatchApplyPreservesTarget(f *testing.F) {
	for _, seed := range []string{
		`null`, `[]`, `{}`, `[{"op":"replace","path":"/optional","value":null}]`,
		`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`,
		`{"optional":null}`, `{"optional":"new"}`, `[{"op":"remove","path":"/optional"}]`,
	} {
		f.Add(seed)
	}
	p, err := parse.NewResource[*productionPatchRow](onSQLite, "production_patch_rows")
	require.NoError(f, err)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 4096 {
			t.Skip()
		}
		for _, merge := range []bool{false, true} {
			target := &productionPatchRow{Name: "old", Optional: new("present"), Secret: true, private: "keep"}
			var parsed *patch.ParsedPatch
			var err error
			if merge {
				parsed, err = p.ParseMergePatch([]byte(body))
			} else {
				parsed, err = p.ParsePatch([]byte(body))
			}
			if err != nil {
				continue
			}
			got, err := p.ApplyValidatedPatch(parsed, target)
			require.Equal(t, "old", target.Name)
			require.Equal(t, "present", *target.Optional)
			require.True(t, target.Secret)
			require.Equal(t, "keep", target.private)
			if err == nil {
				require.NotNil(t, got)
				require.True(t, got.Secret)
				require.Equal(t, "keep", got.private)
			}
		}
	})
}

func FuzzEmptyKeyPatchPaths(f *testing.F) {
	f.Add("", "value")
	f.Add("a/b~c", "value")
	f.Add("\x01", "\x01")
	f.Fuzz(func(t *testing.T, key, value string) {
		// A patch's text holding U+0000 or invalid UTF-8 is refused at parse
		// (TestParse_RefusesInvalidText); stored members may hold U+0000.
		if len(key)+len(value) > 4096 || core.CheckText(key) != nil || core.CheckText(value) != nil {
			t.Skip()
		}
		p := emptyKeyParser(t)
		target := emptyKeyRow{Meta: map[string]any{"": map[string]any{key: "old"}, "\x00": "collision"}}
		path := core.FormatPointer("meta", "", key)
		body, err := json.Marshal([]map[string]any{
			{"op": "replace", "path": path, "value": value},
			{"op": "copy", "from": path, "path": "/meta/destination"},
			{"op": "test", "path": "/meta/destination", "value": value},
		})
		require.NoError(t, err)
		got, err := p.ApplyPatch(body, target)
		require.NoError(t, err)
		require.Equal(t, value, got.Meta["destination"])
		require.Equal(t, "collision", got.Meta["\x00"])
		original, ok := target.Meta[""].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "old", original[key])
	})
}
