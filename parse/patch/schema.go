package patch

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

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

// ops are the recognized patch operations, in documentation order.
var ops = []PatchOp{PatchOpAdd, PatchOpRemove, PatchOpReplace, PatchOpMove, PatchOpCopy, PatchOpTest, PatchOpArrayAppend}

// IsValidOp reports whether op is a recognized patch operation.
func IsValidOp(op PatchOp) bool { return slices.Contains(ops, op) }

// Ops returns the recognized patch operations, in documentation order. The
// slice is the caller's.
func Ops() []PatchOp { return slices.Clone(ops) }

// DefaultMaxOperations is the operation limit a PatchSchema has unless its
// MaxOperations is set.
const DefaultMaxOperations = 50

// DefaultMaxRelationValues is the number of keys one operation on a relation
// path may name, once repeats are removed, unless a PatchSchema's
// MaxRelationValues is set.
const DefaultMaxRelationValues = 1000

// PatchSchema defines the paths a resource's patches may write and the
// limits a patch is held to. Build one with NewPatchSchema, or generate it
// with GeneratePatchSchema; AddPath adds a path, and every parse after it
// sees the path. It is safe for concurrent use.
type PatchSchema struct {
	Resource string
	// MaxOperations bounds the operations of one patch; 0 is unlimited.
	MaxOperations int
	// MaxRelationValues bounds the keys one operation on a relation path
	// names, once repeats are removed (E2119); 0 is unlimited.
	MaxRelationValues int

	// mu serializes AddPath; readers load paths without it.
	mu    sync.Mutex
	paths atomic.Pointer[pathSet]
}

// pathSet is one immutable version of a schema's paths: an exact key is
// looked up in byKey, and a wildcard key is tried in patterns, most specific
// first, so resolution is deterministic and a matched pattern always has its
// path schema.
type pathSet struct {
	byKey    map[string]*PathSchema
	patterns []pathPattern
}

// NewPatchSchema builds the schema of resource's patches with paths, keyed by
// JSON Pointer or pattern (a token is a literal, * for exactly one token or
// ** for zero or more), MaxOperations DefaultMaxOperations and
// MaxRelationValues DefaultMaxRelationValues. It refuses a
// key whose token holds * among other characters, a nil path schema and an
// unknown operation.
func NewPatchSchema(resource string, paths map[string]*PathSchema) (*PatchSchema, error) {
	s := &PatchSchema{Resource: resource, MaxOperations: DefaultMaxOperations, MaxRelationValues: DefaultMaxRelationValues}
	set := &pathSet{byKey: make(map[string]*PathSchema, len(paths))}
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(paths)) {
		if err := set.add(key, paths[key]); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("patch schema %s: %w", resource, err)
	}
	set.sort()
	s.paths.Store(set)
	return s, nil
}

// AddPath adds a path to the schema, checked as NewPatchSchema checks one;
// a key the schema already has is refused. Parses that start after it see
// the path, and none sees a partial change.
func (s *PatchSchema) AddPath(key string, ps *PathSchema) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.pathSet()
	if _, exists := current.byKey[key]; exists {
		return fmt.Errorf("patch schema %s: path %q is declared twice", s.Resource, key)
	}
	next := &pathSet{byKey: maps.Clone(current.byKey), patterns: slices.Clone(current.patterns)}
	if next.byKey == nil {
		next.byKey = map[string]*PathSchema{}
	}
	if err := next.add(key, ps); err != nil {
		return fmt.Errorf("patch schema %s: %w", s.Resource, err)
	}
	next.sort()
	s.paths.Store(next)
	return nil
}

// Path returns the path schema declared under key, a JSON Pointer or
// pattern as given to NewPatchSchema; it does not match patterns.
func (s *PatchSchema) Path(key string) (*PathSchema, bool) {
	ps, ok := s.pathSet().byKey[key]
	return ps, ok
}

// Paths yields the schema's keys and path schemas in key order.
func (s *PatchSchema) Paths() iter.Seq2[string, *PathSchema] {
	set := s.pathSet()
	return func(yield func(string, *PathSchema) bool) {
		for _, key := range slices.Sorted(maps.Keys(set.byKey)) {
			if !yield(key, set.byKey[key]) {
				return
			}
		}
	}
}

// reaching returns the first key, in key order, that admits pointer or a
// path below it.
func (s *PatchSchema) reaching(pointer string) (string, bool) {
	tokens := pointerTokens(pointer)
	for key := range s.Paths() {
		pattern := pathPattern{text: key, tokens: pointerTokens(key)}
		if pattern.reaches(tokens) {
			return key, true
		}
	}
	return "", false
}

// Clone returns a deep copy of the schema: it parses as s does, and its
// paths can be added to or changed without changing s. Validators are
// shared.
func (s *PatchSchema) Clone() *PatchSchema {
	if s == nil {
		return nil
	}
	out := &PatchSchema{Resource: s.Resource, MaxOperations: s.MaxOperations, MaxRelationValues: s.MaxRelationValues}
	set := s.pathSet()
	next := &pathSet{byKey: make(map[string]*PathSchema, len(set.byKey)), patterns: slices.Clone(set.patterns)}
	for key, ps := range set.byKey {
		next.byKey[key] = ps.clone()
	}
	out.paths.Store(next)
	return out
}

