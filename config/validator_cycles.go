package config

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/go-playground/validator/v10"
)

// refuseCycles refuses cfg, a pointer to a config struct, when it holds a
// cycle on a path the default validator walks. go-playground follows
// pointers, interfaces, ValidatorValues and dived collections with no cycle
// check: it spins forever on a pointer that holds its own address, past any
// deadline, and recurses through a struct graph that leads back on itself
// until the stack overflows and the process dies
// (https://github.com/go-playground/validator/issues/1698).
//
// The walk takes the validator's paths with the decisions [ruleWalk] makes
// on types — the fields it reads ([validatorFields]), what each rule does
// ([stepOf]), the structs it walks into ([walksFields]) — taken on the
// value: a pointer or an interface is followed when set, a slice, an array
// or a map only under a dive. A cycle is a reference met again, with the
// same rules ahead of it, while it is still on the path; the validator
// would walk it the same way forever. A reference met again on another
// path is walked once, so a value sharing a struct among fields is not
// refused, and the walk is linear in the value's size.
func refuseCycles(cfg any) error {
	root := reflect.ValueOf(cfg)
	if root.Kind() != reflect.Pointer || root.IsNil() || !walksFields(root.Elem().Type()) {
		return nil // the validator refuses it as not a struct
	}
	w := cycleWalk{root: root, onPath: map[pathState]pathEntry{}, done: map[pathState]bool{}}
	// The validator's namespaces start with the root type's name and a dot,
	// or with nothing for an unnamed type (see [belowValidatorRoot]).
	if name := root.Elem().Type().Name(); name != "" {
		w.structNS = append(w.structNS, name+"."...)
		w.ns = append(w.ns, name+"."...)
	}
	w.rootLen = len(w.structNS)
	return w.through(reference{ptr: root.Pointer(), typ: root.Type()}, nil, func() error {
		return w.fields(root.Elem())
	})
}

// cycleWalk is one refuseCycles walk. structNS and ns are the validator's
// struct namespace and namespace of the field being walked, as a field
// error would spell them; onPath holds the states entered on the current
// path and where, done those walked to the end.
type cycleWalk struct {
	root     reflect.Value
	structNS []byte
	ns       []byte
	rootLen  int
	onPath   map[pathState]pathEntry
	done     map[pathState]bool
}

// pathState is a reference the walk enters with the rules that apply to
// what lies beneath it, held by their first element: rules are slices of
// the fields' cached rules, so equal suffixes share it.
type pathState struct {
	ref   reference
	rules *string
	n     int
}

// reference identifies a pointer or a map by its address and type, and a
// slice by its header as well: a struct and its first field share an
// address, and slices of one array differ by length.
type reference struct {
	ptr uintptr
	len int
	typ reflect.Type
}

// pathEntry is where the walk entered a state: the lengths of its
// namespaces then.
type pathEntry struct {
	structNS, ns int
}

// value walks v, a field's value or an element's, as the validator does
// with rules ahead of it: through the pointers, interfaces and
// ValidatorValues it resolves first, then the rules in turn.
func (w *cycleWalk) value(v reflect.Value, rules []string) error {
	var valued []reflect.Type
	for {
		if !v.IsValid() || (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
			return nil
		}
		if next, ok := validatorValue(v); ok {
			// A ValidatorValue names the value to validate; one that names
			// another of its own type leads the validator back where it was.
			if slices.Contains(valued, v.Type()) {
				return w.cycle(pathEntry{structNS: len(w.structNS), ns: len(w.ns)})
			}
			valued = append(valued, v.Type())
			v = next
			continue
		}
		switch v.Kind() {
		case reflect.Pointer:
			return w.through(reference{ptr: v.Pointer(), typ: v.Type()}, rules, func() error {
				return w.value(v.Elem(), rules)
			})
		case reflect.Interface:
			v = v.Elem()
			continue
		}
		break
	}
	for i, rule := range rules {
		switch stepOf(rule) {
		case stepStop, stepStructOnly:
			return nil
		case stepDive:
			return w.elements(v, rules[i+1:])
		}
		// stepWhenSet: a value it stops at holds nothing to walk.
	}
	if !walksFields(v.Type()) {
		return nil
	}
	mark, markNS := len(w.structNS), len(w.ns)
	w.structNS, w.ns = append(w.structNS, '.'), append(w.ns, '.')
	err := w.fields(v)
	w.structNS, w.ns = w.structNS[:mark], w.ns[:markNS]
	return err
}

// fields walks the fields of struct v the validator reads.
func (w *cycleWalk) fields(v reflect.Value) error {
	read := validatorFields(v.Type())
	if len(read.problems) > 0 {
		return nil // the validator panics on meeting the struct, before any field
	}
	mark, markNS := len(w.structNS), len(w.ns)
	for _, f := range read.fields {
		w.structNS, w.ns = append(w.structNS[:mark], f.name...), append(w.ns[:markNS], f.alt...)
		if err := w.value(v.Field(f.index), f.rules); err != nil {
			return err
		}
	}
	w.structNS, w.ns = w.structNS[:mark], w.ns[:markNS]
	return nil
}

