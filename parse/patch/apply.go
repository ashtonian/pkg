package patch

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"github.com/ashtonian/pkg/gowalker"
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
	// unwritable are the names in fields that core.Field.CheckWritable
	// refuses, sorted (CheckSchema).
	unwritable []string
	// hidden are the patch paths of the server fields the field-name tag
	// hides, which only the direct path can set.
	hidden map[string]bool
	// presented maps each top-level field name whose tag can omit it
	// (omitempty, omitzero) to a one-field struct of the field's type,
	// tagged as the field is without those options. The document path
	// writes an omitted field through it, so test, copy and move read every
	// field that replace and remove treat as existing (wireOp).
	presented map[string]reflect.Type
	// codecNames maps each name in fields to the name the default JSON
	// codec writes the same field under, when the field-name tag is not
	// json; nil when the codec names fields as the schemas do.
	// codecNamesErr is why that codec cannot name T's fields. The document
	// path reports either rather than silently writing another key.
	codecNames    map[string]string
	codecNamesErr error
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
	a := &Applier[T]{
		codec:        cfg.GetCodec(),
		copyBudget:   cfg.MaxPatchCopyBytes,
		gen:          gen,
		fieldNameTag: gen.FieldNameTag(),
		fields:       patchFields(gen, walk),
		hidden:       hiddenServerPaths(gen, walk),
	}
	a.unwritable = unwritableFields(a.fields)
	a.presented = presentedForms[T](a.fieldNameTag, a.fields)
	if a.fieldNameTag == core.JSONTagKey {
		return a, nil
	}
	switch a.codec.(type) {
	case core.StdlibJSON, *core.StdlibJSON:
		a.codecNames, a.codecNamesErr = jsonCodecNames[T](gen, a.fields)
	}
	return a, nil
}

