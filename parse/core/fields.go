package core

import (
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/ashtonian/pkg/gowalker"
)

// StructFields is a struct type's fields, walked once and
// shared by every view parse builds: columns, the filter, sort, search and
// patch schemas, keyset cursor values, server-only patch paths and document
// members. The fields are the ones encoding/json writes under the
// generator's field-name tag — embedded structs promoted as encoding/json
// promotes them — plus the fields that tag hides, which columns, server-only
// paths and relations still read.
//
// A walk is read-only, and so is each *Field it holds: one walk serves every
// generator, resource and stored document of its type, so its methods return
// either a shared read-only handle or a value the caller owns, and nothing a
// caller does with either changes what another reads. Lookups and iteration
// allocate nothing.
type StructFields struct {
	typ          reflect.Type
	fields       []*Field
	external     map[string]*Field
	columnTagged map[string]*Field
	// misspelt are the walked fields' misspelt tag keys, which each
	// generator refuses but for the ones it names as another library's.
	misspelt []misspeltTagKey
	// view is the walked struct's compiled view, which matches a name the
	// way encoding/json decodes it.
	view *gowalker.Shape
}

// Type returns the walked struct type.
func (s *StructFields) Type() reflect.Type { return s.typ }

// Len returns the number of fields.
func (s *StructFields) Len() int { return len(s.fields) }

// Fields yields the fields in declaration order, an embedded struct's
// promoted fields in place of it. slices.Collect gives the caller a slice of
// its own, to reorder or keep.
func (s *StructFields) Fields() iter.Seq[*Field] { return slices.Values(s.fields) }

// External returns the field named name under the field-name tag.
func (s *StructFields) External(name string) (*Field, bool) {
	f, ok := s.external[name]
	return f, ok
}

// FoldedMatch returns the field encoding/json decodes a member named name
// into when no field has that exact name but one matches it under Unicode
// simple case folding: the shallowest, then the first in declaration order
// (gowalker's Shape.Match). A field whose tag sets case:strict never matches
// this way. It allocates nothing and keeps no reference to name.
func (s *StructFields) FoldedMatch(name []byte) (*Field, bool) {
	f, match := s.view.MatchBytes(name)
	if match != gowalker.FoldedMatch {
		return nil, false
	}
	field, ok := s.external[f.Name()]
	return field, ok
}

// ColumnTagged returns the field whose column tag names column exactly.
func (s *StructFields) ColumnTagged(column string) (*Field, bool) {
	f, ok := s.columnTagged[column]
	return f, ok
}

// Field is one field of a StructFields walk: a read-only handle every reader
// of the walk shares. Its accessors return values the caller owns.
type Field struct {
	goName   string
	typ      reflect.Type
	route    []int
	external string
	tags     Tags
	enum     []string
	bounds   Bounds
	// unexportedEmbed is the first unexported embedded pointer on route,
	// nil when there is none (CheckWritable).
	unexportedEmbed reflect.Type
}

// GoName returns the Go field name.
func (f *Field) GoName() string { return f.goName }

// Type returns the field's declared type.
func (f *Field) Type() reflect.Type { return f.typ }

// Route returns the index path from the walked type to the field, through
// the embedded structs it is promoted from, as reflect.Value.FieldByIndex
// takes it. Each call allocates the caller a copy; Value and Settable follow
// the route without one.
func (f *Field) Route() []int { return slices.Clone(f.route) }

// External returns the field's name under the field-name tag, by
// encoding/json's rule (the tag's name, else the Go name). It is empty
// when the tag hides the field (`json:"-"`).
func (f *Field) External() string { return f.external }

// Tags returns the field's annotations, parsed once. Tags holds no
// reference, so the value returned shares nothing with the walk, and
// returning it allocates nothing.
func (f *Field) Tags() Tags { return f.tags }

// Enum returns the values the field's enum tag allows, nil when it has
// none. Each call allocates the caller a copy.
func (f *Field) Enum() []string { return slices.Clone(f.enum) }

