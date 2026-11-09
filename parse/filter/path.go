package filter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
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
// filters, sorts and facets on the member; its SQLExpr is empty and its
// value expression comes from Dialect.PathExpr.
type JSONPath struct {
	// Column is the document column's trusted SQL expression.
	Column string
	// Segments are the member names from the column down.
	Segments []string
	Storage  JSONStorage

	// element makes the path read one array element: Column is the alias
	// of a row of the dialect's element table (elementTable) and Segments
	// is empty. The builder makes such paths for `$some` / `$none`
	// conditions and facets on scalar arrays; no schema declares one.
	element bool
}

var pathSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

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
	if p.Column == "" {
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
			return fmt.Errorf("path member %q is not an identifier ([A-Za-z0-9_-]+)", s)
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

// FieldExpr is the SQL expression of a field's value: its column (a decimal
// column as the dialect's exact decimal), or for a path the dialect's typed
// extraction.
func FieldExpr(d Dialect, f *FieldSchema) string {
	if f.Path != nil {
		return d.PathExpr(f.Path, f.DataType)
	}
	switch f.DataType {
	case core.FieldTypeDate:
		return d.ReadColumn(f.SQLExpr, core.SQLDate)
	case core.FieldTypeDecimal:
		return d.DecimalColumn(f.SQLExpr)
	}
	return f.SQLExpr
}

// timestampShape is a UTC RFC 3339 instant with up to nine fractional
// digits — what encoding/json writes for a UTC time.Time.
var timestampShape = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\.([0-9]{1,9}))?Z$`)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	timestampRegex = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`
	uuidRegex      = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
)

// maxDecimalMember is decimals.MaxMemberLength as SQL text.
var maxDecimalMember = strconv.Itoa(decimals.MaxMemberLength)

// PathValue is the reference evaluator of a path: the member doc holds at
// f.Path, typed as the filter compares it, or present false when the member
// is absent — missing, JSON null, another JSON kind, or behind a non-object.
// SQL dialects render the same rule; a memory backend evaluates paths with
// it. doc is the column's value: JSON text, json.RawMessage, a decoded
// document, or any Go value that encodes to one.
//
// Strings come back as string, numbers as int64 when the literal is an
// integer within int64 and float64 otherwise, booleans as bool, timestamps
// as their canonical text and UUIDs lower-cased. A decimal member is a JSON
// number, or a string of at most core.MaxDecimalDigits characters in plain
// decimal notation, and comes back as its canonical text
// (core.DecimalText).
//
// An array member (FieldTypeArray) comes back as []any of its elements of
// type f.Elem, each read as a member of that type is; elements of another
// kind are left out. It is absent when no element is left: a JSON array
// with no element of the type has no value, as a search engine's `exists`
// reads one.
func PathValue(doc any, f *FieldSchema) (any, bool, error) {
	if f.Path == nil {
		return nil, false, fmt.Errorf("field %s is not a JSON path", f.ExternalName)
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

// pathNumber validates a numeric path key's cursor value: an int path may
// hold a decimal member, so either kind resumes a page.
func pathNumber(v any) (any, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case uint64:
		if n > math.MaxInt64 {
			return nil, fmt.Errorf("%d is outside the portable integer range", n)
		}
		return int64(n), nil
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%v is not a finite number", n)
		}
		return n, nil
	}
	return nil, fmt.Errorf("expected a number, got %T", v)
}

