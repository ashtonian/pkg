// Package jsonmerge implements RFC 7396 (JSON Merge Patch) on top of
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
//     RFC 6902 (JSON Patch), not RFC 7396.
//
// Apply operates on `any` decoded from JSON via
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
// Apply mutates neither argument, and copies only what it rewrites. The
// result shares with both arguments:
//
//   - with the target, every value the patch does not reach: a member the
//     patch does not name keeps the target's map, array or scalar.
//   - with the patch, every value it takes from it that is not an object:
//     an array, and every map and array inside it, is the patch's own.
//     An object in the patch is rebuilt, so the result never holds the
//     patch's maps outside an array.
//
// A caller that mutates the result, or either argument afterwards, needs a
// deep clone first (marshal and unmarshal through encoding/json).
package jsonmerge

import "maps"

// Apply returns the result of merging patch into target per RFC 7396.
// Both arguments are read-only — Apply does not mutate them; the package
// documentation states what the result shares with each.
//
// Top-level behaviour:
//
//   - patch is not an object (string, number, bool, array, nil) →
//     return patch verbatim. The target is replaced.
//   - patch is an object → recursively merge each key into target,
//     coercing target to an object first if it isn't already.
//
// nil patch is a special case: per RFC 7396 it semantically means
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

// asObject reports whether v is a JSON object (map[string]any) and
// returns the typed map. JSON-decoded objects always have string
// keys; this assertion is sound for values produced by encoding/json.
func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}
