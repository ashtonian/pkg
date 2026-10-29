package patch

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// ParseMergePatch parses a JSON Merge Patch (RFC 7396) body into a ParsedPatch.
func ParseMergePatch(body []byte, schema *PatchSchema, config *core.ParserConfig) (*ParsedPatch, error) {
	if config == nil {
		config = core.DefaultConfig()
	}

	if config.MaxInputSize > 0 && len(body) > config.MaxInputSize {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInputTooLarge.String(),
			Message: fmt.Sprintf("input size %d exceeds maximum %d bytes", len(body), config.MaxInputSize),
		}
	}

	var raw map[string]any
	codec := config.GetCodec()
	if err := codec.Decode(body, &raw); err != nil {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInvalidJSON.String(),
			Message: "invalid JSON Merge Patch syntax", Cause: err,
		}
	}
	return ParseMergePatchMap(raw, schema, config)
}

// ParseMergePatchMap is ParseMergePatch for a merge patch already decoded,
// or built by the server: a nested map merges into a document member, any
// other value — a Go value of the path's type included (time.Time,
// uuid.UUID, a struct) — replaces, and null removes or clears. Null is
// whatever encoding/json writes as null: nil, or a nil pointer, map or
// slice. It avoids the JSON round trip of building a body only to parse it
// again.
func ParseMergePatchMap(raw map[string]any, schema *PatchSchema, config *core.ParserConfig) (*ParsedPatch, error) {
	if config == nil {
		config = core.DefaultConfig()
	}
	if raw == nil {
		return nil, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodeInvalidJSON.String(), Message: "resource merge patch must be an object"}
	}
	raw, _ = jsonNulls(raw)
	codec := config.GetCodec()

	// Bound the patch like the equivalent JSON Patch: a member nested d
	// objects deep is a d-segment pointer (MaxDepth), and every top-level
	// member and every nested object that merges is one operation
	// (MaxOperations). The second bound is what keeps the compiled SQL and
	// its arguments in proportion: scalar members of one object bind as one
	// value however many there are.
	ops, depth := mergeShape(raw, 1)
	if config.MaxDepth > 0 && depth > config.MaxDepth {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePathTooDeep.String(),
			Message: fmt.Sprintf("merge patch nests %d levels deep; maximum is %d", depth, config.MaxDepth),
		}
	}
	if schema != nil && schema.MaxOperations > 0 && ops > schema.MaxOperations {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeTooManyOps.String(),
			Message: fmt.Sprintf("too many operations: %d exceeds maximum %d", ops, schema.MaxOperations),
		}
	}

	result := &ParsedPatch{Operations: make([]*PatchOperation, 0, len(raw))}
	seen := make(map[string]struct{})

	// Sorted walk: map order made the operation list — and so the SQL SET
	// clause and apply order — vary run to run.
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		value := raw[key]
		path := core.FormatPointer(key)
		pathSchema, err := matchPath(schema, path)
		if err != nil {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(),
				Message: fmt.Sprintf("path '%s': %v", path, err),
				Path:    path, Cause: err,
			}
		}
		var op PatchOp
		if value == nil {
			op = PatchOpRemove
		} else {
			op = PatchOpReplace
		}
		// Only an object in the patch merges; a Go value normalizes to an
		// object too (a struct) but replaces the member whole.
		_, merges := value.(map[string]any)

		// RFC 7396 null means "remove"; report it as a nullability error
		// rather than a confusing "remove not allowed".
		if err := checkNullability(pathSchema, op, value, 1); err != nil {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("invalid value for path '%s': %v", path, err),
				Path:    path,
			}
		}
		if !pathSchema.AllowsOp(op) {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodeOpNotAllowed.String(),
				Message: fmt.Sprintf("operation '%s' not allowed on path '%s'", op, path),
				Path:    path,
			}
		}

		if value != nil {
			normalized, err := normalizeMergeValue(pathSchema, op, value, codec)
			if err != nil {
				return nil, &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
					Message: fmt.Sprintf("invalid value for path '%s': %v", path, err),
					Path:    path, Cause: err,
				}
			}
			value = normalized
		}
		// Explicit null is a removal, but still a proposed value that custom
		// validators may forbid, just as for JSON Patch replace with null.
		for _, v := range pathSchema.Validators {
			if err := v(value); err != nil {
				return nil, &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
					Message: fmt.Sprintf("invalid value for path '%s': %v", path, err),
					Path:    path, Cause: err,
				}
			}
		}
		if err := checkItems(pathSchema, op, value, 1); err != nil {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("invalid value for path '%s': %v", path, err),
				Path:    path, Cause: err,
			}
		}

		result.Operations = append(result.Operations, &PatchOperation{
			Op: op, Path: path, Value: value, Schema: pathSchema,
			// RFC 7396 §2: object values merge recursively; scalars/arrays/null
			// replace or remove. Mark object values so apply/SQL honor merge
			// semantics instead of silently replacing sub-objects.
			Merge: merges,
		})
		seen[path] = struct{}{}
	}

	result.AffectedPaths = slices.Sorted(maps.Keys(seen))
	result.EstimatedCost = estimatePatchCost(result)

	return result, nil
}

// jsonNulls returns obj with each member encoding/json writes as null — a
// nil pointer, map or slice — as nil, in nested objects too, and whether it
// replaced any. obj itself is returned, never modified, when none is there.
func jsonNulls(obj map[string]any) (map[string]any, bool) {
	var out map[string]any
	for k, v := range obj {
		var n any
		switch tv := v.(type) {
		case nil:
			continue
		case map[string]any:
			if tv != nil {
				nested, changed := jsonNulls(tv)
				if !changed {
					continue
				}
				n = nested
			}
		default:
			rv := reflect.ValueOf(v)
			switch rv.Kind() {
			case reflect.Pointer, reflect.Map, reflect.Slice:
				if !rv.IsNil() {
					continue
				}
			default:
				continue
			}
		}
		if out == nil {
			out = maps.Clone(obj)
		}
		out[k] = n
	}
	if out == nil {
		return obj, false
	}
	return out, true
}

// mergeShape returns the operations a merge object at depth costs — each of
// its members at the top level, and below that each member that is itself
// an object — and the depth of its deepest member. Arrays replace whole, so
// objects inside them do not merge and are not counted.
func mergeShape(obj map[string]any, depth int) (ops, deepest int) {
	deepest = depth
	for _, v := range obj {
		nested, isObject := v.(map[string]any)
		if depth == 1 || isObject {
			ops++
		}
		if !isObject {
			continue
		}
		subOps, subDepth := mergeShape(nested, depth+1)
		ops += subOps
		deepest = max(deepest, subDepth)
	}
	if len(obj) == 0 {
		deepest = depth - 1
	}
	return ops, deepest
}
