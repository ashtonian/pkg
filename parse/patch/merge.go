package patch

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// ParseMergePatch parses a JSON Merge Patch (RFC 7396) body into a ParsedPatch.
func ParseMergePatch(body []byte, schema *PatchSchema, config *core.ParserConfig) (*ParsedPatch, error) {
	if schema == nil {
		return nil, errNoSchema
	}
	config = config.Effective()

	if config.MaxInputSize > 0 && len(body) > config.MaxInputSize {
		return nil, core.NewError(ECodeInputTooLarge, fmt.Sprintf("input size %d exceeds maximum %d bytes", len(body), config.MaxInputSize))
	}

	var raw map[string]any
	codec := config.GetCodec()
	if err := codec.Decode(body, &raw); err != nil {
		return nil, core.NewError(ECodeInvalidJSON, "invalid JSON Merge Patch syntax", core.WithCause(err))
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
	if schema == nil {
		return nil, errNoSchema
	}
	config = config.Effective()
	if raw == nil {
		return nil, core.NewError(ECodeInvalidJSON, "resource merge patch must be an object")
	}
	raw, _, err := jsonNulls(raw, config.MaxDepth)
	if err != nil {
		tooDeep := core.NewError(ECodePathTooDeep, fmt.Sprintf("merge patch nests more than %d levels deep", config.MaxDepth),
			core.WithCause(err))
		return nil, tooDeep
	}
	codec := config.GetCodec()

	// Bound the patch like the equivalent JSON Patch: a member nested d
	// objects deep is a d-segment pointer (MaxDepth), and every top-level
	// member and every nested object that merges is one operation
	// (MaxOperations). The second bound is what keeps the compiled SQL and
	// its arguments in proportion: scalar members of one object bind as one
	// value however many there are.
	ops, depth := mergeShape(raw, 1)
	if depth > config.MaxDepth {
		return nil, core.NewError(ECodePathTooDeep, fmt.Sprintf("merge patch nests %d levels deep; maximum is %d", depth, config.MaxDepth))
	}
	if schema.MaxOperations > 0 && ops > schema.MaxOperations {
		return nil, core.NewError(ECodeTooManyOps, fmt.Sprintf("too many operations: %d exceeds maximum %d", ops, schema.MaxOperations))
	}

	result := &ParsedPatch{Operations: make([]*PatchOperation, 0, len(raw)), schema: schema}

	// Sorted walk: map order would make the operation list — and so the SQL
	// SET clause and apply order — vary run to run.
	var failures []error
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		operation, err := validateMergeMember(schema, codec, key, raw[key])
		if err != nil {
			failures = append(failures, err)
			continue
		}
		result.Operations = append(result.Operations, operation)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}

	result.AffectedPaths = affectedPaths(result.Operations)

	return result, nil
}

func validateMergeMember(schema *PatchSchema, codec core.Codec, key string, value any) (*PatchOperation, error) {
	path := core.FormatPointer(key)
	err := core.CheckText(key)
	var pathSchema *PathSchema
	if err == nil {
		pathSchema, err = matchPath(schema, path)
	}
	if err != nil {
		return nil, core.NewError(ECodePatchFieldNotFound, fmt.Sprintf("path '%s': %v", path, err),
			core.WithPath(path), core.WithCause(err))
	}
	// RFC 7396: null removes the member, any other value replaces it, and
	// only an object in the patch merges. A Go value normalizes to an object
	// too (a struct) but replaces the member whole.
	op := PatchOpReplace
	if value == nil && pathSchema.Relation == nil {
		op = PatchOpRemove
	}
	_, merges := value.(map[string]any)
	value, failures := newValueChecker(schema, codec).check(pathSchema, op, value, 1, true)
	if len(failures) > 0 {
		errs := make([]error, len(failures))
		for i, f := range failures {
			if f.code == ECodeOpNotAllowed {
				errs[i] = core.NewError(f.code, fmt.Sprintf("operation '%s' not allowed on path '%s'", op, path), core.WithPath(path))
				continue
			}
			errs[i] = core.NewError(f.code, fmt.Sprintf("invalid value for path '%s': %v", path, f.cause), f.options(core.WithPath(path))...)
		}
		return nil, errors.Join(errs...)
	}
	return &PatchOperation{Op: op, Path: path, Value: value, Schema: pathSchema, Merge: merges}, nil
}

// jsonNulls returns obj with each member encoding/json writes as null — a
// nil pointer, map or slice — as nil, in nested objects too, and whether it
// replaced any. obj itself is returned, never modified, when none is there.
// An object nested more than limit levels below obj, as every level of a
// map that holds itself is, is refused (errNestedTooDeep): mergeShape,
// which bounds the patch's depth, walks only a patch this one has.
func jsonNulls(obj map[string]any, limit int) (map[string]any, bool, error) {
	if limit < 0 {
		return nil, false, errNestedTooDeep
	}
	var out map[string]any
	for k, v := range obj {
		var n any
		switch tv := v.(type) {
		case nil:
			continue
		case map[string]any:
			if tv != nil {
				nested, changed, err := jsonNulls(tv, limit-1)
				if err != nil {
					return nil, false, err
				}
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
		return obj, false, nil
	}
	return out, true, nil
}

// errNestedTooDeep is jsonNulls' refusal of an object nested past its limit.
var errNestedTooDeep = errors.New("object nested past the depth limit")

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
