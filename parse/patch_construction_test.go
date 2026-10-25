//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

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
	p, err := parse.NewResource[sensor]("sensors")
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
		assert.ErrorAs(t, mergeErr, &pe, name)
	}
	server, err := p.ServerMergePatchMap(map[string]any{"rotated": at, "name": "s2"})
	require.NoError(t, err)
	set, _ := setSQL(t, server)
	assert.Contains(t, set, "rotated = $")
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
	p, err := parse.NewResource[gauge]("gauges")
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
	assert.Equal(t, "settings = $1", set)
	value, ok := args[0].(string)
	require.True(t, ok)
	assert.JSONEq(t, `{"size":2,"theme":null}`, value)
}

// Operations built in code validate like a JSON Patch body.
func TestValidateOperations_IsTheBodysPatch(t *testing.T) {
	p, err := parse.NewResource[sensor]("sensors")
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
	assert.Error(t, err, "a path the schema does not admit")
	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: time.Now()}})
	assert.Error(t, err, "a server field is not a request's")
	_, err = p.ValidateServerOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: time.Now()}})
	assert.NoError(t, err)
}
