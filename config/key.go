package config

import "slices"

// SegmentKind identifies one step in a configuration key.
type SegmentKind uint8

const (
	SegmentField SegmentKind = SegmentKind(segField) // A struct field.
	SegmentKey   SegmentKind = SegmentKind(segKey)   // A quoted map key.
	SegmentIndex SegmentKind = SegmentKind(segIndex) // A list index.
	SegmentAny   SegmentKind = SegmentKind(segAny)   // Any map entry or list element: [*].
)

// Segment is a field, map key, list index or wildcard. Name is used by
// SegmentField and SegmentKey; Index is used by SegmentIndex.
type Segment struct {
	Kind  SegmentKind
	Name  string
	Index int
}

// Key is a configuration key in the grammar used by Diff, FieldFor and appliers.
type Key []Segment

// ParseKey parses fields, quoted map keys, decimal list indexes and [*]
// patterns. A key begins with a field, or with a quoted map key naming a
// source key at the root that no field's key can spell (["a.b"]), as a
// report of unknown keys names it. A map key is its entry's text, whatever
// the map's key type (ports["80"], levels["WARN"]); ParseKey reads no
// schema, so [CanonicalKey] says which entry it names. Malformed and empty
// keys return an error wrapping ErrKey.
func ParseKey(text string) (Key, error) {
	p, err := parseKeyPath(text)
	if err != nil {
		return nil, err
	}
	key := make(Key, len(p))
	for i, s := range p {
		key[i] = Segment{Kind: SegmentKind(s.kind), Name: s.name, Index: s.index}
	}
	return key, nil
}

// String returns the canonical spelling, quoting map keys as Go strings.
func (k Key) String() string {
	p := make(keyPath, len(k))
	for i, s := range k {
		p[i] = segment{kind: segKind(s.Kind), name: s.Name, index: s.Index}
	}
	return p.String()
}

// Fields returns only the struct fields, without map entries or list selectors.
// For example, items[0].port becomes items.port.
func (k Key) Fields() Key {
	var out Key
	for _, s := range k {
		if s.Kind == SegmentField {
			out = append(out, s)
		}
	}
	return out
}

// LastName returns the last field or map key, skipping indexes and wildcards.
// An empty key has no name and returns an empty string.
func (k Key) LastName() string {
	for _, seg := range slices.Backward(k) {
		if seg.Kind == SegmentField || seg.Kind == SegmentKey {
			return seg.Name
		}
	}
	return ""
}

// Within reports whether k is p or a key beneath it: p's segments begin k.
// A field and a map key of one name are one segment, as a key beneath an
// interface or remain field can be written either way — CanonicalKey writes
// it as a field — and a wildcard in p stands for any one of them or a list
// index. Within reads no schema, so a wildcard after a struct matches its
// fields too. Keys spelled by CanonicalKey compare exactly, so a store finds
// the rows a write of p replaces.
func (k Key) Within(p Key) bool {
	if len(p) > len(k) {
		return false
	}
	for i, s := range p {
		t := k[i]
		switch s.Kind {
		case SegmentAny:
			// Any segment: a field may be an interface's map key.
		case SegmentIndex:
			if t.Kind != SegmentIndex || t.Index != s.Index {
				return false
			}
		default:
			if t.Kind != SegmentField && t.Kind != SegmentKey || t.Name != s.Name {
				return false
			}
		}
	}
	return true
}
