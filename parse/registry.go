package parse

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// ErrRegistered is a registration under a name the registry already holds.
var ErrRegistered = errors.New("already registered")

// SchemaRegistry is the schema document's source: the schemas of each
// registered name, served by SchemaFor and the accessors. A name is
// registered once. It is safe for concurrent use.
type SchemaRegistry struct {
	mu        sync.RWMutex
	entries   map[string]registryEntry
	generator *core.SchemaGenerator
}

// registryEntry is one name's schemas; sort is nil without sort fields and
// search nil without search fields.
type registryEntry struct {
	filter *filter.ResourceSchema
	patch  *patch.PatchSchema
	sort   *filter.SortSchema
	search *filter.SearchSchema
}

// NewSchemaRegistry creates a registry; opts configure the generator
// Register[T] builds schemas with.
func NewSchemaRegistry(opts ...core.GeneratorOption) *SchemaRegistry {
	return &SchemaRegistry{
		entries:   make(map[string]registryEntry),
		generator: core.NewSchemaGenerator(opts...),
	}
}

// Register generates T's filter, patch, sort and search schemas with the
// registry's generator and registers them under T's table name, for a type
// no Resource serves: schema-only discovery. It never mirrors a Resource:
// the schemas have no relations, read set, computed columns or the
// resource's options, so they can describe a grammar the resource does not
// parse. Register a Resource with RegisterResource. A name already
// registered is ErrRegistered, and the registry is unchanged.
func Register[T any](r *SchemaRegistry) error {
	// TypeFor, not TypeOf of a zero T: an interface T's zero value is nil,
	// which has no type.
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	filterSchema, err := filter.GenerateFilterSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("parse: registering %s: filter schema: %w", gowalker.TypeText(t), err)
	}
	patchSchema, err := patch.GeneratePatchSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("parse: registering %s: patch schema: %w", gowalker.TypeText(t), err)
	}
	sortSchema, err := filter.GenerateSortSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("parse: registering %s: sort schema: %w", gowalker.TypeText(t), err)
	}
	searchSchema, err := filter.GenerateSearchSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("parse: registering %s: search schema: %w", gowalker.TypeText(t), err)
	}
	return r.add(filterSchema.Name, filterSchema, patchSchema, sortSchema, searchSchema)
}

// RegisterResource registers res's schemas under its table: its filter
// schema (declared relations included), patch, sort and search schemas, as
// live as the resource — relations declared after registering are served
// too. A table already registered, by another resource over it or by
// Register, is ErrRegistered, and the registry is unchanged: register the
// one resource whose grammar discovery describes. A WithPaths view, a value
// for one request, is not registered.
func RegisterResource[T any](r *SchemaRegistry, res *Resource[T]) error {
	if res == nil {
		return errors.New("parse: registering a nil resource")
	}
	if res.view {
		return fmt.Errorf("parse: %s: a WithPaths view is a value for one request and is not registered", res.table)
	}
	return r.add(res.table, res.filterSchema, res.patchSchema, res.sortSchema, res.searchSchema)
}

func (r *SchemaRegistry) add(name string, fs *filter.ResourceSchema, ps *patch.PatchSchema, ss *filter.SortSchema, search *filter.SearchSchema) error {
	if ss != nil && len(ss.Fields) == 0 {
		ss = nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.entries[name]; taken {
		return fmt.Errorf("parse: %s: %w", name, ErrRegistered)
	}
	r.entries[name] = registryEntry{filter: fs, patch: ps, sort: ss, search: search}
	return nil
}

// MustRegister is like Register but panics with its error.
func MustRegister[T any](r *SchemaRegistry) {
	if err := Register[T](r); err != nil {
		panic(err)
	}
}

// MustRegisterResource is like RegisterResource but panics with its error.
func MustRegisterResource[T any](r *SchemaRegistry, res *Resource[T]) {
	if err := RegisterResource(r, res); err != nil {
		panic(err)
	}
}

func (r *SchemaRegistry) entry(resource string) (registryEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[resource]
	return e, ok
}

// FilterSchema returns a copy of the filter schema for the named resource:
// changing it changes nothing a registered resource parses.
func (r *SchemaRegistry) FilterSchema(resource string) (*filter.ResourceSchema, bool) {
	e, ok := r.entry(resource)
	return e.filter.Clone(), ok && e.filter != nil
}

// PatchSchema returns a copy of the patch schema for the named resource.
func (r *SchemaRegistry) PatchSchema(resource string) (*patch.PatchSchema, bool) {
	e, ok := r.entry(resource)
	return e.patch.Clone(), ok && e.patch != nil
}

// SortSchema returns a copy of the sort schema for the named resource;
// false for one without sort fields.
func (r *SchemaRegistry) SortSchema(resource string) (*filter.SortSchema, bool) {
	e, ok := r.entry(resource)
	return e.sort.Clone(), ok && e.sort != nil
}

// SearchSchema returns a copy of the search schema for the named resource;
// false for one without search fields.
func (r *SchemaRegistry) SearchSchema(resource string) (*filter.SearchSchema, bool) {
	e, ok := r.entry(resource)
	return e.search.Clone(), ok && e.search != nil
}

// ResourceNames returns all registered resource names, sorted.
func (r *SchemaRegistry) ResourceNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Sorted(maps.Keys(r.entries))
}

