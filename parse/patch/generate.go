package patch

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/jsonkeys"
)

// GeneratePatchSchema generates a PatchSchema from struct tags on type T.
func GeneratePatchSchema[T any](g *core.SchemaGenerator) (*PatchSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generatePatchSchema(g, t, false)
}

// GenerateServerPatchSchema generates the patch schema of the server's own
// writes: every field GeneratePatchSchema admits, and every field tagged
// `parse:"server"` — a column only the server sets, such as a credential's
// fingerprint, which a request body must never reach. A server field the
// field-name tag hides (`json:"-"`) is addressed by its column.
func GenerateServerPatchSchema[T any](g *core.SchemaGenerator) (*PatchSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generatePatchSchema(g, t, true)
}

// serverFieldName returns the patch path name of a server field: its
// external name, or its column when the field-name tag hides it. ok is false
// for a field that is not a server field.
func serverFieldName(g *core.SchemaGenerator, field *core.Field) (name string, ok bool) {
	if !field.Tags().Parse.Server {
		return "", false
	}
	if field.External() != "" {
		return field.External(), true
	}
	col, ok, err := g.FieldColumn(field)
	return col, ok && err == nil
}

func generatePatchSchema(g *core.SchemaGenerator, t reflect.Type, server bool) (*PatchSchema, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %s", t.Kind())
	}

	tableName, err := g.TableName(t)
	if err != nil {
		return nil, err
	}
	walk, err := g.StructFields(t)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]*PathSchema)

	for field := range walk.Fields() {
		tags := field.Tags()
		serverField := server && tags.Parse.Server
		if !tags.Parse.Patch && !serverField {
			continue
		}
		bounds := field.Bounds()
		if tags.Parse.Patch && tags.Immutable {
			// Nothing creates rows through a patch, so every patch of the
			// field would change it. The server's own write is not a
			// request's: a server field may stay immutable.
			return nil, fmt.Errorf("field %s: an immutable field cannot have the patch capability", field.GoName())
		}
		// Refused here rather than when a request writes it: the path would
		// be offered and then fail every write. A value decodes into the
		// field's type, so a member encoding/json cannot decode fails it too.
		err = field.CheckWritable()
		if err != nil {
			return nil, err
		}
		err = jsonkeys.CheckDecodable(field.Type())
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
		}
		fieldName := field.External()
		if serverField {
			fieldName, _ = serverFieldName(g, field)
		}
		if fieldName == "" {
			fieldName = field.RelationName()
		}
		if fieldName == "" {
			continue
		}

		info, err := g.FieldType(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
		}
		fieldType := info.Type

		nullable := acceptsNull(field.Type(), bounds)
		var allowedOps []PatchOp
		if opsTag := tags.PatchOps; opsTag != "" {
			for p := range strings.SplitSeq(opsTag, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				op := PatchOp(p)
				if !IsValidOp(op) {
					return nil, fmt.Errorf("field %s: unknown patch operation %q", field.GoName(), p)
				}
				if op == PatchOpRemove && !nullable && tags.Relation == "" {
					if mustHoldElement(field.Type(), bounds) {
						return nil, fmt.Errorf("field %s: patch operation remove empties the array, which minitems %d refuses", field.GoName(), *bounds.MinItems)
					}
					return nil, fmt.Errorf("field %s: %w (core.IsNullableType: a pointer, slice, map or interface, or a type whose zero value writes NULL and scans NULL back, such as sql.NullString), got %s", field.GoName(), ErrRemoveNeedsNull, gowalker.TypeText(field.Type()))
				}
				if op == PatchOpArrayAppend && isFixedArray(field.Type()) {
					return nil, fmt.Errorf("field %s: array_append cannot grow a fixed-size array (%s)", field.GoName(), gowalker.TypeText(field.Type()))
				}
				allowedOps = append(allowedOps, op)
			}
		} else {
			defaults, configured, err := defaultOpsFor(g, fieldType)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
			}
			if isFixedArray(field.Type()) {
				// A fixed-size array cannot grow.
				defaults = slices.DeleteFunc(defaults, func(op PatchOp) bool { return op == PatchOpArrayAppend })
			}
			switch {
			case !nullable:
				// Only advertise what applies: removing a non-nullable
				// field has no representation on any backend.
				defaults = slices.DeleteFunc(defaults, func(op PatchOp) bool { return op == PatchOpRemove })
			case !configured && len(defaults) > 0 && !slices.Contains(defaults, PatchOpRemove):
				// A nullable scalar can be cleared: a merge-patch null or
				// a remove sets it to NULL.
				defaults = append(defaults, PatchOpRemove)
			}
			allowedOps = defaults
		}

		sqlColumn, err := g.ColumnName(fieldName, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
		}

		var validators []Validator
		enumValues := field.Enum()
		if len(enumValues) > 0 {
			validators = append(validators, enumValidator(enumValues))
		}
		if !bounds.IsZero() {
			if tags.Relation != "" {
				return nil, fmt.Errorf("field %s: minlen, maxlen, min, max, minitems and maxitems do not apply to a relation", field.GoName())
			}
			err = bounds.Check(info)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
			}
			if bounds.BoundsValues() {
				validators = append(validators, boundsValidator(bounds, info))
			}
		}

		// a field carrying both `parse:"patch"` and `relation:"…"`
		// becomes a relation-patch path. Override the allowed-ops to the
		// relation-verb subset (replace / add / remove) and clear SQLColumn
		// so BuildPatchSQL routes it into RelationOps instead of the SET
		// clause.
		var relSchema *core.RelationSchema
		// A target key of several columns: its membership's keys are
		// arrays of a value per column, as a declared relation's.
		var membership *PathSchema
		if tags.Relation != "" {
			rel, err := g.RelationTag(walk, field)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
			}
			if len(rel.TargetColumns) == 1 {
				err = checkRelationKeyType(field.Type())
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
				}
			} else {
				membership, err = tagMembership(g, field, rel)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName(), err)
				}
			}
			relSchema = rel
			allowedOps = relationPatchOps()
			sqlColumn = "" // relation paths don't touch a column
		}

		path := core.FormatPointer(fieldName)
		if _, duplicate := paths[path]; duplicate {
			return nil, fmt.Errorf("field %s: duplicate patch path %q", field.GoName(), path)
		}
		if membership != nil {
			paths[path] = membership
			continue
		}
		paths[path] = &PathSchema{
			AllowedOps: allowedOps,
			DataType:   fieldType,
			SQLType:    info.SQLType,
			GoType:     field.Type(),
			Wire:       field.Wire(),
			SQLColumn:  sqlColumn,
			Validators: validators,
			EnumValues: enumValues,
			Bounds:     bounds,
			Relation:   relSchema,
		}
	}

	return NewPatchSchema(tableName, paths)
}