// Bounds returns the field's minlen, maxlen, min, max, minitems and maxitems
// tags. Each call allocates the caller a copy of each length it declares.
func (f *Field) Bounds() Bounds { return f.bounds.Clone() }

// Hidden reports a field the field-name tag hides: it has columns, but no
// name a request can use (relations and server fields name themselves).
func (f *Field) Hidden() bool { return f.external == "" }

// CheckWritable refuses a field promoted through an unexported embedded
// pointer. Reflection can neither allocate that pointer nor point it at a
// copy of its parent, so a writer that must not write the parent it shares
// with the caller, as a patch must not, cannot set the field.
func (f *Field) CheckWritable() error {
	if f.unexportedEmbed == nil {
		return nil
	}
	return fmt.Errorf("field %s: a write reaches it through the unexported embedded %s, which reflection can neither allocate nor copy; export the type or embed it by value", f.goName, gowalker.TypeText(f.unexportedEmbed))
}

// Value reads the field from v, a value of the walked struct type. A nil
// embedded pointer on the route is an error: the field is not there.
func (f *Field) Value(v reflect.Value) (reflect.Value, error) {
	if len(f.route) == 1 {
		return v.Field(f.route[0]), nil
	}
	fv, err := v.FieldByIndexErr(f.route)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("field %s: %w", f.goName, err)
	}
	return fv, nil
}

// Settable returns the field of v, an addressable value of the walked
// struct type, for writing: nil embedded pointers on the route are
// allocated, as encoding/json allocates them when decoding.
func (f *Field) Settable(v reflect.Value) (reflect.Value, error) {
	return f.SettableThrough(v, allocateEmbedded)
}

// SettableThrough is Settable with the caller deciding what each embedded
// pointer on the route points at: parent receives the pointer before the
// route follows it and must leave it non-nil. The pointer is settable
// unless it is unexported. A writer that must not write through a parent
// it shares points the pointer at a copy.
func (f *Field) SettableThrough(v reflect.Value, parent func(embedded reflect.Value) error) (reflect.Value, error) {
	for i, x := range f.route {
		if i > 0 && v.Kind() == reflect.Pointer {
			err := parent(v)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("field %s: %w", f.goName, err)
			}
			v = v.Elem()
		}
		v = v.Field(x)
	}
	return v, nil
}

func allocateEmbedded(p reflect.Value) error {
	if !p.IsNil() {
		return nil
	}
	if !p.CanSet() {
		return fmt.Errorf("allocating the unexported embedded %s it is promoted from: reflection cannot set it", gowalker.TypeText(p.Type().Elem()))
	}
	p.Set(reflect.New(p.Type().Elem()))
	return nil
}

// Tags are the struct tags parse reads on a field, but for the enum and
// bounds tags (Field.Enum, Field.Bounds). Option-bearing tags hold their raw
// text for the view that interprets them. Tags holds no reference: a copy
// shares nothing with the walk it came from.
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
	Facet                  string
	Paths, Elements        string
	Storage                string
	Geo, GeoSRID           string
	GeoLat, GeoLon         string
	Indexed, Immutable     bool
	Label, Description     string
	Group, Order           string
	SortDefault            string
}

// Capability is a value of a field's `parse` tag. A document's `paths`
// tag grants its members the same filter, sort and facet capabilities.
type Capability string

const (
	CapabilityFilter  Capability = "filter"
	CapabilitySort    Capability = "sort"
	CapabilityPatch   Capability = "patch"
	CapabilitySearch  Capability = "search"
	CapabilityFacet   Capability = "facet"
	CapabilityInclude Capability = "include"
	CapabilityServer  Capability = "server"
)

// Capabilities are the values of a field's `parse` tag. Excluded is
// `parse:"-"`, which also keeps a document's declared paths out of sorts.
type Capabilities struct {
	Filter, Sort, Patch, Search, Facet, Include, Server bool
	Excluded                                            bool
}