// --- Schema Response Types ---
//
// These *Response structs are the schema document: the JSON a
// schema-introspection endpoint serves (GET /api/{resource}/schema, see
// examples/server). Clients validate requests against it and generators read
// filter parameters from it, so it is a versioned format, pinned by a golden
// snapshot (testdata/schema_document.json). parse builds it with SchemaFor;
// the consumer's HTTP layer serializes it.

// SchemaVersion is the schema document's format version. It changes when a
// member is removed or renamed or its meaning changes; adding a member does
// not change it. A client refuses a version it does not know.
const SchemaVersion = 1

// SchemaResponse is the schema document of one resource.
type SchemaResponse struct {
	// Version is SchemaVersion.
	Version  int    `json:"version"`
	Resource string `json:"resource"`
	// Key are the key columns, in key order: together they name one row, and
	// every list ends its keyset with them. Absent for a schema registered
	// without a key.
	Key    []string              `json:"key,omitempty"`
	Filter *FilterSchemaResponse `json:"filter,omitempty"`
	Patch  *PatchSchemaResponse  `json:"patch,omitempty"`
	Sort   *SortSchemaResponse   `json:"sort,omitempty"`
	Search *SearchSchemaResponse `json:"search,omitempty"`
}

// FilterSchemaResponse is the JSON representation of a filter schema.
type FilterSchemaResponse struct {
	// Fields are the names a request filter may name
	// (filter.ResourceSchema.RequestFields): the filter fields and the key.
	Fields map[string]*FilterFieldResponse `json:"fields"`
}

// FilterFieldResponse is the JSON representation of a filter field.
type FilterFieldResponse struct {
	Name        string         `json:"name"`
	Label       string         `json:"label,omitempty"`
	Description string         `json:"description,omitempty"`
	DataType    core.FieldType `json:"data_type"`
	// Wire is "string" for a bool, integer or float field whose comparison
	// and set operands are JSON strings holding the value ("42"), as the
	// field reads in responses (json:",string"); absent, operands take the
	// data type's own JSON form.
	Wire core.Wire `json:"wire,omitempty"`
	// Operators are the operators the parser takes on the field
	// (filter.FieldSchema.EffectiveOperators): those it declares, then the
	// NULL tests a declared one implies.
	Operators  []filter.FilterOperator `json:"operators"`
	Indexed    bool                    `json:"indexed,omitempty"`
	Immutable  bool                    `json:"immutable,omitempty"`
	EnumValues []string                `json:"enum_values,omitempty"`
	Facetable  bool                    `json:"facetable,omitempty"`
	Searchable bool                    `json:"searchable,omitempty"`
	Includable bool                    `json:"includable,omitempty"`
	Group      string                  `json:"group,omitempty"`
	Order      int                     `json:"order,omitempty"`
	GeoConfig  *GeoFieldConfigResponse `json:"geo_config,omitempty"`
	// Relation describes a relation field: its kind and, for a declared
	// relation, the target resource whose schema its `$some` / `$none` /
	// `$is` / `$is_not` filters use.
	Relation *RelationResponse `json:"relation,omitempty"`
	// Elements are a JSON array's element paths, which `$some` / `$none`
	// filter on one element.
	Elements map[string]*FilterFieldResponse `json:"elements,omitempty"`
	// Elem is an array's element type.
	Elem core.FieldType `json:"elem,omitempty"`
	// Element is a scalar array's element (an array column's, or an array
	// path's), which `$some` / `$none` conditions compare with its
	// operators.
	Element *FilterFieldResponse `json:"element,omitempty"`
}

// RelationResponse is the JSON representation of a relation field.
type RelationResponse struct {
	// Kind is many_to_many, has_many or belongs_to.
	Kind string `json:"kind"`
	// Target is the target resource's name; empty for a pair table
	// compared by key alone.
	Target string `json:"target,omitempty"`
}

// GeoFieldConfigResponse is the JSON representation of geo field metadata.
type GeoFieldConfigResponse struct {
	StorageType string `json:"storage_type"`
	SRID        int    `json:"srid"`
}

