package config

import (
	"reflect"
	"strings"
)

// elementDefaults applies the defaults of a map's or slice's element struct
// to every entry the merged tree holds, below whatever the sources set: an
// entry features["audit"] gets features["audit"].limit = "50" from `Limit int
// default:"50"` unless a source set it, and what the element type's
// Defaulters set above its tags. The defaults of keys outside maps and
// slices are loaded as the lowest sources instead; entries exist only once a
// source names them, so their defaults are filled here, after every source
// has merged. The tree is keyed in the fields' own spelling and finite, so
// walking it ends even for a recursive type.
//
// A value set to null is explicit and stays null — an entry, or a field on
// the way to a default — so a null that turns an optional part off is not
// turned back on. Write {} for an entry that should take its defaults. The
// collection shapes decoding accepts weakly — a map for a one-element slice,
// a list of maps for one map — are filled the same way.
type elementDefaults struct {
	opts *options // the load's decode hooks, for an element's tags; nil for none
	code map[reflect.Type]map[string]any
	// filled records, by map and key, which kind of default filled each key
	// in, so provenance attributes it; nil when the load records none.
	filled map[uintptr]map[string]SourceKind
}

// newElementDefaults returns a filler for one load; with record, it notes
// what it fills for provenance.
func newElementDefaults(o *options, record bool) *elementDefaults {
	e := &elementDefaults{opts: o, code: map[reflect.Type]map[string]any{}}
	if record {
		e.filled = map[uintptr]map[string]SourceKind{}
	}
	return e
}

// fill fills the element defaults of t's collections into data. A
// Defaulter element type that cannot run fails with ErrSchema.
func (e *elementDefaults) fill(t reflect.Type, data any) error {
	t = derefType(t)
	v := reflect.ValueOf(data)
	if !v.IsValid() || opaque(t) {
		return nil
	}
	switch {
	case t.Kind() == reflect.Map && v.Kind() == reflect.Map:
		for _, k := range v.MapKeys() {
			if err := e.element(t.Elem(), v.MapIndex(k).Interface()); err != nil {
				return err
			}
		}
	case t.Kind() == reflect.Map && isList(v):
		for i := range v.Len() {
			if err := e.fill(t, v.Index(i).Interface()); err != nil {
				return err
			}
		}
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && isList(v):
		for i := range v.Len() {
			if err := e.element(t.Elem(), v.Index(i).Interface()); err != nil {
				return err
			}
		}
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && v.Kind() == reflect.Map:
		return e.element(t.Elem(), data)
	case t.Kind() == reflect.Struct && v.Kind() == reflect.Map:
		m, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		n, err := schemaOf(t)
		if err != nil {
			return nil
		}
		for _, f := range n.fields {
			if child, ok := m[f.key]; ok {
				if err := e.fill(f.sf.Type, child); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// element fills one entry of element type elem with elem's defaults, in
// place: its Defaulters' first, then its tags beneath them. An entry that is
// not a map — null included — is left alone.
func (e *elementDefaults) element(elem reflect.Type, entry any) error {
	st := derefType(elem)
	m, ok := entry.(map[string]any)
	if !ok || st.Kind() != reflect.Struct || opaque(st) {
		return e.fill(elem, entry)
	}
	code, err := e.codeOf(st)
	if err != nil {
		return err
	}
	e.fillAbsent(m, code)
	for path, def := range defaultsFrom(fieldsOf(st)) {
		e.setIfAbsent(m, strings.Split(path, "."), def)
	}
	return e.fill(st, m)
}

// codeOf is what st's Defaulters set above its tags, once per load.
func (e *elementDefaults) codeOf(st reflect.Type) (map[string]any, error) {
	if code, ok := e.code[st]; ok {
		return code, nil
	}
	var code map[string]any
	if setsDefaults(st) {
		schema, err := schemaOf(st)
		if err != nil {
			return nil, err
		}
		tags, unrecorded := map[string]any{}, &elementDefaults{}
		for path, def := range defaultsFrom(fieldsOf(st)) {
			unrecorded.setIfAbsent(tags, strings.Split(path, "."), def)
		}
		if code, err = codeDefaults(st, schema, tags, e.opts); err != nil {
			return nil, err
		}
	}
	e.code[st] = code
	return code, nil
}

// fillAbsent copies into m every key of defaults m does not hold, merging
// into a map m holds; anything else m holds — a value, or an explicit null —
// stays.
func (e *elementDefaults) fillAbsent(m, defaults map[string]any) {
	for key, def := range defaults {
		existing, ok := m[key]
		if !ok {
			if def != nil {
				def = deepCopy(reflect.ValueOf(def)).Interface() // each entry its own
			}
			m[key] = def
			e.record(m, key, SourceCodeDefaults)
			continue
		}
		into, isMap := existing.(map[string]any)
		from, fromMap := def.(map[string]any)
		if isMap && fromMap {
			e.fillAbsent(into, from)
		}
	}
}

// setIfAbsent sets the value at key path segs in m when nothing is there,
// creating the maps on the way. Anything already on the way — a value, or
// an explicit null — leaves m as it is.
func (e *elementDefaults) setIfAbsent(m map[string]any, segs []string, value any) {
	for _, seg := range segs[:len(segs)-1] {
		next, ok := m[seg]
		if !ok {
			child := map[string]any{}
			m[seg] = child
			e.record(m, seg, SourceTag)
			m = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return
		}
		m = child
	}
	last := segs[len(segs)-1]
	if existing, ok := m[last]; !ok {
		m[last] = value
		e.record(m, last, SourceTag)
	} else if defaults, ok := value.(map[string]any); ok {
		if entries, ok := existing.(map[string]any); ok {
			for key, def := range defaults {
				e.setIfAbsent(entries, []string{key}, def)
			}
		}
	}
}

func (e *elementDefaults) record(m map[string]any, key string, kind SourceKind) {
	if e.filled == nil {
		return
	}
	at := reflect.ValueOf(m).Pointer()
	if e.filled[at] == nil {
		e.filled[at] = map[string]SourceKind{}
	}
	e.filled[at][key] = kind
}

// filledKind is the kind of default that filled key into m, when one did.
func (e *elementDefaults) filledKind(m reflect.Value, key string) (SourceKind, bool) {
	if e == nil || e.filled == nil || m.Kind() != reflect.Map {
		return "", false
	}
	kind, ok := e.filled[m.Pointer()][key]
	return kind, ok
}
