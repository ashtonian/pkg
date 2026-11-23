package patch

import (
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/rfc6902"
)

// jsonPatchRun applies runs of RFC 6902 operations to a document with
// github.com/evanphx/json-patch/v5, which works on JSON text: the document
// and operations are encoded with the codec that decoded the document, and
// the result decoded with it again, so numbers keep the codec's handling
// throughout. One copy budget covers every step of a patch.
type jsonPatchRun struct {
	codec core.Codec
	// copyBudget bounds the bytes copies add (0: unlimited); copied is
	// what the patch's copies have added so far.
	copyBudget int64
	copied     int64
}

// apply applies ops to doc; indexes holds each op's index in the parsed
// patch, for error reporting.
func (r *jsonPatchRun) apply(doc map[string]any, ops []map[string]any, indexes []int) (map[string]any, error) {
	// Copies are applied individually, so charge their source size rather than
	// document growth (overwriting an existing value may not grow it at all).
	for k, op := range ops {
		if from, hasFrom := op[rfc6902.From].(string); hasFrom && op[rfc6902.Op] == string(PatchOpCopy) {
			if value, ok := pointerValue(doc, from); ok {
				encoded, err := r.codec.Encode(value)
				if err != nil {
					return nil, fmt.Errorf("patch: encoding copy source: %w", err)
				}
				r.copied += int64(len(encoded))
				if r.copyBudget > 0 && r.copied > r.copyBudget {
					return nil, copyBudgetError(indexes[k], r.copyBudget)
				}
			}
		}
	}
	doc, ops, restore, err := r.escapeEmptyKeys(doc, ops)
	if err != nil {
		return nil, err
	}
	patchJSON, err := r.codec.Encode(ops)
	if err != nil {
		return nil, fmt.Errorf("patch: encoding patch operations: %w", err)
	}
	decoded, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, fmt.Errorf("patch: decoding patch operations: %w", err)
	}
	docJSON, err := r.codec.Encode(doc)
	if err != nil {
		return nil, fmt.Errorf("patch: encoding document: %w", err)
	}
	opts := jsonpatch.NewApplyOptions()
	opts.SupportNegativeIndices = false // RFC 6901: array indices are non-negative
	patched, err := decoded.ApplyWithOptions(docJSON, opts)
	if err != nil {
		return nil, failedOperation(decoded, docJSON, opts, indexes, err)
	}
	var out map[string]any
	if err := r.codec.Decode(patched, &out); err != nil {
		return nil, fmt.Errorf("patch: decoding patched document: %w", err)
	}
	return restore(out), nil
}

// failedOperation is the error of the run's first operation that fails,
// which json-patch's error for the whole run does not name: the run is
// replayed one operation at a time up to it.
func failedOperation(ops jsonpatch.Patch, doc []byte, opts *jsonpatch.ApplyOptions, indexes []int, runErr error) error {
	index, cause := indexes[0], runErr
	for k := range ops {
		next, err := jsonpatch.Patch{ops[k]}.ApplyWithOptions(doc, opts)
		if err != nil {
			index, cause = indexes[k], err
			break
		}
		doc = next
	}
	failed := &core.ParseError{
		Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
		Message: fmt.Sprintf("applying operation %d: %v", index, cause),
		Index:   new(index), Cause: cause,
	}
	return failed
}

func copyBudgetError(offset int, budget int64) error {
	return &core.ParseError{
		Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
		Message: fmt.Sprintf("operation %d: copy operations exceed the %d-byte budget", offset, budget),
		Index:   new(offset),
	}
}

// escapeEmptyKeys isolates json-patch's special treatment of an empty object
// key, which it resolves as the current object: when a run's pointers name an
// empty token, the document's and values' empty keys are renamed to a key
// nothing else uses for the run, and restore renames them back. Runs without
// an empty token pass through untouched; the RFC engine still owns operation
// semantics.
func (r *jsonPatchRun) escapeEmptyKeys(doc map[string]any, ops []map[string]any) (map[string]any, []map[string]any, func(map[string]any) map[string]any, error) {
	identity := func(v map[string]any) map[string]any { return v }
	needsEscape := false
	used := make(map[string]bool)
	for _, op := range ops {
		for _, key := range []string{rfc6902.Path, rfc6902.From} {
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
	// Canonicalize typed operation values as JSON before renaming keys
	// inside them.
	encoded, err := r.codec.Encode(ops)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("patch: encoding patch operations: %w", err)
	}
	var wire []map[string]any
	if err := r.codec.Decode(encoded, &wire); err != nil {
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
		collect(op[rfc6902.Value])
	}
	sentinel := "\x00"
	for used[sentinel] {
		sentinel += "\x00"
	}
	for _, op := range wire {
		for _, key := range []string{rfc6902.Path, rfc6902.From} {
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
		if value, ok := op[rfc6902.Value]; ok {
			op[rfc6902.Value] = renameKey(value, "", sentinel)
		}
	}
	restore := func(v map[string]any) map[string]any { return renameMapKey(v, sentinel, "") }
	return renameMapKey(doc, "", sentinel), wire, restore, nil
}

func renameMapKey(m map[string]any, from, to string) map[string]any {
	out := make(map[string]any, len(m))
	for key, child := range m {
		if key == from {
			key = to
		}
		out[key] = renameKey(child, from, to)
	}
	return out
}

func renameKey(value any, from, to string) any {
	switch v := value.(type) {
	case map[string]any:
		return renameMapKey(v, from, to)
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = renameKey(child, from, to)
		}
		return out
	default:
		return value
	}
}
