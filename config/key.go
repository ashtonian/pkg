package config

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
// patterns. Malformed and empty keys return an error wrapping ErrKey.
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
	for i := len(k) - 1; i >= 0; i-- {
		if k[i].Kind == SegmentField || k[i].Kind == SegmentKey {
			return k[i].Name
		}
	}
	return ""
}
