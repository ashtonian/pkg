package patch

import (
	"reflect"
	"slices"
	"sync"

	"github.com/ashtonian/pkg/parse/core"
)

// PatchOp represents a JSON Patch operation type.
type PatchOp string

const (
	PatchOpAdd     PatchOp = "add"
	PatchOpRemove  PatchOp = "remove"
	PatchOpReplace PatchOp = "replace"
	PatchOpMove    PatchOp = "move"
	PatchOpCopy    PatchOp = "copy"
	PatchOpTest    PatchOp = "test"

	// PatchOpArrayAppend is the extension to JSON Patch RFC 6902
	// for atomic single-statement array append. BuildPatchSQL renders it
	// to dialect-specific SQL:
	//
	//   Postgres: col = array_append(COALESCE(col, '{}'), $1)
	//   SQLite:   col = json_insert(COALESCE(col, '[]'), '$[#]', $1)
	//
	// Concurrent appenders converge without read-modify-write.
	PatchOpArrayAppend PatchOp = "array_append"
)

// ValidPatchOps is the set of all recognized patch operations.
var ValidPatchOps = map[PatchOp]bool{
	PatchOpAdd: true, PatchOpRemove: true, PatchOpReplace: true,
	PatchOpMove: true, PatchOpCopy: true, PatchOpTest: true,
	PatchOpArrayAppend: true,
}

// DefaultMaxOperations is the operation limit used by GeneratePatchSchema and
// GenerateServerPatchSchema. Callers constructing a PatchSchema choose its limit.
const DefaultMaxOperations = 50

// PatchSchema defines allowed patch operations for a resource.
type PatchSchema struct {
	Resource      string
	AllowedPaths  map[string]*PathSchema
	MaxOperations int

	// sortedPathsOnce/List memoize AllowedPaths' keys most specific first,
	// so wildcard resolution is deterministic.
	sortedPathsOnce sync.Once
	sortedPathsList []pathPattern
}

// sortedPaths returns the AllowedPaths keys as patterns, most specific first
// (compareSpecificity). Exact-match lookup happens before this list is
// consulted, so it only orders wildcard candidates.
func (s *PatchSchema) sortedPaths() []pathPattern {
	s.sortedPathsOnce.Do(func() {
		for key := range s.AllowedPaths {
			s.sortedPathsList = append(s.sortedPathsList, pathPattern{text: key, tokens: pointerTokens(key)})
		}
		slices.SortFunc(s.sortedPathsList, compareSpecificity)
	})
	return s.sortedPathsList
}

// PathSchema defines a single patchable path.
type PathSchema struct {
	AllowedOps []PatchOp
	DataType   core.FieldType
	// SQLType distinguishes declared JSON from native driver-value storage.
	SQLType core.SQLType
	// GoType is the Go type the path's value decodes into (the struct
	// field's type, pointer included). It drives parse-time validation so an
	// accepted operation always applies: numeric range
	// against the exact kind, nullability (only pointer, slice, map and
	// interface fields accept null or remove), array element types, and the
	// shape of struct / map values. nil — hand-built schemas — validates by
	// DataType alone over the widest numeric domain and admits null.
	GoType reflect.Type
	// Wire is the form operations give the path's value in: core.WireString
	// for a bool, integer or float field tagged json:",string", whose value
	// is a JSON string ("42", not 42), as encoding/json writes the field.
	// Values normalize from it, so SQL binds and validators see the number.
	Wire       core.Wire
	Validators []core.Validator
	// EnumValues is the path's `enum` allowlist (also a Validator), for
	// schema discovery.
	EnumValues []string
	// Bounds are the path's minlen, maxlen, min and max bounds (also a
	// Validator) and its minitems and maxitems element counts (checked on
	// an array set whole, and by Nullable), for schema discovery.
	Bounds    core.Bounds
	SQLColumn string

	// Relation is non-nil when the path patches an M2M relation rather
	// than a scalar column. When set, BuildPatchSQL emits
	// junction INSERT/DELETE statements into PatchSQLResult.RelationOps
	// instead of touching SQLColumn (which is "" for relation paths).
	// AllowedOps for a relation path is the relation-verb subset
	// (replace / add / remove); RFC 6902's move/copy/test are rejected
	// at validation with E2117.
	Relation *core.RelationSchema
}

