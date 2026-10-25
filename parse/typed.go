package parse

import (
	"encoding"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/ashtonian/pkg/jsonvalue"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
	"github.com/ashtonian/pkg/parse/patch"
)

// typedParser combines filter, patch, sort, and search parsing for a resource type.
type typedParser[T any] struct {
	filterSchema *filter.ResourceSchema
	patchSchema  *patch.PatchSchema
	// serverPatchSchema admits the server's own writes: patchSchema's paths
	// and the `parse:"server"` fields (ParseServerMergePatch).
	serverPatchSchema *patch.PatchSchema
	// hiddenPaths are the server fields the field-name tag hides, which only
	// the direct apply path can set.
	hiddenPaths  map[string]bool
	sortSchema   *filter.SortSchema
	codec        core.Codec
	searchSchema *filter.SearchSchema

	// cfg is shared by every sub-parser, so limits, codec and clock apply
	// consistently through the resource API.
	cfg *core.ParserConfig

	// fieldNameTag keeps direct patch application on the schema's external
	// names (default "json"; core.WithFieldNameTag overrides).
	fieldNameTag string

	// patchFields maps a top-level patch path's field name to T's field,
	// shared with every view (Resource.WithPaths).
	patchFields map[string]*core.Field
	gen         *core.SchemaGenerator
}

