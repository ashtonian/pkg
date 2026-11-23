package patch

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/ashtonian/pkg/jsonvalue"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
	"github.com/ashtonian/pkg/parse/internal/rfc6902"
)

// ErrUnparsed is returned for a ParsedPatch no parser produced: one assembled
// by hand, whose operations skipped validation, or an operation without a
// path schema. Build patches with Parser.Parse, Parser.ParseOps,
// ParseMergePatch or ParseMergePatchMap.
var ErrUnparsed = errors.New("patch: not produced by a parser")

// checkParsed refuses a patch no parser produced.
func checkParsed(parsed *ParsedPatch) error {
	if parsed == nil {
		return fmt.Errorf("%w: the patch is nil", ErrUnparsed)
	}
	if parsed.schema == nil {
		return fmt.Errorf("%w: the patch records no schema", ErrUnparsed)
	}
	for i, op := range parsed.Operations {
		if op == nil || op.Schema == nil {
			return fmt.Errorf("%w: operation %d has no path schema", ErrUnparsed, i)
		}
	}
	return nil
}

// Applier applies parsed patches to Go values of T: the counterpart of
// BuildPatchSQL for a value held in memory. Top-level replace, add and
// remove set struct fields directly; every other operation applies through
// T's JSON document, RFC 6902 operations with github.com/evanphx/json-patch
// and merge objects with RFC 7396. It is safe for concurrent use.
type Applier[T any] struct {
	codec core.Codec
	// copyBudget bounds the bytes a patch's copies may add
	// (ParserConfig.MaxPatchCopyBytes; 0 is unlimited).
	copyBudget int64
	gen        *core.SchemaGenerator
	// fieldNameTag names fields as the patch schemas do; the document path
	// checks the codec names them the same way.
	fieldNameTag string
	// fields maps a top-level patch path's field name to T's field.
	fields map[string]*core.Field
	// hidden are the patch paths of the server fields the field-name tag
	// hides, which only the direct path can set.
	hidden map[string]bool
}

// NewApplier builds the applier for T, naming fields with gen, the generator
// T's patch schemas came from, and decoding with cfg's codec within its copy
// budget. A nil cfg is core.DefaultConfig.
func NewApplier[T any](gen *core.SchemaGenerator, cfg *core.ParserConfig) (*Applier[T], error) {
	if gen == nil {
		return nil, errors.New("patch: NewApplier needs the schema generator")
	}
	cfg = cfg.Effective()
	walk, err := gen.StructFields(reflect.TypeFor[T]())
	if err != nil {
		return nil, fmt.Errorf("patch: walking fields: %w", err)
	}
	return &Applier[T]{
		codec:        cfg.GetCodec(),
		copyBudget:   cfg.MaxPatchCopyBytes,
		gen:          gen,
		fieldNameTag: gen.FieldNameTag(),
		fields:       patchFields(gen, walk),
		hidden:       hiddenServerPaths(gen, walk),
	}, nil
}

// patchFields maps each top-level patch field name to T's field, by the
// same walk the schemas came from. A hidden server field is addressed by its
// column, unless a visible field has that name.
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
		if name, ok := serverFieldName(gen, f); ok {
			if _, taken := m[name]; !taken {
				m[name] = f
			}
		}
	}
	return m
}

// hiddenServerPaths returns the patch paths of T's server fields that the
// field-name tag hides.
func hiddenServerPaths(gen *core.SchemaGenerator, walk *core.StructFields) map[string]bool {
	hidden := map[string]bool{}
	for _, f := range walk.Fields {
		if !f.Hidden() {
			continue
		}
		if name, ok := serverFieldName(gen, f); ok {
			hidden[core.FormatPointer(name)] = true
		}
	}
	return hidden
}

// Apply applies a parsed patch to target and returns the patched value. It
// never mutates target: when T is a pointer the patch applies to a copy of
// the pointee, so an operation that fails part-way leaves the caller's value
// untouched. A patch no parser produced is ErrUnparsed.
func (a *Applier[T]) Apply(parsed *ParsedPatch, target T) (T, error) {
	var zero T
	if err := checkParsed(parsed); err != nil {
		return zero, err
	}
	target = copyPointee(target)
	if rv := reflect.ValueOf(target); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return zero, errors.New("patch: cannot apply a patch to a nil pointer")
	}
	if canApplyDirect(parsed) {
		return a.applyDirect(parsed, target)
	}
	// The document path round-trips T through its field-name tag, which a
	// hidden server field is not part of: the op would silently not apply.
	for _, op := range parsed.Operations {
		if a.hidden[op.Path] {
			return zero, fmt.Errorf("patch: %s is a server field without a %q name; set it with a top-level replace", op.Path, a.fieldNameTag)
		}
	}
	return a.applyDocument(parsed, target)
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

