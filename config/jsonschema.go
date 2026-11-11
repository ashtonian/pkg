package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
)

// JSONSchemaDraft is the dialect JSONSchema writes.
const JSONSchemaDraft = "https://json-schema.org/draft/2020-12/schema"

// JSONSchema returns a JSON Schema (Draft 2020-12) for a config file of T,
// for editors and linters: the keys and their types, descriptions from
// `desc` tags, defaults diagnostics may print, and the `validate` rules JSON
// Schema can state. It is advisory: Load is the one authority, and the
// schema neither knows what other sources supply nor every rule.
//
//   - It describes one file. A `validate:"required"` key is not required in
//     it — a default, another file or the environment may supply it — and is
//     listed in the key's x-config-validation instead.
//   - Keys are spelled as the fields' koanf keys; Load also accepts them in
//     any letter case, and a value of another scalar type it can decode
//     weakly ("8080" for a number), neither of which the schema allows.
//   - Translated rules: min, max, len, gt, gte, lt, lte and eq on numbers,
//     string lengths and collection sizes; oneof as an enum on strings and
//     integers, read as the validator reads it, and eq on a string as a
//     const. Every rule's name, translated or not, is listed in
//     x-config-validation; an operand appears only where a rule is
//     translated.
//   - A sensitive key is writeOnly. It, and any key that could hold a
//     sensitive value, has no default, enum or const — an operand can
//     itself be a credential — though its size bounds remain. Numeric value
//     bounds on sensitive fields are withheld too.
//   - Rules after omitempty apply to nonzero values. The zero alternative
//     follows the declared type, preserving nil versus non-nil empty values;
//     earlier rules still apply. dive and keys stop translation.
//   - A duration is a string such as "30s". Its x-config-duration-bounds
//     annotation describes runtime bounds in integer nanoseconds; JSON Schema
//     validators do not enforce that annotation. A type that decodes itself is
//     described by its Go type in x-config-type, unconstrained, or as a
//     string when it decodes from text.
//   - A struct a type holds within itself is a $defs entry, referenced by
//     a name made of its Go type's: letters, digits, '.', '_' and '-'.
//   - With envPrefixes, as [WithEnvPrefixes] takes them, a key a variable
//     sets lists in x-config-env every variable that sets it, in precedence
//     order: under each prefix, then the names its env tag lists. A map's
//     entries are named with a <KEY> placeholder. A $defs entry, shared by
//     every path that reaches it, names none.
//
// The output is deterministic: properties sort by key. T that is not a
// struct, or whose keys cannot be told apart, is an ErrSchema error.
func JSONSchema[T any](envPrefixes ...string) (json.RawMessage, error) {
	t := reflect.TypeFor[T]()
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	g := &schemaGen{defs: map[string]any{}, recursive: recursiveNodes(root), names: map[*schemaNode]string{}, taken: map[string]bool{}}
	defaults, err := readDefaults(t, true)
	if err != nil {
		return nil, err
	}
	g.defaults = defaults
	doc := g.object(root, cursor{}, defaults.value, rootEnvNames(envPrefixList(envPrefixes)))
	if g.err != nil {
		return nil, g.err
	}
	doc["$schema"] = JSONSchemaDraft
	doc["title"] = typeText(derefType(t))
	if len(g.defs) > 0 {
		doc["$defs"] = g.defs
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSchema, err)
	}
	return b, nil
}

type schemaGen struct {
	defaults  declaredDefaults
	err       error
	defs      map[string]any
	recursive map[*schemaNode]bool
	defining  map[*schemaNode]bool
	names     map[*schemaNode]string // each $defs entry's name
	taken     map[string]bool
}

// defName is the $defs name of n: its Go type's name with every character
// but letters, digits, '.', '_' and '-' made '_', so it is one JSON Pointer
// token and a URI fragment as written, and numbered when another type
// already took it.
func (g *schemaGen) defName(n *schemaNode) string {
	if name, ok := g.names[n]; ok {
		return name
	}
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, typeText(n.typ))
	name := base
	for i := 2; g.taken[name]; i++ {
		name = base + "_" + strconv.Itoa(i)
	}
	g.names[n], g.taken[name] = name, true
	return name
}

// recursiveNodes finds the struct nodes a type reaches again from within
// themselves: each is written once, under $defs.
func recursiveNodes(root *schemaNode) map[*schemaNode]bool {
	out := map[*schemaNode]bool{}
	onPath := map[*schemaNode]bool{}
	done := map[*schemaNode]bool{}
	var walk func(n *schemaNode)
	walk = func(n *schemaNode) {
		if onPath[n] {
			out[n] = true
			return
		}
		if done[n] {
			return
		}
		onPath[n] = true
		switch n.kind {
		case nodeBranch:
			for _, f := range n.fields {
				walk(f.node)
			}
		case nodeMap, nodeList:
			walk(n.elem)
		}
		delete(onPath, n)
		done[n] = true
	}
	walk(root)
	return out
}

