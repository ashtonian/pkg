package parse

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// escapeEmptyPatchKeys isolates json-patch's special treatment of an empty
// object key (it resolves it as the current object). Only runs addressing an
// empty token need the adapter; the RFC engine still owns operation semantics.
func escapeEmptyPatchKeys(doc map[string]any, ops []map[string]any) (map[string]any, []map[string]any, func(map[string]any) map[string]any, error) {
	identity := func(v map[string]any) map[string]any { return v }
	needsEscape := false
	used := make(map[string]bool)
	for _, op := range ops {
		for _, key := range []string{"path", "from"} {
			if pointer, ok := op[key].(string); ok {
				tokens, err := core.ParsePointer(pointer)
				if err != nil {
					return nil, nil, nil, err
				}
				for _, token := range tokens {
					used[token] = true
					needsEscape = needsEscape || token == ""
				}
			}
		}
	}
	if !needsEscape {
		return doc, ops, identity, nil
	}
	// Canonicalize typed operation values before recursively renaming keys.
	encoded, err := json.Marshal(ops)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encoding patch operations: %w", err)
	}
	var wire []map[string]any
	if err := (core.StdlibJSON{}).Decode(encoded, &wire); err != nil {
		return nil, nil, nil, err
	}
	var collect func(any)
	collect = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				used[key] = true
				collect(child)
			}
		case []any:
			for _, child := range v {
				collect(child)
			}
		}
	}
	collect(doc)
	for _, op := range wire {
		collect(op["value"])
	}
	sentinel := "\x00"
	for used[sentinel] {
		sentinel += "\x00"
	}
	for _, op := range wire {
		for _, key := range []string{"path", "from"} {
			if pointer, ok := op[key].(string); ok {
				// Split the encoded pointer so escaped slashes and tildes stay intact.
				tokens := strings.Split(pointer, "/")
				for i := 1; i < len(tokens); i++ {
					if tokens[i] == "" {
						tokens[i] = sentinel
					}
				}
				op[key] = strings.Join(tokens, "/")
			}
		}
		if value, ok := op["value"]; ok {
			op["value"] = renamePatchKey(value, "", sentinel)
		}
	}
	restore := func(v map[string]any) map[string]any { return renamePatchMapKey(v, sentinel, "") }
	return renamePatchMapKey(doc, "", sentinel), wire, restore, nil
}

func renamePatchMapKey(m map[string]any, from, to string) map[string]any {
	out := make(map[string]any, len(m))
	for key, child := range m {
		if key == from {
			key = to
		}
		out[key] = renamePatchKey(child, from, to)
	}
	return out
}

func renamePatchKey(value any, from, to string) any {
	switch v := value.(type) {
	case map[string]any:
		return renamePatchMapKey(v, from, to)
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = renamePatchKey(child, from, to)
		}
		return out
	default:
		return value
	}
}
