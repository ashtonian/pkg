package patch

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/rfc6902"
)

// Parser parses and validates JSON Patch documents.
type Parser struct {
	schema *PatchSchema
	config *core.ParserConfig
	codec  core.Codec
}

// NewParser creates a new patch Parser.
func NewParser(schema *PatchSchema, config *core.ParserConfig) *Parser {
	config = config.Effective()
	return &Parser{schema: schema, config: config, codec: config.GetCodec()}
}

// RawPatchOp is a decoded patch operation. Used directly by [Parser.ParseOps]
// for non-JSON inputs, and internally by [Parser.Parse] after JSON decoding.
type RawPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	From  string `json:"from,omitempty"`
	Value any    `json:"value,omitempty"`
	// HasValue reports that the operation carried a value member, so an
	// explicit null is told from no value at all. Parse sets it; a caller of
	// ParseOps sets it for a null value — a non-nil Value implies it.
	HasValue bool `json:"-"`
}

// present reports whether op carried a value member.
func (r RawPatchOp) present() bool { return r.HasValue || r.Value != nil }

// ParseOps validates pre-decoded patch operations against the schema.
// Use this when the input has already been decoded from a non-JSON format
// (protobuf, msgpack, YAML, etc.).
func (p *Parser) ParseOps(ops []RawPatchOp) (*ParsedPatch, error) {
	if p.schema == nil {
		return nil, errNoSchema
	}
	return p.validateOps(ops)
}

// Parse validates and parses a JSON Patch document from JSON bytes.
func (p *Parser) Parse(body []byte) (*ParsedPatch, error) {
	if p.schema == nil {
		return nil, errNoSchema
	}
	if p.config.MaxInputSize > 0 && len(body) > p.config.MaxInputSize {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInputTooLarge.String(),
			Message: fmt.Sprintf("input size %d exceeds maximum %d bytes", len(body), p.config.MaxInputSize),
		}
	}

	// Operations are decoded as objects first, with the configured codec so
	// values keep its number handling, to learn which members are present:
	// a struct field cannot tell a missing "value" from "value": null.
	var docs []map[string]any
	if err := p.codec.Decode(body, &docs); err != nil {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInvalidJSON.String(),
			Message: "invalid JSON Patch syntax", Cause: err,
		}
	}
	if docs == nil {
		return nil, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodeInvalidJSON.String(), Message: "JSON Patch must be an array of operations"}
	}
	ops := make([]RawPatchOp, len(docs))
	for i, doc := range docs {
		op, err := rawOpFrom(doc, i)
		if err != nil {
			return nil, err
		}
		ops[i] = op
	}
	return p.validateOps(ops)
}

// rawOpFrom reads one decoded JSON Patch operation object.
func rawOpFrom(doc map[string]any, i int) (RawPatchOp, error) {
	var op RawPatchOp
	for _, m := range []struct {
		name string
		dst  *string
	}{{rfc6902.Op, &op.Op}, {rfc6902.Path, &op.Path}, {rfc6902.From, &op.From}} {
		v, ok := doc[m.name]
		if !ok {
			continue
		}
		s, isString := v.(string)
		if !isString {
			return op, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodeInvalidJSON.String(),
				Message: fmt.Sprintf("member %q of the operation at index %d must be a string", m.name, i),
				Index:   new(i),
			}
		}
		*m.dst = s
	}
	op.Value, op.HasValue = doc[rfc6902.Value]
	return op, nil
}

func (p *Parser) validateOps(ops []RawPatchOp) (*ParsedPatch, error) {
	if p.schema.MaxOperations > 0 && len(ops) > p.schema.MaxOperations {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeTooManyOps.String(),
			Message: fmt.Sprintf("too many operations: %d exceeds maximum %d", len(ops), p.schema.MaxOperations),
		}
	}

	result := &ParsedPatch{
		Operations: make([]*PatchOperation, 0, len(ops)),
		schema:     p.schema,
	}

	var failures []error
	for i, raw := range ops {
		operation, err := p.validateOperation(raw, i)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		result.Operations = append(result.Operations, operation)
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}

	result.AffectedPaths = affectedPaths(result.Operations)

	return result, nil
}

