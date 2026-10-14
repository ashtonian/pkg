package config

import (
	"iter"
	"reflect"
	"slices"
	"strings"
)

// Field is one leaf key of a configuration struct: the unit a default tag, an
// environment variable or a CLI flag sets.
type Field struct {
	// Path is the koanf dot-path, e.g. "db.max_conns".
	Path string
	// Tag is the leaf's struct tag; read default, flag, desc and the rest from it.
	Tag reflect.StructTag
	// Type is the leaf's declared Go type.
	Type reflect.Type
	// Sensitive reports that PrintRedacted redacts the leaf's value: it, or
	// a struct above it, is tagged sensitive:"true", or its Go field name
	// looks like a credential (password, secret, token, key, ...) and it is
	// not tagged sensitive:"false". A struct field's name does not cover the
	// struct it holds.
	Sensitive bool
	// Index is the leaf's index sequence in the config struct, for
	// reflect.Value.FieldByIndex (or FieldByIndexErr, when a pointer on the
	// way may be nil).
	Index []int
}

// Fields reports every leaf key of T in declaration order. It is the single
// definition of how a config struct maps to keys, shared by struct-tag
// defaults, environment-variable mapping, redacted printing and the
// kongprovider flag bridge:
//
//   - A key segment is the koanf tag's name, or the lowercased field name when
//     the tag has none. Options after a comma are not part of the name.
//   - Unexported fields, koanf:"-" and koanf:",remain" are skipped.
//     koanf:",squash" merges a struct's keys into its parent, as mapstructure
//     decodes it — an unexported embedded struct's exported fields too, as
//     encoding/json promotes them. An embedded pointer to an unexported
//     struct cannot be allocated, and a schema squashing one is refused with
//     ErrSchema.
//   - Pointers are followed. A struct is descended into unless it decodes
//     itself — encoding.TextUnmarshaler (time.Time, netip.Addr, ...) or
//     mapstructure's Unmarshaler: such a struct is one leaf decoded whole,
//     exactly as decoding treats it.
//   - Maps, slices, arrays and interfaces are leaves.
//   - A struct type is never re-entered while it is being walked, so a
//     recursive type yields the keys above the recursion rather than
//     recursing forever. Values for the recursive part still decode from
//     files; they just have no default, environment or flag key.
//
// A T that is not a struct (or pointer to one) has no fields.
func Fields[T any]() []Field {
	return fieldsOf(reflect.TypeFor[T]())
}

// walkFields appends t's leaves under prefix, at index sequence index;
// sensitive is true beneath a struct tagged sensitive:"true".
func walkFields(t reflect.Type, prefix string, index []int, sensitive bool, walking map[reflect.Type]bool, out *[]Field) {
	t = derefType(t)
	if !isBranch(t) || walking[t] {
		return
	}
	walking[t] = true
	defer delete(walking, t)

	for kf := range keyedFields(t) {
		path := joinPath(prefix, kf.key)
		tagged := sensitive || taggedSensitive(kf.field.Tag)
		at := append(slices.Clone(index), kf.field.Index...)
		if isBranch(derefType(kf.field.Type)) {
			walkFields(kf.field.Type, path, at, tagged, walking, out)
			continue
		}
		if kf.key == "" {
			// squash on a leaf has no key to contribute; mapstructure rejects it too.
			continue
		}
		*out = append(*out, Field{
			Path:      path,
			Tag:       kf.field.Tag,
			Type:      kf.field.Type,
			Sensitive: tagged || nameSensitive(kf.field),
			Index:     at,
		})
	}
}

// keyedField is a struct field paired with the key segment it contributes.
// key is empty when the field is squashed into its parent.
// FieldName returns the config key segment struct field f is known by: its
// koanf tag name, or its lower-cased Go name when the tag names none — the
// rule [Fields] applies. Register it on a go-playground validator
// (RegisterTagNameFunc) so validation errors read db.max_conns, the key the
// operator wrote, rather than DB.MaxConns. A field tagged koanf:"-" keeps its
// Go name, because "-" tells the validator to skip the field.
func FieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("koanf"), ",")
	switch name {
	case "-":
		return f.Name
	case "":
		return strings.ToLower(f.Name)
	}
	return name
}

type keyedField struct {
	field reflect.StructField
	key   string
}

