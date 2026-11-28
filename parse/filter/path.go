package filter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// JSONStorage is how a document column holds its JSON. Documents are jsonb
// on PostgreSQL unless declared otherwise; SQLite always holds JSON text.
type JSONStorage string

const (
	StorageJSONB JSONStorage = "jsonb"
	StorageJSON  JSONStorage = "json"
	StorageText  JSONStorage = "text"
)

// JSONPath locates a typed member inside a document column: a scalar, or
// an array of scalars (FieldTypeArray with Elem). A FieldSchema with a Path
// filters, sorts and facets on the member; its value expression comes from
// Dialect.PathExpr.
type JSONPath struct {
	// Table, Column and Expr are the document column, read as a
	// FieldSchema reads its column: Column of Table, quoted by the
	// dialect, unless Expr is set.
	Table  string
	Column string
	Expr   string
	// Segments are the member names from the column down.
	Segments []string
	Storage  JSONStorage

	// element makes the path read one array element: alias is a row of the
	// dialect's element table (Dialect.elementRows) and Segments is empty.
	// The builder makes such paths for `$some` / `$none` conditions and
	// facets on scalar arrays; no schema declares one. columnElement marks
	// an array column's element, which SQLite holds as JSON text too: it
	// reads as the column's value (ColumnValue), not as a document member,
	// so a number element has no member bound (decimals.Number).
	element       bool
	columnElement bool
	alias         string
}

// document is the SQL that reads the path's document: its column, or the
// element row's alias.
func (p *JSONPath) document(d dialect.Dialect) string {
	if p.element {
		return p.alias
	}
	return columnSQL(d, p.Table, p.Column, p.Expr)
}

// pathMemberChars are the characters a path member is made of.
const pathMemberChars = `[A-Za-z0-9_-]`

var pathSegment = regexp.MustCompile(`^` + pathMemberChars + `{1,` + strconv.Itoa(MaxPathMemberLen) + `}$`)

// pathTypes are the member types a path may declare, and the scalars a
// `storage:"scalar:<type>"` struct may be held as.
var pathTypes = map[core.FieldType]bool{
	core.FieldTypeString: true, core.FieldTypeInt: true, core.FieldTypeFloat: true, core.FieldTypeDecimal: true,
	core.FieldTypeBool: true, core.FieldTypeTimestamp: true, core.FieldTypeUUID: true,
}

// pathTypeNames lists pathTypes for errors.
const pathTypeNames = "string, int, float, decimal, bool, timestamp or uuid"

// validate checks a declared path of type t, elem being an array member's
// element type.
func (p *JSONPath) validate(t, elem core.FieldType, maxDepth int) error {
	if p.Column == "" && p.Expr == "" {
		return fmt.Errorf("path has no document column")
	}
	if len(p.Segments) == 0 {
		return fmt.Errorf("path has no members")
	}
	if maxDepth > 0 && len(p.Segments) > maxDepth {
		return fmt.Errorf("path %s is %d members deep, above the maximum %d", strings.Join(p.Segments, "."), len(p.Segments), maxDepth)
	}
	for _, s := range p.Segments {
		if !pathSegment.MatchString(s) {
			return fmt.Errorf("path member %q is not an identifier: 1 to %d of %s", s, MaxPathMemberLen, pathMemberChars)
		}
	}
	switch p.Storage {
	case StorageJSONB, StorageJSON, StorageText:
	default:
		return fmt.Errorf("unknown JSON storage %q", p.Storage)
	}
	if t == core.FieldTypeArray {
		if !pathTypes[elem] {
			return fmt.Errorf("path %s is an array of %s; an array member holds %s elements", strings.Join(p.Segments, "."), elem, pathTypeNames)
		}
		return nil
	}
	if !pathTypes[t] {
		return fmt.Errorf("path %s has type %s; paths hold %s members, or arrays of them", strings.Join(p.Segments, "."), t, pathTypeNames)
	}
	return nil
}

