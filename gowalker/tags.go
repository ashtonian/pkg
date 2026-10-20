package gowalker

import (
	"reflect"
	"strings"
)

// Tag is one struct-tag value in the "name,option,option" form used by
// encoding/json, db column tags, koanf and most tag-driven libraries.
// Options holds everything after the first comma, unparsed.
type Tag struct {
	Name    string
	Options string
}

// ParseTag splits value at its first comma. Whitespace is preserved.
func ParseTag(value string) Tag {
	name, options, _ := strings.Cut(value, ",")
	return Tag{Name: name, Options: options}
}

// LookupTag parses key's value from tag. ok is false when the key is absent,
// so db:"" and a missing db tag stay distinguishable.
func LookupTag(tag reflect.StructTag, key string) (Tag, bool) {
	value, ok := tag.Lookup(key)
	if !ok {
		return Tag{}, false
	}
	return ParseTag(value), true
}

// Has reports whether option is one of t's comma-separated options. The
// comparison is exact: "omitempty" does not match " omitempty".
func (t Tag) Has(option string) bool {
	for rest := t.Options; rest != ""; {
		var current string
		current, rest, _ = strings.Cut(rest, ",")
		if current == option {
			return true
		}
	}
	return false
}

// tagNaming is the field rule WithTaggedFields builds. It is part of the
// engine's settings, rather than a FieldResolver closure, because promotion
// must respect the engine's leaf rules: an embedded time.Time stays one field.
type tagNaming struct {
	key       string
	untagged  func(string) string
	valid     func(string) error
	promote   bool
	nested    bool
	dashComma bool
}

// TagOption adjusts the naming convention of [WithTaggedFields].
type TagOption func(*tagNaming) error

// UntaggedName names exported fields that carry no explicit tag name — an
// absent tag or one such as json:",omitempty" — name(goName), instead of
// excluding them. Use strings.ToLower for koanf-style keys, or return the Go
// name unchanged for encoding/json-style names. An empty result excludes the field.
func UntaggedName(name func(goName string) string) TagOption {
	return func(n *tagNaming) error {
		if name == nil {
			return ErrSchema
		}
		n.untagged = name
		return nil
	}
}

// PromoteEmbedded exposes the fields of an embedded struct, or pointer to one,
// that has no explicit tag name, as encoding/json does. A promoted name that
// collides with another fails compilation.
// An embedded field with an explicit tag name, or of a leaf type, stays one field.
func PromoteEmbedded() TagOption {
	return func(n *tagNaming) error { n.promote = true; return nil }
}

// DashCommaName excludes a field only when its tag value is exactly "-", and
// reads "-," followed by options as the name "-" with those options, as
// encoding/json does: json:"-," and json:"-,omitempty" name a field "-".
// By default any tag whose name is "-" excludes the field.
func DashCommaName() TagOption {
	return func(n *tagNaming) error { n.dashComma = true; return nil }
}

// ValidName checks each name a tag gives a selected field; an error fails
// compilation with ErrSchema and wraps the error. Use it to refuse names
// another reader of the tag parses differently, such as a json name holding
// a quote or backslash, which encoding/json does not read as written. Names
// from [UntaggedName] are not checked.
func ValidName(valid func(name string) error) TagOption {
	return func(n *tagNaming) error {
		if valid == nil {
			return ErrSchema
		}
		n.valid = valid
		return nil
	}
}

// NestedFields lets paths and walks descend into selected struct, map and
// collection fields, so ResolveText("server.port") works with tag names.
// By default each selected field is a whole value.
func NestedFields() TagOption {
	return func(n *tagNaming) error { n.nested = true; return nil }
}

// WithTaggedFields names fields by a "name,options" tag such as db:"id" or
// json:"id". By default it selects exported fields whose tag names them
// explicitly; missing and empty names, "-" and private fields are excluded,
// whitespace in names is preserved, comma options are ignored, each selected
// field is a whole value and embedded fields are not promoted. Options relax
// those defaults. Duplicate names fail compilation.
//
// This replaces any field resolver; the last WithTaggedFields or
// [WithFieldResolver] option applies. Use WithFieldResolver for aliases,
// option-driven inlining or validation. To read annotations without renaming
// fields, use [Field.Tag] with the default engine instead.
func WithTaggedFields(key string, options ...TagOption) Option {
	return func(s *settings) error {
		if key == "" {
			return ErrSchema
		}
		for _, c := range key {
			if c <= ' ' || c == ':' || c == '"' || c == 0x7f {
				return ErrSchema
			}
		}
		naming := &tagNaming{key: key}
		for _, option := range options {
			if option == nil {
				return ErrSchema
			}
			if err := option(naming); err != nil {
				return err
			}
		}
		s.resolver, s.tagged = nil, naming
		return nil
	}
}

func (n *tagNaming) rule(sf reflect.StructField, atomic func(reflect.Type) bool) (FieldRule, error) {
	raw, _ := sf.Tag.Lookup(n.key)
	tag := ParseTag(raw)
	if raw == "-" || tag.Name == "-" && !n.dashComma {
		return FieldRule{Skip: true}, nil
	}
	if tag.Name == "" && n.promote && sf.Anonymous && promotable(sf.Type, atomic) {
		return FieldRule{Inline: true}, nil
	}
	name := tag.Name
	if name != "" && n.valid != nil && sf.IsExported() {
		if err := n.valid(name); err != nil {
			return FieldRule{}, err
		}
	}
	if name == "" && n.untagged != nil && sf.IsExported() {
		name = n.untagged(sf.Name)
	}
	return FieldRule{Name: name, Skip: name == "" || !sf.IsExported(), Atomic: !n.nested, Explicit: tag.Name != ""}, nil
}

func promotable(t reflect.Type, atomic func(reflect.Type) bool) bool {
	if atomic(t) {
		return false
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
		if atomic(t) {
			return false
		}
	}
	return t.Kind() == reflect.Struct
}