// keyedFields yields the fields of struct type t that map to config keys,
// applying the koanf tag rules documented on [Fields].
func keyedFields(t reflect.Type) iter.Seq[keyedField] {
	return func(yield func(keyedField) bool) {
		for i := range t.NumField() {
			f := t.Field(i)
			key, skip := fieldKey(f)
			if skip {
				continue
			}
			if !yield(keyedField{field: f, key: key}) {
				return
			}
		}
	}
}

// fieldKey is the key segment struct field f contributes under the rules on
// [Fields]: "" for a squashed field, and skip for one that maps to no key.
// An unexported embedded struct that is squashed contributes its exported
// fields, as encoding/json promotes them and as decoding fills them; through
// a pointer it cannot be allocated, and schemas refuse it (see
// unexportedSquashedPointer).
func fieldKey(f reflect.StructField) (key string, skip bool) {
	name, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
	tagOpts := strings.Split(opts, ",")
	if !f.IsExported() && !(f.Anonymous && f.Type.Kind() == reflect.Struct && slices.Contains(tagOpts, "squash")) {
		return "", true
	}
	switch {
	case name == "-", slices.Contains(tagOpts, "remain"):
		return "", true
	case slices.Contains(tagOpts, "squash"):
		return "", false
	case name == "":
		return strings.ToLower(f.Name), false
	}
	return name, false
}

// unexportedSquashedPointer reports an embedded pointer to an unexported
// struct tagged to squash, which nothing can allocate: reflection cannot set
// an unexported field.
func unexportedSquashedPointer(f reflect.StructField) bool {
	_, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
	return !f.IsExported() && f.Anonymous && f.Type.Kind() == reflect.Pointer && slices.Contains(strings.Split(opts, ","), "squash")
}

// isBranch reports whether t is a struct whose fields are keys of their own,
// as opposed to a leaf value decoded whole: a struct that decodes itself, from
// text or from a map, is one value.
func isBranch(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !opaque(t)
}

// derefType follows pointer types to the type they point at. The bound stops
// a pointer type that points at itself (type P *P).
func derefType(t reflect.Type) reflect.Type {
	for range maxPointerDepth {
		if t.Kind() != reflect.Pointer {
			break
		}
		t = t.Elem()
	}
	return t
}

const maxPointerDepth = 32

func joinPath(prefix, key string) string {
	switch {
	case key == "":
		return prefix
	case prefix == "":
		return key
	default:
		return prefix + "." + key
	}
}

// DefaultsFromTags returns T's `default:"value"` struct tags keyed by koanf
// dot-path, ready for [WithDefaults] or a confmap provider: each the tag's
// text (split into a list when sep is set), but a map field's key=value
// entries as a map, so a source that sets one entry merges with the others.
// Load reads them as its lowest-precedence source, and applies a map's or
// slice's element defaults to each entry the sources create; koanf and
// mapstructure do not read the tag themselves.
//
//	type ServerConfig struct {
//	    Host string `koanf:"host" default:"localhost"`
//	    Port int    `koanf:"port" default:"8080"`
//	}
//	type AppConfig struct {
//	    Server ServerConfig `koanf:"server"`
//	    Debug  bool         `koanf:"debug" default:"false"`
//	}
//	config.DefaultsFromTags[AppConfig]()
//	// {"server.host": "localhost", "server.port": "8080", "debug": "false"}
func DefaultsFromTags[T any]() map[string]any {
	return defaultsFrom(Fields[T]())
}

func defaultsFrom(fields []Field) map[string]any {
	out := make(map[string]any)
	for _, f := range fields {
		v, ok := f.Tag.Lookup("default")
		if !ok {
			continue
		}
		out[f.Path] = v
		if _, ok := f.Tag.Lookup("sep"); ok {
			out[f.Path] = tagText(f.Tag, v)
			continue
		}
		// A map's default is its entries, so a source that sets one entry
		// merges with the rest rather than replacing them.
		if textMap(derefType(f.Type)) {
			if m, err := parseMapEntries(v); err == nil {
				out[f.Path] = m
			}
		}
	}
	return out
}

// fieldsOf is Fields for a type known only at run time.
func fieldsOf(t reflect.Type) []Field {
	var out []Field
	walkFields(t, "", nil, false, map[reflect.Type]bool{}, &out)
	return out
}