func (p *PathSchema) clone() *PathSchema {
	out := *p
	out.AllowedOps = slices.Clone(p.AllowedOps)
	out.Validators = slices.Clone(p.Validators)
	out.EnumValues = slices.Clone(p.EnumValues)
	out.Bounds = p.Bounds.Clone()
	out.Relation = p.Relation.Clone()
	out.RelationKey = slices.Clone(p.RelationKey)
	return &out
}

// pathSet is the schema's current paths; none for a zero PatchSchema.
func (s *PatchSchema) pathSet() *pathSet {
	if set := s.paths.Load(); set != nil {
		return set
	}
	return &pathSet{}
}

// resolve returns the path schema for a request path: its exact key, else
// the most specific pattern that matches it.
func (set *pathSet) resolve(path string) (*PathSchema, bool) {
	if ps, ok := set.byKey[path]; ok {
		return ps, true
	}
	tokens := pointerTokens(path)
	for _, pattern := range set.patterns {
		if pattern.matches(tokens) {
			return set.byKey[pattern.text], true
		}
	}
	return nil, false
}

func (set *pathSet) add(key string, ps *PathSchema) error {
	pattern, err := parsePattern(key)
	if err != nil {
		return fmt.Errorf("path: %w", err)
	}
	if ps == nil {
		return fmt.Errorf("path %q has no path schema", key)
	}
	for _, op := range ps.AllowedOps {
		if !IsValidOp(op) {
			return fmt.Errorf("path %q: unknown patch operation %q", key, op)
		}
	}
	set.byKey[key] = ps
	if pattern.isWildcard() {
		set.patterns = append(set.patterns, pattern)
	}
	return nil
}

func (set *pathSet) sort() { slices.SortFunc(set.patterns, compareSpecificity) }

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
	// A relation path's wire is each key's: its target key's.
	Wire       core.Wire
	Validators []Validator
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
	// AllowedOps for a generated relation path is the relation-verb subset
	// (replace / add / remove), so the parser refuses RFC 6902's
	// move/copy/test there (E2064); BuildPatchSQL refuses one a hand-built
	// schema allows (E2117).
	Relation *core.RelationSchema
	// RelationKey are a membership path's target key columns when the key
	// has several (RelationPath): each key is a JSON array of a value per
	// column, normalized to a core.Key. A one-column key's type is GoType's
	// element.
	RelationKey []RelationKey
}

// Nullable reports whether the path accepts null: its Go type can hold
// null, and null does not clear it to an empty array its least element count
// refuses. Unknown types (GoType nil) are treated as nullable.
func (p *PathSchema) Nullable() bool {
	return p.GoType == nil || acceptsNull(p.GoType, p.Bounds)
}

// clearedValue is what removing the path, or setting it to null, stores:
// nil (SQL NULL) for pointers, interfaces and untyped paths, and the empty
// value for a slice or map. Slice and map columns are usually NOT NULL with
// an empty default, where NULL fails the write, and empty is a valid cleared
// value for a nullable one too — so clearing empties them on every backend.
func (p *PathSchema) clearedValue() any {
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

// wholeValue is what op, an add, replace or remove of its whole field,
// leaves there, and whether op cleared the field: op's value, or for a
// remove or a null the path's clearedValue, nil where it clears to SQL
// NULL. Apply and every SQL path start from it, so members written after a
// clear, or a clear after members, leave one value on every backend.
func wholeValue(op *PatchOperation) (value any, cleared bool) {
	if op.Op == PatchOpRemove || op.Value == nil {
		return op.Schema.clearedValue(), true
	}
	return op.Value, false
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

// ParsedPatch is a validated patch: what Parser.Parse and ParseOps make of a
// JSON Patch document, and ParseMergePatch and ParseMergePatchMap of a merge
// patch.
type ParsedPatch struct {
	Operations []*PatchOperation
	// AffectedPaths are the paths the operations write, sorted and once each:
	// a move's source included, a test's path not.
	AffectedPaths []string

	// schema is the schema the parser validated the patch against; a patch
	// without one was not produced by a parser (ErrUnparsed).
	schema *PatchSchema
	// policy is the policy ApplyPatchPolicyConstraints held the patch to.
	policy *PatchPolicyConstraints
}

// ParsedFor reports whether a parser validated the patch against schema: a
// resource applies and compiles only the patches it parsed.
func (p *ParsedPatch) ParsedFor(schema *PatchSchema) bool {
	return p != nil && schema != nil && p.schema == schema
}

// AuditInfo returns a map of audit-relevant information.
func (p *ParsedPatch) AuditInfo() map[string]any {
	info := map[string]any{
		"operation_count": len(p.Operations),
		"affected_paths":  p.AffectedPaths,
	}
	if p.policy != nil {
		info["forbidden_path_count"] = len(p.policy.forbidden)
		info["value_constraint_count"] = len(p.policy.constraints)
		info["validator_count"] = len(p.policy.validators)
	}
	return info
}
