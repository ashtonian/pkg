package parse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sync"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// SchemaRegistry holds all generated schemas with caching.
type SchemaRegistry struct {
	mu            sync.RWMutex
	filterSchemas map[string]*filter.ResourceSchema
	patchSchemas  map[string]*patch.PatchSchema
	sortSchemas   map[string]*filter.SortSchema
	searchSchemas map[string]*filter.SearchSchema
	generator     *core.SchemaGenerator
}

// NewSchemaRegistry creates a new SchemaRegistry.
func NewSchemaRegistry(opts ...core.GeneratorOption) *SchemaRegistry {
	return &SchemaRegistry{
		filterSchemas: make(map[string]*filter.ResourceSchema),
		patchSchemas:  make(map[string]*patch.PatchSchema),
		sortSchemas:   make(map[string]*filter.SortSchema),
		searchSchemas: make(map[string]*filter.SearchSchema),
		generator:     core.NewSchemaGenerator(opts...),
	}
}

// Register generates and registers all schemas for type T.
func Register[T any](r *SchemaRegistry) error {
	t := reflect.TypeFor[T]() // 1.22+ idiom; correct for interface T (TypeOf(nil-iface) panics)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	filterSchema, err := filter.GenerateFilterSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("registering %s filter: %w", t.Name(), err)
	}
	patchSchema, err := patch.GeneratePatchSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("registering %s patch: %w", t.Name(), err)
	}
	sortSchema, err := filter.GenerateSortSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("registering %s sort: %w", t.Name(), err)
	}
	searchSchema, err := filter.GenerateSearchSchema[T](r.generator)
	if err != nil {
		return fmt.Errorf("registering %s search: %w", t.Name(), err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.filterSchemas[filterSchema.Name] = filterSchema
	r.patchSchemas[patchSchema.Resource] = patchSchema
	if len(sortSchema.Fields) > 0 {
		r.sortSchemas[filterSchema.Name] = sortSchema
	} else {
		delete(r.sortSchemas, filterSchema.Name)
	}
	if searchSchema != nil {
		r.searchSchemas[filterSchema.Name] = searchSchema
	} else {
		delete(r.searchSchemas, filterSchema.Name)
	}

	return nil
}

// RegisterResource registers a resource's schemas under its table: its
// filter schema (declared relations included), patch, sort and search
// schemas, as live as the resource — relations declared after registering
// are served too.
func RegisterResource[T any](r *SchemaRegistry, res *Resource[T]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := res.Table()
	r.filterSchemas[name] = res.FilterSchema()
	r.patchSchemas[name] = res.PatchSchema()
	if ss := res.SortSchema(); ss != nil && len(ss.Fields) > 0 {
		r.sortSchemas[name] = ss
	} else {
		delete(r.sortSchemas, name)
	}
	if ss := res.SearchSchema(); ss != nil {
		r.searchSchemas[name] = ss
	} else {
		delete(r.searchSchemas, name)
	}
}

// MustRegister is like Register but panics on error.
func MustRegister[T any](r *SchemaRegistry) {
	if err := Register[T](r); err != nil {
		panic(fmt.Sprintf("MustRegister: %v", err))
	}
}

// FilterSchema returns the filter schema for the named resource.
func (r *SchemaRegistry) FilterSchema(resource string) (*filter.ResourceSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.filterSchemas[resource]
	return s, ok
}

// PatchSchema returns the patch schema for the named resource.
func (r *SchemaRegistry) PatchSchema(resource string) (*patch.PatchSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.patchSchemas[resource]
	return s, ok
}

// SortSchema returns the sort schema for the named resource.
func (r *SchemaRegistry) SortSchema(resource string) (*filter.SortSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sortSchemas[resource]
	return s, ok
}

// ResourceNames returns all registered resource names.
func (r *SchemaRegistry) ResourceNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]struct{})
	for k := range r.filterSchemas {
		seen[k] = struct{}{}
	}
	for k := range r.patchSchemas {
		seen[k] = struct{}{}
	}
	for k := range r.sortSchemas {
		seen[k] = struct{}{}
	}
	for k := range r.searchSchemas {
		seen[k] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	slices.Sort(names) // deterministic API listing
	return names
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
	Version  int                   `json:"version"`
	Resource string                `json:"resource"`
	Filter   *FilterSchemaResponse `json:"filter,omitempty"`
	Patch    *PatchSchemaResponse  `json:"patch,omitempty"`
	Sort     *SortSchemaResponse   `json:"sort,omitempty"`
	Search   *SearchSchemaResponse `json:"search,omitempty"`
}

// FilterSchemaResponse is the JSON representation of a filter schema.
type FilterSchemaResponse struct {
	Fields map[string]*FilterFieldResponse `json:"fields"`
}

