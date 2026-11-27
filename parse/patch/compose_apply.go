package patch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
)

// StoredRow reads one row's stored values: column's value in the form a
// row stores it (core.StorageValue), nil for NULL, and false for a column
// the row does not have.
type StoredRow func(column string) (value any, ok bool)

// RowWrite is what a composed update writes to one row.
type RowWrite struct {
	// Values are the stored values of the columns the update assigns
	// (ComposedPatch.Columns), in that order: each in the form a row
	// stores it, nil for NULL.
	Values []any
	// Changed reports whether a patch column reads back other than the row
	// held it: the rows WithChangePredicate's condition holds for. The
	// stamp and the increments are left out, so an update that skips
	// unchanged rows writes none of Values to a row it does not change.
	Changed bool
}

// Apply is the update applied to row, the stored values of one target: the
// values it writes, as the SQL BuildPatchSQL renders writes them, and
// whether the patch changes the row. An update that assigns no column
// writes nothing.
//
// A row that does not hold what a member operation needs (Preconditions)
// is one the patch does not apply to: Apply returns the first such
// operation's Precondition.Err, as a SQL update reports it, and writes
// nothing. A stamp of a SQL expression (WithAutoTimestampSQL) is an error,
// since only a database evaluates it, and so is a stored value that is not
// one of its column's type: a document or array that is not JSON text, or
// a counter that is not an integer.
//
// Each column is written as the database writes it:
//   - a value as core.StorageValue stores it for the type its path
//     declares, a clear as NULL or its path's empty value;
//   - a merge as RFC 7396 merges into the stored document, a NULL or
//     non-object one as {};
//   - appends onto the stored array, a NULL one as [];
//   - member writes over the stored document, in patch order;
//   - the stamp as its canonical timestamp text (core.FormatTimestamp), and
//     each counter one more than it holds, a NULL one left NULL.
//
// Changed compares each patch column's stored and written values null-safe:
// a document as PostgreSQL's jsonb text compares it (member order aside, a
// number by its value and scale), a native array element by element as
// written, a float with its zero's sign, and any other value as its
// column's type orders it (core.CompareCanonical): a timestamp by its
// instant, a decimal by its value.
func (c *ComposedPatch) Apply(row StoredRow) (RowWrite, error) {
	if !c.Assigns() {
		return RowWrite{}, nil
	}
	r := &storedRow{row: row, documents: map[string]any{}}
	for _, need := range c.preconditions {
		doc, err := r.document(need.Column)
		if err != nil {
			return RowWrite{}, err
		}
		if !need.holds(doc) {
			return RowWrite{}, need.Err()
		}
	}
	write := RowWrite{Values: make([]any, 0, len(c.columns)+1+len(c.increments))}
	for _, ch := range c.columns {
		before, err := r.value(ch.column)
		if err != nil {
			return RowWrite{}, err
		}
		after, err := ch.apply(r)
		if err != nil {
			return RowWrite{}, err
		}
		same, err := storedEqual(ch.compareType(), before, after)
		if err != nil {
			return RowWrite{}, fmt.Errorf("patch: comparing column %s: %w", ch.column, err)
		}
		write.Changed = write.Changed || !same
		write.Values = append(write.Values, after)
	}
	if c.stamp != "" {
		if c.stampSQL != "" {
			return RowWrite{}, fmt.Errorf("patch: column %s is stamped by the SQL expression %s, which only a database evaluates", c.stamp, c.stampSQL)
		}
		write.Values = append(write.Values, core.FormatTimestamp(c.stampAt))
	}
	for _, col := range c.increments {
		counted, err := r.increment(col)
		if err != nil {
			return RowWrite{}, err
		}
		write.Values = append(write.Values, counted)
	}
	return write, nil
}

// holds reports whether doc, a stored document as encoding/json decodes
// it, nil for NULL, holds what p needs, reading members through objects
// only, as the SQL dialects read them.
func (p Precondition) holds(doc any) bool {
	if p.Exists {
		_, found := documentMember(doc, p.Member)
		return found
	}
	parent, found := documentMember(doc, p.Member[:len(p.Member)-1])
	if !found {
		return false
	}
	_, object := parent.(map[string]any)
	return object
}