// newTypedParser creates a typedParser with filter, patch, sort, and search capabilities.
func newTypedParser[T any](opts ...core.Option) (*typedParser[T], error) {
	cfg := core.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	gen := core.NewSchemaGenerator(cfg.GeneratorOpts...)

	walk, err := gen.StructFields(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("walking fields: %w", err)
	}

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

	p := &typedParser[T]{
		filterSchema:      filterSchema,
		patchSchema:       patchSchema,
		serverPatchSchema: serverPatchSchema,
		hiddenPaths:       hiddenServerPaths(gen, walk),
		sortSchema:        sortSchema,
		codec:             cfg.GetCodec(),
		searchSchema:      searchSchema,
		cfg:               cfg,
		fieldNameTag:      gen.FieldNameTag(),
		patchFields:       patchFields(gen, walk),
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
	if cfg.policy != nil {
		if err := patch.ApplyPatchPolicyConstraints(parsed, cfg.policy, cfg.subject); err != nil {
			return nil, err
		}
		parsed.PolicyConstraints = cfg.policy
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

// hiddenServerPaths returns the patch paths of T's server fields that the
// field-name tag hides.
func hiddenServerPaths(gen *core.SchemaGenerator, walk *core.StructFields) map[string]bool {
	hidden := map[string]bool{}
	for _, f := range walk.Fields {
		if !f.Hidden() {
			continue
		}
		if name, ok := patch.ServerFieldName(gen, f); ok {
			hidden[core.FormatPointer(name)] = true
		}
	}
	return hidden
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
// ListSearch query composes a dialect-aware search predicate.
func (p *typedParser[T]) ListQuery(dialect filter.Dialect) *filter.ListQuery {
	return filter.NewListQuery(p.filterSchema, p.sortSchema, dialect).
		WithFilterConfig(p.cfg).
		WithSearchSchema(p.searchSchema)
}

// ApplyValidatedPatch applies an already-parsed patch to a target struct and
// returns the patched value. It never mutates target: when T is a pointer the
// patch applies to a copy of the pointee, so an operation that fails part-way
// leaves the caller's value untouched. Uses a reflection-based fast path for
// the common case (top-level replace/add/remove), falling back to a document
// round-trip for everything else.
func (p *typedParser[T]) ApplyValidatedPatch(parsed *patch.ParsedPatch, target T) (T, error) {
	target = copyPointee(target)
	if rv := reflect.ValueOf(target); rv.Kind() == reflect.Pointer && rv.IsNil() {
		var zero T
		return zero, fmt.Errorf("cannot apply patch to nil pointer")
	}
	// Fast path: all ops are top-level replace — set struct fields directly via reflection.
	if canApplyDirect(parsed) {
		return p.applyDirect(parsed, target)
	}
	// The document path round-trips T through its field-name tag, which a
	// hidden server field is not part of: the op would silently not apply.
	for _, op := range parsed.Operations {
		if p.hiddenPaths[op.Path] {
			var zero T
			return zero, fmt.Errorf("patch: %s is a server field without a %q name; set it with a top-level replace", op.Path, p.fieldNameTag)
		}
	}
	// Document path: move/copy/test/array_append/nested/merge objects.
	return p.applyDocument(parsed, target)
}

// copyPointee returns a shallow copy of a non-nil pointer's target (a new
// pointer), or v unchanged for non-pointer T. Field replacement on the copy
// never writes through to the caller's struct.
func copyPointee[T any](v T) T {
	rv := reflect.ValueOf(&v).Elem()
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return v
	}
	cp := reflect.New(rv.Type().Elem())
	cp.Elem().Set(rv.Elem())
	rv.Set(cp)
	return v
}

// canApplyDirect returns true when all operations are top-level replace or remove
// (no nested paths, no move/copy/test), including ordinary scalar merge patches.
func canApplyDirect(parsed *patch.ParsedPatch) bool {
	for _, op := range parsed.Operations {
		if op.Op != patch.PatchOpReplace && op.Op != patch.PatchOpRemove && op.Op != patch.PatchOpAdd {
			return false
		}
		if op.Merge {
			// RFC 7396 object values deep-merge into the existing value —
			// reflection whole-field set would drop sibling keys; the JSON path merges via jsonmerge.
			return false
		}
		if tokens, err := core.ParsePointer(op.Path); err != nil || len(tokens) != 1 {
			return false
		}
	}
	return true
}

// patchFields maps each top-level patch field name to T's field, by the
// same walk the schemas came from. A hidden server field is
// addressed by its column, unless a visible field has that name.
func patchFields(gen *core.SchemaGenerator, walk *core.StructFields) map[string]*core.Field {
	m := make(map[string]*core.Field, len(walk.Fields))
	for _, f := range walk.Fields {
		if !f.Hidden() {
			m[f.External] = f
		}
	}
	for _, f := range walk.Fields {
		if !f.Hidden() {
			continue
		}
		if name, ok := patch.ServerFieldName(gen, f); ok {
			if _, taken := m[name]; !taken {
				m[name] = f
			}
		}
	}
	return m
}

// applyDirect sets struct fields directly via reflection, avoiding all serialization.
func (p *typedParser[T]) applyDirect(parsed *patch.ParsedPatch, target T) (T, error) {
	var zero T
	rv := reflect.ValueOf(&target).Elem()
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return zero, fmt.Errorf("cannot apply patch to nil pointer")
		}
		rv = rv.Elem()
	}

	for _, op := range parsed.Operations {
		tokens, err := core.ParsePointer(op.Path)
		if err != nil || len(tokens) != 1 {
			return zero, fmt.Errorf("direct apply: path %q is not a top-level field", op.Path)
		}
		fieldName := tokens[0]
		field, ok := p.patchFields[fieldName]
		if !ok {
			// The schema validated this path, so a struct-map miss is an
			// internal invariant violation (tag drift between generator and
			// apply) — silently continuing returned the unmodified struct
			// with no error.
			return zero, &core.ParseError{
				Kind:    core.ParseErrorKindPatch,
				Code:    patch.ECodePatchFieldNotFound.String(),
				Message: fmt.Sprintf("schema-validated path %q has no matching %q-tagged struct field on %T", op.Path, p.fieldNameTag, target),
				Path:    op.Path,
			}
		}
		fv, err := field.Settable(rv)
		if err != nil {
			return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchValueInvalid.String(), Message: err.Error(), Path: op.Path, Cause: err}
		}
		if !fv.CanSet() {
			continue
		}

		if op.Schema.SQLType.IsJSON() {
			value := op.Value
			if op.Op == patch.PatchOpRemove || value == nil {
				value = op.Schema.ClearedValue()
			}
			if err := assignStoredJSON(fv, value); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", fieldName, err), Path: op.Path, Cause: err}
			}
			continue
		}
		if op.Op == patch.PatchOpRemove || op.Value == nil {
			if cleared := op.Schema.ClearedValue(); cleared != nil {
				fv.Set(reflect.ValueOf(cleared))
			} else {
				fv.SetZero()
			}
			continue
		}

		if err := assignValue(fv, op.Value, p.codec); err != nil {
			return zero, &core.ParseError{
				Kind:    core.ParseErrorKindPatch,
				Code:    patch.ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("setting field %s: %v", fieldName, err),
				Path:    op.Path,
				Cause:   err,
			}
		}
	}

	return target, nil
}