// elements walks what a dive reaches in v with rules, those after the dive.
func (w *cycleWalk) elements(v reflect.Value, rules []string) error {
	switch v.Kind() {
	case reflect.Array:
		return w.each(v, rules)
	case reflect.Slice:
		if v.Len() == 0 {
			return nil
		}
		return w.through(reference{ptr: v.Pointer(), len: v.Len(), typ: v.Type()}, rules, func() error {
			return w.each(v, rules)
		})
	case reflect.Map:
		if v.Len() == 0 {
			return nil
		}
		return w.through(reference{ptr: v.Pointer(), typ: v.Type()}, rules, func() error {
			return w.entries(v, rules)
		})
	}
	return nil // a dive the validator cannot take: it panics there instead
}

// each walks the elements of slice or array v with rules, each named by
// its index.
func (w *cycleWalk) each(v reflect.Value, rules []string) error {
	mark, markNS := len(w.structNS), len(w.ns)
	for i := range v.Len() {
		w.structNS = append(strconv.AppendInt(append(w.structNS[:mark], '['), int64(i), 10), ']')
		w.ns = append(strconv.AppendInt(append(w.ns[:markNS], '['), int64(i), 10), ']')
		if err := w.value(v.Index(i), rules); err != nil {
			return err
		}
	}
	w.structNS, w.ns = w.structNS[:mark], w.ns[:markNS]
	return nil
}

// entries walks the entries of map v with rules, each named by its key as
// the validator prints it.
func (w *cycleWalk) entries(v reflect.Value, rules []string) error {
	mark, markNS := len(w.structNS), len(w.ns)
	for it := v.MapRange(); it.Next(); {
		w.structNS = fmt.Appendf(w.structNS[:mark], "[%v]", it.Key())
		w.ns = fmt.Appendf(w.ns[:markNS], "[%v]", it.Key())
		if err := w.entry(it.Key(), it.Value(), rules); err != nil {
			return err
		}
	}
	w.structNS, w.ns = w.structNS[:mark], w.ns[:markNS]
	return nil
}

// entry walks one map entry with rules. Under keys, the key takes the
// rules through endkeys and the value those after; with none after, the
// validator walks the value only into the struct it is or points to.
func (w *cycleWalk) entry(key, value reflect.Value, rules []string) error {
	if len(rules) == 0 || rules[0] != "keys" {
		return w.value(value, rules)
	}
	keyRules, valueRules := rules[1:], []string(nil)
	if end := slices.Index(rules, "endkeys"); end >= 0 {
		keyRules, valueRules = rules[1:end+1], rules[end+1:]
	}
	if err := w.value(key, keyRules); err != nil {
		return err
	}
	if len(valueRules) > 0 {
		return w.value(value, valueRules)
	}
	if value.Kind() == reflect.Struct || value.Kind() == reflect.Pointer && value.Elem().Kind() == reflect.Struct {
		return w.value(value, nil)
	}
	return nil
}

// through walks what lies beneath ref, entered with rules ahead of it, by
// walk. A state still on the path is a cycle; one already walked to the
// end led back to nothing on the path then, and so leads to nothing on it
// now.
func (w *cycleWalk) through(ref reference, rules []string, walk func() error) error {
	state := pathState{ref: ref, n: len(rules)}
	if len(rules) > 0 {
		state.rules = &rules[0]
	}
	if entered, ok := w.onPath[state]; ok {
		return w.cycle(entered)
	}
	if w.done[state] {
		return nil
	}
	w.onPath[state] = pathEntry{structNS: len(w.structNS), ns: len(w.ns)}
	err := walk()
	delete(w.onPath, state)
	w.done[state] = true
	return err
}

// cycle is the error for a cycle closing at the field being walked and
// leading back to the one the walk entered at back, each named by key.
func (w *cycleWalk) cycle(back pathEntry) error {
	return fmt.Errorf("%s holds a cycle back to %s", w.key(pathEntry{structNS: len(w.structNS), ns: len(w.ns)}), w.key(back))
}

// key names the field whose namespaces end at at, as a field error is
// named (see [validationKey]): the root is "$".
func (w *cycleWalk) key(at pathEntry) string {
	if at.structNS == w.rootLen {
		return keyPath(nil).orRoot()
	}
	schema, _ := rootSchema(w.root.Type())
	return validationKey(w.root, schema, string(w.structNS[:at.structNS]), string(w.ns[:at.ns]))
}

// validatorValue is the value the validator validates in place of v when
// v's type, or the type v holds, names one: its ValidatorValue. A value
// read through an unexported field cannot be asked.
func validatorValue(v reflect.Value) (reflect.Value, bool) {
	t := v.Type()
	if v.Kind() == reflect.Interface {
		t = v.Elem().Type()
	}
	if !v.CanInterface() || !t.Implements(validatorValuer) {
		return reflect.Value{}, false
	}
	return reflect.ValueOf(v.Interface().(validator.Valuer).ValidatorValue()), true
}
