package config

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/gowalker"
)

// WithKnownVariables claims exact, case-sensitive environment variable names
// read by the caller. They are omitted from unknown-variable reports; normal
// configuration decoding and unknown-key checks are unchanged. Calls accumulate.
func WithKnownVariables(names ...string) Option {
	owned := append([]string(nil), names...)
	return func(o *options) {
		if o.knownVariables == nil {
			o.knownVariables = map[string]bool{}
		}
		for _, name := range owned {
			o.knownVariables[name] = true
		}
	}
}

// WithVariableFilter claims otherwise unmatched environment variable names for
// which claimed returns true. It sees names only, never values. A nil predicate
// clears the filter. The last call wins.
func WithVariableFilter(claimed func(name string) bool) Option {
	return func(o *options) { o.variableFilter = claimed }
}

func checkEnvRef(f reflect.StructField) error {
	value, tagged := f.Tag.Lookup("envref")
	if !tagged {
		return nil
	}
	t := derefType(f.Type)
	valid := t.Kind() == reflect.String || t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String
	if (value != "true" && value != "false") || !valid {
		return fmt.Errorf("%w: field %s has envref:%q; want true or false on a string or []string", ErrSchema, f.Name, value)
	}
	return nil
}

// referencedVariables reads the decoded values before secret resolution and
// post-load hooks, at the same boundary as unknown-key acceptance.
func referencedVariables(root *schemaNode, value reflect.Value) (map[string]bool, error) {
	names := map[string]bool{}
	visiting := map[visit]bool{}
	var walk func(*schemaNode, reflect.Value) error
	walk = func(n *schemaNode, v reflect.Value) error {
		if !v.IsValid() {
			return nil
		}
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			at := visitOf(v)
			if visiting[at] {
				return nil
			}
			visiting[at] = true
			defer delete(visiting, at)
			v = v.Elem()
		}
		switch n.kind {
		case nodeBranch:
			for _, f := range n.fields {
				fv, err := f.value(v)
				if errors.Is(err, gowalker.ErrMissing) {
					continue
				}
				if err != nil {
					return fmt.Errorf("%w: reading environment references: %w", ErrSchema, err)
				}
				if f.sf.Tag.Get("envref") == "true" {
					ref := derefValue(fv)
					switch ref.Kind() {
					case reflect.String:
						names[ref.String()] = true
					case reflect.Slice:
						for i := range ref.Len() {
							names[ref.Index(i).String()] = true
						}
					}
				}
				if err := walk(f.node, fv); err != nil {
					return err
				}
			}
		case nodeMap, nodeList:
			if v.Kind() != reflect.Array {
				if v.IsNil() {
					return nil
				}
				at := visitOf(v)
				if visiting[at] {
					return nil
				}
				visiting[at] = true
				defer delete(visiting, at)
			}
			if n.kind == nodeMap {
				it := v.MapRange()
				for it.Next() {
					if err := walk(n.elem, it.Value()); err != nil {
						return err
					}
				}
			} else {
				for i := range v.Len() {
					if err := walk(n.elem, v.Index(i)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	err := walk(root, value)
	return names, err
}
