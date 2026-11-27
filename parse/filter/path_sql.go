package filter

import (
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// FieldExpr is the SQL expression of a field's value: its column (a decimal
// column as the dialect's exact decimal, an interval as its canonical text,
// which compares field by field where PostgreSQL's interval equality equates
// a month with 30 days), or for a path the dialect's typed extraction.
func FieldExpr(d Dialect, f *FieldSchema) string {
	if f.Path != nil {
		return d.PathExpr(f.Path, f.DataType)
	}
	column := f.sql(d)
	switch {
	case f.DataType == core.FieldTypeDate:
		return d.ReadColumn(column, core.SQLDate)
	case f.DataType == core.FieldTypeDecimal:
		return d.DecimalValue(DecimalColumn(column))
	case f.DataType == core.FieldTypeInterval:
		return d.ReadColumn(column, core.SQLInterval)
	case f.DataType == core.FieldTypeArray && f.Elem == core.FieldTypeInterval:
		return d.ReadColumn(column, core.SQLIntervalArray)
	}
	return column
}

const (
	timestampRegex = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`
	uuidRegex      = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
)

// maxDecimalMember is decimals.MaxMemberLength as SQL text, and
// maxNumberDigits core.MaxDecimalDigits.
var (
	maxDecimalMember = strconv.Itoa(decimals.MaxMemberLength)
	maxNumberDigits  = strconv.Itoa(core.MaxDecimalDigits)
)

// pgMember is the jsonb member of doc at segs, read one object member at a
// time with ->, and pgMemberText its text, the last member read with ->>.
// Both operators take a text key, which reads an object's member and is
// NULL on an array or a scalar: no segment is read as an array subscript or
// a NULL element, as a text[] path given to #> reads `0` or `null`, so
// PostgreSQL reads the member the reference rule (memberAt) reads. Segments
// are identifiers (pathSegment), so no literal needs escaping.
func pgMember(doc string, segs []string) string {
	return doc + pgKeys(segs, " -> ")
}

func pgMemberText(doc string, segs []string) string {
	last := len(segs) - 1
	return doc + pgKeys(segs[:last], " -> ") + pgKeys(segs[last:], " ->> ")
}

func pgKeys(segs []string, op string) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(op)
		b.WriteString("'")
		b.WriteString(s)
		b.WriteString("'")
	}
	return b.String()
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

// pgPathDocument is p's document as jsonb.
func pgPathDocument(d dialect.Dialect, p *JSONPath) string {
	if p.Storage != StorageJSONB {
		return "(" + p.document(d) + ")::jsonb"
	}
	return p.document(d)
}

// pgTypedMember is a JSON value of type t, NULL when it has another kind:
// kind is its jsonb_typeof, text its text (->> or #>> '{}').
func pgTypedMember(t core.FieldType, kind, text string) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'string' THEN " + text + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + pgNumberMember(kind, text) + " THEN " + text + "::numeric END)"
	case core.FieldTypeDecimal:
		return "(CASE WHEN " + pgNumberMember(kind, text) + " THEN " + text + "::numeric WHEN " + kind + " = 'string' AND length" + text + " <= " + maxDecimalMember +
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

// pgNumberMember holds for a number member (decimals.Number): kind is
// 'number' and text, the member's numeric output (plain notation, the scale
// it was written with kept), has at most core.MaxDecimalDigits digits once
// its fraction's trailing zeros are dropped.
func pgNumberMember(kind, text string) string {
	digits := "length(split_part(ltrim(" + text + ", '-'), '.', 1)) + length(rtrim(split_part(" + text + ", '.', 2), '0'))"
	return "(" + kind + " = 'number' AND " + digits + " <= " + maxNumberDigits + ")"
}

// sqliteNumber holds for a JSON number of kind; for a document member
// (member) only for a number member (decimals.Number): literal, its JSON
// text as written, has at most core.MaxDecimalDigits digits in plain
// notation. A literal of at most that many characters and no exponent has,
// so only another reaches parse_decimal, which reads it as decimals.Number
// does.
func sqliteNumber(kind, literal string, member bool) string {
	number := kind + " IN ('integer', 'real')"
	if !member {
		return number
	}
	literal = "(" + literal + ")"
	return "(" + number + " AND ((length(" + literal + ") <= " + maxNumberDigits + " AND " + literal + " NOT GLOB '*[eE]*') OR " +
		decimals.SQLiteValueFunc + "(" + literal + ") IS NOT NULL))"
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

// sqlitePathMember is p's member as SQLite reads it: its json_type, its
// json_extract value and its JSON text as written (`->`). An element path's
// are its json_each row's type and value, and the source document's text at
// the row's fullkey.
func sqlitePathMember(d dialect.Dialect, p *JSONPath) (kind, val, literal string) {
	doc := p.document(d)
	if p.element {
		return doc + ".type", doc + ".value", doc + ".json -> " + doc + ".fullkey"
	}
	path := sqlitePathLiteral(p.Segments)
	return "json_type(" + doc + ", " + path + ")", "json_extract(" + doc + ", " + path + ")", doc + " -> " + path
}

// sqliteTypedMember is a JSON value of type t, NULL when it has another
// kind: kind is its json_type, val its json_extract and literal its JSON
// text as written (`->`); member is false for an array column's element
// (JSONPath.columnElement). A date is an element of SQLite's JSON text
// array column, read as ReadColumn reads a date column; a time, interval or
// network element is the canonical text the column was written with.
func sqliteTypedMember(t core.FieldType, kind, val, literal string, member bool) string {
	switch t {
	case core.FieldTypeString:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeInt, core.FieldTypeFloat:
		return "(CASE WHEN " + sqliteNumber(kind, literal, member) + " THEN " + val + " END)"
	case core.FieldTypeDecimal:
		return decimals.SQLiteValueFunc + "(" + literal + ")"
	case core.FieldTypeDate:
		return "(CASE WHEN " + kind + " = 'text' THEN substr(" + val + ", 1, 10) END)"
	case core.FieldTypeTime, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		return "(CASE WHEN " + kind + " = 'text' THEN " + val + " END)"
	case core.FieldTypeBool:
		return "(CASE " + kind + " WHEN 'true' THEN 1 WHEN 'false' THEN 0 END)"
	case core.FieldTypeTimestamp:
		return sqliteTimestamp(kind, val)
	case core.FieldTypeUUID:
		return "(CASE WHEN " + kind + " = 'text' AND lower(" + val + ") GLOB '" + sqliteUUIDGlob + "' THEN lower(" + val + ") END)"
	}
	return "NULL"
}