func decodeDocument(doc any) (any, error) {
	var data []byte
	switch d := doc.(type) {
	case nil:
		return nil, nil
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

func pathMember(v any, t core.FieldType) (any, bool, error) {
	switch t {
	case core.FieldTypeString:
		s, ok := v.(string)
		return s, ok, nil
	case core.FieldTypeInt, core.FieldTypeFloat:
		n, ok := jsonNumber(v)
		if !ok {
			return nil, false, nil
		}
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return i, true, nil
		}
		f, err := strconv.ParseFloat(string(n), 64)
		if err != nil || math.IsInf(f, 0) {
			return nil, false, nil
		}
		return f, true, nil
	case core.FieldTypeDecimal:
		text, ok := decimals.Member(v)
		if !ok {
			return nil, false, nil
		}
		return text, true, nil
	case core.FieldTypeBool:
		b, ok := v.(bool)
		return b, ok, nil
	case core.FieldTypeTimestamp:
		s, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		return canonicalTimestamp(s)
	case core.FieldTypeUUID:
		s, ok := v.(string)
		if !ok {
			return nil, false, nil
		}
		s = asciiLower(s)
		if !uuidShape.MatchString(s) {
			return nil, false, nil
		}
		return s, true, nil
	}
	return nil, false, fmt.Errorf("path type %s is not supported", t)
}

// jsonNumber is a JSON number member as its literal: a json.Number as
// decoded with UseNumber, or a Go number as encoding/json writes it, so a
// document decoded either way reads alike.
func jsonNumber(v any) (json.Number, bool) {
	if n, ok := v.(json.Number); ok {
		return n, true
	}
	if !core.IsNumber(v) {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil { // NaN or an infinity
		return "", false
	}
	return json.Number(b), true
}

// canonicalTimestamp pads a UTC RFC 3339 member's fraction to nine digits —
// core.TimestampLayout — so instants compare as text; another shape is
// absent.
func canonicalTimestamp(s string) (any, bool, error) {
	m := timestampShape.FindStringSubmatch(s)
	if m == nil {
		return nil, false, nil
	}
	return m[1] + "." + (m[3] + "000000000")[:9] + "Z", true, nil
}

// asciiLower folds ASCII letters only, as SQLite's lower() does.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// pgPathLiteral is the text[] literal of a path's segments; segments are
// identifiers, so no element needs quoting.
func pgPathLiteral(segs []string) string {
	return "'{" + strings.Join(segs, ",") + "}'"
}

// sqlitePathLiteral is the JSON path of a path's segments, every label
// quoted so a hyphen is a name character.
func sqlitePathLiteral(segs []string) string {
	var b strings.Builder
	b.WriteString("'$")
	for _, s := range segs {
		b.WriteString(`."`)
		b.WriteString(s)
		b.WriteString(`"`)
	}
	b.WriteString("'")
	return b.String()
}

// orderedAsText reports whether ordering comparisons on f compare text and so
// need code-point collation: strings, and a path's timestamp and UUID members,
// which compare as canonical text.
func orderedAsText(f *FieldSchema) bool {
	if f.DataType == core.FieldTypeString {
		return true
	}
	return f.Path != nil && (f.DataType == core.FieldTypeTimestamp || f.DataType == core.FieldTypeUUID)
}

// PathExpr renders p's typed value for PostgreSQL. The document is read as
// jsonb; jsonb_typeof selects the member's JSON kind before any cast, so a
// wrong-typed member is NULL rather than a cast error. An element path
// reads its row's element, jsonb_array_elements' v.
func (PostgresDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	if p.element {
		v := p.Column + ".v"
		return pgTypedMember(t, "jsonb_typeof("+v+")", "("+v+" #>> '{}')")
	}
	doc := pgPathDocument(p)
	path := pgPathLiteral(p.Segments)
	return pgTypedMember(t, "jsonb_typeof("+doc+" #> "+path+")", "("+doc+" #>> "+path+")")
}

// pgPathDocument is p's document as jsonb.
func pgPathDocument(p *JSONPath) string {
	if p.Storage != StorageJSONB {
		return "(" + p.Column + ")::jsonb"
	}
	return p.Column
}

// pgTypedMember is a JSON value of type t, NULL when it has another kind:
// kind is its jsonb_typeof, text its text (#>>).
func pgTypedMember(t core.FieldType, kind, text string) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'string' THEN " + text + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric END)"
	case core.FieldTypeDecimal:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric WHEN " + kind + " = 'string' AND length" + text + " <= " + maxDecimalMember +
			" AND " + text + " ~ '" + decimals.PlainRegex + "' THEN " + text + "::numeric END)"
	case core.FieldTypeBool:
		return "(CASE WHEN " + kind + " = 'boolean' THEN " + text + "::boolean END)"
	case core.FieldTypeTimestamp:
		// Pad the fraction to nine digits: the canonical text request values carry.
		return "(CASE WHEN " + kind + " = 'string' AND " + text + " ~ '" + timestampRegex + "' THEN substr(" + text + ", 1, 19) || '.' || rpad(coalesce(substring(" + text + " from '[.]([0-9]+)Z$'), ''), 9, '0') || 'Z' END)"
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'string' AND lower" + text + " ~ '" + uuidRegex + "' THEN lower" + text + " END)"
	}
	return "NULL"
}