// Stored JSON uses the same decoder as SQL row scanning. The request codec
// still owns request parsing, including exact numeric input validation.
func assignStoredJSON(dst reflect.Value, value any) error {
	if value == nil {
		dst.SetZero()
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return jsonvalue.UnmarshalPresent(data, dst.Addr().Interface())
}

// assignValue stores a normalized patch value into dst through one checked,
// recursive conversion: integers and floats are range-checked
// against dst's exact kind (no wrap, no truncation), strings reach
// encoding.TextUnmarshaler destinations (time.Time, uuid.UUID), pointers are
// allocated and assigned through, and composite kinds (slices, maps,
// structs) decode via the codec, whose decoder range-checks every leaf.
func assignValue(dst reflect.Value, v any, codec core.Codec) error {
	t := dst.Type()
	if v == nil {
		dst.SetZero()
		return nil
	}
	if t.Kind() == reflect.Pointer {
		elem := reflect.New(t.Elem())
		if err := assignValue(elem.Elem(), v, codec); err != nil {
			return err
		}
		dst.Set(elem)
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Type() == t {
		dst.Set(rv)
		return nil
	}
	if s, ok := v.(string); ok {
		if tu, ok := dst.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return tu.UnmarshalText([]byte(s))
		}
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := core.NormalizeInt(v, t.Kind())
		if err != nil {
			return err
		}
		dst.SetInt(n)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := core.NormalizeUint(v, t.Kind())
		if err != nil {
			return err
		}
		dst.SetUint(n)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := core.NormalizeFloat(v, t.Kind())
		if err != nil {
			return err
		}
		dst.SetFloat(f)
		return nil
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("cannot assign %T to %s", v, t)
		}
		dst.SetString(s)
		return nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("cannot assign %T to %s", v, t)
		}
		dst.SetBool(b)
		return nil
	case reflect.Interface:
		if rv.Type().AssignableTo(t) {
			dst.Set(rv)
			return nil
		}
	}
	data, err := codec.Encode(v)
	if err != nil {
		return fmt.Errorf("encoding %T for %s: %w", v, t, err)
	}
	decoded := reflect.New(t)
	if err := codec.Decode(data, decoded.Interface()); err != nil {
		return fmt.Errorf("cannot assign %T to %s: %w", v, t, err)
	}
	dst.Set(decoded.Elem())
	return nil
}

// checkDocumentFieldNames catches the default JSON codec paired with a
// different schema tag before a document operation can silently hit another
// key. Custom codecs must encode the configured field-name tag themselves.
func (p *typedParser[T]) checkDocumentFieldNames(parsed *patch.ParsedPatch) error {
	if p.fieldNameTag == "json" {
		return nil
	}
	switch p.codec.(type) {
	case core.StdlibJSON, *core.StdlibJSON:
	default:
		return nil
	}
	jsonWalk, err := core.StructFieldsOf(reflect.TypeFor[T](), "json", p.gen.ColumnTagKey())
	if err != nil {
		return fmt.Errorf("patch: the JSON codec cannot name T's fields: %w", err)
	}
	for _, op := range parsed.Operations {
		for _, path := range []string{op.Path, op.From} {
			if path == "" {
				continue
			}
			tokens, err := core.ParsePointer(path)
			if err != nil || len(tokens) == 0 {
				return fmt.Errorf("patch: invalid field path %q", path)
			}
			field, ok := p.patchFields[tokens[0]]
			if !ok {
				continue
			}
			i := slices.IndexFunc(jsonWalk.Fields, func(f *core.Field) bool { return slices.Equal(f.Route, field.Route) })
			if i < 0 || jsonWalk.Fields[i].External != tokens[0] {
				name := "(none)"
				if i >= 0 {
					name = jsonWalk.Fields[i].External
				}
				return fmt.Errorf("patch: field %s uses %q name %q but the JSON codec uses %q; configure a matching codec for document operations", field.GoName, p.fieldNameTag, tokens[0], name)
			}
		}
	}
	return nil
}