func (p *Parser) validateOperation(raw RawPatchOp, i int) (*PatchOperation, error) {
	if !isValidPatchOp(raw.Op) {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInvalidPatchOp.String(),
			Message: fmt.Sprintf("invalid operation '%s' at index %d", raw.Op, i),
			Index:   new(i),
		}
	}
	op := PatchOp(raw.Op)
	if opTakesValue(op) && !raw.present() {
		// RFC 6902 §4.1, §4.3, §4.6: add, replace and test carry a
		// value (as array_append carries its element); without one an
		// operation is malformed, not a clear.
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInvalidPatchOp.String(),
			Message: fmt.Sprintf("'value' required for %s at index %d", raw.Op, i),
			Path:    raw.Path, Index: new(i),
		}
	}

	pathSchema, depth, err := p.resolvePath(raw.Path, i)
	if err != nil {
		return nil, err
	}

	// An operation carries only the value it writes: a remove writes null
	// whatever value a client attached, and a move or copy writes its
	// source's. A relation's remove names the members it removes.
	var value any
	if opTakesValue(op) || (pathSchema.Relation != nil && op == PatchOpRemove) {
		value = normalizeNilPointer(raw.Value)
	}
	value, failures := newValueChecker(p.schema, p.codec).check(pathSchema, op, value, depth, false)
	if len(failures) > 0 {
		errs := make([]error, len(failures))
		for j, f := range failures {
			if f.code == ECodeOpNotAllowed {
				errs[j] = &core.ParseError{
					Kind: core.ParseErrorKindPatch, Code: f.code.String(),
					Message: fmt.Sprintf("operation '%s' not allowed on path '%s' at index %d", op, raw.Path, i),
					Path:    raw.Path, Index: new(i),
				}
				continue
			}
			errs[j] = &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: f.code.String(),
				Message: fmt.Sprintf("invalid value for path '%s' at index %d: %v", raw.Path, i, f.cause),
				Path:    raw.Path, Index: new(i), Cause: f.cause, Details: f.details,
			}
		}
		return nil, errors.Join(errs...)
	}

	if depth > 1 {
		if err := checkDocumentMember(p.schema, op, raw.Path, value); err != nil {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("invalid member '%s' at index %d: %v", raw.Path, i, err),
				Path:    raw.Path, Index: new(i), Cause: err,
			}
		}
	}

	var fromSchema *PathSchema
	if op == PatchOpMove || op == PatchOpCopy {
		if raw.From == "" {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodeInvalidPatchOp.String(),
				Message: fmt.Sprintf("'from' required for %s at index %d", raw.Op, i),
				Index:   new(i),
			}
		}
		// The source must itself be a schema path: move/copy read it, and
		// an unlisted source would expose a field the schema never made
		// patchable.
		from, fromDepth, err := p.resolvePath(raw.From, i)
		if err != nil {
			return nil, err
		}
		if err := checkTransfer(op, from, fromDepth, pathSchema, depth); err != nil {
			return nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: err.code.String(),
				Message: fmt.Sprintf("%s from '%s' to '%s' at index %d: %s", op, raw.From, raw.Path, i, err.msg),
				Path:    raw.Path, Index: new(i), Cause: err.cause,
			}
		}
		fromSchema = from
	}
	return &PatchOperation{Op: op, Path: raw.Path, From: raw.From, Value: value, Schema: pathSchema, fromSchema: fromSchema}, nil
}

// resolvePath validates a JSON Pointer (RFC 6901 syntax, depth within
// ParserConfig.MaxDepth) and returns its schema entry and token count.
func (p *Parser) resolvePath(path string, index int) (*PathSchema, int, error) {
	tokens, err := core.ParsePointer(path)
	if err != nil {
		return nil, 0, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeInvalidPatchOp.String(),
			Message: fmt.Sprintf("invalid path at index %d: %v", index, err),
			Path:    path, Index: new(index), Cause: err,
		}
	}
	if len(tokens) == 0 {
		return nil, 0, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(),
			Message: fmt.Sprintf("path at index %d targets the whole document; patch a field instead", index),
			Path:    path, Index: new(index),
		}
	}
	if len(tokens) > p.config.MaxDepth {
		return nil, 0, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePathTooDeep.String(),
			Message: fmt.Sprintf("path '%s' at index %d has %d segments; maximum is %d", path, index, len(tokens), p.config.MaxDepth),
			Path:    path, Index: new(index),
		}
	}
	ps, err := matchPath(p.schema, path)
	if err != nil {
		return nil, 0, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(),
			Message: fmt.Sprintf("path '%s' at index %d: %v", path, index, err),
			Path:    path, Index: new(index), Cause: err,
		}
	}
	return ps, len(tokens), nil
}

