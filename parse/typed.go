package parse

import (
	"fmt"
	"net/url"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// typedParser combines filter, patch, sort, and search parsing for a resource type.
type typedParser[T any] struct {
	filterSchema *filter.ResourceSchema
	patchSchema  *patch.PatchSchema
	// serverPatchSchema admits the server's own writes: patchSchema's paths
	// and the `parse:"server"` fields (ParseServerMergePatch).
	serverPatchSchema *patch.PatchSchema
	// applier applies either schema's patches to a T.
	applier      *patch.Applier[T]
	sortSchema   *filter.SortSchema
	codec        core.Codec
	searchSchema *filter.SearchSchema

	// cfg is shared by every sub-parser, so limits, codec and clock apply
	// consistently through the resource API.
	cfg *core.ParserConfig
	gen *core.SchemaGenerator
}

// newTypedParser creates a typedParser with filter, patch, sort, and search capabilities.
func newTypedParser[T any](opts ...core.Option) (*typedParser[T], error) {
	cfg := core.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	gen := core.NewSchemaGenerator(cfg.GeneratorOpts...)

	filterSchema, err := filter.GenerateFilterSchema[T](gen)
	if err != nil {
		return nil, fmt.Errorf("creating filter schema: %w", err)
	}

	patchSchema, err := patch.GeneratePatchSchema[T](gen)
	if err != nil {
		return nil, fmt.Errorf("creating patch schema: %w", err)
	}

	serverPatchSchema, err := patch.GenerateServerPatchSchema[T](gen)
	if err != nil {
		return nil, fmt.Errorf("creating server patch schema: %w", err)
	}

	sortSchema, err := filter.GenerateSortSchema[T](gen)
	if err != nil {
		return nil, fmt.Errorf("generating sort schema: %w", err)
	}

	searchSchema, err := filter.GenerateSearchSchema[T](gen)
	if err != nil {
		return nil, fmt.Errorf("creating search schema: %w", err)
	}

	applier, err := patch.NewApplier[T](gen, cfg)
	if err != nil {
		return nil, err
	}

	p := &typedParser[T]{
		filterSchema:      filterSchema,
		patchSchema:       patchSchema,
		serverPatchSchema: serverPatchSchema,
		applier:           applier,
		sortSchema:        sortSchema,
		codec:             cfg.GetCodec(),
		searchSchema:      searchSchema,
		cfg:               cfg,
		gen:               gen,
	}
	return p, nil
}

// ParseFilter parses a filter query string.
func (p *typedParser[T]) ParseFilter(query string) (*filter.ParsedFilter, error) {
	return p.parseFilter(query, p.cfg)
}

func (p *typedParser[T]) parseFilter(query string, cfg *core.ParserConfig) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(p.filterSchema, cfg)
	ast, err := fp.Parse(query)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilter(ast), nil
}

// ParseRequestFilter parses a list request's filter: the JSON filter
// parameter (filter.ParamFilter) and the flat field parameters
// (status=active&price_gte=10, see filter.QueryParamParser), ANDed and bounded
// by the parser's limits as one filter. A parameter that is neither a list
// parameter (filter.Param*), a name filter.QueryParamAccept lists, nor a
// filterable field is E2071; filter.QueryParamLenient ignores it instead. The
// parser's own configuration bounds the flat parameters, whatever opts say.
func (p *typedParser[T]) ParseRequestFilter(q url.Values, opts ...filter.QueryParamOption) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(p.filterSchema, p.cfg)
	jsonAST, err := fp.Parse(q.Get(filter.ParamFilter))
	if err != nil {
		return nil, err
	}
	flatAST, err := filter.NewQueryParamParser(p.filterSchema, append(slices.Clone(opts), filter.QueryParamConfig(p.cfg))...).Parse(q)
	if err != nil {
		return nil, err
	}
	ast, err := fp.And(jsonAST, flatAST)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilter(ast), nil
}

// ParseFilterMap parses a pre-decoded filter map into a ParsedFilter, skipping
// the JSON marshal/unmarshal roundtrip used by ParseFilter. Prefer this in
// adapter and service code that constructs filters from typed values:
//
//	parsed, err := SessionParser.ParseFilterMap(map[string]any{"user_id": userID})
//
// instead of
//
//	body, _ := json.Marshal(map[string]any{"user_id": userID})
//	parsed, err := SessionParser.ParseFilter(string(body))
func (p *typedParser[T]) ParseFilterMap(m map[string]any) (*filter.ParsedFilter, error) {
	fp := filter.NewFilterParser(p.filterSchema, p.cfg)
	ast, err := fp.ParseMap(m)
	if err != nil {
		return nil, err
	}
	return filter.NewParsedFilter(ast), nil
}