// patchFields maps each top-level patch field name to T's field, by the
// same walk the schemas came from. A hidden server field is addressed by its
// column, unless a visible field has that name.
func patchFields(gen *core.SchemaGenerator, walk *core.StructFields) map[string]*core.Field {
	m := make(map[string]*core.Field, walk.Len())
	for f := range walk.Fields() {
		if !f.Hidden() {
			m[f.External()] = f
		}
	}
	for f := range walk.Fields() {
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

// unwritableFields returns the names of the fields a patch cannot write
// (core.Field.CheckWritable), sorted.
func unwritableFields(fields map[string]*core.Field) []string {
	var names []string
	for name, f := range fields {
		if f.CheckWritable() != nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// presentedForms builds Applier.presented: a form for each visible field
// whose json or field-name tag can omit it. A codec names fields by one of
// those tags, so the form carries both, without their omit options.
func presentedForms[T any](fieldNameTag string, fields map[string]*core.Field) map[string]reflect.Type {
	root := reflect.TypeFor[T]()
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	forms := make(map[string]reflect.Type)
	for name, f := range fields {
		if f.Hidden() {
			continue
		}
		tag, omits := presentedTag(root.FieldByIndex(f.Route()).Tag, core.JSONTagKey, fieldNameTag)
		if !omits {
			continue
		}
		forms[name] = reflect.StructOf([]reflect.StructField{{Name: "V", Type: f.Type(), Tag: tag}})
	}
	return forms
}

// presentedTag is tag's entries for keys with their omitempty and omitzero
// options dropped, and whether any had one.
func presentedTag(tag reflect.StructTag, keys ...string) (reflect.StructTag, bool) {
	var entries []string
	omits := false
	for _, key := range slices.Compact(slices.Sorted(slices.Values(keys))) {
		value, ok := tag.Lookup(key)
		if !ok {
			continue
		}
		name, options, _ := strings.Cut(value, ",")
		kept := []string{name}
		for option := range strings.SplitSeq(options, ",") {
			switch option {
			case "":
			case jsonOmitEmpty, jsonOmitZero:
				omits = true
			default:
				kept = append(kept, option)
			}
		}
		entries = append(entries, key+":"+strconv.Quote(strings.Join(kept, ",")))
	}
	return reflect.StructTag(strings.Join(entries, " ")), omits
}

// presentOmitted writes into doc, the document of target, each top-level
// field an operation names that the encoding omitted, as its presented
// form (Applier.presented) encodes the value it holds. A pointer T encodes
// its form through a pointer too, so a MarshalJSON with a pointer receiver
// writes the field as it would in T.
func (a *Applier[T]) presentOmitted(doc map[string]any, target T, ops []*PatchOperation) error {
	if len(a.presented) == 0 {
		return nil
	}
	root := reflect.ValueOf(&target).Elem()
	throughPointer := root.Kind() == reflect.Pointer
	for root.Kind() == reflect.Pointer {
		if root.IsNil() {
			return nil
		}
		root = root.Elem()
	}
	for _, op := range ops {
		for _, path := range []string{op.Path, op.From} {
			tokens, err := core.ParsePointer(path)
			if err != nil || len(tokens) == 0 {
				continue
			}
			name := tokens[0]
			form, ok := a.presented[name]
			if !ok {
				continue
			}
			_, exists := doc[name]
			if exists {
				continue
			}
			value, err := a.fields[name].Value(root)
			if err != nil {
				continue // behind a nil embedded pointer, the field is not there
			}
			holder := reflect.New(form)
			holder.Elem().Field(0).Set(value)
			encoded := holder.Elem().Interface()
			if throughPointer {
				encoded = holder.Interface()
			}
			data, err := a.codec.Encode(encoded)
			if err != nil {
				return fmt.Errorf("patch: encoding field %s: %w", name, err)
			}
			var member map[string]any
			err = a.codec.Decode(data, &member)
			if err != nil {
				return fmt.Errorf("patch: decoding field %s: %w", name, err)
			}
			for _, v := range member {
				doc[name] = v
			}
		}
	}
	return nil
}

// presentWrapped writes each nullable wrapper column an operation names
// (wrapperField) into doc as the column holds it, its value or null, where
// the codec wrote the wrapper's own fields: test, copy and move read the
// value replace writes, and the field is set back through assignWrapped.
func (a *Applier[T]) presentWrapped(doc map[string]any, target T, ops []*PatchOperation) error {
	root := reflect.ValueOf(&target).Elem()
	for root.Kind() == reflect.Pointer {
		if root.IsNil() {
			return nil
		}
		root = root.Elem()
	}
	for _, op := range ops {
		for _, path := range []string{op.Path, op.From} {
			tokens, err := core.ParsePointer(path)
			if err != nil || len(tokens) == 0 {
				continue
			}
			name := tokens[0]
			w, ok := wrapperField(a.fields[name])
			if !ok {
				continue
			}
			value, err := a.fields[name].Value(root)
			if err != nil {
				continue // behind a nil embedded pointer, the field is not there
			}
			doc[name], err = a.wrappedDocument(w, value)
			if err != nil {
				return fmt.Errorf("patch: encoding field %s: %w", name, err)
			}
		}
	}
	return nil
}

// wrappedDocument is the document value of v, a wrapper or a pointer to
// one: its value as the codec writes it, nil while it is NULL.
func (a *Applier[T]) wrappedDocument(w core.NullWrapper, v reflect.Value) (any, error) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	held, valid := w.Held(v)
	if !valid {
		return nil, nil
	}
	data, err := a.codec.Encode(held.Interface())
	if err != nil {
		return nil, err
	}
	var doc any
	err = a.codec.Decode(data, &doc)
	return doc, err
}

// assignWrapped sets dst, a wrapper field, from its document value: NULL
// for nil, else the value decoded into the wrapper's value type.
func (a *Applier[T]) assignWrapped(dst reflect.Value, w core.NullWrapper, doc any) error {
	if doc == nil {
		dst.SetZero()
		return nil
	}
	data, err := a.codec.Encode(doc)
	if err != nil {
		return err
	}
	value := reflect.New(w.ValueType())
	err = a.codec.Decode(data, value.Interface())
	if err != nil {
		return err
	}
	return assignValue(dst, value.Elem().Interface(), a.codec)
}

// wrapperField is the layout of f's type, pointers stripped, when it is a
// nullable wrapper; false for a nil field.
func wrapperField(f *core.Field) (core.NullWrapper, bool) {
	if f == nil {
		return core.NullWrapper{}, false
	}
	t := f.Type()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return core.NullWrapperOf(t)
}

// CheckSchema refuses a schema with a path that would write a field of T
// the applier cannot write: one promoted through an unexported embedded
// pointer (core.Field.CheckWritable), reached by an exact key or a pattern.
// GeneratePatchSchema refuses such a field's tags; this refuses a path added
// by hand (PatchSchema.AddPath). NewResource checks its schemas once they
// are customized; a caller composing a schema and an applier itself checks
// the finished schema, or Apply fails the write.
func (a *Applier[T]) CheckSchema(schema *PatchSchema) error {
	if schema == nil {
		return nil
	}
	for _, name := range a.unwritable {
		key, ok := schema.reaching(core.FormatPointer(name))
		if !ok {
			continue
		}
		err := a.fields[name].CheckWritable()
		return fmt.Errorf("patch schema %s: path %s: %w", schema.Resource, key, err)
	}
	return nil
}

// hiddenServerPaths returns the patch paths of T's server fields that the
// field-name tag hides.
func hiddenServerPaths(gen *core.SchemaGenerator, walk *core.StructFields) map[string]bool {
	hidden := map[string]bool{}
	for f := range walk.Fields() {
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
// never writes target or anything target points to: the patch writes a
// workingCopy, so a patch that succeeds and one that fails part-way alike
// leave the caller's value, and any pointer it kept to an embedded parent,
// as they were. A patch no parser produced is ErrUnparsed.
func (a *Applier[T]) Apply(parsed *ParsedPatch, target T) (T, error) {
	var zero T
	if err := checkParsed(parsed); err != nil {
		return zero, err
	}
	rv := reflect.ValueOf(target)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
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

// workingCopy is the copy of a target that one Apply writes. It starts
// shallow: an embedded pointer a field is promoted through still points into
// the caller's value until field gives the copy its own parent.
type workingCopy[T any] struct {
	value T
	// root is value's struct, addressable.
	root reflect.Value
	// owned are the embedded parents field allocated.
	owned []unsafe.Pointer
}

func newWorkingCopy[T any](target T) *workingCopy[T] {
	w := &workingCopy[T]{value: target}
	w.root = reflect.ValueOf(&w.value).Elem()
	if w.root.Kind() == reflect.Pointer {
		pointee := reflect.New(w.root.Type().Elem())
		pointee.Elem().Set(w.root.Elem())
		w.root.Set(pointee)
		w.root = pointee.Elem()
	}
	return w
}

// field returns f's field in the copy, ready to write. Each embedded pointer
// on f's route is first pointed at a parent the copy owns: a nil one at a new
// parent, as encoding/json allocates it, and one the copy still shares with
// the caller at a copy of that parent. A parent is copied once, so every
// write through it lands in the same copy. A field promoted through an
// unexported embedded pointer, whose parent can be neither, is refused
// before anything is written.
func (w *workingCopy[T]) field(f *core.Field) (reflect.Value, error) {
	err := f.CheckWritable()
	if err != nil {
		return reflect.Value{}, err
	}
	v, err := f.SettableThrough(w.root, w.own)
	if err != nil {
		return reflect.Value{}, err
	}
	if !v.CanSet() {
		return reflect.Value{}, fmt.Errorf("field %s cannot be set", f.GoName())
	}
	return v, nil
}

// own points p, an embedded pointer in the copy, at a parent the copy owns.
// field has refused every route through an unexported embedded pointer, so
// p is settable.
func (w *workingCopy[T]) own(p reflect.Value) error {
	if !p.IsNil() && slices.Contains(w.owned, p.UnsafePointer()) {
		return nil
	}
	parent := reflect.New(p.Type().Elem())
	if !p.IsNil() {
		parent.Elem().Set(p.Elem())
	}
	p.Set(parent)
	w.owned = append(w.owned, parent.UnsafePointer())
	return nil
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
	out := newWorkingCopy(target)
	for _, op := range parsed.Operations {
		tokens, err := core.ParsePointer(op.Path)
		if err != nil || len(tokens) != 1 {
			return zero, fmt.Errorf("patch: direct apply: path %q is not a top-level field", op.Path)
		}
		fieldName := tokens[0]
		fv, err := a.writable(out, fieldName, op.Path)
		if err != nil {
			return zero, err
		}
		value, cleared := wholeValue(op)
		if op.Schema.SQLType.IsJSON() {
			if err := assignStoredJSON(fv, value); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", fieldName, err), Path: op.Path, Cause: err}
			}
			continue
		}
		if cleared {
			if value == nil {
				fv.SetZero()
			} else {
				// clearedValue is a fresh value on every call: the result's own.
				fv.Set(reflect.ValueOf(value))
			}
			continue
		}
		err = assignValue(fv, value, a.codec)
		if err != nil {
			invalid := &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(),
				Message: fmt.Sprintf("setting field %s: %v", fieldName, err),
				Path:    op.Path, Cause: err,
			}
			return zero, invalid
		}
	}
	return out.value, nil
}

// writable is the working copy's field for a top-level patch path name,
// ready to set. The schema validated the path, so a field that is missing or
// cannot be set is drift between the schema's field tags and T: an error,
// never a skipped operation. A field the copy cannot write is no
// core.ParseError: construction refuses its path (GeneratePatchSchema,
// CheckSchema), so only a schema completed by hand and never checked
// reaches it, the server's fault and not the request's.
func (a *Applier[T]) writable(w *workingCopy[T], name, path string) (reflect.Value, error) {
	field, ok := a.fields[name]
	if !ok {
		return reflect.Value{}, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodePatchFieldNotFound.String(),
			Message: fmt.Sprintf("schema-validated path %q has no matching %q-tagged struct field on %s", path, a.fieldNameTag, gowalker.TypeText(w.root.Type())),
			Path:    path,
		}
	}
	fv, err := w.field(field)
	if err != nil {
		return reflect.Value{}, fmt.Errorf("patch: writing %s: %w", path, err)
	}
	return fv, nil
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
// through, a nullable wrapper (core.NullWrapperOf) holds the value Valid,
// and composite kinds (slices, maps, structs) decode via the codec,
// whose decoder range-checks every leaf. dst never shares memory with v: a
// value that can (a map, a slice, an interface holding a document) is
// decoded afresh, so each result is its own, and changing it changes
// neither the patch nor another result.
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
	if rv.Type() == t && !sharesMemory(t) {
		dst.Set(rv)
		return nil
	}
	w, wrapped := core.NullWrapperOf(t)
	if wrapped {
		// A wrapper's column holds its value (columnType), which the
		// wrapper holds Valid.
		held := reflect.New(t).Elem()
		err := assignValue(w.Set(held), v, codec)
		if err != nil {
			return err
		}
		dst.Set(held)
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
			return fmt.Errorf("assigning %s to %s: incompatible types", gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(t))
		}
		dst.SetString(s)
		return nil
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return fmt.Errorf("assigning %s to %s: incompatible types", gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(t))
		}
		dst.SetBool(b)
		return nil
	case reflect.Interface:
		// A scalar is shared safely, and no document decodes into an
		// interface with methods: it holds the value parsed.
		if rv.Type().AssignableTo(t) && (!sharesMemory(rv.Type()) || t.NumMethod() > 0) {
			dst.Set(rv)
			return nil
		}
	}
	data, err := codec.Encode(v)
	if err != nil {
		return fmt.Errorf("encoding %s for %s: %w", gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(t), err)
	}
	decoded := reflect.New(t)
	if err := codec.Decode(data, decoded.Interface()); err != nil {
		return fmt.Errorf("assigning %s to %s: %w", gowalker.TypeText(reflect.TypeOf(v)), gowalker.TypeText(t), err)
	}
	dst.Set(decoded.Elem())
	return nil
}

// sharesMemory reports whether two values of t can share memory that one
// of them changes: t is, or holds in an element or a field, a map, slice,
// pointer, interface, channel or function. A string shares only memory no
// one changes.
func sharesMemory(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Array:
		return sharesMemory(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			if sharesMemory(t.Field(i).Type) {
				return true
			}
		}
	}
	return false
}

