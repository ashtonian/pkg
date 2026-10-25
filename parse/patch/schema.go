package patch

import (
	"maps"
	"reflect"
	"slices"
	"strings"
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

// PatchSchema defines allowed patch operations for a resource.
type PatchSchema struct {
	Resource      string
	AllowedPaths  map[string]*PathSchema
	MaxOperations int

	// sortedPathsOnce/List memoize AllowedPaths' pattern keys in
	// most-specific-first order so wildcard resolution is deterministic
	//. Lazy so struct-literal construction (tests, callers
	// bypassing GeneratePatchSchema) still works.
	sortedPathsOnce sync.Once
	sortedPathsList []string
}

// sortedPaths returns the AllowedPaths pattern keys sorted most-specific
// first: longest literal prefix before the first '*' desc, then fewest
// wildcards asc, then lexicographic. Exact-match lookup happens before this
// list is consulted, so it only orders wildcard candidates.
func (s *PatchSchema) sortedPaths() []string {
	s.sortedPathsOnce.Do(func() {
		s.sortedPathsList = sortPatternsBySpecificity(slices.Collect(maps.Keys(s.AllowedPaths)))
	})
	return s.sortedPathsList
}

// sortPatternsBySpecificity orders wildcard patterns deterministically,
// most-specific first. Shared by schema path matching and policy value-
// constraint matching.
func sortPatternsBySpecificity(patterns []string) []string {
	slices.SortFunc(patterns, func(a, b string) int {
		la, lb := literalPrefixLen(a), literalPrefixLen(b)
		if la != lb {
			return lb - la // longest literal prefix first
		}
		wa, wb := strings.Count(a, "*"), strings.Count(b, "*")
		if wa != wb {
			return wa - wb // fewest wildcards first
		}
		return strings.Compare(a, b)
	})
	return patterns
}

func literalPrefixLen(pattern string) int {
	if i := strings.IndexByte(pattern, '*'); i >= 0 {
		return i
	}
	return len(pattern)
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
	GoType     reflect.Type
	Validators []core.Validator
	// EnumValues is the path's `enum` allowlist (also a Validator), for
	// schema discovery.
	EnumValues []string
	SQLColumn  string

	// Relation is non-nil when the path patches an M2M relation rather
	// than a scalar column. When set, BuildPatchSQL emits
	// junction INSERT/DELETE statements into PatchSQLResult.RelationOps
	// instead of touching SQLColumn (which is "" for relation paths).
	// AllowedOps for a relation path is the relation-verb subset
	// (replace / add / remove); RFC 6902's move/copy/test are rejected
	// at validation with E2117.
	Relation *core.RelationSchema
}

// Nullable reports whether the path's Go type can hold null. Unknown types
// (GoType nil) are treated as nullable.
func (p *PathSchema) Nullable() bool {
	return p.GoType == nil || isNullableType(p.GoType)
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
	switch p.GoType.Kind() {
	case reflect.Slice:
		if p.GoType.Elem().Kind() == reflect.Uint8 { // []byte, json.RawMessage
			return nil
		}
		return reflect.MakeSlice(p.GoType, 0, 0).Interface()
	case reflect.Map:
		return reflect.MakeMap(p.GoType).Interface()
	}
	return nil
}

// AllowsOp checks whether op is in the path's allowed ops list.
func (p *PathSchema) AllowsOp(op PatchOp) bool {
	return slices.Contains(p.AllowedOps, op)
}

// PatchOperation represents a validated patch operation.
type PatchOperation struct {
	Op     PatchOp
	Path   string
	From   string
	Value  any
	Schema *PathSchema

	// Merge marks an op derived from an RFC 7396 merge patch whose value is
	// an object — it requires RECURSIVE merge semantics, not whole-value
	// replace. The apply path deep-merges via
	// internal/jsonmerge; BuildPatchSQL compiles it to json_patch on SQLite
	// and per-level jsonb construction on PostgreSQL, so sibling keys survive.
	Merge bool
}

// ParsedPatch is the result of parsing a JSON Patch document.
type ParsedPatch struct {
	Operations        []*PatchOperation
	EstimatedCost     int
	AffectedPaths     []string
	PolicyConstraints *PatchPolicyConstraints
}

// AuditInfo returns a map of audit-relevant information.
func (p *ParsedPatch) AuditInfo() map[string]any {
	info := map[string]any{
		"operation_count": len(p.Operations),
		"estimated_cost":  p.EstimatedCost,
		"affected_paths":  p.AffectedPaths,
	}
	if p.PolicyConstraints != nil {
		info["forbidden_path_count"] = len(p.PolicyConstraints.ForbiddenPaths)
		info["value_constraint_count"] = len(p.PolicyConstraints.ValueConstraints)
		info["validator_count"] = len(p.PolicyConstraints.Validators)
	}
	return info
}