// canApplyDirect reports a patch whose operations are all top-level replace,
// add or remove (no nested paths, merge objects, move, copy or test),
// ordinary scalar merge patches included.
func canApplyDirect(parsed *ParsedPatch) bool {
	for _, op := range parsed.Operations {
		if op.Op != PatchOpReplace && op.Op != PatchOpRemove && op.Op != PatchOpAdd {
			return false
		}
		if op.Merge {
			// An RFC 7396 object deep-merges into the existing value; a
			// whole-field set would drop sibling keys.
			return false
		}
		if tokens, err := core.ParsePointer(op.Path); err != nil || len(tokens) != 1 {
			return false
		}
	}
	return true
}

// applyDirect sets struct fields directly via reflection, avoiding all
// serialization.
func (a *Applier[T]) applyDirect(parsed *ParsedPatch, target T) (T, error) {
	var zero T
	rv := reflect.ValueOf(&target).Elem()
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	for _, op := range parsed.Operations {
		tokens, err := core.ParsePointer(op.Path)
		if err != nil || len(tokens) != 1 {
			return zero, fmt.Errorf("patch: direct apply: path %q is not a top-level field", op.Path)
		}
		fieldName := tokens[0]
		fv, err := a.settable(rv, fieldName, op.Path)
		if err != nil {
			return zero, err
		}
		if op.Schema.SQLType.IsJSON() {
			value := op.Value
			if op.Op == PatchOpRemove || value == nil {
				value = op.Schema.clearedValue()
			}
			if err := assignStoredJSON(fv, value); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", fieldName, err), Path: op.Path, Cause: err}
			}
			continue
		}
		if op.Op == PatchOpRemove || op.Value == nil {
			if cleared := op.Schema.clearedValue(); cleared != nil {
				fv.Set(reflect.ValueOf(cleared))
			} else {
				fv.SetZero()
			}
			continue
		}
		if err := assignValue(fv, op.Value, a.codec); err != nil {
			return zero, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("setting field %s: %v", fieldName, err),
				Path:    op.Path, Cause: err,
			}
		}
	}
	return target, nil
}

// settable is T's field for a top-level patch path name, ready to set. The
// schema validated the path, so a field that is missing or cannot be set is
// drift between the schema's field tags and T: an error, never a skipped
// operation.
func (a *Applier[T]) settable(rv reflect.Value, name, path string) (reflect.Value, error) {
	field, ok := a.fields[name]
	if !ok {
		return reflect.Value{}, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(),
			Message: fmt.Sprintf("schema-validated path %q has no matching %q-tagged struct field on %s", path, a.fieldNameTag, rv.Type()),
			Path:    path,
		}
	}
	fv, err := detachedField(rv, field)
	if err == nil && !fv.CanSet() {
		err = fmt.Errorf("field %s cannot be set", field.GoName)
	}
	if err != nil {
		return reflect.Value{}, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(), Message: fmt.Sprintf("patch field %q is not writable: %v", name, err), Path: path, Cause: err}
	}
	return fv, nil
}

// detachedField copies the embedded pointers along the write path. A shallow
// copy of the row still shares those pointees with the caller. Copying from
// the current row preserves earlier writes to siblings of this field.
func detachedField(v reflect.Value, field *core.Field) (reflect.Value, error) {
	for _, index := range field.Route {
		if v.Kind() == reflect.Pointer {
			if !v.CanSet() {
				return reflect.Value{}, fmt.Errorf("field %s: cannot detach the unexported embedded %s it is promoted from", field.GoName, v.Type().Elem())
			}
			parent := reflect.New(v.Type().Elem())
			if !v.IsNil() {
				parent.Elem().Set(v.Elem())
			}
			v.Set(parent)
			v = parent.Elem()
		}
		v = v.Field(index)
	}
	return v, nil
}

// assignStoredJSON stores a declared JSON column's value with the decoder SQL
// row scanning uses, so a value applied in Go reads as one read back from the
// database. The request codec still owns request parsing.
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
// recursive conversion: integers and floats are range-checked against dst's
// exact kind (no wrap, no truncation), strings reach encoding.TextUnmarshaler
// destinations (time.Time, uuid.UUID), pointers are allocated and assigned
// through, and composite kinds (slices, maps, structs) decode via the codec,
// whose decoder range-checks every leaf.
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
func (a *Applier[T]) checkDocumentFieldNames(parsed *ParsedPatch) error {
	if a.fieldNameTag == core.JSONTagKey {
		return nil
	}
	switch a.codec.(type) {
	case core.StdlibJSON, *core.StdlibJSON:
	default:
		return nil
	}
	jsonWalk, err := core.StructFieldsOf(reflect.TypeFor[T](), core.JSONTagKey, a.gen.ColumnTagKey())
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
			field, ok := a.fields[tokens[0]]
			if !ok {
				continue
			}
			i := slices.IndexFunc(jsonWalk.Fields, func(f *core.Field) bool { return slices.Equal(f.Route, field.Route) })
			if i < 0 || jsonWalk.Fields[i].External != tokens[0] {
				name := "(none)"
				if i >= 0 {
					name = jsonWalk.Fields[i].External
				}
				return fmt.Errorf("patch: field %s uses %q name %q but the JSON codec uses %q; configure a matching codec for document operations", field.GoName, a.fieldNameTag, tokens[0], name)
			}
		}
	}
	return nil
}

