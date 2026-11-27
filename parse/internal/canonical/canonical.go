// Package canonical is the form a stored value takes when an update asks
// whether its write changes a column: the one rule SQLite's change
// predicate (filter.SQLiteDialect, through the parse_canonical function
// parsesql registers) and patch.ComposedPatch.Apply both follow, so each
// compares a column by its value, as PostgreSQL's typed columns do,
// whatever text another writer stored.
package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
)

// SQLiteFunc is the SQLite function filter.SQLiteDialect calls and
// parsesql registers: Value, of the stored value and its Kind's name.
const SQLiteFunc = "parse_canonical"

// Kind is how the stored values of a column compare, by the name SQLite's
// function receives: Document; a value type whose text can spell one value
// more than one way ("decimal", "uuid", "timestamp", "date", "time",
// "interval", "inet", "cidr", "macaddr"); or a native array, "array" and
// its element type ("array:uuid"), or "array" alone for elements of no
// scalar type.
type Kind string

// Document compares a JSON document as PostgreSQL's jsonb text compares:
// its members whatever their order and spacing, and a number by its value
// and the scale its literal gives, exactly, whatever its length.
const Document Kind = "document"

const arrayKind = "array"

// IsArray reports whether k compares a native array.
func (k Kind) IsArray() bool {
	return k == arrayKind || strings.HasPrefix(string(k), arrayKind+":")
}

// KindOf is how a column of type t compares, false for one compared as it
// is stored: text, an integer, a float or a boolean, whose stored value is
// its value, and a geo or untyped column, which has no other.
func KindOf(t core.TypeInfo) (Kind, bool) {
	switch {
	case core.IsDocument(t.Type) || t.SQLType.IsJSON():
		return Document, true
	case t.Type == core.FieldTypeArray:
		if core.IsKeyType(t.Elem) {
			return Kind(arrayKind + ":" + string(t.Elem)), true
		}
		return Kind(arrayKind), true
	case valueType(t.Type):
		return Kind(t.Type), true
	}
	return "", false
}

// ParseKind is the Kind named name, false for a name no Kind has.
func ParseKind(name string) (Kind, bool) {
	kind := Kind(name)
	if kind == Document || name == arrayKind || valueType(core.FieldType(name)) {
		return kind, true
	}
	elem, found := strings.CutPrefix(name, arrayKind+":")
	return kind, found && core.IsKeyType(core.FieldType(elem))
}

// valueType reports whether t is a type whose stored text can spell one
// value more than one way: "1.50" and "1.5", a UUID in either case, a
// timestamp in any offset.
func valueType(t core.FieldType) bool {
	switch t {
	case core.FieldTypeDecimal, core.FieldTypeUUID, core.FieldTypeTimestamp, core.FieldTypeDate, core.FieldTypeTime,
		core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		return true
	}
	return false
}

// Text is v, a non-NULL stored value of a column compared as kind — in the
// form a row stores it (core.StorageValue), or as SQLite hands it to a
// function — in the form two values of one value share:
//
//   - Document: the document v's JSON text holds (Decode), its members
//     sorted, unspaced, and each number in the one spelling of its value
//     and display scale (numberForm), the literal's fractional digits less
//     its exponent, which is what jsonb's text keeps of a number: 1e2, 100
//     and 10e1 as 1e2, 1.50e1 and 15.0 as 150e-1, -0 as 0, and 1.0, 10e-1,
//     apart from 1. The form never pads, so its cost is linear in v's.
//   - an array: the array v's JSON text holds, unspaced, each element in
//     its type's canonical value (core.NormalizeStored), as PostgreSQL's
//     typed array holds it: a UUID in lower case, a timestamp at its
//     instant, a float by its value, keeping the sign of zero; an element
//     that holds no value of its type as written.
//   - a decimal: its canonical decimal text (core.DecimalText), as
//     parse_decimal reads an INTEGER, a REAL or TEXT (decimals.Value).
//   - any other Kind: the canonical text of the value v holds
//     (core.NormalizeStored).
//
// A v that holds no value of kind is an error. Text is idempotent: the
// form of a form is itself.
func Text(kind Kind, v any) (string, error) {
	switch {
	case kind == Document:
		doc, err := Decode(v)
		if err != nil {
			return "", err
		}
		return encode(numerics(doc))
	case kind.IsArray():
		elems, err := Decode(v)
		if err != nil {
			return "", err
		}
		_, elem, _ := strings.Cut(string(kind), ":")
		return encode(elements(core.FieldType(elem), elems))
	case kind == Kind(core.FieldTypeDecimal):
		text, ok := decimals.Value(v)
		if !ok {
			return "", fmt.Errorf("%s holds no decimal", gowalker.TypeText(reflect.TypeOf(v)))
		}
		return text, nil
	}
	normalized, err := core.NormalizeStored(core.FieldType(kind), reflect.Invalid, v)
	if err != nil {
		return "", err
	}
	text, ok := normalized.(string)
	if !ok {
		return "", fmt.Errorf("%s has no canonical text", kind)
	}
	return text, nil
}