// object describes a struct: its keys, with defaults from val, the struct
// its tag defaults decode to (invalid beneath a nil pointer), and the
// variables beneath env.
func (g *schemaGen) object(n *schemaNode, c cursor, val reflect.Value, env envNames) map[string]any {
	if val.IsValid() && val.Kind() != reflect.Struct {
		val = reflect.Value{} // a nil pointer: no defaults beneath it
	}
	props := make(map[string]any, len(n.fields))
	for _, f := range n.fields {
		fc := c.intoField(f)
		var fv reflect.Value
		if val.IsValid() {
			fv, _ = f.value(val)
		}
		s := g.field(f, fc, fv, env.field(f.key))
		props[f.key] = s
		for _, a := range aliasNames(f.sf.Tag) {
			alias := maps.Clone(s)
			alias["deprecated"] = true
			alias["description"] = "Renamed to " + f.key + "."
			delete(alias, "x-config-env") // variables are named by the key, never an alias
			props[a] = alias
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": acceptsUnknownKeys(n.typ),
	}
}

// acceptsUnknownKeys reports a struct with a koanf:",remain" field, which
// collects the keys no other field takes — its own, or one of a struct it
// squashes, as decoding finds it.
func acceptsUnknownKeys(t reflect.Type) bool {
	view, err := compileView(t)
	if err != nil {
		return false
	}
	for _, f := range view.Root().DeclaredFields() {
		tag := koanfTag(f)
		switch {
		case tag.Has("remain"):
			return true
		case tag.Has("squash") && f.IsExported():
			if st := derefType(f.Type); st.Kind() == reflect.Struct && acceptsUnknownKeys(st) {
				return true
			}
		}
	}
	return false
}

// field describes one key: its value's schema, then what the field's tags
// add; env names the key's variables.
func (g *schemaGen) field(f *schemaField, c cursor, fv reflect.Value, env envNames) map[string]any {
	s := orNull(g.node(f.node, c, derefValue(fv), env), f.sf.Type)
	if jsonFormat(f.sf.Tag) {
		// A file may hold the value or, as a variable must, its JSON text.
		s = map[string]any{"anyOf": []any{s, map[string]any{"type": "string", "contentMediaType": "application/json"}}}
	}
	if desc := f.sf.Tag.Get("desc"); desc != "" {
		s["description"] = desc
	}
	s["x-reload"] = c.owner.Reload
	if c.owner.EnvRef {
		s["x-env-ref"] = true
	}
	if names := env.set(f.sf.Tag, f.sf.Type); len(names) > 0 {
		s["x-config-env"] = names
	}
	sensitive := c.sensitive() || mayHoldSensitive(f.sf.Type, map[reflect.Type]bool{})
	if c.sensitive() {
		s["writeOnly"] = true
	}
	if _, has := f.sf.Tag.Lookup("default"); (has || g.defaults.has(c.path)) && !sensitive && (f.node.kind != nodeBranch || derefValue(fv).Kind() != reflect.Struct) && fv.IsValid() {
		w := &valueTree{visiting: map[visit]bool{}}
		node := w.value(fv, site{})
		if !node.hasRedaction() {
			s["default"] = node.plain()
		}
		if g.err == nil {
			g.err = w.err
		}
	}
	if ex, has := f.sf.Tag.Lookup("example"); has && !sensitive {
		if v, err := decodeTag(f.sf.Type, f.sf.Tag, ex); err == nil {
			w := &valueTree{visiting: map[visit]bool{}}
			s["examples"] = []any{w.value(v, site{}).plain()}
			if g.err == nil {
				g.err = w.err
			}
		}
	}
	rules(s, f.node, f.sf.Tag.Get("validate"), !sensitive, f.sf.Type)
	return s
}

// orNull lets a value of type t be null: a pointer, map, slice or interface.
func orNull(s map[string]any, t reflect.Type) map[string]any {
	if !nullable(t) {
		return s
	}
	return map[string]any{"anyOf": []any{s, map[string]any{"type": "null"}}}
}

// nullable reports a field a file may set to null: a pointer, map, slice or
// interface.
func nullable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return true
	}
	return false
}

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	decoderType         = reflect.TypeFor[mapstructure.Unmarshaler]()
)

