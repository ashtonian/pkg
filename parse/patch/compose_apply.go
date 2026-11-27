package patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/canonical"
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
// number by its value and scale) and a native array element by element,
// each by its type's value, in the forms SQLite's change predicate
// compares (canonical.Text); a float with its zero's sign; and any other
// value as its column's type orders it (core.CompareCanonical): a
// timestamp by its instant, a decimal by its value. A scalar that holds no
// value of its column's type compares as stored, as SQLite compares it.
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
	doc, err = canonical.Decode(v)
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
	kind, byForm := canonical.KindOf(t)
	if byForm && (kind == canonical.Document || kind.IsArray()) {
		x, err := canonical.Text(kind, a)
		if err != nil {
			return false, err
		}
		y, err := canonical.Text(kind, b)
		if err != nil {
			return false, err
		}
		return x == y, nil
	}
	switch t.Type {
	case core.FieldTypeGeo, core.FieldTypeAny:
		// No order: equal as stored.
		return storedBytesEqual(a, b), nil
	case core.FieldTypeFloat:
		x, xok := a.(float64)
		y, yok := b.(float64)
		if xok && yok && x == 0 && y == 0 {
			return math.Signbit(x) == math.Signbit(y), nil
		}
	}
	order, err := core.CompareCanonical(t, a, b)
	if err != nil {
		// A stored value that holds no value of the column's type, which
		// only SQLite stores (text that is no UUID, a legacy midnight
		// timestamp in a date column), compares as stored, as SQLite's
		// change predicate compares it; an undeclared type orders scalars
		// of one kind, and values of two kinds differ.
		return storedBytesEqual(a, b), nil
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