// checkDocumentFieldNames catches the default JSON codec paired with a
// different schema tag before a document operation can silently hit another
// key. Custom codecs must encode the configured field-name tag themselves.
func (a *Applier[T]) checkDocumentFieldNames(parsed *ParsedPatch) error {
	if a.codecNamesErr != nil {
		return a.codecNamesErr
	}
	if a.codecNames == nil {
		return nil
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
			name, ok := a.codecNames[tokens[0]]
			if !ok || name == tokens[0] {
				continue
			}
			return fmt.Errorf("patch: field %s uses %q name %q but the JSON codec uses %q; configure a matching codec for document operations", a.fields[tokens[0]].GoName(), a.fieldNameTag, tokens[0], name)
		}
	}
	return nil
}

// jsonCodecNames maps each name in fields to the name encoding/json, the
// default codec, writes the same Go field under: "" when its json tag hides
// the field, "(none)" when it does not write the field at all.
func jsonCodecNames[T any](gen *core.SchemaGenerator, fields map[string]*core.Field) (map[string]string, error) {
	jsonWalk, err := core.StructFieldsOf(reflect.TypeFor[T](), core.JSONTagKey, gen.ColumnTagKey())
	if err != nil {
		return nil, fmt.Errorf("patch: the JSON codec cannot name T's fields: %w", err)
	}
	type routed struct {
		route []int
		name  string
	}
	jsonFields := make([]routed, 0, jsonWalk.Len())
	for f := range jsonWalk.Fields() {
		jsonFields = append(jsonFields, routed{f.Route(), f.External()})
	}
	names := make(map[string]string, len(fields))
	for name, field := range fields {
		route := field.Route()
		i := slices.IndexFunc(jsonFields, func(j routed) bool { return slices.Equal(j.route, route) })
		names[name] = "(none)"
		if i >= 0 {
			names[name] = jsonFields[i].name
		}
	}
	return names, nil
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
	err = a.presentOmitted(doc, target, parsed.Operations)
	if err != nil {
		return zero, err
	}
	err = a.presentWrapped(doc, target, parsed.Operations)
	if err != nil {
		return zero, err
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
			value, err := wireValue(op)
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
		_, wrapped := wrapperField(changed[name].field)
		if !changed[name].storage.IsJSON() && !wrapped {
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
		err = a.codec.Decode(out, &result)
		if err != nil {
			unfit := &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("patched document does not fit %s: %v", gowalker.TypeText(reflect.TypeFor[T]()), err), Cause: err}
			return zero, unfit
		}
	}
	out := newWorkingCopy(target)
	for _, name := range names {
		change := changed[name]
		d, err := a.writable(out, name, core.FormatPointer(name))
		if err != nil {
			return zero, err
		}
		if change.storage.IsJSON() {
			if err := assignStoredJSON(d, doc[name]); err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting JSON field %s: %v", name, err), Path: core.FormatPointer(name), Cause: err}
			}
			continue
		}
		w, wrapped := wrapperField(change.field)
		if wrapped {
			err := a.assignWrapped(d, w, doc[name])
			if err != nil {
				return zero, &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("setting field %s: %v", name, err), Path: core.FormatPointer(name), Cause: err}
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
		value, err := change.field.Value(src)
		if err != nil {
			d.SetZero()
			continue
		}
		d.Set(value)
	}
	return out.value, nil
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
	value, err := wireValue(op)
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
	// Top-level schema fields exist even when omitempty omits their current
	// value, so writing or clearing one adds the value it leaves
	// (wholeValue), as the direct path sets it; nested JSON members keep
	// RFC 6902's existence requirements.
	if topLevel && (op.Op == PatchOpAdd || op.Op == PatchOpReplace || op.Op == PatchOpRemove) {
		value, cleared := wholeValue(op)
		if cleared {
			return addOp(op.Path, value), nil
		}
		wire, err := wireValue(op)
		if err != nil {
			return nil, err
		}
		return addOp(op.Path, wire), nil
	}
	value, err := wireValue(op)
	if err != nil {
		return nil, err
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

// wireValue is op's value as the target's JSON document holds it: a
// quoted field's (PathSchema.Wire) in its wire form, which the document
// decodes back into the field from.
func wireValue(op *PatchOperation) (any, error) {
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
