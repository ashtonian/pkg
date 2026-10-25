//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse"
)

type cfgEntity struct {
	ID     string         `json:"id,omitempty" db:"id"`
	Name   string         `json:"name"   parse:"filter,patch" patch_ops:"replace"`
	Config map[string]any `json:"config" parse:"patch"        patch_ops:"replace,remove"`
}

// TestMergePatchPreservesSiblings: RFC 7396 §2 requires recursive merge
// for object values. Previously ParseMergePatch produced a whole-value
// replace, so applying {"config":{"theme":"dark"}} deleted config.lang.
func TestMergePatchPreservesSiblings(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity]("cfg_entities")
	if err != nil {
		t.Fatal(err)
	}

	target := cfgEntity{
		Name:   "dev",
		Config: map[string]any{"theme": "light", "lang": "en"},
	}

	parsed, err := tp.ParseMergePatch([]byte(`{"config": {"theme": "dark"}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	if out.Config["theme"] != "dark" {
		t.Errorf("theme = %v; want dark", out.Config["theme"])
	}
	if out.Config["lang"] != "en" {
		t.Errorf("lang = %v; want en preserved (RFC 7396 recursive merge)", out.Config["lang"])
	}
	if out.Name != "dev" {
		t.Errorf("Name = %q; want untouched", out.Name)
	}
}

// TestMergePatchNestedNullDeletes: within an object merge, null at a key
// removes that key (RFC 7396) — previously the null was set literally.
func TestMergePatchNestedNullDeletes(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity]("cfg_entities")
	if err != nil {
		t.Fatal(err)
	}
	target := cfgEntity{Config: map[string]any{"theme": "light", "lang": "en"}}

	parsed, err := tp.ParseMergePatch([]byte(`{"config": {"lang": null}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := out.Config["lang"]; exists {
		t.Errorf("lang still present (%v); want removed per RFC 7396 nested null", out.Config["lang"])
	}
	if out.Config["theme"] != "light" {
		t.Errorf("theme = %v; want light preserved", out.Config["theme"])
	}
}

// TestTopLevelNullStillRemoves: a top-level null clears the whole field.
// A map has no NULL state (patch.PathSchema.ClearedValue), so it empties.
func TestTopLevelNullStillRemoves(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity]("cfg_entities")
	if err != nil {
		t.Fatal(err)
	}
	target := cfgEntity{Config: map[string]any{"theme": "light"}}
	parsed, err := tp.ParseMergePatch([]byte(`{"config": null}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	if out.Config == nil || len(out.Config) != 0 {
		t.Errorf("Config = %#v; want an empty map", out.Config)
	}
}
