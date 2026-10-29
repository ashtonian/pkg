package patch

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
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
// field-name tag hides (`json:"-"`) is addressed by ServerFieldName.
func GenerateServerPatchSchema[T any](g *core.SchemaGenerator) (*PatchSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generatePatchSchema(g, t, true)
}

// ServerFieldName returns the patch path name of a server field: its
// external name, or its column when the field-name tag hides it. ok is false
// for a field that is not a server field.
func ServerFieldName(g *core.SchemaGenerator, field *core.Field) (name string, ok bool) {
	if !field.Tags.Parse.Server {
		return "", false
	}
	if field.External != "" {
		return field.External, true
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

	for _, field := range walk.Fields {
		tags := &field.Tags
		serverField := server && tags.Parse.Server
		if !tags.Parse.Patch && !serverField {
			continue
		}
		fieldName := field.External
		if serverField {
			fieldName, _ = ServerFieldName(g, field)
		}
		if fieldName == "" {
			// virtual relation fields conventionally carry
			// `json:"-"` to hide from response payloads. They still
			// need an addressable patch path. Generators preserve the declared
			// API name with relation_name; hand-written structs can fall back
			// to snake_case without exposing the virtual field in JSON.
			if tags.Relation == "" {
				continue
			}
			fieldName = cmp.Or(tags.RelationName, core.ToSnakeCase(field.GoName))
		}

		info, err := g.FieldType(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fieldType := info.Type

		nullable := isNullableType(field.Type)
		var allowedOps []PatchOp
		if opsTag := tags.PatchOps; opsTag != "" {
			for p := range strings.SplitSeq(opsTag, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				op := PatchOp(p)
				if !ValidPatchOps[op] {
					return nil, fmt.Errorf("field %s: unknown patch operation %q", field.GoName, p)
				}
				if op == PatchOpRemove && !nullable && tags.Relation == "" {
					return nil, fmt.Errorf("field %s: patch operation remove requires a nullable Go type (pointer, slice, map), got %s", field.GoName, field.Type)
				}
				if op == PatchOpArrayAppend && isFixedArray(field.Type) {
					return nil, fmt.Errorf("field %s: array_append cannot grow a fixed-size array (%s)", field.GoName, field.Type)
				}
				allowedOps = append(allowedOps, op)
			}
		} else {
			defaults, configured, err := defaultOpsFor(g, fieldType)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			if isFixedArray(field.Type) {
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
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		var validators []core.Validator
		var enumValues []string
		if len(tags.Enum) > 0 {
			enumValues = slices.Clone(tags.Enum)
			validators = append(validators, enumValidator(enumValues))
		}

		// a field carrying both `parse:"patch"` and `relation:"…"`
		// becomes a relation-patch path. Override the allowed-ops to the
		// relation-verb subset (replace / add / remove) and clear SQLColumn
		// so BuildPatchSQL routes it into RelationOps instead of the SET
		// clause.
		var relSchema *core.RelationSchema
		if tags.Relation != "" {
			rel, err := core.ParseRelationTag(tags.Relation)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			relSchema = rel
			allowedOps = relationPatchOps()
			sqlColumn = "" // relation paths don't touch a column
		}

		path := core.FormatPointer(fieldName)
		if _, duplicate := paths[path]; duplicate {
			return nil, fmt.Errorf("field %s: duplicate patch path %q", field.GoName, path)
		}
		paths[path] = &PathSchema{
			AllowedOps: allowedOps,
			DataType:   fieldType,
			SQLType:    info.SQLType,
			GoType:     field.Type,
			SQLColumn:  sqlColumn,
			Validators: validators,
			EnumValues: enumValues,
			Relation:   relSchema,
		}
	}

	return &PatchSchema{
		Resource: tableName, AllowedPaths: paths, MaxOperations: 50,
	}, nil
}

// defaultOpsFor returns a fresh copy of the default operations for a field
// type: GeneratorOptions.DefaultPatchOps when it names the type (configured),
// otherwise the package defaults. Every schema owns its slice — sharing the
// package-level slice let configuring one resource rewrite another's allowed
// operations, and race with its requests.
func defaultOpsFor(g *core.SchemaGenerator, ft core.FieldType) (ops []PatchOp, configured bool, err error) {
	if names, ok := g.Opts.DefaultPatchOps[ft]; ok {
		ops := make([]PatchOp, 0, len(names))
		for _, name := range names {
			op := PatchOp(strings.TrimSpace(name))
			if !ValidPatchOps[op] {
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

// isNullableType reports whether a Go type can represent null.
func isNullableType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return true
	}
	return false
}

// enumValidator admits a value in the enum set; on an array field each
// element must be, as the filter side reads the tag.
func enumValidator(values []string) core.Validator {
	allowed := make(map[string]struct{}, len(values))
	names := make([]string, len(values))
	for i, v := range values {
		names[i] = strings.TrimSpace(v)
		allowed[names[i]] = struct{}{}
	}
	var check func(value any) error
	check = func(value any) error {
		if items, ok := core.AnySlice(value); ok {
			for _, item := range items {
				if err := check(item); err != nil {
					return err
				}
			}
			return nil
		}
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string for enum validation, got %T", value)
		}
		if _, found := allowed[s]; !found {
			return fmt.Errorf("value %q is not one of: %s", s, strings.Join(names, ", "))
		}
		return nil
	}
	return check
}

// defaultPatchOps maps FieldType to standard patch operators.
// Unexported per ; call DefaultPatchOps() for a defensive copy.
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
// path: replace / add / remove. JSON Patch's move / copy / test verbs
// have no clean translation to "set / extend / shrink junction
// membership" so they're rejected at validation (E2117). .
func relationPatchOps() []PatchOp {
	return []PatchOp{PatchOpReplace, PatchOpAdd, PatchOpRemove}
}

// RelationPath is the patch path of a relation's membership: replace, add
// and remove of a slice of target keys (goType), routed to RelationOps.
func RelationPath(rel *core.RelationSchema, goType reflect.Type) *PathSchema {
	return &PathSchema{AllowedOps: relationPatchOps(), DataType: core.TypeOf(goType).Type, GoType: goType, Relation: rel}
}