// applyDocument applies a patch through the target's document form:
// encode with the parser codec (whose field names match the schema's field
// tag) into a number-preserving map, apply each run of RFC 6902 operations
// with github.com/evanphx/json-patch/v5 and each RFC 7396 merge object with
// jsonmerge, then decode changed fields through their storage/request codec.
// Untouched fields retain their values without another decoder invocation.
func (p *typedParser[T]) applyDocument(parsed *patch.ParsedPatch, target T) (T, error) {
	var zero T

	if err := p.checkDocumentFieldNames(parsed); err != nil {
		return zero, err
	}

	data, err := p.codec.Encode(target)
	if err != nil {
		return zero, fmt.Errorf("encoding target: %w", err)
	}
	var doc map[string]any
	if err = p.codec.Decode(data, &doc); err != nil {
		return zero, fmt.Errorf("decoding target to a document: %w", err)
	}

	run := &jsonPatchRun{copyBudget: p.cfg.MaxPatchCopyBytes}
	ops := parsed.Operations
	for i := 0; i < len(ops); {
		op := ops[i]
		switch {
		case op.Merge:
			// RFC 7396 recursive merge: nested keys merge,
			// nested nulls delete, siblings survive. Merge ops are top-level.
			tokens, pointerErr := core.ParsePointer(op.Path)
			if pointerErr != nil || len(tokens) != 1 {
				return zero, fmt.Errorf("merge path %q is not a top-level field", op.Path)
			}
			doc[tokens[0]] = jsonmerge.Apply(doc[tokens[0]], op.Value)
			i++
		case op.Op == patch.PatchOpTest:
			// RFC 6902 §4.6: the target must exist and equal the value as
			// JSON — numbers by value, so 1.0 equals 1. json-patch compares
			// numbers by their spelling and treats a missing member as null,
			// so parse decides test itself, against the document as it
			// stands at this step.
			if err = testValue(doc, op, i); err != nil {
				return zero, err
			}
			i++
		case op.Op == patch.PatchOpCopy || op.Op == patch.PatchOpMove:
			value, exists := pointerValue(doc, op.From)
			if exists {
				if err = patch.ValidateTransferValue(op, value, p.codec, i); err != nil {
					return zero, err
				}
			}
			if doc, err = run.apply(doc, []map[string]any{wireOp(op)}, i); err != nil {
				return zero, err
			}
			i++
		case op.Op == patch.PatchOpArrayAppend:
			// Translated to `add <path>/-`, preceded by `add <path> []` when
			// the array is missing or null at this step — the SQL backends'
			// COALESCE-to-empty.
			steps := []map[string]any{{"op": "add", "path": op.Path + "/-", "value": op.Value}}
			if current, ok := pointerValue(doc, op.Path); !ok || current == nil {
				steps = append([]map[string]any{{"op": "add", "path": op.Path, "value": []any{}}}, steps...)
			}
			if doc, err = run.apply(doc, steps, i); err != nil {
				return zero, err
			}
			i++
		default:
			j := i
			var wire []map[string]any
			for j < len(ops) && !ops[j].Merge && ops[j].Op != patch.PatchOpTest && ops[j].Op != patch.PatchOpArrayAppend && ops[j].Op != patch.PatchOpCopy && ops[j].Op != patch.PatchOpMove {
				wire = append(wire, wireOp(ops[j]))
				j++
			}
			if doc, err = run.apply(doc, wire, i); err != nil {
				return zero, err
			}
			i = j
		}
	}

	type changedField struct {
		field   *core.Field
		storage core.SQLType
	}
	changed := make(map[string]changedField)
	var names []string
	for _, op := range parsed.Operations {
		if op.Op == patch.PatchOpTest {
			continue
		}
		paths := []string{op.Path}
		if op.Op == patch.PatchOpMove {
			paths = append(paths, op.From)
		}
		for _, path := range paths {
			tokens, err := core.ParsePointer(path)
			if err != nil || len(tokens) == 0 {
				return zero, fmt.Errorf("patch: invalid field path %q", path)
			}
			name := tokens[0]
			field, ok := p.patchFields[name]
			if !ok {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchFieldNotFound.String(), Message: fmt.Sprintf("patch path %q has no writable struct field", path), Path: path}
			}
			if _, seen := changed[name]; !seen {
				info, err := p.gen.FieldType(field)
				if err != nil {
					return zero, fmt.Errorf("patch field %s: %w", name, err)
				}
				changed[name] = changedField{field: field, storage: info.SQLType}
				names = append(names, name)
			}
		}
	}
	// Decode only changed fields: an unrelated custom decoder must neither
	// transform untouched state nor reject an otherwise valid update.
	native := make(map[string]any)
	for _, name := range names {
		if !changed[name].storage.IsJSON() {
			if value, exists := doc[name]; exists {
				native[name] = value
			}
		}
	}
	var result T
	if len(native) > 0 {
		out, err := p.codec.Encode(native)
		if err != nil {
			return zero, fmt.Errorf("encoding patched document: %w", err)
		}
		if err := p.codec.Decode(out, &result); err != nil {
			return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchValueInvalid.String(), Message: fmt.Sprintf("patched document does not fit %T: %v", result, err), Cause: err}
		}
	}
	preserved := copyPointee(target)
	dst := reflect.ValueOf(&preserved).Elem()
	if dst.Kind() == reflect.Pointer {
		dst = dst.Elem()
	}
	for _, name := range names {
		change := changed[name]
		d, err := change.field.Settable(dst)
		if err != nil || !d.CanSet() {
			return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchFieldNotFound.String(), Message: fmt.Sprintf("patch field %q is not writable", name), Path: core.FormatPointer(name)}
		}
		if change.storage.IsJSON() {
			if err := assignStoredJSON(d, doc[name]); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: patch.ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", name, err), Path: core.FormatPointer(name), Cause: err}
			}
			continue
		}
		src := reflect.ValueOf(result)
		if src.Kind() == reflect.Pointer {
			if src.IsNil() {
				d.SetZero()
				continue
			}
			src = src.Elem()
		}
		if value, err := change.field.Value(src); err == nil {
			d.Set(value)
		} else {
			d.SetZero()
		}
	}
	return preserved, nil
}