// Value is v, a SQLite value of a column compared as kind, in its Text
// form, which SQLite's change predicate compares; NULL is NULL.
//
// A document or an array that holds no JSON value — text or a BLOB that is
// not one JSON value, or not valid UTF-8 — is an error, as PostgreSQL's
// cast to jsonb refuses a text column's and Apply refuses one: no form
// decides whether a write changes it, and a merge or member write cannot
// read it.
//
// A scalar that holds no value of kind — a BLOB, as parse_decimal reads
// one, or text that does not parse, such as a legacy midnight timestamp in
// a date column — is v itself, compared as stored, as Apply compares it: it
// equals another value only where the two are one as stored, since every
// form is a value of kind and Text is idempotent, so the write that stores
// parse's form counts as a change.
func Value(kind Kind, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if kind == Document || kind.IsArray() {
		return Text(kind, v)
	}
	_, blob := v.([]byte)
	if blob {
		return v, nil
	}
	text, err := Text(kind, v)
	if err != nil {
		return v, nil
	}
	return text, nil
}

// Decode is the JSON value v, a stored document's or array's text,
// decoded with each number as written (json.Number); nil for NULL. A value
// that is not text, text that is not valid UTF-8, which encoding/json
// would read as U+FFFD, text that is not one JSON value, and a value
// followed by more text are errors.
func Decode(v any) (any, error) {
	var text []byte
	switch d := v.(type) {
	case nil:
		return nil, nil
	case string:
		text = []byte(d)
	case []byte:
		text = d
	case json.RawMessage:
		text = d
	default:
		return nil, fmt.Errorf("a stored document is JSON text, not %s", gowalker.TypeText(reflect.TypeOf(v)))
	}
	if !utf8.Valid(text) {
		return nil, errors.New("decoding the stored document: its text is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var doc any
	err := dec.Decode(&doc)
	if err != nil {
		return nil, fmt.Errorf("decoding the stored document: %w", err)
	}
	_, err = dec.Token()
	if !errors.Is(err, io.EOF) {
		return nil, errors.New("decoding the stored document: text follows its value")
	}
	return doc, nil
}

// encode is doc's JSON text, members sorted and unspaced, with no HTML
// escaping, which would only lengthen the form.
func encode(doc any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(doc)
	if err != nil {
		return "", fmt.Errorf("encoding the stored document: %w", err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// numerics is doc, a decoded document, with each number in its form
// (numberForm), rewritten in place.
func numerics(doc any) any {
	switch d := doc.(type) {
	case map[string]any:
		for k, v := range d {
			d[k] = numerics(v)
		}
	case []any:
		for i, v := range d {
			d[i] = numerics(v)
		}
	case json.Number:
		return numberForm(d)
	}
	return doc
}

// elements is doc, a decoded array, with each element in its canonical
// value as a value of elem (core.NormalizeStored), rewritten in place. An
// element that holds none, and the elements of an array of no scalar
// type, are kept as written.
func elements(elem core.FieldType, doc any) any {
	elems, ok := doc.([]any)
	if !ok || !core.IsKeyType(elem) {
		return doc
	}
	for i, e := range elems {
		if e == nil {
			continue
		}
		normalized, err := core.NormalizeStored(elem, reflect.Invalid, e)
		if err == nil {
			elems[i] = normalized
		}
	}
	return elems
}
