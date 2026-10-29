package core

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/ashtonian/pkg/gowalker"
)

// StructFields is a struct type's fields, walked once and
// shared by every view parse builds: columns, the filter, sort, search and
// patch schemas, keyset cursor values, server-only patch paths and document
// members. The fields are the ones encoding/json writes under the
// generator's field-name tag — embedded structs promoted as encoding/json
// promotes them — plus the fields that tag hides, which columns, server-only
// paths and relations still read.
type StructFields struct {
	// Type is the walked struct type.
	Type reflect.Type
	// Fields holds the fields in declaration order, an embedded struct's
	// promoted fields in place of it.
	Fields []*Field

	external     map[string]*Field
	columnTagged map[string]*Field
}

// External returns the field named name under the field-name tag.
func (s *StructFields) External(name string) (*Field, bool) {
	f, ok := s.external[name]
	return f, ok
}

// ColumnTagged returns the field whose column tag names column exactly.
func (s *StructFields) ColumnTagged(column string) (*Field, bool) {
	f, ok := s.columnTagged[column]
	return f, ok
}

// Field is one field of a StructFields walk.
type Field struct {
	// GoName is the Go field name.
	GoName string
	// Type is the field's declared type.
	Type reflect.Type
	// Route is the index path from the walked type to the field, through
	// the embedded structs it is promoted from.
	Route []int
	// External is the field's name under the field-name tag, by
	// encoding/json's rule (the tag's name, else the Go name). It is empty
	// when the tag hides the field, or the embedded struct it is promoted
	// from (`json:"-"`).
	External string
	// Tags are the field's annotations, parsed once.
	Tags Tags
}

// Hidden reports a field the field-name tag hides: it has columns, but no
// name a request can use (relations and server fields name themselves).
func (f *Field) Hidden() bool { return f.External == "" }

// Value reads the field from v, a value of the walked struct type. A nil
// embedded pointer on the route is an error: the field is not there.
func (f *Field) Value(v reflect.Value) (reflect.Value, error) {
	if len(f.Route) == 1 {
		return v.Field(f.Route[0]), nil
	}
	fv, err := v.FieldByIndexErr(f.Route)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("field %s: %w", f.GoName, err)
	}
	return fv, nil
}

// Settable returns the field of v, an addressable value of the walked
// struct type, for writing: nil embedded pointers on the route are
// allocated, as encoding/json allocates them when decoding.
func (f *Field) Settable(v reflect.Value) (reflect.Value, error) {
	for i, x := range f.Route {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, fmt.Errorf("field %s: cannot allocate the unexported embedded %s it is promoted from", f.GoName, v.Type().Elem())
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, nil
}

// Tags are the struct tags parse reads on a field. Option-bearing tags hold
// their raw text for the view that interprets them.
type Tags struct {
	// Parse is the field's parse capabilities.
	Parse Capabilities
	// Column is the column tag's text: empty when absent, "-" for a field
	// that is not a column.
	Column string
	// FieldNameOptions are the field-name tag's options (omitempty, ...).
	FieldNameOptions string

	Relation, RelationName string
	FilterOps, PatchOps    string
	Enum                   []string
	Bounds                 Bounds
	Facet                  string
	Paths, Elements        string
	Storage                string
	Geo, GeoSRID           string
	GeoLat, GeoLon         string
	Indexed, Immutable     bool
	Label, Description     string
	Group, Order           string
	SortDefault            string
	SearchWeight           string
}

// Capabilities are the values of a field's `parse` tag. Excluded is
// `parse:"-"`, which also keeps a document's declared paths out of sorts.
type Capabilities struct {
	Filter, Sort, Patch, Search, Facet, Include, Server bool
	Excluded                                            bool
}

// parseCapabilities reads a `parse` tag. "-" or an empty tag grants
// nothing; any other value must be a list of known capabilities, so a
// misspelt one fails schema generation instead of silently turning a
// capability off.
func parseCapabilities(tag string) (Capabilities, error) {
	var c Capabilities
	if tag == "-" {
		return Capabilities{Excluded: true}, nil
	}
	if tag == "" {
		return c, nil
	}
	for part := range strings.SplitSeq(tag, ",") {
		switch strings.TrimSpace(part) {
		case "":
		case "filter":
			c.Filter = true
		case "sort":
			c.Sort = true
		case "patch":
			c.Patch = true
		case "search":
			c.Search = true
		case "facet":
			c.Facet = true
		case "include":
			c.Include = true
		case "server":
			c.Server = true
		default:
			return Capabilities{}, fmt.Errorf("unknown parse capability %q", strings.TrimSpace(part))
		}
	}
	return c, nil
}

// fieldsCacheCapacity is how many struct types each field-name tag keeps
// walked: every resource, document and element type of a large API.
const fieldsCacheCapacity = 1024

// hiddenPrefix starts the walk-internal name of a field the field-name tag
// hides. No field-name tag name can hold a NUL (it is a control
// character), so a hidden field never collides with a named one.
const hiddenPrefix = "\x00"

type fieldsKey struct{ name, column string }

var fieldCaches sync.Map // fieldsKey -> *gowalker.TypeCache[*StructFields]

// StructFields walks t (a struct, or a pointer to one) under the
// generator's field-name and column tags. The walk is cached per type and
// tag pair. A name two fields claim — a shallower field shadowing a promoted
// one, or two promoted fields at one depth — is an error naming both
// fields: encoding/json keeps the shallower field or drops both, and parse
// must see exactly the fields encoding/json writes.
func (g *SchemaGenerator) StructFields(t reflect.Type) (*StructFields, error) {
	return StructFieldsOf(t, g.FieldNameTag(), g.ColumnTagKey())
}

// StructFieldsOf is SchemaGenerator.StructFields for explicit tag keys.
func StructFieldsOf(t reflect.Type, nameTag, columnTag string) (*StructFields, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %s", t.Kind())
	}
	c, err := fieldsCache(fieldsKey{nameTag, columnTag})
	if err != nil {
		return nil, err
	}
	s, err := c.Get(t)
	if err != nil {
		return nil, walkError(t, err)
	}
	return s, nil
}