// timestampShape is a UTC RFC 3339 instant with up to nine fractional
// digits — what encoding/json writes for a UTC time.Time.
var timestampShape = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\.([0-9]{1,9}))?Z$`)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// PathValue is the reference evaluator of a path: the member doc holds at
// f.Path, typed as the filter compares it, or present false when the member
// is absent — missing, JSON null, another JSON kind, or behind a non-object.
// SQL dialects render the same rule; a memory backend evaluates paths with
// it. doc is the column's value: a Document, read as it was decoded, or
// JSON text, json.RawMessage, a decoded document or any Go value that
// encodes to one, decoded on every call.
//
// Strings come back as string, booleans as bool, timestamps as their
// canonical text and UUIDs lower-cased. An int or float member is a number
// member: a JSON number of at most core.MaxDecimalDigits digits in plain
// notation, which comes back as a json.Number of its canonical decimal text
// (core.DecimalText), the exact value PostgreSQL compares the member as
// (::numeric), and compares exactly, as a decimal (Evaluate); a JSON number
// beyond the bound is absent on every backend. A decimal member is a number
// member, or a string of at most core.MaxDecimalDigits characters in plain
// decimal notation, and comes back as its canonical text.
//
// An array member (FieldTypeArray) comes back as []any of its elements of
// type f.Elem, each read as a member of that type is; elements of another
// kind are left out. It is absent when no element is left: a JSON array
// with no element of the type has no value, as a search engine's `exists`
// reads one.
//
// An element path (a field of FieldSchema.Elements) reads one element of a
// JSON array document, not a column's document, and is an error here:
// ElementRow reads it on the element DocumentElements decodes, where a
// string element is a JSON string rather than JSON text.
func PathValue(doc any, f *FieldSchema) (any, bool, error) {
	if f.Path == nil {
		return nil, false, fmt.Errorf("field %s is not a JSON path", f.ExternalName)
	}
	if isElementPath(f) {
		return nil, false, fmt.Errorf("field %s is an element path: read it on an element with ElementRow", f.ExternalName)
	}
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	v, present, err := decodedPathValue(root, f.Path.Segments, f.DataType, f.Elem)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	return v, present, nil
}

// decodedPathValue is the member of a document already decoded to generic
// JSON (decodeDocument's form, or encoding/json's without UseNumber) at
// segs, of type t (elem an array's element type).
func decodedPathValue(root any, segs []string, t, elem core.FieldType) (any, bool, error) {
	v, ok := memberAt(root, segs)
	if !ok {
		return nil, false, nil
	}
	if t == core.FieldTypeArray {
		return arrayMember(v, elem)
	}
	return pathMember(v, t)
}

// memberAt is the JSON value at segs below root, a decoded document; ok is
// false when a segment is missing or crosses a non-object.
func memberAt(root any, segs []string) (any, bool) {
	v := root
	for _, seg := range segs {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = obj[seg]; !ok {
			return nil, false
		}
	}
	return v, true
}

// arrayMember is a JSON array's elements of type elem, absent when it has
// none.
func arrayMember(v any, elem core.FieldType) (any, bool, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, false, nil
	}
	var out []any
	for _, item := range items {
		e, present, err := pathMember(item, elem)
		if err != nil {
			return nil, false, err
		}
		if present {
			out = append(out, e)
		}
	}
	return out, len(out) > 0, nil
}

func decodeDocument(doc any) (any, error) {
	var data []byte
	switch d := doc.(type) {
	case nil:
		return nil, nil
	case Document:
		return d.root, nil
	case *Document:
		if d == nil {
			return nil, nil
		}
		return d.root, nil
	case string:
		data = []byte(d)
	case []byte:
		data = d
	case json.RawMessage:
		data = d
	default:
		b, err := json.Marshal(d)
		if err != nil {
			return nil, fmt.Errorf("encode document: %w", err)
		}
		data = b
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}
	return out, nil
}

// pathMember is document member v read as a path of type t, in the domain
// Row describes, and false when it holds no value of t. A member already in
// its canonical form is returned as the value it is, since a filter reads it
// on every row and boxing it again would allocate there.
func pathMember(v any, t core.FieldType) (any, bool, error) {
	switch t {
	case core.FieldTypeString:
		_, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		return v, true, nil
	case core.FieldTypeInt, core.FieldTypeFloat:
		text, ok := decimals.Number(v)
		if !ok {
			return nil, false, nil
		}
		return heldAs[json.Number](v, text), true, nil
	case core.FieldTypeDecimal:
		text, ok := decimals.Member(v)
		if !ok {
			return nil, false, nil
		}
		return heldAs[string](v, text), true, nil
	case core.FieldTypeBool:
		b, ok := v.(bool)
		return b, ok, nil
	case core.FieldTypeTimestamp:
		s, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		return canonicalTimestamp(v, s)
	case core.FieldTypeUUID:
		s, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		lower := asciiLower(s)
		if !uuidShape.MatchString(lower) {
			return nil, false, nil
		}
		return heldAs[string](v, lower), true, nil
	}
	return nil, false, fmt.Errorf("path type %s is not supported", t)
}

// heldAs is v itself when it already holds text as a T, and text as a T
// otherwise.
func heldAs[T ~string](v any, text string) any {
	held, ok := v.(T)
	if ok && string(held) == text {
		return v
	}
	return T(text)
}

// memberType is the type a member read as a path of type t reads it
// (pathMember) compares and keys in: a number member as a decimal, exactly,
// as PostgreSQL compares it (::numeric); any other in t.
func memberType(t core.FieldType) core.TypeInfo {
	if t == core.FieldTypeInt || t == core.FieldTypeFloat {
		t = core.FieldTypeDecimal
	}
	info := core.TypeInfo{Type: t}
	return info
}

// canonicalTimestamp pads a UTC RFC 3339 member's fraction to nine digits —
// core.TimestampLayout — so instants compare as text; another shape is
// absent. v is the member, s its text: a member with nine fractional
// digits already is returned as it is.
func canonicalTimestamp(v any, s string) (any, bool, error) {
	const canonicalWidth = len("2006-01-02T15:04:05.000000000Z")
	if len(s) == canonicalWidth && timestampShape.MatchString(s) {
		return v, true, nil
	}
	m := timestampShape.FindStringSubmatch(s)
	if m == nil {
		return nil, false, nil
	}
	return m[1] + "." + (m[3] + "000000000")[:9] + "Z", true, nil
}

// asciiLower folds ASCII letters only, as SQLite's lower() does.
func asciiLower(s string) string {
	if !strings.ContainsFunc(s, isASCIIUpper) {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func isASCIIUpper(r rune) bool { return 'A' <= r && r <= 'Z' }