// Has reports whether c grants capability.
func (c Capabilities) Has(capability Capability) bool {
	switch capability {
	case CapabilityFilter:
		return c.Filter
	case CapabilitySort:
		return c.Sort
	case CapabilityPatch:
		return c.Patch
	case CapabilitySearch:
		return c.Search
	case CapabilityFacet:
		return c.Facet
	case CapabilityInclude:
		return c.Include
	case CapabilityServer:
		return c.Server
	}
	return false
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
		switch Capability(strings.TrimSpace(part)) {
		case "":
		case CapabilityFilter:
			c.Filter = true
		case CapabilitySort:
			c.Sort = true
		case CapabilityPatch:
			c.Patch = true
		case CapabilitySearch:
			c.Search = true
		case CapabilityFacet:
			c.Facet = true
		case CapabilityInclude:
			c.Include = true
		case CapabilityServer:
			c.Server = true
		default:
			return Capabilities{}, fmt.Errorf("unknown parse capability %q", strings.TrimSpace(part))
		}
	}
	return c, nil
}

type fieldsKey struct{ name, column string }

// fieldEngines holds one engine per field-name tag: naming reads no other
// tag. Each keeps every type it compiles, as views key maps by *Field and a
// walk must be the same *StructFields every time.
var fieldEngines sync.Map // field-name tag -> *gowalker.Engine

var fieldCaches sync.Map // fieldsKey -> *gowalker.TypeCache[*StructFields]

