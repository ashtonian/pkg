//go:build unit || concurrency

package jsonmerge_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
)

// The fifteen examples of RFC 7396 Appendix A, then further cases. Each row
// is (target, patch, expected), all three JSON text as the RFC presents
// them. The test decodes each, runs jsonmerge.Apply, and asserts deep
// equality.
func TestApply_RFC7396Examples(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		target string
		patch  string
		expect string
	}{
		{name: "A.1 replace a member", target: `{"a":"b"}`, patch: `{"a":"c"}`, expect: `{"a":"c"}`},
		{name: "A.2 add a member", target: `{"a":"b"}`, patch: `{"b":"c"}`, expect: `{"a":"b","b":"c"}`},
		{name: "A.3 null removes the member", target: `{"a":"b"}`, patch: `{"a":null}`, expect: `{}`},
		{name: "A.4 null removes one member of two", target: `{"a":"b","b":"c"}`, patch: `{"a":null}`, expect: `{"b":"c"}`},
		{name: "A.5 a scalar replaces an array", target: `{"a":["b"]}`, patch: `{"a":"c"}`, expect: `{"a":"c"}`},
		{name: "A.6 an array replaces a scalar", target: `{"a":"c"}`, patch: `{"a":["b"]}`, expect: `{"a":["b"]}`},
		{name: "A.7 nested replace and remove", target: `{"a":{"b":"c"}}`, patch: `{"a":{"b":"d","c":null}}`, expect: `{"a":{"b":"d"}}`},
		{name: "A.8 an array replaces an array of objects", target: `{"a":[{"b":"c"}]}`, patch: `{"a":[1]}`, expect: `{"a":[1]}`},
		{name: "A.9 an array patch replaces an array", target: `["a","b"]`, patch: `["c","d"]`, expect: `["c","d"]`},
		{name: "A.10 an array patch replaces an object", target: `{"a":"b"}`, patch: `["c"]`, expect: `["c"]`},
		{name: "A.11 a null patch replaces the target", target: `{"a":"foo"}`, patch: `null`, expect: `null`},
		{name: "A.12 a string patch replaces the target", target: `{"a":"foo"}`, patch: `"bar"`, expect: `"bar"`},
		{name: "A.13 a null member of the target stays", target: `{"e":null}`, patch: `{"a":1}`, expect: `{"e":null,"a":1}`},
		{name: "A.14 an object patch replaces an array, without its null members", target: `[1,2]`, patch: `{"a":"b","c":null}`, expect: `{"a":"b"}`},
		{name: "A.15 null inside a new member leaves an empty object", target: `{}`, patch: `{"a":{"bb":{"ccc":null}}}`, expect: `{"a":{"bb":{}}}`},
		{name: "array_replaces", target: `{"a":[1,2,3]}`, patch: `{"a":[4,5]}`, expect: `{"a":[4,5]}`},
		{name: "object_replaces_scalar", target: `{"a":"b"}`, patch: `{"a":{"x":1}}`, expect: `{"a":{"x":1}}`},
		{name: "scalar_replaces_object", target: `{"a":{"x":1}}`, patch: `{"a":"b"}`, expect: `{"a":"b"}`},
		{name: "patch_replaces_non_object", target: `[1,2]`, patch: `{"a":"b"}`, expect: `{"a":"b"}`},
		{name: "null_removes_the_last_nested_member", target: `{"a":{"b":1}}`, patch: `{"a":{"b":null}}`, expect: `{"a":{}}`},
		// An array is a value, not a patch: a null inside one stays.
		{name: "patch_replaces_scalar_keeping_nulls_in_arrays", target: `"scalar"`, patch: `{"nested":{"deep":[1,{"k":null}]}}`, expect: `{"nested":{"deep":[1,{"k":null}]}}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var target, patch, expect any
			if err := json.Unmarshal([]byte(tt.target), &target); err != nil {
				t.Fatalf("decode target: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.patch), &patch); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.expect), &expect); err != nil {
				t.Fatalf("decode expect: %v", err)
			}
			got := jsonmerge.Apply(target, patch)
			if !reflect.DeepEqual(got, expect) {
				t.Errorf("Apply(%s, %s) = %#v, want %#v", tt.target, tt.patch, got, expect)
			}
		})
	}
}

func TestApply_DoesNotMutateTarget(t *testing.T) {
	t.Parallel()
	target := map[string]any{"a": map[string]any{"x": 1}}
	patch := map[string]any{"a": map[string]any{"y": 2}}
	_ = jsonmerge.Apply(target, patch)
	// Original target's nested map must not have grown a `y` key.
	a, ok := target["a"].(map[string]any)
	if !ok {
		t.Fatalf("nested target = %T, want map[string]any", target["a"])
	}
	if _, found := a["y"]; found {
		t.Errorf("Apply mutated target — got %#v", target)
	}
}

// The result shares with the target what the patch does not reach, and with
// the patch the arrays it takes; it holds no map of the patch outside one.
func TestApply_SharesUnrewrittenValues(t *testing.T) {
	t.Parallel()
	kept := []any{"t"}
	target := map[string]any{"kept": kept, "doc": map[string]any{"x": 1}}
	taken := []any{map[string]any{"in": "array"}}
	patchDoc := map[string]any{"y": 2}
	patch := map[string]any{"list": taken, "doc": patchDoc}

	got := jsonmerge.Apply(target, patch).(map[string]any)

	got["kept"].([]any)[0] = "changed"
	if kept[0] != "changed" {
		t.Errorf("a member the patch does not name is not the target's")
	}
	got["list"].([]any)[0].(map[string]any)["in"] = "changed"
	if taken[0].(map[string]any)["in"] != "changed" {
		t.Errorf("an array taken from the patch is not the patch's")
	}
	got["doc"].(map[string]any)["z"] = 3
	if _, found := patchDoc["z"]; found {
		t.Errorf("the result holds a map of the patch")
	}
	if _, found := target["doc"].(map[string]any)["z"]; found {
		t.Errorf("the result holds the target's map along the patched path")
	}
}