// sqliteTimestampGlob and sqliteUUIDGlob are the canonical shapes as GLOB
// patterns; SQLite has no built-in regular expressions.
var (
	sqliteTimestampGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z"
	sqliteUUIDGlob      = hexGlob(8) + "-" + hexGlob(4) + "-" + hexGlob(4) + "-" + hexGlob(4) + "-" + hexGlob(12)
)

// sqliteTimestamp is the canonical text of a UTC RFC 3339 member val, or
// NULL: the 19-character date-time, then "Z" alone (length 20) or a "."
// and one to nine digits, the fraction padded to nine.
func sqliteTimestamp(kind, val string) string {
	frac := "substr(" + val + ", 21, length(" + val + ") - 21)"
	return "(CASE WHEN " + kind + " = 'text' AND " + val + " GLOB '" + sqliteTimestampGlob + "' AND (length(" + val + ") = 20 OR (substr(" + val + ", 20, 1) = '.' AND length(" + val + ") BETWEEN 22 AND 30 AND " + frac + " NOT GLOB '*[^0-9]*'))" +
		" THEN substr(" + val + ", 1, 19) || '.' || substr((CASE WHEN length(" + val + ") = 20 THEN '' ELSE " + frac + " END) || '000000000', 1, 9) || 'Z' END)"
}

func hexGlob(n int) string { return strings.Repeat("[0-9a-f]", n) }

// PathExpr renders p's typed value for SQLite, whose documents are JSON
// text: json_type selects the member's kind; json_extract reads it. A
// decimal member is read as the JSON literal `->` returns, which keeps the
// number as written where json_extract would round it to a double, through
// parse_decimal (parsesqlite). An element path reads its json_each row: its
// type and value are json_type's and json_extract's of the element, and
// its literal is the source document's (the hidden json column) at its
// fullkey.
func (SQLiteDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	kind, val, literal := sqlitePathMember(p)
	return sqliteTypedMember(t, kind, val, literal)
}

// sqlitePathMember is p's member as SQLite reads it: its json_type, its
// json_extract value and its JSON text as written (`->`). An element path's
// are its json_each row's type and value, and the source document's text at
// the row's fullkey.
func sqlitePathMember(p *JSONPath) (kind, val, literal string) {
	if p.element {
		row := p.Column
		return row + ".type", row + ".value", row + ".json -> " + row + ".fullkey"
	}
	path := sqlitePathLiteral(p.Segments)
	return "json_type(" + p.Column + ", " + path + ")", "json_extract(" + p.Column + ", " + path + ")", p.Column + " -> " + path
}

// sqliteTypedMember is a JSON value of type t, NULL when it has another
// kind: kind is its json_type, val its json_extract and literal its JSON
// text as written (`->`). A date is an element of SQLite's JSON text array
// column, read as ReadColumn reads a date column.
func sqliteTypedMember(t core.FieldType, kind, val, literal string) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + val + " END)"
	case core.FieldTypeDecimal:
		return decimals.SQLiteValueFunc + "(" + literal + ")"
	case core.FieldTypeDate:
		return "(CASE WHEN " + kind + " = 'text' THEN substr(" + val + ", 1, 10) END)"
	case core.FieldTypeBool:
		return "(CASE " + kind + " WHEN 'true' THEN 1 WHEN 'false' THEN 0 END)"
	case core.FieldTypeTimestamp:
		return sqliteTimestamp(kind, val)
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'text' AND lower(" + val + ") GLOB '" + sqliteUUIDGlob + "' THEN lower(" + val + ") END)"
	}
	return "NULL"
}
