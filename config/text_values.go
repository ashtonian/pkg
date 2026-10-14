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

// stringToSlice splits a string bound for a slice on commas: "1,2" fills a
// []int and "1s,2s" a []time.Duration. A []byte, or a slice type that
// decodes itself, takes the string whole. A field with a sep tag is split
// before decoding instead (see splitList), so this sees only the others —
// after any WithDecodeHook hook, which can split them its own way.
func stringToSlice(from, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to.Kind() != reflect.Slice || to.Elem().Kind() == reflect.Uint8 || decodesItself(to) {
		return data, nil
	}
	s := reflect.ValueOf(data).String()
	if s == "" {
		return []string{}, nil
	}
	return strings.Split(s, ","), nil
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

// splitList is s as the elements of a list a sep tag splits it into: on sep,
// or with sep "none" as one element. An empty s is no elements.
func splitList(s, sep string) []any {
	if s == "" {
		return []any{}
	}
	if sep == "none" {
		return []any{s}
	}
	parts := strings.Split(s, sep)
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

// tagText is the input the text s of a tag beside tag is: s, or the
// elements a sep tag there splits it into.
func tagText(tag reflect.StructTag, s string) any {
	if sep, ok := tag.Lookup("sep"); ok {
		return splitList(s, sep)
	}
	return s
}

// textMap reports a map one string can fill: string keys, values that are
// not structs, and no decoding of its own.
func textMap(t reflect.Type) bool {
	return t.Kind() == reflect.Map && t.Key().Kind() == reflect.String && !isBranch(derefType(t.Elem())) && !decodesItself(t)
}

// decodeTag decodes s, the text of a tag of a field of type t whose tags are
// tag, into a new value of type t as a tag default decodes: split by a sep
// tag, weakly typed, through the built-in hooks.
func decodeTag(t reflect.Type, tag reflect.StructTag, s string) (reflect.Value, error) {
	out := reflect.New(t)
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       (&options{}).decodeHook(nil),
		WeaklyTypedInput: true,
		Result:           out.Interface(),
	})
	if err != nil {
		return reflect.Value{}, err
	}
	if err := dec.Decode(tagText(tag, s)); err != nil {
		return reflect.Value{}, err
	}
	return out.Elem(), nil
}

func decodesItself(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(textUnmarshaler) || p.Implements(mapstructureUnmarshaler)
}

// parseMapEntries reads comma-separated key=value entries, a value running to
// the next comma and keeping any "=" after the first. An empty string is an
// empty map. Errors name an entry by position, never by its text, which may
// hold a credential.
func parseMapEntries(s string) (map[string]any, error) {
	out := map[string]any{}
	if s == "" {
		return out, nil
	}
	for i, entry := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("map entry %d is not key=value", i+1)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("map entry %d repeats key %q", i+1, k)
		}
		out[k] = v
	}
	return out, nil
}
