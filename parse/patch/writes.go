package patch

import (
	"maps"
	"slices"
	"strconv"

	"github.com/ashtonian/pkg/parse/core"
)

// writeKind is how an operation writes a path.
type writeKind uint8

const (
	// writeSet replaces the path whole with a value known at parse.
	writeSet writeKind = iota
	// writeCopy replaces the path whole with a value read from the document
	// when the patch applies (move and copy destinations).
	writeCopy
	// writeClear removes the path, or sets it to null, whole.
	writeClear
	// writeElements writes each value of a list as an element of the array at
	// the path (array_append, a relation's add and remove), leaving the
	// array's other elements as they are.
	writeElements
	// writeMerge merges an object into the path (RFC 7396): the members it
	// names are written, the others are untouched.
	writeMerge
)

// write is one path an operation writes.
type write struct {
	path  string
	value any
	kind  writeKind
	// schema is the path's schema when the write is at an operation's own
	// path or source; nil below them, inside a document.
	schema *PathSchema
}

// writes enumerates what op writes, as policy and the audit list read it: an
// operation writes more than its path (a whole value writes every member
// below it, a merge the members it names, a move its source) and nothing but
// what it carries (a remove writes null whatever value a client attached).
// A test writes nothing. A merge's members are expanded by members.
func (op *PatchOperation) writes() []write {
	switch {
	case op.Op == PatchOpTest:
		return nil
	case op.Merge:
		return []write{{path: op.Path, value: op.Value, kind: writeMerge, schema: op.Schema}}
	case op.Op == PatchOpMove:
		return []write{
			{path: op.From, kind: writeClear, schema: op.fromSchema},
			{path: op.Path, kind: writeCopy, schema: op.Schema},
		}
	case op.Op == PatchOpCopy:
		return []write{{path: op.Path, kind: writeCopy, schema: op.Schema}}
	case op.Op == PatchOpArrayAppend:
		return []write{{path: op.Path, value: []any{op.Value}, kind: writeElements, schema: op.Schema}}
	case op.Schema != nil && op.Schema.Relation != nil && op.Op != PatchOpReplace && op.Value != nil:
		// A membership add or remove names elements of the set.
		values, _ := core.AnySlice(op.Value)
		return []write{{path: op.Path, value: values, kind: writeElements, schema: op.Schema}}
	case op.Op == PatchOpRemove || op.Value == nil:
		return []write{{path: op.Path, kind: writeClear, schema: op.Schema}}
	}
	return []write{{path: op.Path, value: op.Value, kind: writeSet, schema: op.Schema}}
}

// reads are the paths op reads from the document: a move's or copy's
// source, and the path a test compares.
func (op *PatchOperation) reads() []string {
	switch op.Op {
	case PatchOpMove, PatchOpCopy:
		return []string{op.From}
	case PatchOpTest:
		return []string{op.Path}
	}
	return nil
}

// members expands a merge write into the writes of the members its object
// names: an object member merges in turn, null clears, any other value
// replaces whole. Members are in name order.
func (w write) members() []write {
	obj, _ := w.value.(map[string]any)
	out := make([]write, 0, len(obj))
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		member := write{path: w.path + "/" + core.EscapePointerToken(name), value: obj[name]}
		switch obj[name].(type) {
		case nil:
			member.kind = writeClear
		case map[string]any:
			member.kind = writeMerge
		default:
			member.kind = writeSet
		}
		out = append(out, member)
	}
	return out
}

// affectedPaths are the paths parsed's operations write, sorted and once
// each: a move's source included, a test's path not.
func affectedPaths(ops []*PatchOperation) []string {
	seen := make(map[string]struct{}, len(ops))
	for _, op := range ops {
		for _, w := range op.writes() {
			seen[w.path] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// absentPaths are the paths below base that pattern names by literal tokens
// alone and that value, written whole at base, does not hold: a whole write
// leaves them null. A member under a wildcard names no path while it is
// absent, and neither does an array index.
func absentPaths(pattern pathPattern, base []string, value any) []string {
	var out []string
	for _, rest := range pattern.remainders(base) {
		collectAbsent(rest, value, true, joinPointer(base), &out)
	}
	return out
}

func collectAbsent(rest []string, value any, present bool, path string, out *[]string) {
	if len(rest) == 0 {
		if !present {
			*out = append(*out, path)
		}
		return
	}
	var children []docMember
	if present {
		children = documentMembers(value)
	}
	switch rest[0] {
	case wildcardAny:
		collectAbsent(rest[1:], value, present, path, out)
		for _, c := range children {
			collectAbsent(rest, c.value, true, path+"/"+c.token, out)
		}
	case wildcardOne:
		for _, c := range children {
			collectAbsent(rest[1:], c.value, true, path+"/"+c.token, out)
		}
	default:
		for _, c := range children {
			if c.token == rest[0] {
				collectAbsent(rest[1:], c.value, true, path+"/"+c.token, out)
				return
			}
		}
		if _, array := asDocument(value).([]any); present && array {
			return
		}
		if !slices.ContainsFunc(rest, isWildcardToken) {
			*out = append(*out, path+joinPointer(rest))
		}
	}
}

// docMember is one member or element of a document value, by its escaped
// reference token.
type docMember struct {
	token string
	value any
}

// documentMembers are a document value's members, in name order, or its
// elements; none for a scalar.
func documentMembers(value any) []docMember {
	switch v := asDocument(value).(type) {
	case map[string]any:
		out := make([]docMember, 0, len(v))
		for _, name := range slices.Sorted(maps.Keys(v)) {
			out = append(out, docMember{core.EscapePointerToken(name), v[name]})
		}
		return out
	case []any:
		out := make([]docMember, len(v))
		for i, e := range v {
			out[i] = docMember{strconv.Itoa(i), e}
		}
		return out
	}
	return nil
}

// pathValue is a value a write leaves at a path below its own.
type pathValue struct {
	path  string
	value any
}

// presentPaths are the members and elements value holds at any depth, each
// at its path below base, in document order.
func presentPaths(base string, value any) []pathValue {
	var out []pathValue
	var walk func(path string, v any)
	walk = func(path string, v any) {
		for _, c := range documentMembers(v) {
			p := path + "/" + c.token
			out = append(out, pathValue{p, c.value})
			walk(p, c.value)
		}
	}
	walk(base, value)
	return out
}
