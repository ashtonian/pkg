package config

import (
	"encoding"
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
//     decodes it.
//   - Pointers are followed. A struct is descended into unless it implements
//     encoding.TextUnmarshaler (time.Time, netip.Addr, ...): such a struct is
//     one leaf decoded from one string, exactly as the decode hook treats it.
//   - Maps, slices, arrays and interfaces are leaves.
//   - A struct type is never re-entered while it is being walked, so a
//     recursive type yields the keys above the recursion rather than
//     recursing forever. Values for the recursive part still decode from
//     files; they just have no default, environment or flag key.
//
// A T that is not a struct (or pointer to one) has no fields.
func Fields[T any]() []Field {
	var out []Field
	walkFields(reflect.TypeFor[T](), "", map[reflect.Type]bool{}, &out)
	return out
}

func walkFields(t reflect.Type, prefix string, walking map[reflect.Type]bool, out *[]Field) {
	t = derefType(t)
	if !isBranch(t) || walking[t] {
		return
	}
	walking[t] = true
	defer delete(walking, t)

	for kf := range keyedFields(t) {
		path := joinPath(prefix, kf.key)
		if isBranch(derefType(kf.field.Type)) {
			walkFields(kf.field.Type, path, walking, out)
			continue
		}
		if kf.key == "" {
			// squash on a leaf has no key to contribute; mapstructure rejects it too.
			continue
		}
		*out = append(*out, Field{Path: path, Tag: kf.field.Tag, Type: kf.field.Type})
	}
}

// keyedField is a struct field paired with the key segment it contributes.
// key is empty when the field is squashed into its parent.
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
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
			tagOpts := strings.Split(opts, ",")
			if name == "-" || slices.Contains(tagOpts, "remain") {
				continue
			}
			key := name
			switch {
			case slices.Contains(tagOpts, "squash"):
				key = ""
			case key == "":
				key = strings.ToLower(f.Name)
			}
			if !yield(keyedField{field: f, key: key}) {
				return
			}
		}
	}
}

var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

// isBranch reports whether t is a struct whose fields are keys of their own,
// as opposed to a leaf value decoded whole.
func isBranch(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !reflect.PointerTo(t).Implements(textUnmarshalerType)
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

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
// dot-path, ready for [WithDefaults] or a confmap provider. GetConfig loads
// them as its lowest-precedence source; koanf and mapstructure do not read
// the tag themselves.
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
		if v, ok := f.Tag.Lookup("default"); ok {
			out[f.Path] = v
		}
	}
	return out
}

// envMapping maps each leaf's environment variable name to its koanf path.
// The name is the prefix plus the upper-cased path with "." replaced by "_",
// so an underscore inside a key survives: APP_DB_MAX_CONNS resolves to
// db.max_conns, where the naive "_" → "." transform would produce
// db.max.conns.
func envMapping(fields []Field, prefix string) map[string]string {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		out[prefix+"_"+strings.ToUpper(strings.ReplaceAll(f.Path, ".", "_"))] = f.Path
	}
	return out
}
