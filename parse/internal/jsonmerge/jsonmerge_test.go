//go:build unit

package jsonmerge_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
)

// rfc7386Examples mirrors the worked test vectors from RFC 7386 §3.
// Each row is (target, patch, expected) — all three serialised as
// JSON strings to match the spec's presentation. The test decodes
// each, runs jsonmerge.Apply, and asserts deep equality.
func TestApply_RFC7386Examples(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		target string
		patch  string
		expect string
	}{
		{
			name:   "add_key",
			target: `{"a":"b"}`,
			patch:  `{"a":"c"}`,
			expect: `{"a":"c"}`,
		},
		{
			name:   "add_new_key",
			target: `{"a":"b"}`,
			patch:  `{"b":"c"}`,
			expect: `{"a":"b","b":"c"}`,
		},
		{
			name:   "delete_via_null",
			target: `{"a":"b"}`,
			patch:  `{"a":null}`,
			expect: `{}`,
		},
		{
			name:   "delete_then_add",
			target: `{"a":"b","b":"c"}`,
			patch:  `{"a":null}`,
			expect: `{"b":"c"}`,
		},
		{
			name:   "nested_replace",
			target: `{"a":{"b":"c"}}`,
			patch:  `{"a":{"b":"d","c":null}}`,
			expect: `{"a":{"b":"d"}}`,
		},
		{
			name:   "array_replaces",
			target: `{"a":[1,2,3]}`,
			patch:  `{"a":[4,5]}`,
			expect: `{"a":[4,5]}`,
		},
		{
			name:   "object_replaces_scalar",
			target: `{"a":"b"}`,
			patch:  `{"a":{"x":1}}`,
			expect: `{"a":{"x":1}}`,
		},
		{
			name:   "scalar_replaces_object",
			target: `{"a":{"x":1}}`,
			patch:  `{"a":"b"}`,
			expect: `{"a":"b"}`,
		},
		{
			name:   "patch_replaces_non_object",
			target: `[1,2]`,
			patch:  `{"a":"b"}`,
			expect: `{"a":"b"}`,
		},
		{
			name:   "non_object_patch_replaces_target",
			target: `{"a":"b"}`,
			patch:  `null`,
			expect: `null`,
		},
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

func TestApplyAll_LayersInOrder(t *testing.T) {
	t.Parallel()
	platform := map[string]any{"a": 1, "b": 1, "c": 1}
	tenant := map[string]any{"b": 2}
	devType := map[string]any{"c": 3}
	got := jsonmerge.ApplyAll([]any{platform, tenant, devType})
	want := map[string]any{"a": 1, "b": 2, "c": 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyAll = %#v, want %#v", got, want)
	}
}

func TestApplyAll_NullDeletesAcrossLayers(t *testing.T) {
	t.Parallel()
	platform := map[string]any{"flag": true, "v": 1}
	tenant := map[string]any{"flag": nil} // explicit null deletes
	got := jsonmerge.ApplyAll([]any{platform, tenant})
	want := map[string]any{"v": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyAll = %#v, want %#v", got, want)
	}
}

func TestApplyAll_NilLayerSkipped(t *testing.T) {
	t.Parallel()
	got := jsonmerge.ApplyAll([]any{
		map[string]any{"a": 1},
		nil, // skipped
		map[string]any{"b": 2},
	})
	want := map[string]any{"a": 1, "b": 2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyAll = %#v, want %#v", got, want)
	}
}

func TestApplyAll_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	if got := jsonmerge.ApplyAll(nil); got != nil {
		t.Errorf("ApplyAll(nil) = %#v, want nil", got)
	}
}

// FuzzApply hardens the merge against arbitrary decoded JSON — the patch is
// frequently tenant-supplied. Apply must never panic on any pair of decoded
// JSON values, and its result must always re-marshal cleanly.
func FuzzApply(f *testing.F) {
	f.Add([]byte(`{"a":1}`), []byte(`{"b":2}`))
	f.Add([]byte(`{"a":{"b":1}}`), []byte(`{"a":{"b":null}}`))
	f.Add([]byte(`{"a":1}`), []byte(`null`))
	f.Add([]byte(`[1,2,3]`), []byte(`{"x":1}`))
	f.Add([]byte(`"scalar"`), []byte(`{"nested":{"deep":[1,{"k":null}]}}`))
	f.Fuzz(func(t *testing.T, targetJSON, patchJSON []byte) {
		var target, patch any
		if err := json.Unmarshal(targetJSON, &target); err != nil {
			t.Skip()
		}
		if err := json.Unmarshal(patchJSON, &patch); err != nil {
			t.Skip()
		}
		got := jsonmerge.Apply(target, patch)
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("Apply result not marshalable: %v\ntarget=%s patch=%s", err, targetJSON, patchJSON)
		}
	})
}
