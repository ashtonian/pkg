package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// A default tag or an environment variable holds one string; these read it
// as a list or a map when its field is one, leaving each element or value to
// decode on its own.

// ListElements returns the elements a list keeps of parts, the pieces a
// separator split one string into: each trimmed of surrounding white space,
// and the empty ones dropped, so "a, b," gives [a b]. Every list set from a
// string goes through it — a variable, a tag default, a file's string, an
// element default — so "a,b" and "a, b" set the same list. A command-line
// integration that splits a list flag itself, as Kong does, passes the
// elements through it for the flag to set the list the variable would.
func ListElements(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitText is s as the elements of a list split on sep (see
// [ListElements]), or with sep "none" as one element, kept as written. An
// empty s is no elements.
func splitText(s, sep string) []string {
	if s == "" {
		return []string{}
	}
	if sep == "none" {
		return []string{s}
	}
	return ListElements(strings.Split(s, sep))
}

// stringToSlice splits a string bound for a slice on commas: "1, 2" fills a
// []int and "1s,2s" a []time.Duration. A []byte, or a slice type that
// decodes itself, takes the string whole. A field with a sep tag is split
// before decoding instead (see splitList), so this sees only the others —
// after any WithDecodeHook hook, which can split them its own way.
func stringToSlice(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Slice || to.Elem().Kind() == reflect.Uint8 || decodesItself(to) {
		return data, nil
	}
	return splitText(reflect.ValueOf(data).String(), ","), nil
}

// stringToMap reads a string bound for a map as key=value entries (see
// [parseMapEntries]): "team=core,tier=1" fills a map[string]string. A map of
// structs, or a map type that decodes itself, takes the string as it is.
func stringToMap(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || !textMap(to) {
		return data, nil
	}
	return parseMapEntries(reflect.ValueOf(data).String())
}

// splitList is s as the elements of a list a sep tag splits it into (see
// splitText), as decoding input.
func splitList(s, sep string) []any {
	parts := splitText(s, sep)
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

// tagText is the input the text s of a tag beside tag is: s, the elements a
// sep tag there splits it into, or the value it holds as JSON under
// format:"json".
func tagText(tag reflect.StructTag, s string) (any, error) {
	if jsonFormat(tag) {
		return parseJSON([]byte(s))
	}
	if sep, ok := tag.Lookup("sep"); ok {
		return splitList(s, sep), nil
	}
	return s, nil
}

// jsonFormat reports a field whose strings hold JSON: format:"json".
func jsonFormat(tag reflect.StructTag) bool {
	return tag.Get("format") == "json"
}

// textMap reports a map one string can fill: string keys, values that are
// not structs, and no decoding of its own.
func textMap(t reflect.Type) bool {
	return t.Kind() == reflect.Map && t.Key().Kind() == reflect.String && !isBranch(derefType(t.Elem())) && !decodesItself(t)
}

// decodeTag decodes s, the text of a tag of a field of type t whose tags are
// tag, into a new value of type t as a tag default decodes: split by a sep
// tag or parsed as JSON, weakly typed, through the built-in hooks, with every
// key a field's.
func decodeTag(t reflect.Type, tag reflect.StructTag, s string) (reflect.Value, error) {
	in, err := tagText(tag, s)
	if err != nil {
		return reflect.Value{}, err
	}
	out := reflect.New(t)
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       (&options{}).decodeHook(nil),
		WeaklyTypedInput: true,
		ErrorUnused:      true,
		TagName:          "koanf",
		Result:           out.Interface(),
	})
	if err != nil {
		return reflect.Value{}, err
	}
	if err := dec.Decode(in); err != nil {
		return reflect.Value{}, err
	}
	return out.Elem(), nil
}

// variableSetsField reports a field one environment variable can set whole:
// any field whose strings are JSON, else as variableSets says.
func variableSetsField(tag reflect.StructTag, t reflect.Type) bool {
	return jsonFormat(tag) || variableSets(t)
}

// variableSets reports a value one environment variable can set whole: not
// a section, a list of sections, or a map a string cannot fill.
func variableSets(t reflect.Type) bool {
	t = derefType(t)
	switch {
	case decodesItself(t):
		return true
	case isBranch(t):
		return false
	case t.Kind() == reflect.Map:
		return textMap(t)
	case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
		return !isBranch(derefType(t.Elem()))
	}
	return true
}

func decodesItself(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(textUnmarshaler) || p.Implements(mapstructureUnmarshaler)
}

// parseMapEntries reads comma-separated key=value entries, a value running to
// the next comma and keeping any "=" after the first. Entries are list
// elements (see [ListElements]): "team=core, tier=1," is two. An empty string
// is an empty map. Errors name an entry by position, never by its text, which
// may hold a credential.
func parseMapEntries(s string) (map[string]any, error) {
	out := map[string]any{}
	for i, entry := range splitText(s, ",") {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("map entry %d is not key=value", i+1)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("map entry %d repeats a key", i+1)
		}
		out[k] = v
	}
	return out, nil
}
