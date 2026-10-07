package config

import (
	"encoding"
	"fmt"
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
	return fieldsOf(reflect.TypeFor[T]())
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
func fieldKey(f reflect.StructField) (key string, skip bool) {
	if !f.IsExported() {
		return "", true
	}
	name, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
	tagOpts := strings.Split(opts, ",")
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

// envResolver turns an environment variable name into a koanf path. Every
// leaf's name is the prefix plus its path upper-cased with "." → "_", so an
// underscore inside a key survives: APP_DB_MAX_CONNS resolves to
// db.max_conns, where the naive "_" → "." transform would produce
// db.max.conns.
//
// A map of structs (map[string]Struct) has keys no schema names, so its
// variables are matched by shape instead: PREFIX_<MAP>_<KEY>_<FIELD>, where
// FIELD is one of the element struct's leaves and KEY is the rest,
// lower-cased — so the key may contain underscores too.
// APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE resolves to
// features.audit_logs.max_page_size. When two leaves both fit — `size` and
// `max_page_size` make APP_FEATURES_AUDIT_MAX_PAGE_SIZE either
// audit.max_page_size or audit_max_page.size — the variable is ambiguous:
// it sets nothing, and the load fails naming it, rather than configuring an
// entry the operator did not mean.
type envResolver struct {
	fullPrefix string
	exact      map[string]string
	maps       []envMap
	// ambiguous collects the variables path refused, with their readings.
	ambiguous *[]string
}

// envMap is one map-of-structs field: the variable prefix that addresses it
// and the element struct's leaves.
type envMap struct {
	prefix string // e.g. "APP_FEATURES_"
	path   string // e.g. "features"
	leaves []envLeaf
}

type envLeaf struct {
	suffix string // e.g. "_MAX_PAGE_SIZE"
	path   string // e.g. "max_page_size"
}

func newEnvResolver(fields []Field, prefix string) envResolver {
	r := envResolver{fullPrefix: prefix + "_", exact: make(map[string]string, len(fields)), ambiguous: new([]string)}
	for _, f := range fields {
		name := envName(prefix, f.Path)
		r.exact[name] = f.Path
		if elem, ok := structMapElem(f.Type); ok {
			m := envMap{prefix: name + "_", path: f.Path}
			for _, leaf := range fieldsOf(elem) {
				m.leaves = append(m.leaves, envLeaf{suffix: envName("", leaf.Path), path: leaf.Path})
			}
			r.maps = append(r.maps, m)
		}
	}
	slices.SortFunc(r.maps, func(a, b envMap) int { return len(b.prefix) - len(a.prefix) })
	return r
}

func (r envResolver) path(name string) string {
	if p, ok := r.exact[name]; ok {
		return p
	}
	for _, m := range r.maps {
		rest, ok := strings.CutPrefix(name, m.prefix)
		if !ok {
			continue
		}
		var readings []string
		for _, leaf := range m.leaves {
			if key, ok := strings.CutSuffix(rest, leaf.suffix); ok && key != "" {
				readings = append(readings, m.path+"."+strings.ToLower(key)+"."+leaf.path)
			}
		}
		switch len(readings) {
		case 0:
			continue
		case 1:
			return readings[0]
		}
		*r.ambiguous = append(*r.ambiguous, name+" (could set "+strings.Join(readings, " or ")+")")
		return "" // koanf skips a variable mapped to no key
	}
	// Anything else (a map of scalars, say) falls back to "_" → ".".
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, r.fullPrefix)), "_", ".")
}

// ambiguity reports every variable path refused because it fits more than
// one entry and field.
func (r envResolver) ambiguity() error {
	if len(*r.ambiguous) == 0 {
		return nil
	}
	return fmt.Errorf("ambiguous environment variables: %s; rename a map key or field so only one reading fits", strings.Join(*r.ambiguous, "; "))
}

// envName is the variable name for path under prefix; an empty prefix yields
// the "_"-led suffix form used to match a map element's leaves.
func envName(prefix, path string) string {
	return prefix + "_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// structMapElem reports the element struct type of a map[string]Struct (or
// pointer-to-struct) field.
func structMapElem(t reflect.Type) (reflect.Type, bool) {
	t = derefType(t)
	if t.Kind() != reflect.Map || t.Key().Kind() != reflect.String {
		return nil, false
	}
	elem := derefType(t.Elem())
	return elem, isBranch(elem)
}

// fieldsOf is Fields for a type known only at run time.
func fieldsOf(t reflect.Type) []Field {
	var out []Field
	walkFields(t, "", map[reflect.Type]bool{}, &out)
	return out
}