// storedRow reads a row's stored values, each document decoded once.
type storedRow struct {
	row       StoredRow
	documents map[string]any
}

func (r *storedRow) value(column string) (any, error) {
	v, ok := r.row(column)
	if !ok {
		return nil, fmt.Errorf("patch: the row holds no column %s", column)
	}
	return v, nil
}

// document is the stored document column holds, decoded; nil for NULL.
// Member writes edit it in place, after every precondition has read it.
func (r *storedRow) document(column string) (any, error) {
	doc, ok := r.documents[column]
	if ok {
		return doc, nil
	}
	v, err := r.value(column)
	if err != nil {
		return nil, err
	}
	doc, err = storedDocument(v)
	if err != nil {
		return nil, fmt.Errorf("patch: column %s: %w", column, err)
	}
	r.documents[column] = doc
	return doc, nil
}

// increment is one more than the counter column holds, NULL for NULL, as
// SQL adds.
func (r *storedRow) increment(column string) (any, error) {
	v, err := r.value(column)
	if err != nil || v == nil {
		return nil, err
	}
	n, ok := v.(int64)
	if !ok {
		return nil, fmt.Errorf("patch: counter column %s holds %s, not an integer", column, gowalker.TypeText(reflect.TypeOf(v)))
	}
	if n == math.MaxInt64 {
		return nil, fmt.Errorf("patch: counter column %s holds %d, the largest integer it can", column, n)
	}
	return n + 1, nil
}

// apply is the value ch writes over the row's stored one.
func (ch *columnChange) apply(r *storedRow) (any, error) {
	switch ch.kind {
	case changeMerge:
		doc, err := r.document(ch.column)
		if err != nil {
			return nil, err
		}
		patch, err := ch.mergeDocument()
		if err != nil {
			return nil, fmt.Errorf("%s for %s: %w", ch.ops[0].Op, ch.ops[0].Path, err)
		}
		return documentText(jsonmerge.Apply(doc, patch))
	case changeAppend:
		return ch.appendTo(r)
	case changeMembers:
		doc, err := r.document(ch.column)
		if err != nil {
			return nil, err
		}
		for _, e := range ch.edits {
			var value any
			if e.op.Op != PatchOpRemove {
				// Decoded for each row: a later edit writes into the value
				// an earlier one set.
				err := decodeJSONDocument(e.value, &value)
				if err != nil {
					return nil, fmt.Errorf("%s for %s: %w", e.op.Op, e.op.Path, err)
				}
			}
			var applied bool
			doc, applied = applyMember(doc, e.op.Op, e.member, value)
			if !applied {
				return nil, e.need.Err()
			}
		}
		return documentText(doc)
	}
	if ch.stored == nil {
		return nil, nil
	}
	v, err := ch.stored()
	if err != nil {
		return nil, fmt.Errorf("marshaling value for %s: %w", ch.base.Path, err)
	}
	return v, nil
}

// appendTo is the stored array with ch's elements appended, a NULL array
// as an empty one.
func (ch *columnChange) appendTo(r *storedRow) (any, error) {
	doc, err := r.document(ch.column)
	if err != nil {
		return nil, err
	}
	var elems []any
	if doc != nil {
		arr, ok := doc.([]any)
		if !ok {
			return nil, fmt.Errorf("patch: column %s holds %s, not an array, to append to", ch.column, jsonKind(doc))
		}
		elems = arr
	}
	for _, op := range ch.appends {
		elems = append(elems, op.Value)
	}
	v, err := core.StorageValue(pathType(ch.ops[0].Schema), elems)
	if err != nil {
		return nil, fmt.Errorf("marshaling value for %s: %w", ch.ops[0].Path, err)
	}
	return v, nil
}

// compareType is the type ch's column is compared as: a column written
// whole as its path declares, a merged or member-written one as a
// document, an appended one as an array.
func (ch *columnChange) compareType() core.TypeInfo {
	switch ch.kind {
	case changeMerge, changeMembers:
		document := core.TypeInfo{Type: core.FieldTypeJSON}
		return document
	case changeAppend:
		array := core.TypeInfo{Type: core.FieldTypeArray}
		return array
	}
	return pathType(ch.schema)
}