// PatchSchemaResponse is the JSON representation of a patch schema.
type PatchSchemaResponse struct {
	Paths         map[string]*PatchPathResponse `json:"paths"`
	MaxOperations int                           `json:"max_operations"`
	// MaxRelationValues is the most keys one operation on a relation path
	// names, repeats counted once (E2119); 0 is unlimited.
	MaxRelationValues int `json:"max_relation_values"`
}

// PatchPathResponse is the JSON representation of a patchable path: what a
// client needs to judge a value as the server will.
type PatchPathResponse struct {
	AllowedOps []patch.PatchOp `json:"allowed_ops"`
	DataType   core.FieldType  `json:"data_type"`
	// Wire is "string" for a path whose value is a JSON string holding a
	// bool, integer or float ("42"), as the field reads in responses
	// (json:",string"); absent, values take the data type's own JSON form.
	Wire core.Wire `json:"wire,omitempty"`
	// Nullable reports that the path accepts null (remove, or clear).
	Nullable bool `json:"nullable,omitempty"`
	// EnumValues is the path's allowlist.
	EnumValues []string `json:"enum_values,omitempty"`
	// MinLength and MaxLength are the fewest and most characters (Unicode
	// code points) of a string path, or of each element of an array path.
	MinLength *int `json:"min_length,omitempty"`
	MaxLength *int `json:"max_length,omitempty"`
	// Minimum and Maximum are the inclusive bounds of a number path, or of
	// each element of an array path, as exact decimal text.
	Minimum string `json:"minimum,omitempty"`
	Maximum string `json:"maximum,omitempty"`
	// MinItems and MaxItems are the fewest and most elements of an array
	// path.
	MinItems *int `json:"min_items,omitempty"`
	MaxItems *int `json:"max_items,omitempty"`
	// Elem is an array path's element type.
	Elem core.FieldType `json:"elem,omitempty"`
	// NumberKind is the Go numeric kind of a number path, or of an array
	// path's elements (int8 … int64, uint8 … uint64, float32, float64).
	NumberKind string `json:"number_kind,omitempty"`
	// Relation reports a relation's membership path: its value is a list
	// of target keys.
	Relation bool `json:"relation,omitempty"`
	// Key are the target key's columns, in key order, of a membership
	// path whose target key has several: each key is a JSON array of a
	// value per column. A one-column key's type is Elem's.
	Key []*RelationKeyResponse `json:"key,omitempty"`
}

// RelationKeyResponse is a target key column of a membership path: the
// type and wire form of its value in each key.
type RelationKeyResponse struct {
	DataType core.FieldType `json:"data_type"`
	// Wire is "string" for an integer column whose value is a JSON string
	// holding it, as the target's field reads in responses
	// (json:",string").
	Wire       core.Wire `json:"wire,omitempty"`
	NumberKind string    `json:"number_kind,omitempty"`
}

// SortSchemaResponse is the JSON representation of a sort schema.
type SortSchemaResponse struct {
	Fields       map[string]*SortFieldResponse `json:"fields"`
	DefaultField string                        `json:"default_field,omitempty"`
	DefaultDir   filter.SortDirection          `json:"default_dir,omitempty"`
	MaxFields    int                           `json:"max_fields"`
}

// SortFieldResponse is the JSON representation of a sortable field.
type SortFieldResponse struct {
	Indexed bool `json:"indexed,omitempty"`
}

// SearchSchemaResponse is the JSON representation of a search schema.
type SearchSchemaResponse struct {
	Enabled bool     `json:"enabled"`
	Fields  []string `json:"fields"`
	// Param is the query parameter a GET list takes the search in
	// (core.WithSearchParam). Resource.ParseList reads it, and reads
	// filter.ParamSearch as well, the name every bundled client sends.
	Param string `json:"param"`
	// Member is the member a list body takes the search in
	// (Resource.ParseListBody): filter.ParamSearch, whatever Param is.
	Member string `json:"member"`
}

// SchemaFor builds an independent metadata snapshot for the given resource.
// Changing the response does not change the registered schemas.
func (r *SchemaRegistry) SchemaFor(resource string) *SchemaResponse {
	e, ok := r.entry(resource)
	if !ok {
		return nil
	}
	resp := &SchemaResponse{Version: SchemaVersion, Resource: resource}
	if e.filter != nil {
		resp.Filter = filterSchemaToResponse(e.filter)
		resp.Key = slices.Clone(e.filter.PrimaryKey)
	}
	if e.patch != nil {
		resp.Patch = patchSchemaToResponse(e.patch)
	}
	if e.sort != nil {
		resp.Sort = sortSchemaToResponse(e.sort)
	}
	if e.search != nil {
		resp.Search = searchSchemaToResponse(e.search)
	}
	return resp
}