func matchPath(schema *PatchSchema, path string) (*PathSchema, error) {
	if ps, ok := schema.pathSet().resolve(path); ok {
		return ps, nil
	}
	return nil, errors.New("path not allowed")
}

func isValidPatchOp(op string) bool { return IsValidOp(PatchOp(op)) }

// opTakesValue reports whether op consumes the raw.Value field and so
// should run the field-level value validators. RFC 6902 add / replace
// / test all take a value; remove drops the column to NULL; move /
// copy take a `from` path. The extension array_append takes a
// scalar value (the single element to append) and is validator-eligible.
func opTakesValue(op PatchOp) bool {
	switch op {
	case PatchOpReplace, PatchOpAdd, PatchOpTest, PatchOpArrayAppend:
		return true
	}
	return false
}

// transferError is why a move or copy is refused, with the code to report.
type transferError struct {
	code  core.Code
	msg   string
	cause error
}

// checkTransfer applies a move's or copy's source-side rules. RFC 6902
// §4.4 defines move as removing from and adding to path, so the source must
// allow remove and, as a field, be nullable — otherwise a move would empty a
// field its own remove refuses. Both operations write the source's value
// into the destination, so two fields must hold the same type; members
// inside JSON documents carry no static type and are checked when the
// patched document decodes.
func checkTransfer(op PatchOp, from *PathSchema, fromDepth int, to *PathSchema, toDepth int) *transferError {
	if op == PatchOpMove {
		if !from.AllowsOp(PatchOpRemove) {
			return &transferError{code: ECodeOpNotAllowed, msg: "the source does not allow remove"}
		}
		if err := checkNullability(from, PatchOpRemove, nil, fromDepth); err != nil {
			return &transferError{ECodePatchValueInvalid, "the source " + err.Error(), err}
		}
		// The move writes null into its source, as a remove does.
		for _, validator := range from.Validators {
			if err := validator(nil); err != nil {
				return &transferError{ECodePatchValueInvalid, "the source " + err.Error(), err}
			}
		}
	}
	if fromDepth == 1 && toDepth == 1 && from.GoType != nil && to.GoType != nil && valueType(from.GoType) != valueType(to.GoType) {
		return &transferError{code: ECodePatchValueInvalid, msg: fmt.Sprintf("the source holds %s, the destination %s", from.GoType, to.GoType)}
	}
	if fromDepth == 1 && toDepth == 1 && from.Wire != to.Wire {
		// The document holds each in its own wire form; the value would not
		// decode into the other.
		return &transferError{code: ECodePatchValueInvalid, msg: fmt.Sprintf("the source travels as %s, the destination as %s", wireName(from.Wire), wireName(to.Wire))}
	}
	return nil
}

// wireName describes a wire form for messages.
func wireName(w core.Wire) string {
	if w == core.WireString {
		return `a JSON string (json:",string")`
	}
	return "its JSON type"
}

// valueType is the type a field's non-null value has: pointers stripped.
func valueType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// normalizeNilPointer gives typed nil pointers the same null contract as
// explicit JSON null, without stripping nonnil pointer methods or changing
// map and slice encoding semantics.
func normalizeNilPointer(v any) any {
	value := reflect.ValueOf(v)
	var seen map[any]struct{}
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return nil
		}
		next := value.Elem()
		if next.Kind() != reflect.Pointer && next.Kind() != reflect.Interface {
			return v
		}
		if value.Kind() == reflect.Pointer {
			pointer := value.Interface()
			if _, cycle := seen[pointer]; cycle {
				return v
			}
			if seen == nil {
				seen = make(map[any]struct{})
			}
			seen[pointer] = struct{}{}
		}
		value = next
	}
	return v
}