// node describes a value of n's type at c; val holds the defaults of a
// struct, and env names the variables that address it.
func (g *schemaGen) node(n *schemaNode, c cursor, val reflect.Value, env envNames) map[string]any {
	switch n.kind {
	case nodeBranch:
		if g.recursive[n] {
			name := g.defName(n)
			if _, ok := g.defs[name]; !ok && !g.defining[n] {
				if g.defining == nil {
					g.defining = map[*schemaNode]bool{}
				}
				g.defining[n] = true
				g.defs[name] = g.object(n, c, reflect.Value{}, envNames{})
				delete(g.defining, n)
			}
			return map[string]any{"$ref": "#/$defs/" + name}
		}
		return g.object(n, c, val, env)
	case nodeMap:
		s := map[string]any{"type": "object"}
		if c.sensitive() {
			return s // its entries are compared whole: describe nothing inside
		}
		entryEnv := env.entries(n.typ)
		entry := orNull(g.node(n.elem, c.intoEntry(segment{kind: segAny}, n.typ.Elem()), elementDefaults(n.elem), entryEnv), n.typ.Elem())
		if names := entryEnv.set("", n.typ.Elem()); len(names) > 0 && n.elem.kind != nodeBranch {
			entry["x-config-env"] = names
		}
		s["additionalProperties"] = entry
		return s
	case nodeList:
		s := map[string]any{"type": "array"}
		if n.typ.Kind() == reflect.Array {
			s["minItems"], s["maxItems"] = n.typ.Len(), n.typ.Len()
		}
		if c.sensitive() {
			return s
		}
		s["items"] = orNull(g.node(n.elem, c.intoEntry(segment{kind: segAny}, n.typ.Elem()), elementDefaults(n.elem), envNames{}), n.typ.Elem())
		return s
	case nodeDynamic:
		return map[string]any{"x-config-type": typeText(n.typ)}
	}
	return leafSchema(n.typ)
}

// elementDefaults decodes an element struct's tag defaults, which the load
// fills into every entry; anything else has none.
func elementDefaults(n *schemaNode) reflect.Value {
	if n.kind != nodeBranch {
		return reflect.Value{}
	}
	v, err := decodedDefaults(n.typ)
	if err != nil {
		return reflect.Value{} // a broken element default fails every load that creates an entry
	}
	return v
}

// leafSchema describes a value decoded whole.
func leafSchema(t reflect.Type) map[string]any {
	if t == durationType {
		return map[string]any{"type": "string", "x-config-type": "time.Duration"}
	}
	p := reflect.PointerTo(t)
	switch {
	case p.Implements(decoderType):
		return map[string]any{"x-config-type": typeText(t)}
	case p.Implements(textUnmarshalerType):
		return map[string]any{"type": "string", "x-config-type": typeText(t)}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer", "minimum": 0}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "x-config-type": typeText(t)}
		}
	case reflect.Map:
		return map[string]any{"type": "object", "x-config-type": typeText(t)}
	}
	return map[string]any{"x-config-type": typeText(t)}
}

// rules adds what a `validate` tag says that JSON Schema can state, and
// lists every rule's name. Without values, rules that name a value — an
// enum, a const — are left out.
func rules(s map[string]any, n *schemaNode, tag string, values bool, declared reflect.Type) {
	if tag == "" {
		return
	}
	var names []string
	translate := true
	conditional := false
	target := s
	if alts, ok := s["anyOf"].([]any); ok {
		target = alts[0].(map[string]any)
	}
	leading := target
	pointerRule := false
	for rule := range strings.SplitSeq(tag, ",") {
		name, param, _ := strings.Cut(rule, "=")
		if name == "" {
			continue
		}
		names = append(names, name)
		switch name {
		case "dive", "keys", "endkeys":
			translate = false
			continue
		case "omitempty":
			if !translate {
				continue
			}
			if !conditional {
				leading = maps.Clone(target)
			}
			zero, ok := omissionSchema(declared)
			if !ok {
				translate = false
				continue
			}
			tail := map[string]any{}
			for _, key := range []string{"type", "x-config-type"} {
				if v, ok := target[key]; ok {
					tail[key] = v
				}
			}
			// Only the following rules are conditional; earlier constraints remain
			// conjoined with this alternative.
			clauses, _ := target["allOf"].([]any)
			target["allOf"] = append(clauses, map[string]any{"anyOf": []any{zero, tail}})
			target = tail
			conditional = true
			continue
		}
		if !translate || strings.Contains(rule, "|") {
			continue
		}
		if name == "required" {
			if zero, ok := omissionSchema(declared); ok && strings.Contains(tag, "omitempty") {
				if conditional {
					target["not"] = zero
				} else {
					s["not"] = zero
				}
			}
			continue
		}
		typ, _ := target["type"].(string)
		if !values && (name == "oneof" || name == "eq" || typ == "integer" || typ == "number" || target["x-config-type"] == "time.Duration") {
			continue
		}
		before := maps.Clone(target)
		translateRule(target, n, name, param)
		if !conditional && !reflect.DeepEqual(before, target) {
			pointerRule = true
		}
	}
	if nullable(declared) {
		rejectNil := requiredBeforeOmission(tag) || declared.Kind() == reflect.Pointer && pointerRule
		switch declared.Kind() {
		case reflect.Map:
			rejectNil = rejectNil || !matchesConstraint(leading, map[string]any{})
		case reflect.Slice:
			rejectNil = rejectNil || !matchesConstraint(leading, []any{})
		}
		if rejectNil {
			s["not"] = map[string]any{"const": nil}
		}
	}
	if len(names) > 0 {
		s["x-config-validation"] = names
	}
}

