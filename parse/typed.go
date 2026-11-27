package parse

import (
	"fmt"
	"net/url"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// generateSchemas builds the parser configuration from opts, and from it
// the schemas T's struct tags declare (generate).
func (r *resourceCore) generateSchemas(opts []core.Option, generate func(*core.SchemaGenerator) (Schemas, error)) error {
	cfg := core.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	err := cfg.Validate()
	if err != nil {
		return fmt.Errorf("parser options: %w", err)
	}
	cfg = cfg.Effective()
	gen := core.NewSchemaGenerator(cfg.GeneratorOpts...)
	s, err := generate(gen)
	if err != nil {
		return err
	}
	r.filterSchema, r.sortSchema, r.searchSchema = s.Filter, s.Sort, s.Search
	r.patchSchema, r.serverPatchSchema = s.Patch, s.ServerPatch
	r.cfg, r.gen, r.codec = cfg, gen, cfg.GetCodec()
	return nil
}

// generateSchemas generates the filter, sort, search and patch schemas T's
// struct tags declare.
func generateSchemas[T any](gen *core.SchemaGenerator) (Schemas, error) {
	var (
		s   Schemas
		err error
	)
	s.Filter, err = filter.GenerateFilterSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating filter schema: %w", err)
	}
	s.Patch, err = patch.GeneratePatchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating patch schema: %w", err)
	}
	s.ServerPatch, err = patch.GenerateServerPatchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating server patch schema: %w", err)
	}
	s.Sort, err = filter.GenerateSortSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("generating sort schema: %w", err)
	}
	s.Search, err = filter.GenerateSearchSchema[T](gen)
	if err != nil {
		return Schemas{}, fmt.Errorf("creating search schema: %w", err)
	}
	return s, nil
}

// ParseFilter parses a filter query string.
func (r *resourceCore) ParseFilter(query string) (*filter.ParsedFilter, error) {
	return r.parseFilter(query, r.cfg)
}

func (r *resourceCore) parseFilter(query string, cfg *core.ParserConfig) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(r.filterSchema, cfg)
	ast, err := fp.Parse(query)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilterFor(r.filterSchema, ast), nil
}

// ParseRequestFilter parses a list request's filter: the JSON filter
// parameter (filter.ParamFilter) and the flat field parameters
// (status=active&price_gte=10, see filter.QueryParamParser), ANDed and bounded
// by the parser's limits as one filter. A parameter that is neither a list
// parameter (filter.Param*), a name filter.QueryParamAccept lists, nor a
// filterable field is E2071; filter.QueryParamLenient ignores it instead. An
// accepted name the parser reads itself, a list parameter or a flat filter
// on a field, is refused with a plain error (filter.CheckAcceptedParam). A
// list parameter given more than once is E2067, as in ParseList. The search
// parameter the search schema advertises (core.WithSearchParam) is a list
// parameter too. The parser's own configuration bounds the flat parameters,
// whatever opts say.
func (r *resourceCore) ParseRequestFilter(q url.Values, opts ...filter.QueryParamOption) (*filter.ParsedFilter, error) {
	err := r.listParamsOnce(q)
	if err != nil {
		return nil, err
	}
	now := r.cfg.Now()
	fp := filter.NewFilterParser(r.filterSchema, r.cfg).At(now)
	jsonAST, err := fp.Parse(q.Get(filter.ParamFilter))
	if err != nil {
		return nil, err
	}
	flatOpts := append(slices.Clone(opts), filter.QueryParamConfig(r.cfg))
	if r.searchParam != "" {
		flatOpts = append(flatOpts, filter.QueryParamAccept(r.searchParam))
	}
	qp, err := filter.NewQueryParamParser(r.filterSchema, flatOpts...)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", r.table, err)
	}
	flatAST, err := qp.At(now).Parse(q)
	if err != nil {
		return nil, err
	}
	ast, err := fp.And(jsonAST, flatAST)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilterFor(r.filterSchema, ast), nil
}

// ParseFilterMap parses a pre-decoded filter map into a ParsedFilter, skipping
// the JSON marshal/unmarshal roundtrip used by ParseFilter. Prefer this in
// adapter and service code that constructs filters from typed values:
//
//	parsed, err := sessions.ParseFilterMap(map[string]any{"user_id": userID})
//
// instead of
//
//	body, _ := json.Marshal(map[string]any{"user_id": userID})
//	parsed, err := sessions.ParseFilter(string(body))
func (r *resourceCore) ParseFilterMap(m map[string]any) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(r.filterSchema, r.cfg)
	ast, err := fp.ParseMap(m)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilterFor(r.filterSchema, ast), nil
}