func fieldsCache(k fieldsKey) (*gowalker.TypeCache[*StructFields], error) {
	if c, ok := fieldCaches.Load(k); ok {
		cache, _ := c.(*gowalker.TypeCache[*StructFields]) // the only type stored
		return cache, nil
	}
	engine, err := gowalker.New(
		gowalker.WithFieldResolver(func(sf reflect.StructField) (gowalker.FieldRule, error) { return fieldRule(sf, k.name) }),
		gowalker.ResolveByDepth(),
		gowalker.WithCacheCapacity(fieldsCacheCapacity),
	)
	if err != nil {
		return nil, fmt.Errorf("field walk engine: %w", err)
	}
	cache := gowalker.NewTypeCache(engine, func(s *gowalker.Schema) (*StructFields, error) {
		return buildStructFields(s, k)
	})
	actual, _ := fieldCaches.LoadOrStore(k, cache)
	stored, _ := actual.(*gowalker.TypeCache[*StructFields])
	return stored, nil
}

// walkError names the fields of a name conflict.
func walkError(t reflect.Type, err error) error {
	if conflict, ok := errors.AsType[*gowalker.NameConflict](err); ok {
		first, second := conflict.First.GoField().Name, conflict.Second.GoField().Name
		if strings.HasPrefix(conflict.Name, hiddenPrefix) {
			return fmt.Errorf("%s: hidden fields %s and %s are declared alike; give one its own column", t, first, second)
		}
		return fmt.Errorf("%s: fields %s and %s are both named %q; rename one (parse refuses names encoding/json resolves by depth)", t, first, second, conflict.Name)
	}
	return fmt.Errorf("%s: %w", t, err)
}

// hiddenName is the walk-internal name of a field the field-name tag hides:
// unique unless two hidden fields share a Go name and every tag, which would
// also share a column.
func hiddenName(sf reflect.StructField) string {
	return hiddenPrefix + sf.Name + hiddenPrefix + string(sf.Tag)
}

// fieldRule names a field as Go 1.27's encoding/json does under tag key:
// the tag's name, else the Go name; a struct embedded without a name has
// its fields promoted, and a name two fields claim goes to the shallower,
// then to the one the tag names (ResolveByDepth, Explicit). A field the
// tag hides keeps a walk-internal name so its column survives. Every named
// field is a leaf: views that read inside one (documents, elements) walk
// its type themselves.
func fieldRule(sf reflect.StructField, key string) (gowalker.FieldRule, error) {
	tag, tagged := sf.Tag.Lookup(key)
	if !sf.IsExported() && !sf.Anonymous {
		return gowalker.FieldRule{Skip: true}, nil
	}
	name, hasName := tagName(tag)
	promotable := sf.Anonymous && !hasName && isStructType(sf.Type)
	switch {
	case tag == "-":
		if sf.Anonymous && isStructType(sf.Type) {
			return gowalker.FieldRule{}, fmt.Errorf("field %s: an embedded struct hidden by %s:\"-\" has no fields encoding/json writes; hide its fields one by one", sf.Name, key)
		}
		if !sf.IsExported() {
			return gowalker.FieldRule{Skip: true}, nil
		}
		return gowalker.FieldRule{Name: hiddenName(sf), Atomic: true}, nil
	case promotable:
		return gowalker.FieldRule{Inline: true}, nil
	case !sf.IsExported():
		if tagged && sf.Anonymous && isStructType(sf.Type) {
			return gowalker.FieldRule{}, fmt.Errorf("field %s: an unexported embedded struct named by %s:%q cannot be read; promote it or export it", sf.Name, key, tag)
		}
		return gowalker.FieldRule{Skip: true}, nil
	}
	if !hasName {
		name = sf.Name
	}
	return gowalker.FieldRule{Name: name, Atomic: true, Explicit: hasName}, nil
}