// checkNullability rejects writing null into, or removing, a field whose Go
// type cannot hold null: SQL would store NULL into a non-nullable column (or
// fail to scan it back) while the memory backend stored a zero value.
// Nested paths address members inside a JSON value, not the field itself;
// relation paths use remove as a membership verb.
func checkNullability(ps *PathSchema, op PatchOp, value any, depth int) error {
	if depth != 1 || ps.Relation != nil || ps.Nullable() {
		return nil
	}
	clears := op == PatchOpRemove || (value == nil && (op == PatchOpReplace || op == PatchOpAdd))
	if !clears {
		return nil
	}
	reason := fmt.Sprintf("field is not nullable (%s)", ps.GoType)
	if mustHoldElement(ps.GoType, ps.Bounds) {
		reason = fmt.Sprintf("field cannot be cleared to an empty array (minitems %d)", *ps.Bounds.MinItems)
	}
	if op == PatchOpRemove {
		reason += " and cannot be removed"
	}
	if mustHoldElement(ps.GoType, ps.Bounds) {
		return NewConstraintError(core.ConstraintMinItems, strconv.Itoa(*ps.Bounds.MinItems), reason)
	}
	return errors.New(reason)
}

// checkItems refuses an array a replace or add sets whole outside the path's
// element counts. Clearing a path that must hold an element is refused as a
// null (Nullable). An element added, appended or removed, and an array moved
// or copied in, change an array the parser does not see; the store counts
// those.
func checkItems(ps *PathSchema, op PatchOp, value any, depth int) error {
	b := ps.Bounds
	if depth != 1 || !b.CountsItems() || ps.Relation != nil || (op != PatchOpReplace && op != PatchOpAdd) {
		return nil
	}
	array := reflect.ValueOf(value)
	if array.Kind() != reflect.Slice {
		return nil
	}
	n := array.Len()
	if b.MinItems != nil && n < *b.MinItems {
		return constraintFailure(core.ConstraintMinItems, strconv.Itoa(*b.MinItems), "%d elements is fewer than the minimum %d", n, *b.MinItems)
	}
	if b.MaxItems != nil && n > *b.MaxItems {
		return constraintFailure(core.ConstraintMaxItems, strconv.Itoa(*b.MaxItems), "%d elements is more than the maximum %d", n, *b.MaxItems)
	}
	return nil
}

// normalizeOpValue validates an operation's value against its path schema
// and returns the canonical value, so an accepted operation
// applies on every backend:
//
//   - scalars normalize per DataType, integers range-checked against the Go kind;
//   - arrays of scalars normalize element-wise (array_append's value is one element);
//   - other composite values (structs, maps) must decode into GoType.
//
// Nested paths address members inside a JSON value and pass through.
// Relation paths carry arrays of target IDs, normalized element-wise.
func normalizeOpValue(ps *PathSchema, op PatchOp, v any, depth int, codec core.Codec) (any, error) {
	if depth != 1 {
		return v, nil
	}
	if v == nil && op != PatchOpArrayAppend {
		return nil, nil
	}
	if ps.Relation != nil {
		return normalizeRelationKeys(ps, v, codec)
	}
	supplied := v
	v, err := core.FromWire(ps.Wire, ps.DataType, v)
	if err != nil {
		return nil, err
	}
	target := ps.GoType
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if op == PatchOpArrayAppend {
		if target == nil || target.Kind() != reflect.Slice {
			if target != nil && target.Kind() == reflect.Array {
				return nil, fmt.Errorf("array_append cannot grow a fixed-size array (%s)", target)
			}
			return v, nil
		}
		return normalizeTyped(target.Elem(), v, codec)
	}
	if ps.SQLType.IsJSON() && ps.GoType != nil {
		normalized, err := normalizeDeclaredJSON(ps.GoType, v)
		if err == nil {
			err = checkNullability(ps, op, normalized, depth)
		}
		if err == nil && op == PatchOpTest && normalized != nil {
			return v, nil
		}
		return normalized, err
	}
	if target == nil {
		return core.NormalizeScalar(ps.DataType, reflect.Invalid, v)
	}
	normalized, err := normalizeTyped(target, v, codec)
	if err == nil && op == PatchOpTest && ps.Wire == "" {
		// RFC 6902 tests compare the supplied document, not a replacement
		// with missing struct members filled or omitted by Go encoding. A
		// quoted field's test compares its canonical wire text instead, so
		// "5.0" tests equal to a stored 5 as it replaces one.
		return supplied, nil
	}
	return normalized, err
}

