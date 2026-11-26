//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type presentRow struct {
	Nick  string   `json:"nick,omitempty"         db:"nick"  parse:"patch" patch_ops:"replace,test,copy"`
	Alias string   `json:"alias"                  db:"alias" parse:"patch" patch_ops:"replace,copy"`
	Note  *string  `json:"note,omitempty"         db:"note"  parse:"patch" patch_ops:"replace,remove,test,move"`
	Prev  *string  `json:"prev"                   db:"prev"  parse:"patch" patch_ops:"replace,remove,move"`
	Count int64    `json:"count,string,omitempty" db:"count" parse:"patch" patch_ops:"replace,test"`
	Tags  []string `json:"tags,omitzero"          db:"tags"  parse:"patch" patch_ops:"replace,test"`
}

// Every top-level field exists for every operation: test, copy and move read
// a field T's encoding omits (omitempty, omitzero) as the value it holds,
// written as T's encoding writes it, as replace and remove already treat it.
func TestApplier_PresentsOmittedTopLevelFields(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[presentRow](gen)
	require.NoError(t, err)
	applier, err := patch.NewApplier[presentRow](gen, nil)
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	seed := func() presentRow {
		prev := "p"
		row := presentRow{Alias: "a", Prev: &prev}
		return row
	}
	for _, tc := range []struct {
		name string
		body string
		want func(*presentRow)
	}{
		{"test an omitted empty string, then replace it", `[{"op":"test","path":"/nick","value":""},{"op":"replace","path":"/nick","value":"x"}]`, func(r *presentRow) { r.Nick = "x" }},
		{"test an omitted nil pointer", `[{"op":"test","path":"/note","value":null}]`, func(*presentRow) {}},
		{"test an omitted quoted zero", `[{"op":"test","path":"/count","value":"0"}]`, func(*presentRow) {}},
		{"test an omitted nil slice", `[{"op":"test","path":"/tags","value":null}]`, func(*presentRow) {}},
		{"copy an omitted field", `[{"op":"copy","from":"/nick","path":"/alias"}]`, func(r *presentRow) { r.Alias = "" }},
		{"move an omitted field", `[{"op":"move","from":"/note","path":"/prev"}]`, func(r *presentRow) { r.Prev = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := parser.Parse([]byte(tc.body))
			require.NoError(t, err)
			got, err := applier.Apply(parsed, seed())
			require.NoError(t, err)
			want := seed()
			tc.want(&want)
			assert.Equal(t, want, got)
		})
	}

	// A value the field does not hold still fails its test.
	parsed, err := parser.Parse([]byte(`[{"op":"test","path":"/nick","value":"x"}]`))
	require.NoError(t, err)
	_, err = applier.Apply(parsed, seed())
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
}