// StructFields walks t (a struct, or a pointer to one) under the
// generator's field-name and column tags. The walk is cached per type and
// tag pair. A name two fields claim goes to the field encoding/json writes
// under it: the shallower one, then at one depth the one the tag names.
// Two fields that claim a name at one depth, both tagged or both untagged,
// are an error naming both, since encoding/json writes neither and parse
// must see exactly the fields it writes. A stored document's members are
// named by JSONFieldsOf, which drops such a tie as encoding/json does.
//
// A walked field's tag key parse does not read that misspells one it does
// (gowalker.MisspeltTagKey) is an error naming the field, the key written
// and the key meant, unless GeneratorOptions.ForeignTagKeys names it.
func (g *SchemaGenerator) StructFields(t reflect.Type) (*StructFields, error) {
	s, err := StructFieldsOf(t, g.FieldNameTag(), g.ColumnTagKey())
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, m := range s.misspelt {
		if !slices.Contains(g.opts.ForeignTagKeys, m.written) {
			errs = append(errs, m.err())
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
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
	engine, err := fieldEngine(k.name)
	if err != nil {
		return nil, err
	}
	cache := gowalker.NewTypeCache(engine, func(s *gowalker.Schema) (*StructFields, error) {
		return buildStructFields(s, k)
	})
	actual, _ := fieldCaches.LoadOrStore(k, cache)
	stored, _ := actual.(*gowalker.TypeCache[*StructFields])
	return stored, nil
}

// fieldEngine names fields as encoding/json does under the field-name tag,
// every named field a leaf (views that read inside one, documents and
// elements, walk its type themselves). A field the tag hides is kept without
// a name, so its column survives.
func fieldEngine(nameTag string) (*gowalker.Engine, error) {
	if e, ok := fieldEngines.Load(nameTag); ok {
		engine, _ := e.(*gowalker.Engine) // the only type stored
		return engine, nil
	}
	engine, err := gowalker.New(
		gowalker.WithJSONFields(nameTag, gowalker.KeepHidden()),
		gowalker.WithUnboundedCache(),
	)
	if err != nil {
		return nil, fmt.Errorf("field walk engine: %w", err)
	}
	actual, _ := fieldEngines.LoadOrStore(nameTag, engine)
	stored, _ := actual.(*gowalker.Engine)
	return stored, nil
}

// walkError names the fields of a name conflict.
func walkError(t reflect.Type, err error) error {
	if conflict, ok := errors.AsType[*gowalker.NameConflict](err); ok {
		first, second := promotedName(conflict.First), promotedName(conflict.Second)
		return fmt.Errorf("%s: fields %s and %s are both named %q at one depth, so encoding/json writes neither; rename one", gowalker.TypeText(t), first, second, conflict.Name)
	}
	return fmt.Errorf("%s: %w", gowalker.TypeText(t), err)
}

// promotedName is a field's Go name behind the embedded fields it is
// promoted through, so two promoted ID fields read as Audit.ID and Base.ID.
func promotedName(f *gowalker.Field) string {
	var b strings.Builder
	for _, parent := range f.InlineParents() {
		b.WriteString(parent.Name)
		b.WriteByte('.')
	}
	b.WriteString(f.GoField().Name)
	return b.String()
}

func isStructType(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// buildStructFields lists the named fields and the ones the field-name tag
// hides, in declaration order, an embedded struct's in place of it.
func buildStructFields(s *gowalker.Schema, k fieldsKey) (*StructFields, error) {
	out := &StructFields{typ: s.Type(), external: map[string]*Field{}, columnTagged: map[string]*Field{}, view: s.Root()}
	for _, d := range s.Root().Declarations() {
		sf := d.GoField
		if sf.Anonymous && isStructType(sf.Type) && sf.Tag.Get(k.name) == "-" {
			return nil, fmt.Errorf("field %s: an embedded struct hidden by %s:\"-\" has no fields encoding/json writes; hide its fields one by one", sf.Name, k.name)
		}
		if d.State != gowalker.Named && d.State != gowalker.Unnamed {
			continue
		}
		// gowalker names it, as encoding/json writes it; a row reads every
		// field's value.
		if d.State == gowalker.Named && !sf.IsExported() {
			return nil, fmt.Errorf("field %s: %s:%q names the unexported embedded %s, which reflection cannot read; export the type or remove the name", sf.Name, k.name, sf.Tag.Get(k.name), gowalker.TypeText(sf.Type))
		}
		tags, err := readTags(sf, k)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", sf.Name, err)
		}
		enum, bounds, err := readConstraints(sf)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", sf.Name, err)
		}
		out.misspelt = append(out.misspelt, misspeltTagKeys(d, k)...)
		f := &Field{goName: sf.Name, typ: sf.Type, route: d.Index, tags: tags, enum: enum, bounds: bounds, unexportedEmbed: unexportedEmbed(out.typ, d.Index)}
		if d.State == gowalker.Named {
			f.external = d.Field.Name()
			out.external[f.external] = f
		}
		if col, _, _ := strings.Cut(tags.Column, ","); col != "" && col != "-" {
			if prev, dup := out.columnTagged[col]; dup {
				return nil, fmt.Errorf("fields %s and %s both have column %q", prev.goName, f.goName, col)
			}
			out.columnTagged[col] = f
		}
		out.fields = append(out.fields, f)
	}
	return out, nil
}

// unexportedEmbed returns the first unexported embedded pointer on route, a
// field's index path through the struct type t, or nil when there is none.
// An unexported embedded struct held by value is no obstacle: reflection
// sets the exported fields promoted from it, as encoding/json does.
func unexportedEmbed(t reflect.Type, route []int) reflect.Type {
	for _, x := range route[:len(route)-1] {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		sf := t.Field(x)
		if sf.Type.Kind() == reflect.Pointer && !sf.IsExported() {
			return sf.Type
		}
		t = sf.Type
	}
	return nil
}

func readTags(sf reflect.StructField, k fieldsKey) (Tags, error) {
	tag := sf.Tag
	caps, err := parseCapabilities(tag.Get("parse"))
	if err != nil {
		return Tags{}, err
	}
	_, opts, _ := strings.Cut(tag.Get(k.name), ",")
	indexed, err := boolTag(tag, "indexed")
	if err != nil {
		return Tags{}, err
	}
	immutable, err := boolTag(tag, "immutable")
	if err != nil {
		return Tags{}, err
	}
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
		Indexed:          indexed,
		Immutable:        immutable,
		Label:            tag.Get("label"),
		Description:      tag.Get("description"),
		Group:            tag.Get("group"),
		Order:            tag.Get("order"),
		SortDefault:      tag.Get("sort_default"),
	}
	return t, nil
}

