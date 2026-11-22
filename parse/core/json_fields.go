package core

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// JSONFieldsOf walks struct type t (or a pointer to one) as encoding/json
// names its members: by the json tag's name, else the Go name; embedded
// structs promoted; a struct embedded with json:"-" contributing nothing;
// a name two fields claim going to the shallower, then to the one the tag
// names, and dropped when that leaves a tie. These are the members a stored
// document of type t holds. No other tag is read, so a document type is
// named the same wherever it is stored and whatever tags other libraries
// put on it. A Field carries its name, type and route, and of its Tags only
// FieldNameOptions; StructFieldsOf is the walk with parse's row rules. Each
// type is walked once.
func JSONFieldsOf(t reflect.Type) (*StructFields, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %s", t.Kind())
	}
	if cached, ok := jsonFieldCache.Load(t); ok {
		fields, _ := cached.(*StructFields) // the only type stored
		return fields, nil
	}
	fields := walkJSONFields(t)
	actual, _ := jsonFieldCache.LoadOrStore(t, fields)
	stored, _ := actual.(*StructFields)
	return stored, nil
}

// jsonFieldCache holds every struct type JSONFieldsOf has walked: types are
// finite and never unloaded.
var jsonFieldCache sync.Map // reflect.Type -> *StructFields

// jsonCandidate is a field that may take a name.
type jsonCandidate struct {
	field  *Field
	tagged bool
}

// walkJSONFields is encoding/json's field resolution: a breadth-first walk
// of embedded structs, each struct type explored once at the shallowest
// depth it is embedded at.
func walkJSONFields(t reflect.Type) *StructFields {
	type embed struct {
		typ   reflect.Type
		route []int
	}
	var candidates []jsonCandidate
	next := []embed{{typ: t}}
	visited := map[reflect.Type]bool{}
	// embedded counts how often each struct type of the depth being walked
	// is embedded at it. One embedded twice gives each of its fields two
	// claims to one name, so neither takes it.
	var embedded map[reflect.Type]int
	for len(next) > 0 {
		current := next
		next = nil
		embeddedHere := embedded
		embedded = map[reflect.Type]int{}
		for _, e := range current {
			if visited[e.typ] {
				continue
			}
			visited[e.typ] = true
			for i := range e.typ.NumField() {
				sf := e.typ.Field(i)
				if !jsonReadable(sf) {
					continue
				}
				tag := sf.Tag.Get(JSONTagKey)
				if tag == "-" {
					continue
				}
				route := append(slices.Clip(e.route), i)
				name, tagged := tagName(tag)
				promoted := derefUnnamed(sf.Type)
				if !tagged && sf.Anonymous && promoted.Kind() == reflect.Struct {
					if embedded[promoted]++; embedded[promoted] == 1 {
						next = append(next, embed{typ: promoted, route: route})
					}
					continue
				}
				if !tagged {
					name = sf.Name
				}
				_, options, _ := strings.Cut(tag, ",")
				c := jsonCandidate{
					field:  &Field{GoName: sf.Name, Type: sf.Type, Route: route, External: name, Tags: Tags{FieldNameOptions: options}},
					tagged: tagged,
				}
				candidates = append(candidates, c)
				if embeddedHere[e.typ] > 1 {
					candidates = append(candidates, c)
				}
			}
		}
	}
	out := &StructFields{Type: t, external: map[string]*Field{}}
	for _, c := range dominantJSONFields(candidates) {
		out.Fields = append(out.Fields, c.field)
		out.external[c.field.External] = c.field
	}
	slices.SortFunc(out.Fields, func(a, b *Field) int { return slices.Compare(a.Route, b.Route) })
	return out
}

// jsonReadable reports a field encoding/json reads: an exported field, or an
// embedded struct, exported or not, whose exported fields it promotes.
func jsonReadable(sf reflect.StructField) bool {
	if sf.Anonymous {
		return sf.IsExported() || derefUnnamed(sf.Type).Kind() == reflect.Struct
	}
	return sf.IsExported()
}

// tagName is the name of a json tag as Go 1.27's encoding/json reads it
// (encoding/json/v2 parseFieldOptions in v1 mode): the text before the first
// comma, cut at a reserved character (comma, backslash, quotes, backtick) to
// its leading identifier; ok is false when there is none, and the field takes
// its Go name. "-," names a field "-".
func tagName(tag string) (name string, ok bool) {
	if tag == "" || strings.HasPrefix(tag, ",") {
		return "", false
	}
	n := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
		return !strings.ContainsRune(",\\'\"`", r)
	}))
	name = tag[:n]
	if !strings.HasPrefix(tag[n:], ",") && len(name) != len(tag) {
		r, _ := utf8.DecodeRuneInString(tag)
		if r != '_' && !unicode.IsLetter(r) {
			return "", false
		}
		m := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool {
			return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
		}))
		name = tag[:m]
	}
	if !utf8.ValidString(name) {
		name = string([]rune(name))
	}
	return name, true
}

// derefUnnamed follows one unnamed pointer, as encoding/json does for an
// embedded or member type.
func derefUnnamed(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		return t.Elem()
	}
	return t
}

// dominantJSONFields keeps, for each name, the field encoding/json gives it:
// the shallowest, then a tagged one over an untagged one; a tie that remains
// drops every claimant.
func dominantJSONFields(candidates []jsonCandidate) []jsonCandidate {
	slices.SortStableFunc(candidates, func(a, b jsonCandidate) int {
		if c := strings.Compare(a.field.External, b.field.External); c != 0 {
			return c
		}
		if c := len(a.field.Route) - len(b.field.Route); c != 0 {
			return c
		}
		switch {
		case a.tagged && !b.tagged:
			return -1
		case !a.tagged && b.tagged:
			return 1
		}
		return slices.Compare(a.field.Route, b.field.Route)
	})
	var out []jsonCandidate
	for i := 0; i < len(candidates); {
		j := i + 1
		for j < len(candidates) && candidates[j].field.External == candidates[i].field.External {
			j++
		}
		first := candidates[i]
		tied := j-i > 1 && len(candidates[i+1].field.Route) == len(first.field.Route) && candidates[i+1].tagged == first.tagged
		if !tied {
			out = append(out, first)
		}
		i = j
	}
	return out
}

// foldName appends name with each rune replaced by the least rune of its
// Unicode simple case folding orbit, so two names fold alike exactly when
// strings.EqualFold reports them equal: the match encoding/json falls back
// to when no field has a member's exact name.
func foldName(dst, name []byte) []byte {
	for len(name) > 0 {
		r, size := utf8.DecodeRune(name)
		name = name[size:]
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		dst = utf8.AppendRune(dst, least)
	}
	return dst
}
