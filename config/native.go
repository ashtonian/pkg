package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// mapstructure and the validator name the field a failure is about in their
// own notation: struct keys joined by ".", map keys and indexes in brackets,
// the map key unescaped — named[a.b].port. A key holding "." or "]" makes
// such a name ambiguous on its own, so these functions read it against the
// keys actually present at each step: a bracket is a map key only when
// exactly one present key matches it there. A name they cannot place exactly
// is reported by its longest proven prefix.

// treeKey translates a mapstructure name against the merged source tree that
// was decoded. It returns the key it proved, how many bytes of name that
// key accounts for, and whether it accounts for all of them.
func treeKey(root *schemaNode, tree any, name string) (p keyPath, used int, exact bool) {
	n, data := root, tree
	for used < len(name) {
		rest := name[used:]
		switch n.kind {
		case nodeBranch:
			if p != nil {
				if rest[0] != '.' {
					return p, used, false
				}
				rest = rest[1:]
			}
			key := rest[:fieldEnd(rest)]
			f := n.byKey[key]
			if f == nil {
				return p, used, false
			}
			used += len(name[used:]) - len(rest) + len(key)
			p, n, data = p.field(key), f.node, treeChild(data, key)
		case nodeMap:
			key, size, ok := matchBracket(rest, treeKeys(data))
			if !ok {
				return p, used, false
			}
			used += size
			p, n, data = p.key(key), n.elem, treeEntry(data, key)
		case nodeList:
			i, size, ok := parseBracketIndex(rest)
			if !ok {
				return p, used, false
			}
			used += size
			p, n, data = p.index(i), n.elem, treeElement(data, i)
		default:
			return p, used, false // beneath a value decoded whole: its keys are its own
		}
	}
	return p, used, true
}

// unusedKey renders a key mapstructure reports as unused: a proven prefix
// and the source key no field took, written as a field when it can be read
// as one and quoted otherwise.
func unusedKey(root *schemaNode, tree any, name string) string {
	p, used, exact := treeKey(root, tree, name)
	if exact {
		return p.orRoot()
	}
	raw := strings.TrimPrefix(name[used:], ".")
	if p != nil && used < len(name) && name[used] != '.' {
		return p.String() + name[used:] // not a key under a struct; show what was written
	}
	if validFieldKey(raw) {
		return p.field(raw).String()
	}
	if p == nil {
		return strconv.Quote(raw)
	}
	return p.String() + "." + strconv.Quote(raw)
}

// fieldEnd is where the struct key s starts with ends.
func fieldEnd(s string) int {
	if i := strings.IndexAny(s, ".["); i >= 0 {
		return i
	}
	return len(s)
}

// matchBracket reads the map key that the bracketed selector s starts with
// writes unescaped: the one key in keys that s holds followed by "]" and the
// end, "." or "[". It returns the key and the bytes the selector took.
func matchBracket(s string, keys []string) (key string, size int, ok bool) {
	if s == "" || s[0] != '[' {
		return "", 0, false
	}
	found := 0
	for _, k := range keys {
		end := 1 + len(k)
		if !strings.HasPrefix(s[1:], k) || end >= len(s) || s[end] != ']' {
			continue
		}
		if after := end + 1; after < len(s) && s[after] != '.' && s[after] != '[' {
			continue
		}
		key, size = k, end+1
		found++
	}
	return key, size, found == 1
}

// parseBracketIndex reads the index s starts with: [0].
func parseBracketIndex(s string) (index, size int, ok bool) {
	if s == "" || s[0] != '[' {
		return 0, 0, false
	}
	end := strings.IndexByte(s, ']')
	if end < 0 {
		return 0, 0, false
	}
	i, err := strconv.Atoi(s[1:end])
	if err != nil || i < 0 {
		return 0, 0, false
	}
	return i, end + 1, true
}

// The merged tree holds a struct or map as map[string]any and a list as
// []any; decoding also takes a map for a one-element list and a list of maps
// for one map, so the helpers read those shapes too.

func treeChild(data any, key string) any {
	if m, ok := data.(map[string]any); ok {
		return m[key]
	}
	return nil
}

func treeKeys(data any) []string {
	var keys []string
	switch d := data.(type) {
	case map[string]any:
		for k := range d {
			keys = append(keys, k)
		}
	case []any:
		for _, e := range d {
			keys = append(keys, treeKeys(e)...)
		}
	}
	return keys
}

func treeEntry(data any, key string) any {
	switch d := data.(type) {
	case map[string]any:
		return d[key]
	case []any:
		var found any
		for _, e := range d {
			if v := treeEntry(e, key); v != nil {
				found = v // a later map wins, as decoding merges them
			}
		}
		return found
	}
	return nil
}

func treeElement(data any, i int) any {
	switch d := data.(type) {
	case []any:
		if i < len(d) {
			return d[i]
		}
	case map[string]any:
		if i == 0 {
			return d
		}
	}
	return nil
}

// validatorKey translates a validator struct namespace — the root type, then
// Go field names, map keys and indexes in brackets: Config.DB.Pools[0].Max —
// against the validated config v. A squashed struct's field name contributes
// no key segment. exact is false when the namespace cannot be placed exactly.
func validatorKey(v reflect.Value, structNS string) (keyPath, bool) {
	_, rest, cut := strings.Cut(structNS, ".")
	if !cut {
		return nil, false
	}
	var p keyPath
	for rest != "" {
		v = derefValue(v)
		switch {
		case v.Kind() == reflect.Struct:
			name := rest[:fieldEnd(rest)]
			f, ok := v.Type().FieldByName(name)
			if !ok {
				return p, false
			}
			// A promoted field is reached through the structs embedding it;
			// a squashed one contributes no key.
			for _, i := range f.Index {
				v = derefValue(v)
				if v.Kind() != reflect.Struct {
					return p, false
				}
				hop := v.Type().Field(i)
				key, skip := fieldKey(hop)
				if skip {
					return p, false
				}
				if key != "" {
					p = p.field(key)
				}
				v = v.Field(i)
			}
			rest = rest[len(name):]
		case v.Kind() == reflect.Map:
			keys := make([]string, 0, v.Len())
			byText := map[string]reflect.Value{}
			for _, k := range v.MapKeys() {
				text := fmt.Sprintf("%v", k.Interface())
				keys = append(keys, text)
				byText[text] = k
			}
			key, size, ok := matchBracket(rest, keys)
			if !ok {
				return p, false
			}
			p, v, rest = p.key(key), v.MapIndex(byText[key]), rest[size:]
		case v.Kind() == reflect.Slice || v.Kind() == reflect.Array:
			i, size, ok := parseBracketIndex(rest)
			if !ok || i >= v.Len() {
				return p, false
			}
			p, v, rest = p.index(i), v.Index(i), rest[size:]
		default:
			return p, false
		}
		rest = strings.TrimPrefix(rest, ".")
	}
	return p, true
}

// derefValue follows pointers and interfaces to the value they hold, stopping
// at a nil one.
func derefValue(v reflect.Value) reflect.Value {
	for range maxPointerDepth {
		if (v.Kind() != reflect.Pointer && v.Kind() != reflect.Interface) || v.IsNil() {
			break
		}
		v = v.Elem()
	}
	return v
}