// translateRule states one rule in JSON Schema terms where its meaning on
// n's kind of value is plain.
func translateRule(s map[string]any, n *schemaNode, name, param string) {
	if s["x-config-type"] == "time.Duration" {
		switch name {
		case "min", "max", "gte", "lte", "gt", "lt", "len", "eq":
		default:
			return
		}
		duration, err := time.ParseDuration(param)
		if err != nil {
			// The validator also accepts integer nanoseconds in Go literal syntax.
			value, parseErr := strconv.ParseInt(param, 0, 64)
			if parseErr != nil {
				return
			}
			duration = time.Duration(value)
		}
		bounds, _ := s["x-config-duration-bounds"].(map[string]any)
		if bounds == nil {
			bounds = map[string]any{"type": "integer"}
		}
		translateRule(bounds, n, name, strconv.FormatInt(int64(duration), 10))
		s["x-config-duration-bounds"] = bounds
		return
	}
	kind := ""
	switch t, _ := s["type"].(string); {
	case t == "integer" || t == "number":
		kind = "number"
	case t == "string" && s["x-config-type"] == nil:
		kind = "string"
	case t == "array":
		kind = "array"
	case t == "object" && n.kind == nodeMap:
		kind = "object"
	}
	if kind == "" {
		return
	}
	switch {
	case name == "oneof" && (kind == "string" || kind == "number"):
		if enum, ok := enumOf(param, s["type"].(string)); ok && len(enum) > 0 {
			s["enum"] = enum
		}
		return
	case name == "eq" && kind == "string":
		s["const"] = param // a string's eq is its value, not its length
		return
	}
	var v any
	if s["type"] == "integer" {
		if n.typ.Kind() >= reflect.Uint && n.typ.Kind() <= reflect.Uintptr {
			value, err := strconv.ParseUint(param, 10, 64)
			if err != nil {
				return
			}
			v = value
		} else {
			value, err := strconv.ParseInt(param, 10, 64)
			if err != nil {
				return
			}
			v = value
		}
	} else {
		value, err := strconv.ParseFloat(param, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return
		}
		whole := value == float64(int64(value))
		if kind != "number" && (!whole || value < 0) {
			return
		}
		v = value
		if whole {
			v = int64(value)
		}
	}
	bounds := map[string][2]string{
		"number": {"minimum", "maximum"},
		"string": {"minLength", "maxLength"},
		"array":  {"minItems", "maxItems"},
		"object": {"minProperties", "maxProperties"},
	}[kind]
	switch name {
	case "min", "gte":
		setBound(s, bounds[0], v, true)
	case "max", "lte":
		setBound(s, bounds[1], v, false)
	case "len", "eq":
		if kind == "number" {
			s["const"] = v
		} else {
			setBound(s, bounds[0], v, true)
			setBound(s, bounds[1], v, false)
		}
	case "gt":
		if kind == "number" {
			setBound(s, "exclusiveMinimum", v, true)
		}
	case "lt":
		if kind == "number" {
			setBound(s, "exclusiveMaximum", v, false)
		}
	}
}

// enumOf is a oneof rule's values as JSON values of typ, read as the
// validator reads them: words, or quoted runs that may hold spaces, every
// quote removed. An integer matches only its decimal text, so a value
// spelled otherwise ("05", "+5") is one nothing matches and is left out. The
// validator compares only strings and integers: on any other type the rule
// is not translated.
func enumOf(param, typ string) ([]any, bool) {
	var out []any
	for _, word := range oneofValues.FindAllString(param, -1) {
		word = strings.ReplaceAll(word, "'", "")
		switch typ {
		case "string":
			out = append(out, word)
		case "integer":
			if i, err := strconv.ParseInt(word, 10, 64); err == nil && strconv.FormatInt(i, 10) == word {
				out = append(out, i)
			} else if u, err := strconv.ParseUint(word, 10, 64); err == nil && strconv.FormatUint(u, 10) == word {
				out = append(out, u)
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// oneofValues splits a oneof rule's parameter as the validator does.
var oneofValues = regexp.MustCompile(`'[^']*'|\S+`)