// ParsePatch validates and parses a JSON Patch document.
func (p *typedParser[T]) ParsePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(p.patchSchema, p.cfg).Parse(body)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ParseMergePatch validates and parses a JSON Merge Patch (RFC 7396) body.
func (p *typedParser[T]) ParseMergePatch(body []byte, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatch(body, p.patchSchema, p.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// MergePatchMap validates a JSON Merge Patch given as a map — decoded
// elsewhere, or built in code — exactly as ParseMergePatch validates its
// body: a nested map merges, any other value (a Go value of the path's type
// included) replaces, nil removes. No JSON round trip.
func (p *typedParser[T]) MergePatchMap(m map[string]any, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.ParseMergePatchMap(m, p.patchSchema, p.cfg)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ServerMergePatchMap is MergePatchMap for a write the server makes, which
// may also set the `parse:"server"` fields (ParseServerMergePatch).
func (p *typedParser[T]) ServerMergePatchMap(m map[string]any) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatchMap(m, p.serverPatchSchema, p.cfg)
}

// ValidateOperations validates JSON Patch operations built in code against
// the patch schema, as ParsePatch validates a body: paths, operations,
// value types, limits. Use it instead of assembling a ParsedPatch by hand.
func (p *typedParser[T]) ValidateOperations(ops []patch.RawPatchOp, opts ...PatchOption) (*patch.ParsedPatch, error) {
	parsed, err := patch.NewParser(p.patchSchema, p.cfg).ParseOps(ops)
	if err != nil {
		return nil, err
	}
	return applyPatchOptions(parsed, opts)
}

// ValidateServerOperations is ValidateOperations for a write the server
// makes, against the server patch schema.
func (p *typedParser[T]) ValidateServerOperations(ops []patch.RawPatchOp) (*patch.ParsedPatch, error) {
	return patch.NewParser(p.serverPatchSchema, p.cfg).ParseOps(ops)
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

// FilterSchema returns the filter schema for this type.
func (p *typedParser[T]) FilterSchema() *filter.ResourceSchema {
	return p.filterSchema
}

// PatchSchema returns the patch schema for this type.
func (p *typedParser[T]) PatchSchema() *patch.PatchSchema {
	return p.patchSchema
}

// ServerPatchSchema returns the schema of the server's own writes: the
// patch schema's paths and the `parse:"server"` fields. Build a patch
// against it only for a write the server makes, never from a request.
func (p *typedParser[T]) ServerPatchSchema() *patch.PatchSchema {
	return p.serverPatchSchema
}

// ParseServerMergePatch parses a JSON Merge Patch the server itself writes:
// it may also set the `parse:"server"` fields — columns only the server
// sets, such as a rotated credential's fingerprint. Never pass it a request
// body; ParseMergePatch is the request path. The result applies through the
// same repositories, ApplyValidatedPatch included.
func (p *typedParser[T]) ParseServerMergePatch(body []byte) (*patch.ParsedPatch, error) {
	return patch.ParseMergePatch(body, p.serverPatchSchema, p.cfg)
}

// SortSchema returns the sort schema for this type.
func (p *typedParser[T]) SortSchema() *filter.SortSchema {
	return p.sortSchema
}

// SearchSchema returns the search schema for this type.
func (p *typedParser[T]) SearchSchema() *filter.SearchSchema {
	return p.searchSchema
}

// ParseSort parses a sort parameter string.
func (p *typedParser[T]) ParseSort(sortParam string) (*filter.SortResult, error) {
	return filter.ParseSort(p.sortSchema, sortParam)
}

// ListQuery returns a ListQuery builder pre-loaded with this type's filter,
// sort, and search schemas and the parser's config, so ListFilter input is
// held to the same limits, codec and clock as ParseFilter. When the type declares `parse:"...,search"` fields, a
// ListSearch query composes a dialect-aware search predicate. The schemas
// are dialect-neutral: a computed column (ReadExpr) compiles as the column
// it names, so a resource with computed columns lists through BuildList.
func (p *typedParser[T]) ListQuery(dialect filter.Dialect) *filter.ListQuery {
	return newListQuery(p.filterSchema, p.sortSchema, p.searchSchema, dialect, p.cfg)
}

func newListQuery(fs *filter.ResourceSchema, ss *filter.SortSchema, search *filter.SearchSchema, d filter.Dialect, cfg *core.ParserConfig) *filter.ListQuery {
	return filter.NewListQuery(fs, ss, d).WithFilterConfig(cfg).WithSearchSchema(search)
}

// ApplyValidatedPatch applies an already-parsed patch to target and returns
// the patched value, never mutating target (patch.Applier). A patch no
// parser produced is patch.ErrUnparsed.
func (p *typedParser[T]) ApplyValidatedPatch(parsed *patch.ParsedPatch, target T) (T, error) {
	return p.applier.Apply(parsed, target)
}

// ApplyPatch validates a JSON Patch document and applies it to a struct.
func (p *typedParser[T]) ApplyPatch(body []byte, target T, opts ...PatchOption) (T, error) {
	var zero T
	parsed, err := p.ParsePatch(body, opts...)
	if err != nil {
		return zero, err
	}
	return p.ApplyValidatedPatch(parsed, target)
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