// defaultOpsFor returns a fresh copy of the default operations for a field
// type: GeneratorOptions.DefaultPatchOps when it names the type (configured),
// otherwise the package defaults. Every schema owns its slice — sharing the
// package-level slice let configuring one resource rewrite another's allowed
// operations, and race with its requests.
func defaultOpsFor(g *core.SchemaGenerator, ft core.FieldType) (ops []PatchOp, configured bool, err error) {
	if names, ok := g.DefaultPatchOps(ft); ok {
		ops := make([]PatchOp, 0, len(names))
		for _, name := range names {
			op := PatchOp(strings.TrimSpace(name))
			if !IsValidOp(op) {
				return nil, true, fmt.Errorf("DefaultPatchOps[%s]: unknown patch operation %q", ft, name)
			}
			ops = append(ops, op)
		}
		return ops, true, nil
	}
	return slices.Clone(defaultPatchOps[ft]), false, nil
}

// isFixedArray reports a Go array type (pointers followed): a value of
// exactly its length, never more.
func isFixedArray(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Array
}

// defaultPatchOps maps FieldType to standard patch operators. It is
// unexported so no caller can change another's defaults; DefaultPatchOps
// returns a copy.
//
// FieldTypeArray includes PatchOpArrayAppend so
// every array-shaped field gets atomic single-statement append for
// free. Concrete fields can override via a `patch_ops:"…"` struct
// tag if they need to lock down to a narrower verb set.
var defaultPatchOps = map[core.FieldType][]PatchOp{
	core.FieldTypeString:    {PatchOpReplace},
	core.FieldTypeInt:       {PatchOpReplace},
	core.FieldTypeFloat:     {PatchOpReplace},
	core.FieldTypeDecimal:   {PatchOpReplace},
	core.FieldTypeBool:      {PatchOpReplace},
	core.FieldTypeTimestamp: {PatchOpReplace},
	core.FieldTypeDate:      {PatchOpReplace},
	core.FieldTypeTime:      {PatchOpReplace},
	core.FieldTypeInterval:  {PatchOpReplace},
	core.FieldTypeInet:      {PatchOpReplace},
	core.FieldTypeCIDR:      {PatchOpReplace},
	core.FieldTypeMACAddr:   {PatchOpReplace},
	core.FieldTypeUUID:      {PatchOpReplace},
	core.FieldTypeJSON:      {PatchOpReplace, PatchOpAdd, PatchOpRemove},
	core.FieldTypeArray:     {PatchOpReplace, PatchOpAdd, PatchOpRemove, PatchOpArrayAppend},
	core.FieldTypeObject:    {PatchOpReplace, PatchOpAdd, PatchOpRemove},
}

