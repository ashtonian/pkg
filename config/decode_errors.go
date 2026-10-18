package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// decodeErrors rewrites a decode failure so it names keys in this package's
// grammar and no value of a sensitive field reaches its text. mapstructure's
// own errors never quote a value, but a text decoder's do (time.Parse quotes
// its input, so does a netip parse), and a custom TextUnmarshaler,
// UnmarshalMapstructure or decode hook may print anything — a whole nested
// decode's errors included — and a value meant for a sensitive field is often
// a credential pasted into the wrong place. A failure is withheld when its
// field is sensitive, as PrintRedacted judges it, or when the field's type
// could hold a sensitive value anywhere beneath it: it is replaced by one
// naming the key and its type only, its original error dropped from the
// chain. A failure whose location cannot be read exactly from the decoder's
// name is withheld too. Every other failure keeps its message under its
// translated key, and errors.As still reaches its *mapstructure.DecodeError.
// tree is the merged source tree that was decoded.
func decodeErrors(root *schemaNode, tree any, err error) error {
	out, _ := withhold(root, tree, err)
	return out
}

// decodeFailure is a mapstructure DecodeError named by its key in this
// package's grammar; errors.As still reaches the original.
type decodeFailure struct {
	key string
	err *mapstructure.DecodeError
}

func (e *decodeFailure) Error() string { return fmt.Sprintf("'%s' %s", e.key, e.err.Unwrap()) }
func (e *decodeFailure) Unwrap() error { return e.err }

// withhold returns e with every failure renamed or replaced, and whether it
// changed any. A wrapper around a changed failure is rebuilt with its own
// text, when that text can be told apart from what it wraps.
func withhold(root *schemaNode, tree any, e error) (error, bool) {
	switch u := e.(type) {
	case *mapstructure.DecodeError:
		p, _, exact := treeKey(root, tree, u.Name())
		if safe := diagnosticPath(root, p); len(safe) < len(p) {
			return fmt.Errorf("'%s' cannot be decoded (sensitive contents are not shown)", safe.orRoot()), true
		}
		c, n, err := root.walkTo(cursor{}, p)
		if !exact || err != nil {
			return fmt.Errorf("'%s' (exact location unavailable) cannot be decoded (the value may be sensitive and is not shown)", p.orRoot()), true
		}
		key := p.orRoot()
		var unconvertible *mapstructure.UnconvertibleTypeError
		if errors.As(u.Unwrap(), &unconvertible) && u.Unwrap() == error(unconvertible) {
			if typeTags.MatchString(unconvertible.Error()) {
				return fmt.Errorf("'%s' expected type '%s', got unconvertible type '%s'", key,
					typeText(unconvertible.Expected.Type()), typeText(reflect.TypeOf(unconvertible.Value))), true
			}
			return &decodeFailure{key: key, err: u}, true // names the expected and the given type, never the value
		}
		// A struct field's name covers a value given to the struct itself: a
		// scalar where an Auth block belongs may be the credential.
		named := n.kind == nodeBranch && len(p) > 0 && p[len(p)-1].kind == segField && isSensitiveName(p[len(p)-1].name) &&
			c.owner.Tag.Get("sensitive") != "false"
		switch {
		case c.sensitive() || named:
			return fmt.Errorf("'%s' cannot be decoded into %s (the value is sensitive and not shown)", key, typeText(n.typ)), true
		case mayHoldSensitive(n.typ, map[reflect.Type]bool{}):
			return fmt.Errorf("'%s' cannot be decoded into %s (it may hold a sensitive value, so the value is not shown)", key, typeText(n.typ)), true
		}
		return &decodeFailure{key: key, err: u}, true
	case interface{ Unwrap() []error }:
		inner := u.Unwrap()
		out := make([]error, len(inner))
		changed := false
		for i, ie := range inner {
			var c bool
			out[i], c = withhold(root, tree, ie)
			changed = changed || c
		}
		if !changed {
			return e, false
		}
		return errors.Join(out...), true
	case interface{ Unwrap() error }:
		inner := u.Unwrap()
		redacted, changed := withhold(root, tree, inner)
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
// credential, or a map or interface, whose keys are only known per value.
// Every exported field counts — of a struct decoded from text as much as of
// any other, and a koanf:",remain" field's map — because a custom decoder
// may fill any of them. A type already being inspected adds nothing.
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
		view, err := compileView(t)
		if err != nil {
			return true // diagnostics withhold a value whose structure is invalid
		}
		for _, f := range view.Root().DeclaredFields() {
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			if taggedSensitive(f.Tag) || nameSensitive(f) || mayHoldSensitive(f.Type, seen) {
				return true
			}
		}
	}
	return false
}
