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
)

// JSONStorage is how a document column holds its JSON. Documents are jsonb
// on PostgreSQL unless declared otherwise; SQLite always holds JSON text.
type JSONStorage string

const (
	StorageJSONB JSONStorage = "jsonb"
	StorageJSON  JSONStorage = "json"
	StorageText  JSONStorage = "text"
)

// JSONPath locates a typed scalar member inside a document column. A
// FieldSchema with a Path filters, sorts and facets on the member; its
// SQLExpr is empty and its value expression comes from Dialect.PathExpr.
type JSONPath struct {
	// Column is the document column's trusted SQL expression.
	Column string
	// Segments are the member names from the column down.
	Segments []string
	Storage  JSONStorage
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

func (p *JSONPath) validate(t core.FieldType, maxDepth int) error {
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
	if !pathTypes[t] {
		return fmt.Errorf("path %s has type %s; paths hold %s members", strings.Join(p.Segments, "."), t, pathTypeNames)
	}
	return nil
}

// FieldExpr is the SQL expression of a field's value: its column, or for a
// path the dialect's typed extraction.
func FieldExpr(d Dialect, f *FieldSchema) string {
	if f.Path == nil && f.DataType == core.FieldTypeDate {
		return d.ReadColumn(f.SQLExpr, core.SQLDate)
	}
	if f.Path == nil {
		return f.SQLExpr
	}
	return d.PathExpr(f.Path, f.DataType)
}

// timestampShape is a UTC RFC 3339 instant with up to nine fractional
// digits — what encoding/json writes for a UTC time.Time.
var timestampShape = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\.([0-9]{1,9}))?Z$`)

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	timestampRegex = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`
	uuidRegex      = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	// decimalRegex is a decimal in plain notation, the text a decimal
	// member's string holds (shopspring's JSON form): no exponent, so no
	// cast of a member can overflow.
	decimalRegex = `^-?(0|[1-9][0-9]*)([.][0-9]+)?$`
)

var decimalShape = regexp.MustCompile(decimalRegex)

// maxDecimalMember bounds a decimal member string's length; within it, its
// digits are within core.MaxDecimalDigits.
var maxDecimalMember = strconv.Itoa(core.MaxDecimalDigits)

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
func PathValue(doc any, f *FieldSchema) (any, bool, error) {
	if f.Path == nil {
		return nil, false, fmt.Errorf("field %s is not a JSON path", f.ExternalName)
	}
	v, present, err := pathValueOf(doc, f.Path, f.DataType)
	if err != nil {
		return nil, false, fmt.Errorf("field %s: %w", f.ExternalName, err)
	}
	return v, present, nil
}

func pathValueOf(doc any, p *JSONPath, t core.FieldType) (any, bool, error) {
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, false, err
	}
	v := root
	for _, seg := range p.Segments {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		if v, ok = obj[seg]; !ok {
			return nil, false, nil
		}
	}
	return pathMember(v, t)
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
		n, ok := v.(json.Number)
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
		switch m := v.(type) {
		case json.Number:
		case string:
			if len(m) > core.MaxDecimalDigits || !decimalShape.MatchString(m) {
				return nil, false, nil
			}
		default:
			return nil, false, nil
		}
		text, err := core.DecimalText(v)
		if err != nil {
			return nil, false, nil // more digits than a decimal holds
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
// wrong-typed member is NULL rather than a cast error.
func (PostgresDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	doc := p.Column
	if p.Storage != StorageJSONB {
		doc = "(" + p.Column + ")::jsonb"
	}
	path := pgPathLiteral(p.Segments)
	kind := "jsonb_typeof(" + doc + " #> " + path + ")"
	text := "(" + doc + " #>> " + path + ")"
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'string' THEN " + text + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric END)"
	case core.FieldTypeDecimal:
		return "(CASE WHEN " + kind + " = 'number' THEN " + text + "::numeric WHEN " + kind + " = 'string' AND length" + text + " <= " + maxDecimalMember +
			" AND " + text + " ~ '" + decimalRegex + "' THEN " + text + "::numeric END)"
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

// sqliteDecimalText tests that the text val is decimalRegex's plain decimal
// within maxDecimalMember characters, in GLOB terms: after an optional
// minus, digits with at most one point, neither leading nor trailing, and
// no zero leading another digit.
func sqliteDecimalText(val string) string {
	body := "substr(" + val + ", 1 + (substr(" + val + ", 1, 1) = '-'))"
	return "(length(" + val + ") <= " + maxDecimalMember + " AND " + body + " <> '' AND " +
		body + " NOT GLOB '*[^0-9.]*' AND " + body + " NOT GLOB '*.*.*' AND " +
		body + " NOT GLOB '.*' AND " + body + " NOT GLOB '*.' AND " + body + " NOT GLOB '0[0-9]*')"
}

// PathExpr renders p's typed value for SQLite, whose documents are JSON
// text: json_type selects the member's kind; json_extract reads it.
func (SQLiteDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	path := sqlitePathLiteral(p.Segments)
	kind := "json_type(" + p.Column + ", " + path + ")"
	val := "json_extract(" + p.Column + ", " + path + ")"
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + val + " END)"
	case core.FieldTypeDecimal:
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + val + " WHEN " + kind + " = 'text' AND " + sqliteDecimalText(val) +
			" THEN CAST(" + val + " AS NUMERIC) END)"
	case core.FieldTypeBool:
		return "(CASE " + kind + " WHEN 'true' THEN 1 WHEN 'false' THEN 0 END)"
	case core.FieldTypeTimestamp:
		return sqliteTimestamp(kind, val)
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'text' AND lower(" + val + ") GLOB '" + sqliteUUIDGlob + "' THEN lower(" + val + ") END)"
	}
	return "NULL"
}