// testValue evaluates a test operation against doc.
func testValue(doc map[string]any, op *patch.PatchOperation, index int) error {
	fail := func(msg string) error {
		return &core.ParseError{
			Kind:    core.ParseErrorKindPatch,
			Code:    patch.ECodePatchValueInvalid.String(),
			Message: fmt.Sprintf("test operation %d: %s", index, msg),
			Path:    op.Path, Index: new(index),
		}
	}
	current, ok := pointerValue(doc, op.Path)
	if !ok {
		return fail(fmt.Sprintf("path %s does not exist", op.Path))
	}
	have, err := core.NormalizeJSON(current)
	if err != nil {
		return fail(fmt.Sprintf("value at %s: %v", op.Path, err))
	}
	want, err := core.NormalizeJSON(op.Value)
	if err != nil {
		return fail(fmt.Sprintf("test value: %v", err))
	}
	if !filter.JSONEqual(have, want) {
		return fail(fmt.Sprintf("value at %s does not equal the test value", op.Path))
	}
	return nil
}

// wireOp renders a parsed operation as an RFC 6902 operation object.
func wireOp(op *patch.PatchOperation) map[string]any {
	// Clearing a top-level slice or map empties it (PathSchema.ClearedValue);
	// add also covers a member the encoding omitted.
	if (op.Op == patch.PatchOpRemove || op.Value == nil) && op.Op != patch.PatchOpMove && op.Op != patch.PatchOpCopy && op.Op != patch.PatchOpTest {
		if tokens, err := core.ParsePointer(op.Path); err == nil && len(tokens) == 1 {
			if cleared := op.Schema.ClearedValue(); cleared != nil {
				return map[string]any{"op": "add", "path": op.Path, "value": cleared}
			}
		}
	}
	// Top-level schema fields exist even when omitempty omits their current
	// value. Keep fallback replacement/clearing equivalent to direct apply;
	// nested JSON members retain RFC 6902's existence requirements.
	if tokens, err := core.ParsePointer(op.Path); err == nil && len(tokens) == 1 {
		switch op.Op {
		case patch.PatchOpReplace:
			return map[string]any{"op": "add", "path": op.Path, "value": op.Value}
		case patch.PatchOpRemove:
			return map[string]any{"op": "add", "path": op.Path, "value": nil}
		}
	}
	w := map[string]any{"op": string(op.Op), "path": op.Path}
	switch op.Op {
	case patch.PatchOpMove, patch.PatchOpCopy:
		w["from"] = op.From
	case patch.PatchOpRemove:
	default:
		w["value"] = op.Value
	}
	return w
}