// storedEqual reports whether a and b, two stored values of a column of
// type t, are one value as Apply's Changed compares them.
func storedEqual(t core.TypeInfo, a, b any) (bool, error) {
	if a == nil || b == nil {
		return a == nil && b == nil, nil
	}
	switch {
	case core.IsDocument(t.Type) || t.SQLType.IsJSON():
		x, y, err := storedDocuments(a, b)
		if err != nil {
			return false, err
		}
		return jsonbEqual(x, y), nil
	case t.Type == core.FieldTypeArray:
		// A native array compares as its elements' text, in order: a number
		// as written, as PostgreSQL's float8 text keeps -0 apart from 0.
		x, y, err := storedDocuments(a, b)
		if err != nil {
			return false, err
		}
		return reflect.DeepEqual(x, y), nil
	case t.Type == core.FieldTypeGeo || t.Type == core.FieldTypeAny:
		// No order: equal as stored.
		return storedBytesEqual(a, b), nil
	case t.Type == core.FieldTypeFloat:
		x, xok := a.(float64)
		y, yok := b.(float64)
		if xok && yok && x == 0 && y == 0 {
			return math.Signbit(x) == math.Signbit(y), nil
		}
	}
	order, err := core.CompareCanonical(t, a, b)
	if err != nil {
		if t.Type == "" {
			// An undeclared type orders scalars of one kind; values of
			// two kinds differ.
			return storedBytesEqual(a, b), nil
		}
		return false, err
	}
	return order == 0, nil
}

// storedBytesEqual reports whether a and b are equal as stored, text and
// bytes alike.
func storedBytesEqual(a, b any) bool {
	x, xbytes := a.([]byte)
	y, ybytes := b.([]byte)
	switch {
	case xbytes && ybytes:
		return bytes.Equal(x, y)
	case xbytes:
		return reflect.DeepEqual(string(x), b)
	case ybytes:
		return reflect.DeepEqual(a, string(y))
	}
	return reflect.DeepEqual(a, b)
}

// storedDocuments decodes a and b, two stored documents or arrays.
func storedDocuments(a, b any) (x, y any, err error) {
	x, err = storedDocument(a)
	if err != nil {
		return nil, nil, err
	}
	y, err = storedDocument(b)
	if err != nil {
		return nil, nil, err
	}
	return x, y, nil
}

// jsonbEqual reports whether a and b, two documents storedDocument decoded,
// have one text as PostgreSQL's jsonb writes it, which the change predicate
// compares: objects by their members, whatever their order, arrays in
// order, and a number as numeric holds it, by its value and the scale its
// literal gives, so 1.0 is not 1 while 1e2 is 100 and -0 is 0.
func jsonbEqual(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !jsonbEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonbEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		return ok && numericEqual(x, y)
	}
	return a == b
}

// numericEqual reports whether two JSON number literals are one numeric:
// one value (core.DecimalText, which bounds the digits a short exponent
// spells) at one display scale.
func numericEqual(a, b json.Number) bool {
	if a == b {
		return true
	}
	x, err := core.DecimalText(a)
	if err != nil {
		return false
	}
	y, err := core.DecimalText(b)
	if err != nil || x != y {
		return false
	}
	xs, xok := numericScale(a)
	ys, yok := numericScale(b)
	return xok && yok && xs == ys
}

// numericScale is the display scale numeric gives literal: its fractional
// digits less its exponent, at least 0 ("1.50" 2, "1.5e1" 0, "1e-3" 3).
func numericScale(literal json.Number) (int, bool) {
	mantissa, exponent, scaled := strings.Cut(strings.ToLower(string(literal)), "e")
	_, fraction, _ := strings.Cut(mantissa, ".")
	scale := len(fraction)
	if scaled {
		e, err := strconv.Atoi(exponent)
		if err != nil {
			return 0, false
		}
		scale -= e
	}
	return max(scale, 0), true
}

// storedDocument decodes v, a stored document or array's JSON text, with
// numbers kept as written; nil is NULL.
func storedDocument(v any) (any, error) {
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

// documentText is doc, a document as encoding/json decodes one, as a row
// stores it: its JSON text.
func documentText(doc any) (any, error) {
	text, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return string(text), nil
}

// jsonKind names the JSON type of v, a decoded document.
func jsonKind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	}
	return "null"
}
