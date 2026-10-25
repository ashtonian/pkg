// Package jsonmerge implements RFC 7386 (JSON Merge Patch) on top of
// stdlib encoding/json's `any` representation.
//
// parse applies merge patches to documents with it. The algorithm is small,
// but it has a few subtle rules worth restating:
//
//   - When the patch is an object, recursively merge it into the
//     target (which is also coerced to an object — non-object targets
//     are replaced wholesale).
//   - When the patch is anything else (primitive, array, null), the
//     patch replaces the target.
//   - Within an object patch, a `null` value at a key REMOVES the
//     key from the target. This is the only way to express deletion.
//   - Arrays are NOT merged element-wise — the patch's array replaces
//     the target's array entirely. Element-wise array patching needs
//     RFC 6902 (JSON Patch), not RFC 7386.
//
// The functions here operate on `any` decoded from JSON via
// stdlib encoding/json. Callers serialise both target and patch to
// JSON and decode them with `json.Unmarshal` before calling [Apply].
//
// Example:
//
//	target := map[string]any{"a": 1, "b": map[string]any{"x": 1}}
//	patch  := map[string]any{"b": map[string]any{"x": nil, "y": 2}}
//	merged := jsonmerge.Apply(target, patch)
//	// merged == map[string]any{"a": 1, "b": map[string]any{"y": 2}}
//
// Merging is non-destructive on the target: the function returns a
// new value rather than mutating the input. Nested maps inside the
// target ARE shared with the result (the implementation copies only
// when a value is rewritten); callers that need a deep clone should
// marshal and unmarshal through encoding/json before applying patches.
package jsonmerge

import "maps"

// Apply returns the result of merging patch into target per RFC 7386.
// Both arguments are read-only — Apply does not mutate them.
//
// Top-level behaviour:
//
//   - patch is not an object (string, number, bool, array, nil) →
//     return patch verbatim. The target is replaced.
//   - patch is an object → recursively merge each key into target,
//     coercing target to an object first if it isn't already.
//
// nil patch is a special case: per RFC 7386 it semantically means
// "replace target with null." Apply returns nil.
func Apply(target, patch any) any {
	pm, isObj := asObject(patch)
	if !isObj {
		// Patch is a scalar / array / null — replace target wholesale.
		return patch
	}

	// Patch is an object — coerce target to an object too.
	tm, _ := asObject(target)
	if tm == nil {
		tm = make(map[string]any, len(pm))
	}

	out := make(map[string]any, len(tm)+len(pm))
	maps.Copy(out, tm)
	for k, v := range pm {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = Apply(out[k], v)
	}
	return out
}

// ApplyAll merges layers in order from index 0 (lowest precedence) to
// the last (highest precedence). Each layer is applied as a JSON
// Merge Patch onto the accumulated result. An empty layers slice
// returns the zero value (nil).
//
// A nil layer is treated as an ABSENT tier (no override) and skipped —
// the common N-tier case where a tier supplied no config. Consequently
// ApplyAll cannot express a top-level JSON `null` "reset to null": a
// decoded JSON null and an absent layer are both Go nil and
// indistinguishable here. If you need RFC 7386 top-level null-replace
// semantics, call Apply(result, nil) directly (it returns nil).
//
// Use this for the canonical N-tier override pattern: pass
// `[]any{platformConfig, tenantConfig, projectConfig, serviceConfig,
// instanceConfig}` and get the effective resolved config.
func ApplyAll(layers []any) any {
	var result any
	for _, layer := range layers {
		if layer == nil {
			continue
		}
		result = Apply(result, layer)
	}
	return result
}

// asObject reports whether v is a JSON object (map[string]any) and
// returns the typed map. JSON-decoded objects always have string
// keys; this assertion is sound for values produced by encoding/json.
func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}