// filterSchemaToResponse lists the fields a request filter may name
// (RequestFields): its filter fields and the key, so a client validating
// against the document accepts what the parser accepts.
func filterSchemaToResponse(s *filter.ResourceSchema) *FilterSchemaResponse {
	fields := make(map[string]*FilterFieldResponse, len(s.Fields)+1)
	for name, f := range s.RequestFields() {
		fields[name] = filterFieldToResponse(f)
	}
	return &FilterSchemaResponse{Fields: fields}
}

func filterFieldsToResponse(in map[string]*filter.FieldSchema) map[string]*FilterFieldResponse {
	fields := make(map[string]*FilterFieldResponse, len(in))
	for key, f := range in {
		fields[key] = filterFieldToResponse(f)
	}
	return fields
}

func filterFieldToResponse(f *filter.FieldSchema) *FilterFieldResponse {
	resp := &FilterFieldResponse{
		Name: f.ExternalName, Label: f.Label, Description: f.Description,
		DataType: f.DataType, Wire: f.Wire, Operators: f.EffectiveOperators(), Indexed: f.Indexed,
		Immutable: f.Immutable, EnumValues: slices.Clone(f.EnumValues), Facetable: f.Facetable,
		Searchable: f.Searchable, Includable: f.Includable, Group: f.Group, Order: f.Order,
	}
	if f.GeoConfig != nil {
		resp.GeoConfig = &GeoFieldConfigResponse{
			StorageType: string(f.GeoConfig.StorageType),
			SRID:        f.GeoConfig.SRID,
		}
	}
	if rel := f.Relation; rel != nil {
		kind := string(rel.Kind)
		if kind == "" {
			kind = "many_to_many"
		}
		resp.Relation = &RelationResponse{Kind: kind}
		if f.Related != nil {
			resp.Relation.Target = f.Related.Name
		}
	}
	if f.Elements != nil {
		resp.Elements = filterFieldsToResponse(f.Elements.Fields)
	}
	resp.Elem = f.Elem
	if f.Element != nil {
		resp.Element = filterFieldToResponse(f.Element)
	}
	return resp
}

func patchSchemaToResponse(s *patch.PatchSchema) *PatchSchemaResponse {
	paths := map[string]*PatchPathResponse{}
	for key, p := range s.Paths() {
		bounds := p.Bounds.Clone()
		resp := &PatchPathResponse{
			AllowedOps: slices.Clone(p.AllowedOps), DataType: p.DataType, Wire: p.Wire,
			Nullable: p.Nullable(), EnumValues: slices.Clone(p.EnumValues), Relation: p.Relation != nil,
			MinLength: bounds.MinLen, MaxLength: bounds.MaxLen, Minimum: bounds.Min, Maximum: bounds.Max,
			MinItems: bounds.MinItems, MaxItems: bounds.MaxItems,
		}
		if p.GoType != nil {
			info := core.TypeOf(p.GoType)
			resp.Elem = info.Elem
			if info.NumberKind != reflect.Invalid {
				resp.NumberKind = info.NumberKind.String()
			}
		}
		for _, k := range p.RelationKey {
			info := core.TypeOf(k.Type)
			column := &RelationKeyResponse{DataType: info.Type, Wire: k.Wire}
			if info.NumberKind != reflect.Invalid {
				column.NumberKind = info.NumberKind.String()
			}
			resp.Key = append(resp.Key, column)
		}
		paths[key] = resp
	}
	return &PatchSchemaResponse{Paths: paths, MaxOperations: s.MaxOperations, MaxRelationValues: s.MaxRelationValues}
}

func sortSchemaToResponse(s *filter.SortSchema) *SortSchemaResponse {
	fields := make(map[string]*SortFieldResponse, len(s.Fields))
	for key, f := range s.Fields {
		fields[key] = &SortFieldResponse{Indexed: f.Indexed}
	}
	return &SortSchemaResponse{
		Fields: fields, DefaultField: s.DefaultField, DefaultDir: s.DefaultDir, MaxFields: s.MaxFields,
	}
}

func searchSchemaToResponse(s *filter.SearchSchema) *SearchSchemaResponse {
	fields := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		fields[i] = f.Name
	}
	param := s.Param
	if param == "" {
		param = filter.ParamSearch // hand-built schema without a Param; the generated default
	}
	resp := &SearchSchemaResponse{Enabled: true, Fields: fields, Param: param, Member: filter.ParamSearch}
	return resp
}