// normalizeRelationKeys normalizes a membership's keys as the target's key:
// each from the path's wire form (a quoted key's JSON string, or a Go value
// code passes), then to the key's Go type.
func normalizeRelationKeys(ps *PathSchema, v any, codec core.Codec) (any, error) {
	if ps.Wire != "" {
		items, ok := core.AnySlice(v)
		if !ok {
			return nil, fmt.Errorf("expected an array, got %T", v)
		}
		key := ps.DataType
		if ps.GoType != nil {
			key = core.TypeOf(ps.GoType.Elem()).Type
		}
		keys := make([]any, len(items))
		for i, item := range items {
			k, err := core.FromWire(ps.Wire, key, item)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			keys[i] = k
		}
		v = keys
	}
	if ps.GoType == nil {
		return v, nil
	}
	return normalizeTyped(ps.GoType, v, codec)
}

// normalizeTyped normalizes v for the Go type t: scalars, decimals included,
// via core.NormalizeScalar, slices and arrays element by element, another
// type that is a database value (driver.Valuer) by decoding into it, and
// remaining composite kinds by decoding and re-encoding their canonical
// representation. SQL and typed apply must store the same JSON document.
func normalizeTyped(t reflect.Type, v any, codec core.Codec) (any, error) {
	v = normalizeNilPointer(v)
	if v == nil {
		if isNullableType(t) {
			return nil, nil
		}
		return nil, fmt.Errorf("null is not valid for %s", t)
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if dt, ok := scalarFieldType(t); ok {
		return core.NormalizeScalar(dt, core.NumericKind(t), v)
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			break // []byte travels as base64 text; the decoder decides
		}
		items, ok := core.AnySlice(v)
		if !ok {
			return nil, fmt.Errorf("expected an array, got %T", v)
		}
		if t.Kind() == reflect.Array && len(items) != t.Len() {
			// Decoding would drop the extra elements or zero-fill the
			// missing ones while SQL stored what was sent.
			return nil, fmt.Errorf("expected exactly %d elements for %s, got %d", t.Len(), t, len(items))
		}
		out := make([]any, len(items))
		for i, item := range items {
			n, err := normalizeTyped(t.Elem(), item, codec)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", i, err)
			}
			out[i] = n
		}
		return out, nil
	case reflect.Interface:
		return v, nil
	}
	data, err := codec.Encode(v)
	if err != nil {
		return nil, err
	}
	dst := reflect.New(t)
	if err = checkJSONMembers(t, data); err != nil {
		return nil, err
	}
	if err = codec.Decode(data, dst.Interface()); err != nil {
		return nil, fmt.Errorf("value does not fit %s: %w", t, err)
	}
	// The driver encodes a database value; its JSON form ("200" for a
	// decimal) is not what the column stores.
	switch {
	case t.Implements(valuerType):
		return dst.Elem().Interface(), nil
	case dst.Type().Implements(valuerType):
		return dst.Interface(), nil
	}
	data, err = codec.Encode(dst.Elem().Interface())
	if err != nil {
		return nil, err
	}
	var canonical any
	if err := codec.Decode(data, &canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

var valuerType = reflect.TypeFor[driver.Valuer]()

// scalarFieldType maps a Go type to the DataType core.NormalizeScalar
// handles, or reports false for composite and unknown types.
func scalarFieldType(t reflect.Type) (core.FieldType, bool) {
	switch ft := core.TypeOf(t).Type; ft {
	case core.FieldTypeString, core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeDecimal, core.FieldTypeBool,
		core.FieldTypeTimestamp, core.FieldTypeDate, core.FieldTypeUUID,
		core.FieldTypeTime, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		return ft, true
	}
	return "", false
}
