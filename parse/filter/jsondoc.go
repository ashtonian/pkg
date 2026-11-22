package filter

import "github.com/ashtonian/pkg/parse/core"

// JSON document semantics shared by every backend. They are
// PostgreSQL jsonb's: `$eq` / `$ne` compare documents (core.JSONEqual),
// `$contains` is `@>`. Arguments are generic documents as core.NormalizeJSON
// returns them.

// JSONContains reports whether doc contains needle: an object contains
// every needle member (recursively), an array contains every needle element
// somewhere in it (order and multiplicity ignored), a scalar contains an
// equal scalar, and — at the top level only — an array contains a scalar
// needle that is one of its elements.
func JSONContains(doc, needle any) bool {
	if arr, ok := doc.([]any); ok && isJSONScalar(needle) {
		for _, e := range arr {
			if core.JSONEqual(e, needle) {
				return true
			}
		}
		return false
	}
	return jsonContains(doc, needle)
}

func jsonContains(doc, needle any) bool {
	switch n := needle.(type) {
	case map[string]any:
		d, ok := doc.(map[string]any)
		if !ok {
			return false
		}
		for k, nv := range n {
			dv, ok := d[k]
			if !ok || !jsonContains(dv, nv) {
				return false
			}
		}
		return true
	case []any:
		d, ok := doc.([]any)
		if !ok {
			return false
		}
		for _, nv := range n {
			found := false
			for _, dv := range d {
				if jsonContains(dv, nv) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	return isJSONScalar(doc) && core.JSONEqual(doc, needle)
}

func isJSONScalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return true
}

// jsonShape reports a document's depth (containers nested, scalars 0) and
// its number of values, for the parser's size limits.
func jsonShape(v any) (depth, nodes int) {
	nodes = 1
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			d, n := jsonShape(e)
			depth, nodes = max(depth, d+1), nodes+n
		}
		if len(x) == 0 {
			depth = 1
		}
	case []any:
		for _, e := range x {
			d, n := jsonShape(e)
			depth, nodes = max(depth, d+1), nodes+n
		}
		if len(x) == 0 {
			depth = 1
		}
	}
	return depth, nodes
}

// JSONHasKey is the reference semantics of `$has_key` (jsonb `?`): doc is a
// decoded document holding key as a top-level object key (whatever its
// value, null included), as a string element of a top-level array, or as
// itself when it is a string.
func JSONHasKey(doc any, key string) bool {
	switch d := doc.(type) {
	case map[string]any:
		_, ok := d[key]
		return ok
	case []any:
		for _, e := range d {
			if s, ok := e.(string); ok && s == key {
				return true
			}
		}
	case string:
		return d == key
	}
	return false
}
