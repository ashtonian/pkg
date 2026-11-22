package gowalker

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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

// TagKeys returns the keys of tag in order. It returns a *TagError when tag
// is not a sequence of key:"value" pairs as [reflect.StructTag] reads them,
// or repeats a key: Lookup stops at the first malformed pair and reports
// every later key absent, and returns only the first of a repeated key, so
// either mistake would turn an annotation off without an error. Compiling a
// type checks every tag the engine reads this way.
func TagKeys(tag reflect.StructTag) ([]string, error) {
	var keys []string
	text, offset := string(tag), 0
	for {
		// The grammar is reflect.StructTag.Lookup's.
		i := 0
		for i < len(text) && text[i] == ' ' {
			i++
		}
		text, offset = text[i:], offset+i
		if text == "" {
			return keys, nil
		}
		i = 0
		for i < len(text) && text[i] > ' ' && text[i] != ':' && text[i] != '"' && text[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(text) || text[i] != ':' || text[i+1] != '"' {
			return keys, &TagError{Tag: tag, Offset: offset}
		}
		key, start := text[:i], offset
		text, offset = text[i+1:], offset+i+1
		i = 1
		for i < len(text) && text[i] != '"' {
			if text[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(text) {
			return keys, &TagError{Tag: tag, Offset: offset}
		}
		if _, err := strconv.Unquote(text[:i+1]); err != nil {
			return keys, &TagError{Tag: tag, Offset: offset}
		}
		if slices.Contains(keys, key) {
			return keys, &TagError{Tag: tag, Offset: start, Key: key}
		}
		keys = append(keys, key)
		text, offset = text[i+1:], offset+i+1
	}
}

// TagError reports a struct tag [TagKeys] refuses. Offset is the byte where
// reading stopped, or where the repeated Key starts; Key is empty for a
// malformed tag. Its text leaves Tag out, since a tag value can hold a secret,
// such as a default; a caller that shows the tag reads Tag.
type TagError struct {
	Tag    reflect.StructTag
	Offset int
	Key    string
}

func (e *TagError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("struct tag repeats the key %q at byte %d", e.Key, e.Offset)
	}
	return fmt.Sprintf("struct tag is malformed at byte %d: want key:\"value\" pairs separated by spaces", e.Offset)
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
	key        string
	untagged   func(string) string
	valid      func(string) error
	promote    bool
	nested     bool
	dashComma  bool
	keepHidden bool
	// json fixes encoding/json's grammar and conventions (WithJSONFields).
	json bool
}

// errJSONConvention refuses a TagOption that would change a convention
// WithJSONFields fixes.
var errJSONConvention = errors.New("WithJSONFields already names untagged fields, promotes embedded structs and reads \"-,\" as encoding/json does")

// TagOption adjusts the naming convention of [WithTaggedFields] or [WithJSONFields].
type TagOption func(*tagNaming) error

// UntaggedName names exported fields that carry no explicit tag name — an
// absent tag or one such as json:",omitempty" — name(goName), instead of
// excluding them. Use strings.ToLower for koanf-style keys. An empty result
// excludes the field. [WithJSONFields] refuses it: untagged fields keep their
// Go names there.
func UntaggedName(name func(goName string) string) TagOption {
	return func(n *tagNaming) error {
		if n.json {
			return errJSONConvention
		}
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
// [WithJSONFields] refuses it as already applied.
func PromoteEmbedded() TagOption {
	return func(n *tagNaming) error {
		if n.json {
			return errJSONConvention
		}
		n.promote = true
		return nil
	}
}

// DashCommaName excludes a field only when its tag value is exactly "-", and
// reads "-," followed by options as the name "-" with those options, as
// encoding/json does: json:"-," and json:"-,omitempty" name a field "-".
// By default any tag whose name is "-" excludes the field. [WithJSONFields]
// refuses it as already applied.
func DashCommaName() TagOption {
	return func(n *tagNaming) error {
		if n.json {
			return errJSONConvention
		}
		n.dashComma = true
		return nil
	}
}

// KeepHidden keeps an exported field the tag hides with "-" as an unnamed
// leaf ([FieldRule.Unnamed]) instead of skipping it: no name, path or walk
// reaches it, and [Shape.Declarations] and [Schema.AccessField] do, for a
// library that reads such fields under another tag, as a column list reads
// the columns of json:"-" fields. A private field stays excluded.
func KeepHidden() TagOption {
	return func(n *tagNaming) error { n.keepHidden = true; return nil }
}

// ValidName checks each name a tag gives a selected field; an error fails
// compilation with ErrSchema and wraps the error. Use it to refuse names
// another reader of the tag would refuse, or that the library cannot use,
// such as a name holding a path separator. Names from [UntaggedName] are not
// checked.
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
// those defaults. Duplicate names fail compilation. The name is the text
// before the first comma; for the members encoding/json writes, use
// [WithJSONFields], which reads names by encoding/json's grammar.
//
// This replaces any field resolver; the last WithTaggedFields, WithJSONFields
// or [WithFieldResolver] option applies. Use WithFieldResolver for aliases,
// option-driven inlining or validation. To read annotations without renaming
// fields, use [Field.Tag] with the default engine instead.
func WithTaggedFields(key string, options ...TagOption) Option {
	return taggedFields(tagNaming{key: key}, options)
}

// WithJSONFields names fields as encoding/json names the members it writes,
// reading the tag key instead of "json" when key is another: parse's API
// tags, say, that follow encoding/json's rules under their own key.
//
//   - A tag's name ends at the first comma. When a backslash, quote or
//     backtick ends it instead, only its leading Go identifier is the name,
//     and with none the field keeps its Go name: json:"a\"b" names "a",
//     json:"'a b'" names nothing. Invalid UTF-8 becomes U+FFFD.
//   - An exported field without a tag name keeps its Go name.
//   - An embedded struct, or pointer to one, without a tag name contributes
//     its fields, an unexported struct type included; an embedded leaf type
//     stays one field.
//   - Only a tag of exactly "-" excludes a field; "-," names it "-".
//   - A name two fields claim goes to the shallower, then to the one a tag
//     names, as with [ResolveByDepth], which this option implies. A tie that
//     remains fails compilation, naming both fields; add [DropTies] to drop
//     every tied field as encoding/json does.
//
// An unexported embedded struct a tag names is a field, as encoding/json
// writes it as a member. Reflection cannot read or write its value whole, so
// reading it, or walking a value into it, returns [ErrAccess]; with
// [NestedFields], paths to its exported fields read and write as through any
// struct. Its declaration's GoField is unexported, for a consumer that reads
// values to refuse it up front.
//
// A type with its own MarshalJSON or MarshalText method, including one
// promoted from an embedded field, is written by that method; this option
// still describes its fields. Add [WithAtomicImplementers] with json.Marshaler
// and encoding.TextMarshaler to treat such types as single values.
//
// [ValidName], [NestedFields] and [KeepHidden] apply. UntaggedName,
// PromoteEmbedded and DashCommaName are refused: they would change what this
// option fixes.
func WithJSONFields(key string, options ...TagOption) Option {
	return taggedFields(tagNaming{key: key, json: true, untagged: goName, promote: true, dashComma: true}, options)
}

func goName(name string) string { return name }

// taggedFields applies options once, when the Option is made, so the Option
// holds no reference to the caller's slice and can be reused concurrently.
func taggedFields(naming tagNaming, options []TagOption) Option {
	configured, err := configureTags(naming, options)
	return func(s *settings) error {
		if err != nil {
			return err
		}
		// configured is never modified, so every engine can share it.
		s.resolver, s.tagged = nil, configured
		return nil
	}
}

func configureTags(naming tagNaming, options []TagOption) (*tagNaming, error) {
	if naming.key == "" {
		return nil, ErrSchema
	}
	for _, c := range naming.key {
		if c <= ' ' || c == ':' || c == '"' || c == 0x7f {
			return nil, ErrSchema
		}
	}
	for _, option := range options {
		if option == nil {
			return nil, ErrSchema
		}
		if err := option(&naming); err != nil {
			return nil, err
		}
	}
	return &naming, nil
}

func (n *tagNaming) rule(sf reflect.StructField, atomic func(reflect.Type) bool) (FieldRule, error) {
	raw, _ := sf.Tag.Lookup(n.key)
	name, explicit := n.tagName(raw)
	if raw == "-" || name == "-" && !n.dashComma {
		return FieldRule{Unnamed: n.keepHidden, Skip: !n.keepHidden || !sf.IsExported()}, nil
	}
	if !explicit && n.promote && sf.Anonymous && promotable(sf.Type, atomic) {
		return FieldRule{Inline: true}, nil
	}
	privateEmbed := n.json && explicit && !sf.IsExported() && sf.Anonymous && promotable(sf.Type, atomic)
	if explicit && n.valid != nil && (sf.IsExported() || privateEmbed) {
		if err := n.valid(name); err != nil {
			return FieldRule{}, err
		}
	}
	if privateEmbed {
		return FieldRule{Name: name, Atomic: !n.nested, Explicit: true, privateEmbed: true}, nil
	}
	if !explicit && n.untagged != nil && sf.IsExported() {
		name = n.untagged(sf.Name)
	}
	return FieldRule{Name: name, Skip: name == "" || !sf.IsExported(), Atomic: !n.nested, Explicit: explicit}, nil
}

// tagName is the name a tag value gives a field, and whether it gives one.
func (n *tagNaming) tagName(tag string) (string, bool) {
	if n.json {
		return jsonTagName(tag)
	}
	name := ParseTag(tag).Name
	return name, name != ""
}

// jsonTagName reads a tag's name as encoding/json does (parseFieldOptions in
// Go 1.27's encoding/json/v2, which the v1 API uses): the text before the
// first comma, cut at a reserved character (comma, backslash, quote,
// backtick) to its leading identifier. ok is false when the tag names
// nothing, and the field keeps its Go name.
func jsonTagName(tag string) (name string, ok bool) {
	if tag == "" || strings.HasPrefix(tag, ",") {
		return "", false
	}
	n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
		return !strings.ContainsRune(",\\'\"`", r)
	}))
	name = tag[:n]
	if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
		r, _ := utf8.DecodeRuneInString(tag)
		if r != '_' && !unicode.IsLetter(r) {
			return "", false
		}
		m := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
		}))
		name = tag[:m]
	}
	if !utf8.ValidString(name) {
		name = string([]rune(name))
	}
	return name, true
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