// ParsePatch validates and parses a JSON Patch document.
func (r *resourceCore) ParsePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(r.patchSchema, r.cfg).Parse(body)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ParseMergePatch validates and parses a JSON Merge Patch (RFC 7396) body.
func (r *resourceCore) ParseMergePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatch(body, r.patchSchema, r.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// MergePatchMap validates a JSON Merge Patch given as a map — decoded
// elsewhere, or built in code — exactly as ParseMergePatch validates its
// body: a nested map merges, any other value (a Go value of the path's type
// included) replaces, nil removes. No JSON round trip.
func (r *resourceCore) MergePatchMap(m map[string]any, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatchMap(m, r.patchSchema, r.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ServerMergePatchMap is MergePatchMap for a write the server makes, which
// may also set the `parse:"server"` fields (ParseServerMergePatch).
func (r *resourceCore) ServerMergePatchMap(m map[string]any) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatchMap(m, r.serverPatchSchema, r.cfg)
}

// ValidateOperations validates JSON Patch operations built in code against
// the patch schema, as ParsePatch validates a body: paths, operations,
// value types, limits. Use it instead of assembling a ParsedPatch by hand.
func (r *resourceCore) ValidateOperations(ops []patch.RawPatchOp, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(r.patchSchema, r.cfg).ParseOps(ops)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ValidateServerOperations is ValidateOperations for a write the server
// makes, against the server patch schema.
func (r *resourceCore) ValidateServerOperations(ops []patch.RawPatchOp) (*patch.ParsedPatch, error) {
	return patch.NewParser(r.serverPatchSchema, r.cfg).ParseOps(ops)
}

func applyPatchOptions(parsed *patch.ParsedPatch, opts []PatchOption) (*patch.ParsedPatch, error) {
	var cfg patchOptConfig
	for _, o := range opts {
		o(&cfg)
	}
	if err := patch.ApplyPatchPolicyConstraints(parsed, cfg.policy, cfg.subject); err != nil {
		return nil, err
	}
	return parsed, nil
}

// FilterSchema returns a copy of the filter schema: changing it changes
// nothing the resource parses. Customize the schemas when the resource is
// built (CustomizeSchemas).
func (r *resourceCore) FilterSchema() *filter.ResourceSchema {
	return r.filterSchema.Clone()
}

// PatchSchema returns a copy of the patch schema; see FilterSchema.
func (r *resourceCore) PatchSchema() *patch.PatchSchema {
	return r.patchSchema.Clone()
}

// ServerPatchSchema returns a copy of the schema of the server's own writes:
// the patch schema's paths and the `parse:"server"` fields. Build a patch
// against it only for a write the server makes, never from a request.
func (r *resourceCore) ServerPatchSchema() *patch.PatchSchema {
	return r.serverPatchSchema.Clone()
}

// ParseServerMergePatch parses a JSON Merge Patch the server itself writes:
// it may also set the `parse:"server"` fields — columns only the server
// sets, such as a rotated credential's fingerprint. Never pass it a request
// body; ParseMergePatch is the request path. The result applies through the
// same repositories, ApplyValidatedPatch included.
func (r *resourceCore) ParseServerMergePatch(body []byte) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatch(body, r.serverPatchSchema, r.cfg)
}

// SortSchema returns a copy of the sort schema; see FilterSchema.
func (r *resourceCore) SortSchema() *filter.SortSchema {
	return r.sortSchema.Clone()
}

// SearchSchema returns a copy of the search schema, nil when T has no
// search fields; see FilterSchema.
func (r *resourceCore) SearchSchema() *filter.SearchSchema {
	return r.searchSchema.Clone()
}

// ParseSort parses a sort parameter string.
func (r *resourceCore) ParseSort(sortParam string) (*filter.SortResult, error) {
	return filter.ParseSort(r.sortSchema, sortParam)
}

// ListQuery returns a ListQuery builder for the provider's dialect,
// pre-loaded with this type's filter, sort, and search schemas and the
// parser's config, so ListFilter input is held to the same limits, codec
// and clock as ParseFilter. When the type declares `parse:"...,search"`
// fields, a ListSearch query composes the dialect's search predicate. A
// computed column (ReadExpr) compiles as its expression. The error is
// filter.NewListQuery's, for a key the list cannot end its keyset on.
func (r *resourceCore) ListQuery() (*filter.ListQuery, error) {
	return newListQuery(r.filterSchema, r.sortSchema, r.searchSchema, r.dialect, r.cfg)
}

func newListQuery(fs *filter.ResourceSchema, ss *filter.SortSchema, search *filter.SearchSchema, d filter.Dialect, cfg *core.ParserConfig) (*filter.ListQuery, error) {
	lq, err := filter.NewListQuery(fs, ss, d)
	if err != nil {
		return nil, err
	}
	return lq.WithFilterConfig(cfg).WithSearchSchema(search), nil
}

// ApplyValidatedPatch applies an already-parsed patch to target and returns
// the patched value, never mutating target (patch.Applier). A patch this
// resource did not parse, by hand or against another schema, is ErrForeign.
func (r *Resource[T]) ApplyValidatedPatch(parsed *patch.ParsedPatch, target T) (T, error) {
	if err := r.ownsPatch(parsed, "patch"); err != nil {
		var zero T
		return zero, err
	}
	return r.applier.Apply(parsed, target)
}

// ownsPatch refuses a patch neither of the resource's patch schemas
// validated. A WithPaths view shares its base's patch schemas, so each
// accepts the other's patches.
func (r *resourceCore) ownsPatch(p *patch.ParsedPatch, role string) error {
	if p.ParsedFor(r.patchSchema) || p.ParsedFor(r.serverPatchSchema) {
		return nil
	}
	return fmt.Errorf("parse: %s: %s: %w", r.table, role, ErrForeign)
}

// ApplyPatch validates a JSON Patch document and applies it to a struct.
func (r *Resource[T]) ApplyPatch(body []byte, target T, opts ...PatchOption) (T, error) {
	var zero T
	parsed, err := r.ParsePatch(body, opts...)
	if err != nil {
		return zero, err
	}
	return r.ApplyValidatedPatch(parsed, target)
}

// PatchOption configures optional behavior for patch parsing.
type PatchOption func(*patchOptConfig)

type patchOptConfig struct {
	policy  *patch.PatchPolicyConstraints
	subject *patch.PolicySubject
}

// WithPatchPolicy sets policy constraints for patch validation.
func WithPatchPolicy(pc *patch.PatchPolicyConstraints, subject *patch.PolicySubject) PatchOption {
	return func(c *patchOptConfig) { c.policy = pc; c.subject = subject }
}
