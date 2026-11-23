package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/go-viper/mapstructure/v2"
)

// A map entry is named in a key by its key's text: ports["80"],
// levels["WARN"], routes["10.0.0.0/8"]. One keyCodec per key type says
// what that text is and reads it back, and every walk that names an entry
// or follows a named one goes through it, so the key a source tree, a
// decoded config, an edit and a validation failure name an entry by is one
// key.
//
// The text is the key's own: what MarshalText writes for a type that reads
// text back with UnmarshalText, a time.Duration's String, and otherwise the
// key's kind — a string as it is, a number in decimal, a bool as true or
// false. Text is read back exactly as a load decodes a source's key into
// the map: weakly typed, through the built-in decode hooks, so "010" is the
// integer 8 and "0x10" is 16, as in a file. A WithDecodeHook that reads a
// key type its own way is not consulted: a key's spelling does not depend
// on a load's options.
//
// A map keyed by any other type — an interface, a struct without text
// methods, a type that reads text but writes none — has no codec: it is
// decoded whole, and its field owns what lies beneath it.

// keyCodec writes and reads the keys of one map key type.
type keyCodec struct {
	typ   reflect.Type
	write func(k reflect.Value) (string, error)
	// plain is a string kind no built-in hook reads: its text is the key.
	plain bool
}

var keyCodecs sync.Map // reflect.Type → *keyCodec, nil for a type with none

// keyCodecOf is t's codec, built once per type, or nil when a key of type t
// has no text.
func keyCodecOf(t reflect.Type) *keyCodec {
	if c, ok := keyCodecs.Load(t); ok {
		return c.(*keyCodec)
	}
	c, _ := keyCodecs.LoadOrStore(t, newKeyCodec(t))
	return c.(*keyCodec)
}

func newKeyCodec(t reflect.Type) *keyCodec {
	p := reflect.PointerTo(t)
	if p.Implements(mapstructureUnmarshaler) {
		return nil // it reads its input its own way
	}
	readsText := p.Implements(textUnmarshaler)
	c := &keyCodec{typ: t, plain: t.Kind() == reflect.String && !readsText}
	switch {
	case t == durationType:
		c.write = func(k reflect.Value) (string, error) { return time.Duration(k.Int()).String(), nil }
	case readsText && marshalsText(t):
		c.write = writeText
	case readsText && t.Kind() != reflect.String:
		return nil // it reads text, but has none of its own to be named by
	default:
		c.write = kindText(t)
	}
	if c.write == nil {
		return nil
	}
	return c
}

// kindText writes a key of t by its kind, as the decoder reads one.
func kindText(t reflect.Type) func(reflect.Value) (string, error) {
	switch t.Kind() {
	case reflect.String:
		return func(k reflect.Value) (string, error) { return k.String(), nil }
	case reflect.Bool:
		return func(k reflect.Value) (string, error) { return strconv.FormatBool(k.Bool()), nil }
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(k reflect.Value) (string, error) { return strconv.FormatInt(k.Int(), 10), nil }
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return func(k reflect.Value) (string, error) { return strconv.FormatUint(k.Uint(), 10), nil }
	case reflect.Float32, reflect.Float64:
		bits := t.Bits()
		return func(k reflect.Value) (string, error) { return strconv.FormatFloat(k.Float(), 'g', -1, bits), nil }
	}
	return nil
}

func writeText(k reflect.Value) (string, error) {
	m, _ := textMarshaler(k)
	b, err := m.MarshalText()
	if err != nil {
		return "", fmt.Errorf("writing a %s map key as text: %w", gowalker.TypeText(k.Type()), err)
	}
	return string(b), nil
}

// text is k's text, k a key of the codec's type or an interface holding one.
func (c *keyCodec) text(k reflect.Value) (string, error) {
	if k.Kind() == reflect.Interface {
		k = k.Elem()
	}
	if k.Type() != c.typ {
		k = k.Convert(c.typ)
	}
	return c.write(k)
}

// parse reads text as a load decodes a source's key into the map.
func (c *keyCodec) parse(text string) (reflect.Value, error) {
	if c.plain {
		return reflect.ValueOf(text).Convert(c.typ), nil
	}
	out := reflect.New(c.typ)
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       builtinDecodeHook(),
		WeaklyTypedInput: true,
		Result:           out.Interface(),
	})
	if err != nil {
		return reflect.Value{}, err
	}
	if err := dec.Decode(text); err != nil {
		return reflect.Value{}, err
	}
	return out.Elem(), nil
}

// canonical is the one text of the key text reads as: "010" is "8".
func (c *keyCodec) canonical(text string) (string, error) {
	if c.plain {
		return text, nil
	}
	k, err := c.parse(text)
	if err != nil {
		return "", err
	}
	return c.text(k)
}

// builtinDecodeHook is the decode hooks every load runs, without a
// caller's: how a key's text is read.
var builtinDecodeHook = sync.OnceValue(func() mapstructure.DecodeHookFunc {
	return (&options{}).decodeHook(nil)
})

// entryName is the text naming the entry of key k in a map a source, a
// tree or a config holds: its codec's text. A key with no text, or whose
// MarshalText fails, is named by how it prints, so a diagnostic can still
// point at it; no key selects that entry.
func entryName(k reflect.Value) string {
	if k.Kind() == reflect.Interface && !k.IsNil() {
		k = k.Elem()
	}
	if c := keyCodecOf(k.Type()); c != nil {
		if text, err := c.text(k); err == nil {
			return text
		}
	}
	return fmt.Sprint(k.Interface())
}

// entryKey is the key of map m whose entry text names, read as a load
// reads a source's key into m; ok is false when m holds no such entry.
func entryKey(m reflect.Value, text string) (reflect.Value, bool) {
	c := keyCodecOf(m.Type().Key())
	if c == nil {
		return reflect.Value{}, false
	}
	k, err := c.parse(text)
	if err != nil || !m.MapIndex(k).IsValid() {
		return reflect.Value{}, false
	}
	return k, true
}

// mapEntry is what v, a map of a tree, holds at the entry text names: an
// entry of a map[string]any or wholeMap by its key as written, one of a
// typed map by its key's codec.
func mapEntry(v any, text string) (e any, ok bool) {
	if m, isMap := mergedMap(v); isMap {
		e, ok = m[text]
		return e, ok
	}
	m := reflect.ValueOf(v)
	if m.Kind() != reflect.Map {
		return nil, false
	}
	k, ok := entryKey(m, text)
	if !ok {
		return nil, false
	}
	return m.MapIndex(k).Interface(), true
}

// mapEntryNames is the text naming each of v's entries, v a map of a tree.
func mapEntryNames(v any) []string {
	if m, isMap := mergedMap(v); isMap {
		return slices.Collect(maps.Keys(m))
	}
	m := reflect.ValueOf(v)
	if m.Kind() != reflect.Map {
		return nil
	}
	names := make([]string, 0, m.Len())
	for it := m.MapRange(); it.Next(); {
		names = append(names, entryName(it.Key()))
	}
	return names
}

// namedEntry is one entry of a map, named by its key's text.
type namedEntry struct {
	name  string
	value reflect.Value
}

// sortedEntries is m's entries in the order of their names.
func sortedEntries(m reflect.Value) []namedEntry {
	out := make([]namedEntry, 0, m.Len())
	for it := m.MapRange(); it.Next(); {
		out = append(out, namedEntry{name: entryName(it.Key()), value: it.Value()})
	}
	slices.SortFunc(out, func(a, b namedEntry) int { return strings.Compare(a.name, b.name) })
	return out
}
