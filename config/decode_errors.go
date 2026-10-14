package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// withholdSensitiveDecodeErrors rewrites a decode failure so no value of a
// sensitive field reaches its text. mapstructure's own errors never quote a
// value, but a text decoder's do (time.Parse quotes its input, so does a
// netip parse, and a custom TextUnmarshaler may print anything), and a value
// meant for a sensitive field is often a credential pasted into the wrong
// place. Each failure on a sensitive field — as PrintRedacted judges
// sensitivity — is replaced by one naming the field and its type only, its
// original error dropped from the chain; every other failure is kept whole,
// so errors.As still reaches its *mapstructure.DecodeError. When no failure
// is on a sensitive field, err is returned unchanged.
func withholdSensitiveDecodeErrors(root reflect.Type, err error) error {
	redacted, _ := withhold(root, err)
	return redacted
}

// withhold returns e with every failure on a sensitive field replaced, and
// whether it replaced any. A wrapper around a replaced failure is rebuilt with
// its own text, when that text can be told apart from what it wraps.
func withhold(root reflect.Type, e error) (error, bool) {
	switch u := e.(type) {
	case *mapstructure.DecodeError:
		if typ, sensitive := decodeTarget(root, u.Name()); sensitive || nestsSensitive(root, u) {
			return fmt.Errorf("'%s' cannot be decoded into %s (the value is sensitive and not shown)", u.Name(), typ), true
		}
		return e, false
	case interface{ Unwrap() []error }:
		inner := u.Unwrap()
		out := make([]error, len(inner))
		changed := false
		for i, ie := range inner {
			var c bool
			out[i], c = withhold(root, ie)
			changed = changed || c
		}
		if !changed {
			return e, false
		}
		return errors.Join(out...), true
	case interface{ Unwrap() error }:
		inner := u.Unwrap()
		redacted, changed := withhold(root, inner)
		if !changed {
			return e, false
		}
		prefix, ok := strings.CutSuffix(e.Error(), inner.Error())
		if !ok {
			prefix = "" // the wrapper's own text is interleaved with what it wraps
		}
		return fmt.Errorf("%s%w", prefix, redacted), true
	}
	return e, false
}

// nestsSensitive reports a failure that carries a whole decode of its own —
// a field type's UnmarshalMapstructure that decodes in turn — when anything
// beneath the field could be sensitive: the inner failures' names are not
// paths from root, so they cannot be judged one by one.
func nestsSensitive(root reflect.Type, de *mapstructure.DecodeError) bool {
	var inner *mapstructure.DecodeError
	if !errors.As(de.Unwrap(), &inner) {
		return false
	}
	t, ok := decodeType(root, de.Name())
	if !ok {
		return true
	}
	for _, f := range fieldsOf(t) {
		if f.Tag.Get("sensitive") == "true" || isSensitiveName(f.Path) || derefType(f.Type).Kind() == reflect.Map {
			return true
		}
	}
	return false
}

// decodeTarget resolves the field mapstructure names — "db.password",
// "features[audit].token", "pools[0].dsn" — against root, and reports its
// type and whether PrintRedacted would redact its value: tagged
// sensitive:"true" or beneath such a field, a leaf or container whose own
// name looks sensitive, or anything beneath a sensitive-looking map key. A
// name that does not resolve is reported sensitive, so an unexpected shape
// withholds rather than leaks.
func decodeTarget(root reflect.Type, name string) (typ string, sensitive bool) {
	t, sensitive, ok := resolveDecodeName(root, name)
	if !ok {
		return "its field type", true
	}
	return t.String(), sensitive
}

// decodeType is the type of the field mapstructure names.
func decodeType(root reflect.Type, name string) (reflect.Type, bool) {
	t, _, ok := resolveDecodeName(root, name)
	return t, ok
}

func resolveDecodeName(root reflect.Type, name string) (t reflect.Type, sensitive, ok bool) {
	segs, ok := splitDecodeName(name)
	if !ok {
		return nil, false, false
	}
	t = derefType(root)
	for i, seg := range segs {
		last := i == len(segs)-1
		if seg.indexed {
			switch t.Kind() {
			case reflect.Map:
				sensitive = sensitive || isSensitiveName(seg.name)
			case reflect.Slice, reflect.Array:
			default:
				return nil, false, false
			}
			t = derefType(t.Elem())
			continue
		}
		f, found := fieldByKey(t, seg.name)
		if !found {
			return nil, false, false
		}
		ft := derefType(f.Type)
		if f.Tag.Get("sensitive") == "true" || isSensitiveName(f.Name) && (last || !isBranch(ft)) {
			sensitive = true
		}
		t = ft
	}
	return t, sensitive, true
}

// fieldByKey finds the struct field of t that config key segment key names,
// looking through squashed structs as mapstructure does.
func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for kf := range keyedFields(t) {
		switch {
		case kf.key == key:
			return kf.field, true
		case kf.key == "" && isBranch(derefType(kf.field.Type)):
			if f, ok := fieldByKey(derefType(kf.field.Type), key); ok {
				return f, true
			}
		}
	}
	return reflect.StructField{}, false
}

// decodeSegment is one step of a mapstructure field name: a struct key, or a
// map key or slice index in brackets.
type decodeSegment struct {
	name    string
	indexed bool
}

// splitDecodeName splits a mapstructure field name into its segments. A map
// key is taken up to a "]" that ends the name or precedes "." or "["; ok is
// false for a name that does not parse.
func splitDecodeName(name string) (segs []decodeSegment, ok bool) {
	for name != "" {
		if rest, found := strings.CutPrefix(name, "["); found {
			end := -1
			for i := 0; i < len(rest); i++ {
				if rest[i] == ']' && (i+1 == len(rest) || rest[i+1] == '.' || rest[i+1] == '[') {
					end = i
					break
				}
			}
			if end < 0 {
				return nil, false
			}
			segs = append(segs, decodeSegment{name: rest[:end], indexed: true})
			name = strings.TrimPrefix(rest[end+1:], ".")
			continue
		}
		end := strings.IndexAny(name, ".[")
		if end < 0 {
			end = len(name)
		}
		if end == 0 {
			return nil, false
		}
		segs = append(segs, decodeSegment{name: name[:end]})
		name = strings.TrimPrefix(name[end:], ".")
	}
	return segs, len(segs) > 0
}