// FilterFieldResponse is the JSON representation of a filter field.
type FilterFieldResponse struct {
	Name        string                  `json:"name"`
	Label       string                  `json:"label,omitempty"`
	Description string                  `json:"description,omitempty"`
	DataType    core.FieldType          `json:"data_type"`
	Operators   []filter.FilterOperator `json:"operators"`
	Indexed     bool                    `json:"indexed,omitempty"`
	Immutable   bool                    `json:"immutable,omitempty"`
	EnumValues  []string                `json:"enum_values,omitempty"`
	Facetable   bool                    `json:"facetable,omitempty"`
	Searchable  bool                    `json:"searchable,omitempty"`
	Includable  bool                    `json:"includable,omitempty"`
	Group       string                  `json:"group,omitempty"`
	Order       int                     `json:"order,omitempty"`
	GeoConfig   *GeoFieldConfigResponse `json:"geo_config,omitempty"`
	// Relation describes a relation field: its kind and, for a declared
	// relation, the target resource whose schema its `$some` / `$none` /
	// `$is` / `$is_not` filters use.
	Relation *RelationResponse `json:"relation,omitempty"`
	// Elements are a JSON array's element paths, which `$some` / `$none`
	// filter on one element.
	Elements map[string]*FilterFieldResponse `json:"elements,omitempty"`
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
}

// PatchPathResponse is the JSON representation of a patchable path: what a
// client needs to judge a value as the server will.
type PatchPathResponse struct {
	AllowedOps []patch.PatchOp `json:"allowed_ops"`
	DataType   core.FieldType  `json:"data_type"`
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
	// Elem is an array path's element type.
	Elem core.FieldType `json:"elem,omitempty"`
	// NumberKind is the Go numeric kind of a number path, or of an array
	// path's elements (int8 … int64, uint8 … uint64, float32, float64).
	NumberKind string `json:"number_kind,omitempty"`
	// Relation reports a relation's membership path: its value is a list
	// of target keys.
	Relation bool `json:"relation,omitempty"`
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
	Param   string   `json:"param"`
}

// SchemaFor builds an independent metadata snapshot for the given resource.
// Changing the response does not change the registered schemas.
func (r *SchemaRegistry) SchemaFor(resource string) *SchemaResponse {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resp := &SchemaResponse{Version: SchemaVersion, Resource: resource}
	found := false

	if fs, ok := r.filterSchemas[resource]; ok {
		found = true
		resp.Filter = filterSchemaToResponse(fs)
	}
	if ps, ok := r.patchSchemas[resource]; ok {
		found = true
		resp.Patch = patchSchemaToResponse(ps)
	}
	if ss, ok := r.sortSchemas[resource]; ok {
		found = true
		resp.Sort = sortSchemaToResponse(ss)
	}
	if ss, ok := r.searchSchemas[resource]; ok {
		found = true
		resp.Search = searchSchemaToResponse(ss)
	}

	if !found {
		return nil
	}
	return resp
}

func filterSchemaToResponse(s *filter.ResourceSchema) *FilterSchemaResponse {
	return &FilterSchemaResponse{Fields: filterFieldsToResponse(s.Fields)}
}

func filterFieldsToResponse(in map[string]*filter.FieldSchema) map[string]*FilterFieldResponse {
	fields := make(map[string]*FilterFieldResponse, len(in))
	for key, f := range in {
		resp := &FilterFieldResponse{
			Name: f.ExternalName, Label: f.Label, Description: f.Description,
			DataType: f.DataType, Operators: slices.Clone(f.AllowedOperators), Indexed: f.Indexed,
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
		fields[key] = resp
	}
	return fields
}

func patchSchemaToResponse(s *patch.PatchSchema) *PatchSchemaResponse {
	paths := make(map[string]*PatchPathResponse, len(s.AllowedPaths))
	for key, p := range s.AllowedPaths {
		resp := &PatchPathResponse{
			AllowedOps: slices.Clone(p.AllowedOps), DataType: p.DataType,
			Nullable: p.Nullable(), EnumValues: slices.Clone(p.EnumValues), Relation: p.Relation != nil,
			MinLength: cloneInt(p.Bounds.MinLen), MaxLength: cloneInt(p.Bounds.MaxLen), Minimum: p.Bounds.Min, Maximum: p.Bounds.Max,
		}
		if p.GoType != nil {
			info := core.TypeOf(p.GoType)
			resp.Elem = info.Elem
			if info.NumberKind != reflect.Invalid {
				resp.NumberKind = info.NumberKind.String()
			}
		}
		paths[key] = resp
	}
	return &PatchSchemaResponse{Paths: paths, MaxOperations: s.MaxOperations}
}

func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	return new(*p)
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
	return &SearchSchemaResponse{Enabled: true, Fields: fields, Param: param}
}

// SchemaHandler returns an http.HandlerFunc that serves schema metadata.
func (r *SchemaRegistry) SchemaHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		resource := req.PathValue("resource")
		if resource == "" {
			names := r.ResourceNames()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"resources": names}) //nolint:errcheck
			return
		}
		resp := r.SchemaFor(resource)
		if resp == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			// The HTTP error body ({code, message, details}), as the
			// HTTP handlers and the TS SDK's ApiError read it.
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"code":    filter.ECodeSchemaNotFound.String(),
				"message": "no schema registered for resource " + resource,
				"details": map[string]any{"resource": resource},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}
}

// SchemaListHandler returns an http.HandlerFunc that lists all schemas.
func (r *SchemaRegistry) SchemaListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		names := r.ResourceNames()
		schemas := make([]*SchemaResponse, 0, len(names))
		for _, name := range names {
			if s := r.SchemaFor(name); s != nil {
				schemas = append(schemas, s)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schemas": schemas}) //nolint:errcheck
	}
}