// DefaultPatchOps returns a defensive copy of the package's default
// FieldType -> patch-ops map. Callers can mutate the returned map without
// affecting the package-level defaults or other callers.
func DefaultPatchOps() map[core.FieldType][]PatchOp {
	out := make(map[core.FieldType][]PatchOp, len(defaultPatchOps))
	for k, v := range defaultPatchOps {
		ops := make([]PatchOp, len(v))
		copy(ops, v)
		out[k] = ops
	}
	return out
}

// relationPatchOps returns the allowed-ops subset for a relation-patch
// path: replace / add / remove. JSON Patch's move / copy / test have no
// translation to setting, extending or shrinking a membership, so the parser
// refuses them on a generated relation path (E2064); BuildPatchSQL refuses
// one a hand-built schema allows (E2117).
func relationPatchOps() []PatchOp {
	return []PatchOp{PatchOpReplace, PatchOpAdd, PatchOpRemove}
}

// RelationKey is a target key column as a membership's keys hold it: the
// column's Go type, and the form its values travel in, core.WireString for
// one tagged json:",string", whose values are JSON strings ("5"), as
// responses write them.
type RelationKey struct {
	Type reflect.Type
	Wire core.Wire
}

// RelationPath is the patch path of a relation's membership: replace, add
// and remove of target keys, routed to RelationOps. key are the target key's
// columns in key order. A one-column key's keys are values of its type, so
// the path's value is a slice of them; a longer key's keys are JSON arrays
// of a value per column, normalized to core.Key values. A key is never
// null, so no column's type may be nullable.
func RelationPath(rel *core.RelationSchema, key ...RelationKey) (*PathSchema, error) {
	if len(key) == 0 {
		return nil, errors.New("a relation's membership names its target key's columns")
	}
	if len(key) == 1 {
		goType := reflect.SliceOf(key[0].Type)
		err := checkRelationKeyType(goType)
		if err != nil {
			return nil, err
		}
		schema := &PathSchema{AllowedOps: relationPatchOps(), DataType: core.TypeOf(goType).Type, GoType: goType, Wire: key[0].Wire, Relation: rel}
		return schema, nil
	}
	for _, k := range key {
		err := checkRelationKeyType(reflect.SliceOf(k.Type))
		if err != nil {
			return nil, err
		}
	}
	schema := &PathSchema{AllowedOps: relationPatchOps(), DataType: core.FieldTypeArray, Relation: rel, RelationKey: slices.Clone(key)}
	return schema, nil
}

// tagMembership is the membership path of a tagged relation whose target
// key has several columns (RelationPath), each column's keys typed, and
// travelling, as the field of the element struct holding it
// (core.SchemaGenerator.RelationTargetFields).
func tagMembership(g *core.SchemaGenerator, field *core.Field, rel *core.RelationSchema) (*PathSchema, error) {
	targets, err := g.RelationTargetFields(field, rel)
	if err != nil {
		return nil, err
	}
	keys := make([]RelationKey, len(targets))
	for i, target := range targets {
		keys[i] = RelationKey{Type: target.Type(), Wire: target.Wire()}
	}
	return RelationPath(rel, keys...)
}

// checkRelationKeyType refuses a membership type that is not a slice of
// keys that cannot be null.
func checkRelationKeyType(t reflect.Type) error {
	if t.Kind() != reflect.Slice {
		return fmt.Errorf("a relation's membership is a slice of keys, not %s", gowalker.TypeText(t))
	}
	if core.IsNullableType(t.Elem()) {
		return fmt.Errorf("a relation's keys are never null, and %s's element %s can hold null; declare a key type without one", gowalker.TypeText(t), gowalker.TypeText(t.Elem()))
	}
	return nil
}
