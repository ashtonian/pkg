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
// netip parse), and a custom TextUnmarshaler or UnmarshalMapstructure may
// print anything — a whole nested decode's errors included — and a value
// meant for a sensitive field is often a credential pasted into the wrong
// place. A failure is withheld when its field is sensitive, as PrintRedacted
// judges it, or when the field's type could hold a sensitive value anywhere
// beneath it: such a failure is replaced by one naming the field and its type
// only, its original error dropped from the chain. Every other failure is
// kept whole, so errors.As still reaches its *mapstructure.DecodeError. When
// nothing is withheld, err is returned unchanged.
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
		t, sensitive, ok := resolveDecodeName(root, u.Name())
		switch {
		case !ok:
			return fmt.Errorf("'%s' cannot be decoded (the value may be sensitive and is not shown)", u.Name()), true
		case sensitive:
			return fmt.Errorf("'%s' cannot be decoded into %s (the value is sensitive and not shown)", u.Name(), t), true
		case mayHoldSensitive(t, map[reflect.Type]bool{}):
			return fmt.Errorf("'%s' cannot be decoded into %s (it may hold a sensitive value, so the value is not shown)", u.Name(), t), true
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

// mayHoldSensitive reports whether a value of type t could hold a sensitive
// value beneath it: a struct field tagged sensitive:"true" or named like a
// credential, or a map or interface, whose keys are only known per value. A
// type already being inspected adds nothing.
func mayHoldSensitive(t reflect.Type, seen map[reflect.Type]bool) bool {
	t = derefType(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Map, reflect.Interface:
		return true
	case reflect.Slice, reflect.Array:
		return mayHoldSensitive(t.Elem(), seen)
	case reflect.Struct:
		if !isBranch(t) {
			return false // one value, decoded from one string
		}
		for kf := range keyedFields(t) {
			if kf.field.Tag.Get("sensitive") == "true" || isSensitiveName(kf.field.Name) || mayHoldSensitive(kf.field.Type, seen) {
				return true
			}
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
		f, viaSensitive, found := fieldByKey(t, seg.name)
		if !found {
			return nil, false, false
		}
		ft := derefType(f.Type)
		if viaSensitive || f.Tag.Get("sensitive") == "true" || isSensitiveName(f.Name) && (last || !isBranch(ft)) {
			sensitive = true
		}
		t = ft
	}
	return t, sensitive, true
}

// fieldByKey finds the struct field of t that config key segment key names,
// looking through squashed structs as mapstructure does. viaSensitive
// reports a squashed struct on the way there tagged sensitive:"true", which
// covers the field as it covers everything beneath it.
func fieldByKey(t reflect.Type, key string) (f reflect.StructField, viaSensitive, ok bool) {
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false, false
	}
	for kf := range keyedFields(t) {
		switch {
		case kf.key == key:
			return kf.field, false, true
		case kf.key == "" && isBranch(derefType(kf.field.Type)):
			if f, via, ok := fieldByKey(derefType(kf.field.Type), key); ok {
				return f, via || kf.field.Tag.Get("sensitive") == "true", true
			}
		}
	}
	return reflect.StructField{}, false, false
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