// jsonPatchRun applies RFC 6902 operations with
// github.com/evanphx/json-patch/v5, enforcing one copy budget across every
// step of a patch (ParserConfig.MaxPatchCopyBytes; 0 means unlimited).
type jsonPatchRun struct {
	copyBudget int64
	copied     int64
}

// apply applies ops to doc; offset is the first op's index in the parsed
// patch, for error reporting.
func (r *jsonPatchRun) apply(doc map[string]any, ops []map[string]any, offset int) (map[string]any, error) {
	// Copies are applied individually, so charge their source size rather than
	// document growth (overwriting an existing value may not grow it at all).
	for _, op := range ops {
		if from, hasFrom := op["from"].(string); hasFrom && op["op"] == string(patch.PatchOpCopy) {
			if value, ok := pointerValue(doc, from); ok {
				encoded, err := json.Marshal(value)
				if err != nil {
					return nil, fmt.Errorf("encoding copy source: %w", err)
				}
				r.copied += int64(len(encoded))
				if r.copyBudget > 0 && r.copied > r.copyBudget {
					return nil, copyBudgetError(offset, r.copyBudget)
				}
			}
		}
	}
	doc, ops, restore, err := escapeEmptyPatchKeys(doc, ops)
	if err != nil {
		return nil, err
	}
	patchJSON, err := json.Marshal(ops)
	if err != nil {
		return nil, fmt.Errorf("encoding patch operations: %w", err)
	}
	decoded, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding patch operations: %w", err)
	}
	docJSON, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding document: %w", err)
	}
	opts := jsonpatch.NewApplyOptions()
	opts.SupportNegativeIndices = false // RFC 6901: array indices are non-negative
	patched, err := decoded.ApplyWithOptions(docJSON, opts)
	if err != nil {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindPatch,
			Code:    patch.ECodePatchValueInvalid.String(),
			Message: fmt.Sprintf("applying operation %d: %v", offset, err),
			Index:   new(offset),
			Cause:   err,
		}
	}
	var out map[string]any
	if err := (core.StdlibJSON{}).Decode(patched, &out); err != nil {
		return nil, fmt.Errorf("decoding patched document: %w", err)
	}
	return restore(out), nil
}

func copyBudgetError(offset int, budget int64) error {
	return &core.ParseError{
		Kind:    core.ParseErrorKindPatch,
		Code:    patch.ECodePatchValueInvalid.String(),
		Message: fmt.Sprintf("operation %d: copy operations exceed the %d-byte budget", offset, budget),
		Index:   new(offset),
	}
}

// pointerValue reads the value at a JSON Pointer in a decoded document,
// reporting whether it exists.
func pointerValue(doc map[string]any, pointer string) (any, bool) {
	tokens, err := core.ParsePointer(pointer)
	if err != nil {
		return nil, false
	}
	var cur any = doc
	for _, tok := range tokens {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
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
	subject *core.PolicySubject
}

// WithPatchPolicy sets policy constraints for patch validation.
func WithPatchPolicy(pc *patch.PatchPolicyConstraints, subject *core.PolicySubject) PatchOption {
	return func(c *patchOptConfig) { c.policy = pc; c.subject = subject }
}