// Nullable reports whether the path accepts null: its Go type can hold
// null, and null does not clear it to an empty array its least element count
// refuses. Unknown types (GoType nil) are treated as nullable.
func (p *PathSchema) Nullable() bool {
	return p.GoType == nil || acceptsNull(p.GoType, p.Bounds)
}

// ClearedValue is what removing the path, or setting it to null, stores:
// nil (SQL NULL) for pointers, interfaces and untyped paths, and the empty
// value for a slice or map. Slice and map columns are usually NOT NULL with
// an empty default, where NULL fails the write, and empty is a valid cleared
// value for a nullable one too — so clearing empties them on every backend.
func (p *PathSchema) ClearedValue() any {
	if p == nil || p.GoType == nil || p.Relation != nil {
		return nil
	}
	switch {
	case clearsToEmptySlice(p.GoType):
		return reflect.MakeSlice(p.GoType, 0, 0).Interface()
	case p.GoType.Kind() == reflect.Map:
		return reflect.MakeMap(p.GoType).Interface()
	}
	return nil
}

// clearsToEmptySlice reports a slice type a clear empties; []byte and
// json.RawMessage clear to NULL.
func clearsToEmptySlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8
}

// acceptsNull reports whether a path of Go type t bounded by b accepts null:
// t can hold null, and null does not clear it to an empty array b refuses.
func acceptsNull(t reflect.Type, b core.Bounds) bool {
	return isNullableType(t) && !mustHoldElement(t, b)
}

// mustHoldElement reports a slice path, which a clear empties, whose least
// element count is above zero.
func mustHoldElement(t reflect.Type, b core.Bounds) bool {
	return b.MinItems != nil && *b.MinItems > 0 && clearsToEmptySlice(t)
}

// AllowsOp checks whether op is in the path's allowed ops list.
func (p *PathSchema) AllowsOp(op PatchOp) bool {
	return slices.Contains(p.AllowedOps, op)
}

// PatchOperation represents a validated patch operation.
type PatchOperation struct {
	Op   PatchOp
	Path string
	From string
	// Value is what the operation writes: nil for a remove of a field and
	// for a move or copy, whose value is read from the document as the patch
	// applies. A relation's remove holds the members it removes, and a test
	// the document it compares.
	Value  any
	Schema *PathSchema
	// fromSchema is a move's or copy's source path schema.
	fromSchema *PathSchema

	// Merge marks an op derived from an RFC 7396 merge patch whose value is
	// an object — it requires RECURSIVE merge semantics, not whole-value
	// replace. The apply path deep-merges via
	// internal/jsonmerge; BuildPatchSQL compiles it to json_patch on SQLite
	// and per-level jsonb construction on PostgreSQL, so sibling keys survive.
	Merge bool
}

// ParsedPatch is the result of parsing a JSON Patch document.
type ParsedPatch struct {
	Operations    []*PatchOperation
	EstimatedCost int
	// AffectedPaths are the paths the operations write, sorted and once each:
	// a move's source included, a test's path not.
	AffectedPaths []string

	// policy is the policy ApplyPatchPolicyConstraints held the patch to.
	policy *PatchPolicyConstraints
}

// AuditInfo returns a map of audit-relevant information.
func (p *ParsedPatch) AuditInfo() map[string]any {
	info := map[string]any{
		"operation_count": len(p.Operations),
		"estimated_cost":  p.EstimatedCost,
		"affected_paths":  p.AffectedPaths,
	}
	if p.policy != nil {
		info["forbidden_path_count"] = len(p.policy.forbidden)
		info["value_constraint_count"] = len(p.policy.constraints)
		info["validator_count"] = len(p.policy.validators)
	}
	return info
}