// applyDocument applies a patch through the target's document form: encode
// with the codec (whose field names match the schema's field tag) into a
// number-preserving map, apply each run of RFC 6902 operations with
// json-patch and each RFC 7396 merge object with jsonmerge, then decode the
// changed fields through their storage or request codec. Untouched fields
// keep their values without another decoder invocation.
func (a *Applier[T]) applyDocument(parsed *ParsedPatch, target T) (T, error) {
	var zero T
	if err := a.checkDocumentFieldNames(parsed); err != nil {
		return zero, err
	}
	data, err := a.codec.Encode(target)
	if err != nil {
		return zero, fmt.Errorf("patch: encoding target: %w", err)
	}
	var doc map[string]any
	if err = a.codec.Decode(data, &doc); err != nil {
		return zero, fmt.Errorf("patch: decoding target to a document: %w", err)
	}

	run := &jsonPatchRun{codec: a.codec, copyBudget: a.copyBudget}
	ops := parsed.Operations
	for i := 0; i < len(ops); {
		op := ops[i]
		switch {
		case op.Merge:
			// RFC 7396: nested keys merge, nested nulls delete, siblings
			// survive. Merge operations are top-level.
			tokens, pointerErr := core.ParsePointer(op.Path)
			if pointerErr != nil || len(tokens) != 1 {
				return zero, fmt.Errorf("patch: merge path %q is not a top-level field", op.Path)
			}
			value, err := documentValue(op)
			if err != nil {
				return zero, err
			}
			doc[tokens[0]] = jsonmerge.Apply(doc[tokens[0]], value)
			i++
		case op.Op == PatchOpTest:
			// RFC 6902 §4.6: the target must exist and equal the value as
			// JSON — numbers by value, so 1.0 equals 1. json-patch compares
			// numbers by their spelling and treats a missing member as null,
			// so the test is decided here, against the document as it stands
			// at this step.
			if err = testValue(doc, op, i); err != nil {
				return zero, err
			}
			i++
		case op.Op == PatchOpCopy || op.Op == PatchOpMove:
			value, exists := pointerValue(doc, op.From)
			if exists {
				if err = validateTransferValue(op, value, a.codec, i); err != nil {
					return zero, err
				}
			}
			w, err := wireOp(op)
			if err != nil {
				return zero, err
			}
			doc, err = run.apply(doc, []map[string]any{w}, []int{i})
			if err != nil {
				return zero, err
			}
			i++
		case op.Op == PatchOpArrayAppend:
			// Translated to `add <path>/-`, preceded by `add <path> []` when
			// the array is missing or null at this step: the SQL backends'
			// COALESCE to empty.
			steps := []map[string]any{addOp(op.Path+"/-", op.Value)}
			if current, ok := pointerValue(doc, op.Path); !ok || current == nil {
				steps = append([]map[string]any{addOp(op.Path, []any{})}, steps...)
			}
			doc, err = run.apply(doc, steps, slices.Repeat([]int{i}, len(steps)))
			if err != nil {
				return zero, err
			}
			i++
		default:
			j := i
			var (
				wire    []map[string]any
				indexes []int
			)
			for j < len(ops) && !ops[j].Merge && ops[j].Op != PatchOpTest && ops[j].Op != PatchOpArrayAppend && ops[j].Op != PatchOpCopy && ops[j].Op != PatchOpMove {
				w, err := wireOp(ops[j])
				if err != nil {
					return zero, err
				}
				wire = append(wire, w)
				indexes = append(indexes, j)
				j++
			}
			doc, err = run.apply(doc, wire, indexes)
			if err != nil {
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
		if op.Op == PatchOpTest {
			continue
		}
		paths := []string{op.Path}
		if op.Op == PatchOpMove {
			paths = append(paths, op.From)
		}
		for _, path := range paths {
			tokens, err := core.ParsePointer(path)
			if err != nil || len(tokens) == 0 {
				return zero, fmt.Errorf("patch: invalid field path %q", path)
			}
			name := tokens[0]
			field, ok := a.fields[name]
			if !ok {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(), Message: fmt.Sprintf("patch path %q has no writable struct field", path), Path: path}
			}
			if _, seen := changed[name]; !seen {
				info, err := a.gen.FieldType(field)
				if err != nil {
					return zero, fmt.Errorf("patch: field %s: %w", name, err)
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
		out, err := a.codec.Encode(native)
		if err != nil {
			return zero, fmt.Errorf("patch: encoding patched document: %w", err)
		}
		if err := a.codec.Decode(out, &result); err != nil {
			return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("patched document does not fit %T: %v", result, err), Cause: err}
		}
	}
	preserved := copyPointee(target)
	dst := reflect.ValueOf(&preserved).Elem()
	if dst.Kind() == reflect.Pointer {
		dst = dst.Elem()
	}
	for _, name := range names {
		change := changed[name]
		d, err := a.settable(dst, name, core.FormatPointer(name))
		if err != nil {
			return zero, err
		}
		if change.storage.IsJSON() {
			if err := assignStoredJSON(d, doc[name]); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", name, err), Path: core.FormatPointer(name), Cause: err}
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

// validateTransferValue validates a copy's or move's source value, resolved
// from the document as the operation applies (after the operations before
// it), against its destination.
func validateTransferValue(op *PatchOperation, value any, codec core.Codec, index int) error {
	tokens, err := core.ParsePointer(op.Path)
	if err == nil {
		err = checkNullability(op.Schema, PatchOpAdd, value, len(tokens))
	}
	if err == nil {
		value, err = normalizeOpValue(op.Schema, PatchOpAdd, value, len(tokens), codec)
	}
	if err == nil {
		for _, validate := range op.Schema.Validators {
			if err = validate(value); err != nil {
				break
			}
		}
	}
	if err != nil {
		return &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("invalid transfer to path %q at index %d: %v", op.Path, index, err), Path: op.Path, Index: new(index), Cause: err}
	}
	return nil
}

// testValue evaluates a test operation against doc.
func testValue(doc map[string]any, op *PatchOperation, index int) error {
	fail := func(msg string) error {
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
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
	value, err := documentValue(op)
	if err != nil {
		return err
	}
	want, err := core.NormalizeJSON(value)
	if err != nil {
		return fail(fmt.Sprintf("test value: %v", err))
	}
	if !core.JSONEqual(have, want) {
		return fail(fmt.Sprintf("value at %s does not equal the test value", op.Path))
	}
	return nil
}

// wireOp renders a parsed operation as an RFC 6902 operation object.
func wireOp(op *PatchOperation) (map[string]any, error) {
	topLevel := false
	if tokens, err := core.ParsePointer(op.Path); err == nil && len(tokens) == 1 {
		topLevel = true
	}
	// Clearing a top-level slice or map empties it (clearedValue); add also
	// covers a member the encoding omitted.
	if topLevel && (op.Op == PatchOpRemove || op.Value == nil) && op.Op != PatchOpMove && op.Op != PatchOpCopy && op.Op != PatchOpTest {
		if cleared := op.Schema.clearedValue(); cleared != nil {
			return addOp(op.Path, cleared), nil
		}
	}
	value, err := documentValue(op)
	if err != nil {
		return nil, err
	}
	// Top-level schema fields exist even when omitempty omits their current
	// value, so replacing or clearing one adds it, as the direct path sets it;
	// nested JSON members keep RFC 6902's existence requirements.
	if topLevel {
		switch op.Op {
		case PatchOpReplace:
			return addOp(op.Path, value), nil
		case PatchOpRemove:
			return addOp(op.Path, nil), nil
		}
	}
	w := map[string]any{rfc6902.Op: string(op.Op), rfc6902.Path: op.Path}
	switch op.Op {
	case PatchOpMove, PatchOpCopy:
		w[rfc6902.From] = op.From
	case PatchOpRemove:
	default:
		w[rfc6902.Value] = value
	}
	return w, nil
}

// documentValue is op's value as the target's JSON document holds it: a
// quoted field's (PathSchema.Wire) in its wire form, which the document
// decodes back into the field from.
func documentValue(op *PatchOperation) (any, error) {
	v, err := core.ToWire(op.Schema.Wire, op.Value)
	if err != nil {
		return nil, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("value for path %s: %v", op.Path, err), Path: op.Path, Cause: err}
	}
	return v, nil
}

// addOp is the RFC 6902 operation adding value at path.
func addOp(path string, value any) map[string]any {
	return map[string]any{rfc6902.Op: string(PatchOpAdd), rfc6902.Path: path, rfc6902.Value: value}
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