// tagName is the name of a field-name tag as Go 1.27's encoding/json reads
// it (encoding/json/v2 parseFieldOptions in v1 mode): the text before the
// first comma, cut at a reserved character (comma, backslash, quotes,
// backtick) to its leading identifier; ok is false when there is none, and
// the field takes its Go name. "-," names a field "-".
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

func isStructType(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

func buildStructFields(s *gowalker.Schema, k fieldsKey) (*StructFields, error) {
	root := s.Root()
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	out := &StructFields{Type: root.Type(), external: map[string]*Field{}, columnTagged: map[string]*Field{}}
	for _, wf := range root.Fields() {
		sf := wf.GoField()
		tags, err := readTags(sf, k)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", sf.Name, err)
		}
		f := &Field{GoName: sf.Name, Type: sf.Type, Route: wf.Index(), Tags: tags}
		if !strings.HasPrefix(wf.Name(), hiddenPrefix) {
			f.External = wf.Name()
			out.external[f.External] = f
		}
		if col, _, _ := strings.Cut(tags.Column, ","); col != "" && col != "-" {
			if prev, dup := out.columnTagged[col]; dup {
				return nil, fmt.Errorf("fields %s and %s both have column %q", prev.GoName, f.GoName, col)
			}
			out.columnTagged[col] = f
		}
		out.Fields = append(out.Fields, f)
	}
	return out, nil
}

func readTags(sf reflect.StructField, k fieldsKey) (Tags, error) {
	tag := sf.Tag
	caps, err := parseCapabilities(tag.Get("parse"))
	if err != nil {
		return Tags{}, err
	}
	_, opts, _ := strings.Cut(tag.Get(k.name), ",")
	t := Tags{
		Parse:            caps,
		Column:           tag.Get(k.column),
		FieldNameOptions: opts,
		Relation:         tag.Get("relation"),
		RelationName:     tag.Get("relation_name"),
		FilterOps:        tag.Get("filter_ops"),
		PatchOps:         tag.Get("patch_ops"),
		Facet:            tag.Get("facet"),
		Paths:            tag.Get("paths"),
		Elements:         tag.Get("elements"),
		Storage:          tag.Get("storage"),
		Geo:              tag.Get("geo"),
		GeoSRID:          tag.Get("geo_srid"),
		GeoLat:           tag.Get("geo_lat"),
		GeoLon:           tag.Get("geo_lon"),
		Indexed:          tag.Get("indexed") == "true",
		Immutable:        tag.Get("immutable") == "true",
		Label:            tag.Get("label"),
		Description:      tag.Get("description"),
		Group:            tag.Get("group"),
		Order:            tag.Get("order"),
		SortDefault:      tag.Get("sort_default"),
		SearchWeight:     tag.Get("search_weight"),
	}
	if enum := tag.Get("enum"); enum != "" {
		for v := range strings.SplitSeq(enum, ",") {
			t.Enum = append(t.Enum, strings.TrimSpace(v)) // enum:"a, b" admits "b"
		}
	}
	if t.Bounds, err = readBounds(tag); err != nil {
		return Tags{}, err
	}
	return t, nil
}

// HasFieldNameOption reports whether the field-name tag carries option
// (omitempty, omitzero, ...).
func (f *Field) HasFieldNameOption(option string) bool {
	return slices.Contains(strings.Split(f.Tags.FieldNameOptions, ","), option)
}

// FieldColumn is the field's column: the column tag's name, else the
// generator's convention applied to its external name (its Go name when the
// field-name tag hides it). ok is false for a field that is not a column
// (column tag "-").
func (g *SchemaGenerator) FieldColumn(f *Field) (column string, ok bool, err error) {
	if name, _, _ := strings.Cut(f.Tags.Column, ","); name == "-" {
		return "", false, nil
	}
	base := f.External
	if base == "" {
		base = f.GoName
	}
	col, err := g.ColumnName(base, f.Tags.Column)
	if err != nil {
		return "", false, err
	}
	return col, true, nil
}