// readConstraints reads the enum and bounds tags of sf.
func readConstraints(sf reflect.StructField) (enum []string, bounds Bounds, err error) {
	text := sf.Tag.Get(ConstraintEnum)
	if text != "" {
		enum, err = ParseEnumTag(text)
		if err != nil {
			return nil, Bounds{}, fmt.Errorf("%s:%q: %w", ConstraintEnum, text, err)
		}
	}
	bounds, err = readBounds(sf.Tag)
	if err != nil {
		return nil, Bounds{}, err
	}
	return enum, bounds, nil
}

// ParseEnumTag reads an enum tag's comma-separated values. A plain value
// is trimmed; a value in single quotes is kept as written, commas and edge
// spaces included, a doubled quote inside it standing for one quote. The
// forms mix by value:
//
//	enum:"a, b"                             a | b
//	enum:"'net 30, eom',' padded','it''s'"  net 30, eom |  padded | it's
//	enum:"'',a"                             (empty) | a
//
// An empty plain value, an unterminated quote and text after a closing
// quote are errors.
func ParseEnumTag(tag string) ([]string, error) {
	var values []string
	rest := tag
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		var v string
		if strings.HasPrefix(rest, "'") {
			var b strings.Builder
			i := 1
			for {
				j := strings.IndexByte(rest[i:], '\'')
				if j < 0 {
					return nil, fmt.Errorf("value %d: unterminated quote", len(values)+1)
				}
				b.WriteString(rest[i : i+j])
				i += j + 1
				if !strings.HasPrefix(rest[i:], "'") {
					break
				}
				b.WriteByte('\'')
				i++
			}
			v, rest = b.String(), strings.TrimLeftFunc(rest[i:], unicode.IsSpace)
			if rest != "" && rest[0] != ',' {
				return nil, fmt.Errorf("value %d: text after its closing quote", len(values)+1)
			}
		} else {
			end := strings.IndexByte(rest, ',')
			if end < 0 {
				end = len(rest)
			}
			v, rest = strings.TrimSpace(rest[:end]), rest[end:]
			if v == "" {
				return nil, fmt.Errorf("value %d is empty; '' declares the empty string", len(values)+1)
			}
		}
		values = append(values, v)
		if rest == "" {
			return values, nil
		}
		rest = rest[1:] // the comma
	}
}

// boolTag reads a tag that is "true", "false" or absent, so a misspelt
// value fails schema generation instead of reading as false.
func boolTag(tag reflect.StructTag, key string) (bool, error) {
	switch v, ok := tag.Lookup(key); {
	case !ok || v == "false":
		return false, nil
	case v == "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s: %q is neither true nor false", key, v)
	}
}

// HasFieldNameOption reports whether the field-name tag carries option
// (omitempty, omitzero, ...).
func (f *Field) HasFieldNameOption(option string) bool {
	return slices.Contains(strings.Split(f.tags.FieldNameOptions, ","), option)
}

// FieldColumn is the field's column: the column tag's name, else the
// generator's convention applied to its external name (its Go name when the
// field-name tag hides it). ok is false for a field that is not a column
// (column tag "-").
func (g *SchemaGenerator) FieldColumn(f *Field) (column string, ok bool, err error) {
	name, _, _ := strings.Cut(f.tags.Column, ",")
	if name == "-" {
		return "", false, nil
	}
	base := f.external
	if base == "" {
		base = f.goName
	}
	col, err := g.ColumnName(base, f.tags.Column)
	if err != nil {
		return "", false, err
	}
	return col, true, nil
}
