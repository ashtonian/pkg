package filter

import (
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// FacetKeys returns the terms buckets a value of f counts in, keyed as
// BuildFacetQuery keys them, for a backend that counts in Go: none for NULL
// or an absent member, one for a scalar, and each distinct element of a
// list once. v is f's value, or for a JSON path its column's document, read
// as PathValue reads one.
//
// A key is the value's canonical text (core.CanonicalText), the text a
// terms facet's statement keys the column by: a string as itself, a
// boolean as true or false, a number as its canonical decimal text — a
// path member's exactly as written, a float32 field's at float32
// precision — a date as YYYY-MM-DD, a timestamp as core.FormatTimestamp and
// a UUID lower-case and hyphenated, however it is spelled. A float's or a
// decimal's NaN or infinity has none.
func FacetKeys(v any, f *FieldSchema) ([]string, error) {
	if f.Path != nil {
		return pathFacetKeys(v, f)
	}
	if f.DataType != core.FieldTypeArray {
		key, ok, err := valueFacetKey(v, f.DataType, f.NumberKind)
		if err != nil || !ok {
			return nil, facetKeyError(f, err)
		}
		return []string{key}, nil
	}
	v, err := core.Deref(v)
	if err != nil {
		return nil, facetKeyError(f, err)
	}
	if v == nil {
		return nil, nil
	}
	items, ok := core.AnySlice(v)
	if !ok {
		return nil, fmt.Errorf("facet %s: %s is not a list", f.ExternalName, gowalker.TypeText(reflect.TypeOf(v)))
	}
	var keys distinctKeys
	for _, item := range items {
		key, ok, err := valueFacetKey(item, f.Elem, f.NumberKind)
		if err != nil {
			return nil, facetKeyError(f, err)
		}
		if ok {
			keys.add(key)
		}
	}
	return keys.list, nil
}

// pathFacetKeys is FacetKeys of a JSON path field over doc.
func pathFacetKeys(doc any, f *FieldSchema) ([]string, error) {
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, fmt.Errorf("facet %s: %w", f.ExternalName, err)
	}
	member, ok := memberAt(root, f.Path.Segments)
	if !ok {
		return nil, nil
	}
	if f.DataType != core.FieldTypeArray {
		key, ok, err := memberFacetKey(member, f.DataType)
		if err != nil || !ok {
			return nil, facetKeyError(f, err)
		}
		return []string{key}, nil
	}
	items, ok := member.([]any)
	if !ok {
		return nil, nil
	}
	var keys distinctKeys
	for _, item := range items {
		key, ok, err := memberFacetKey(item, f.Elem)
		if err != nil {
			return nil, facetKeyError(f, err)
		}
		if ok {
			keys.add(key)
		}
	}
	return keys.list, nil
}

// memberFacetKey is a decoded document member's key as a path of type t
// reads it (pathMember): its canonical text (core.CanonicalText), a number
// member exactly as written.
func memberFacetKey(v any, t core.FieldType) (string, bool, error) {
	m, present, err := pathMember(v, t)
	if err != nil || !present {
		return "", false, err
	}
	text, err := core.CanonicalText(memberType(t), m)
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// valueFacetKey is a column value's key as a field of type t holds it: its
// canonical text (core.CanonicalText), the text a terms facet's statement
// keys the column by; kind is its Go numeric kind (reflect.Float32 keys at
// float32 precision). The value is read as the column holds it
// (core.StoredScalar). NULL and a float's or a decimal's NaN or infinity
// (core.NonFinite) have none.
func valueFacetKey(v any, t core.FieldType, kind reflect.Kind) (string, bool, error) {
	if !facetKeyTypes[t] {
		return "", false, fmt.Errorf("%s values have no facet key every backend reads alike", t)
	}
	stored, present, err := core.StoredScalar(t, kind, v)
	if err != nil || !present || core.NonFinite(t, stored) {
		return "", false, err
	}
	key, err := core.CanonicalText(core.TypeInfo{Type: t, NumberKind: kind}, stored)
	if err != nil {
		return "", false, err
	}
	return key, true, nil
}

func facetKeyError(f *FieldSchema, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("facet %s: %w", f.ExternalName, err)
}

// distinctKeys collects keys in first-seen order, each once.
type distinctKeys struct {
	list []string
	seen map[string]bool
}

func (d *distinctKeys) add(key string) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if !d.seen[key] {
		d.seen[key] = true
		d.list = append(d.list, key)
	}
}
