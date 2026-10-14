package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

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
//     itself be a credential — though its size bounds remain.
//   - A duration is a string such as "30s"; a type that decodes itself is
//     described by its Go type in x-config-type, unconstrained, or as a
//     string when it decodes from text.
//   - A struct a type holds within itself is a $defs entry, referenced by
//     a name made of its Go type's: letters, digits, '.', '_' and '-'.
//
// The output is deterministic: properties sort by key. T that is not a
// struct, or whose keys cannot be told apart, is an ErrSchema error.
func JSONSchema[T any]() (json.RawMessage, error) {
	t := reflect.TypeFor[T]()
	root, err := rootSchema(t)
	if err != nil {
		return nil, err
	}
	g := &schemaGen{defs: map[string]any{}, recursive: recursiveNodes(root), names: map[*schemaNode]string{}, taken: map[string]bool{}}
	defaults, err := decodedDefaults(derefType(t))
	if err != nil {
		return nil, err
	}
	doc := g.object(root, cursor{}, defaults)
	doc["$schema"] = JSONSchemaDraft
	doc["title"] = derefType(t).String()
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
	}, n.typ.String())
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
// its tag defaults decode to (invalid beneath a nil pointer).
func (g *schemaGen) object(n *schemaNode, c cursor, val reflect.Value) map[string]any {
	if val.IsValid() && val.Kind() != reflect.Struct {
		val = reflect.Value{} // a nil pointer: no defaults beneath it
	}
	props := make(map[string]any, len(n.fields))
	for _, f := range n.fields {
		fc := c.intoField(f)
		var fv reflect.Value
		if val.IsValid() {
			fv, _ = val.FieldByIndexErr(f.index)
		}
		s := g.field(f, fc, fv)
		props[f.key] = s
		for _, a := range aliasNames(f.sf.Tag) {
			alias := maps.Clone(s)
			alias["deprecated"] = true
			alias["description"] = "Renamed to " + f.key + "."
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
	for i := range t.NumField() {
		f := t.Field(i)
		_, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
		tagOpts := strings.Split(opts, ",")
		switch {
		case slices.Contains(tagOpts, "remain"):
			return true
		case slices.Contains(tagOpts, "squash") && f.IsExported():
			if st := derefType(f.Type); st.Kind() == reflect.Struct && acceptsUnknownKeys(st) {
				return true
			}
		}
	}
	return false
}

// field describes one key: its value's schema, then what the field's tags
// add.
func (g *schemaGen) field(f *schemaField, c cursor, fv reflect.Value) map[string]any {
	s := orNull(g.node(f.node, c, derefValue(fv)), f.sf.Type)
	if desc := f.sf.Tag.Get("desc"); desc != "" {
		s["description"] = desc
	}
	sensitive := c.sensitive() || mayHoldSensitive(f.sf.Type, map[reflect.Type]bool{})
	if c.sensitive() {
		s["writeOnly"] = true
	}
	if _, has := f.sf.Tag.Lookup("default"); has && !sensitive && f.node.kind != nodeBranch && fv.IsValid() {
		w := &valueTree{visiting: map[visit]bool{}}
		s["default"] = w.value(fv, site{}).plain()
	}
	if ex, has := f.sf.Tag.Lookup("example"); has && !sensitive {
		if v, err := decodeTag(f.sf.Type, f.sf.Tag, ex); err == nil {
			w := &valueTree{visiting: map[visit]bool{}}
			s["examples"] = []any{w.value(v, site{}).plain()}
		}
	}
	rules(s, f.node, f.sf.Tag.Get("validate"), !sensitive)
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
// struct.
func (g *schemaGen) node(n *schemaNode, c cursor, val reflect.Value) map[string]any {
	switch n.kind {
	case nodeBranch:
		if g.recursive[n] {
			name := g.defName(n)
			if _, ok := g.defs[name]; !ok && !g.defining[n] {
				if g.defining == nil {
					g.defining = map[*schemaNode]bool{}
				}
				g.defining[n] = true
				g.defs[name] = g.object(n, c, reflect.Value{})
				delete(g.defining, n)
			}
			return map[string]any{"$ref": "#/$defs/" + name}
		}
		return g.object(n, c, val)
	case nodeMap:
		s := map[string]any{"type": "object"}
		if c.sensitive() {
			return s // its entries are compared whole: describe nothing inside
		}
		s["additionalProperties"] = orNull(g.node(n.elem, c.intoEntry(segment{kind: segAny}, n.typ.Elem()), elementDefaults(n.elem)), n.typ.Elem())
		return s
	case nodeList:
		s := map[string]any{"type": "array"}
		if n.typ.Kind() == reflect.Array {
			s["minItems"], s["maxItems"] = n.typ.Len(), n.typ.Len()
		}
		if c.sensitive() {
			return s
		}
		s["items"] = orNull(g.node(n.elem, c.intoEntry(segment{kind: segAny}, n.typ.Elem()), elementDefaults(n.elem)), n.typ.Elem())
		return s
	case nodeDynamic:
		return map[string]any{"x-config-type": n.typ.String()}
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
		return map[string]any{"x-config-type": t.String()}
	case p.Implements(textUnmarshalerType):
		return map[string]any{"type": "string", "x-config-type": t.String()}
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
			return map[string]any{"type": "string", "x-config-type": t.String()}
		}
	case reflect.Map:
		return map[string]any{"type": "object", "x-config-type": t.String()}
	}
	return map[string]any{"x-config-type": t.String()}
}

// rules adds what a `validate` tag says that JSON Schema can state, and
// lists every rule's name. Without values, rules that name a value — an
// enum, a const — are left out.
func rules(s map[string]any, n *schemaNode, tag string, values bool) {
	if tag == "" {
		return
	}
	var names []string
	translate := true
	target := s
	if alts, ok := s["anyOf"].([]any); ok {
		target = alts[0].(map[string]any) // the non-null branch
	}
	for rule := range strings.SplitSeq(tag, ",") {
		name, param, _ := strings.Cut(rule, "=")
		if name == "" {
			continue
		}
		names = append(names, name)
		switch name {
		case "omitempty", "dive", "keys", "endkeys":
			// An empty value is exempt, or what follows is the elements':
			// leave the rest untranslated.
			translate = false
			continue
		}
		if translate && !strings.Contains(rule, "|") && (values || name != "oneof" && name != "eq") {
			translateRule(target, n, name, param)
		}
	}
	if len(names) > 0 {
		s["x-config-validation"] = names
	}
}

// translateRule states one rule in JSON Schema terms where its meaning on
// n's kind of value is plain.
func translateRule(s map[string]any, n *schemaNode, name, param string) {
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
	value, err := strconv.ParseFloat(param, 64)
	if err != nil {
		return
	}
	bounds := map[string][2]string{
		"number": {"minimum", "maximum"},
		"string": {"minLength", "maxLength"},
		"array":  {"minItems", "maxItems"},
		"object": {"minProperties", "maxProperties"},
	}[kind]
	whole := value == float64(int64(value))
	if kind != "number" && (!whole || value < 0) {
		return
	}
	var v any = value
	if whole {
		v = int64(value)
	}
	switch name {
	case "min", "gte":
		s[bounds[0]] = v
	case "max", "lte":
		s[bounds[1]] = v
	case "len", "eq":
		if kind == "number" {
			s["const"] = v
		} else {
			s[bounds[0]], s[bounds[1]] = v, v
		}
	case "gt":
		if kind == "number" {
			s["exclusiveMinimum"] = v
		}
	case "lt":
		if kind == "number" {
			s["exclusiveMaximum"] = v
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
