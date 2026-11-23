package filter

import (
	"cmp"
	"database/sql/driver"
	"encoding"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// rowColumn is the row column a field named name reads, the column the
// read-set checks hold to T's own. Without a column mapper (expr "") it is
// the column the field's SQL names, MappedExpr having held it to an
// identifier; a column tag of "-" leaves the field reading its
// conventional column, which a resource then refuses as none of T's. With
// a mapper it is FieldColumn's: "" for a column that is no identifier or a
// field that is no column, since only the mapper's own SQL reads it.
func rowColumn(g *core.SchemaGenerator, field *core.Field, name, expr string) (string, error) {
	if expr == "" {
		return g.ColumnName(name, field.Tags.Column)
	}
	if column, ok, err := g.FieldColumn(field); err == nil && ok {
		return column, nil
	}
	return "", nil
}

// GenerateFilterSchema generates a ResourceSchema from struct tags on type T.
func GenerateFilterSchema[T any](g *core.SchemaGenerator) (*ResourceSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generateFilterSchema(g, t)
}

func generateFilterSchema(g *core.SchemaGenerator, t reflect.Type) (*ResourceSchema, error) {
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
	fields := make(map[string]*FieldSchema)
	// candidates are the walk's fields as server policies may name them, in
	// walk order.
	var candidates []policyCandidate

	for _, field := range walk.Fields {
		tags := &field.Tags
		if len(tags.Enum) > 0 {
			if err := enumField(g, field); err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
		}
		fieldName := field.External
		if tags.Parse.Filter && fieldName == "" && tags.Relation != "" {
			// : virtual relation fields conventionally carry
			// `json:"-"` to hide from response payloads. They still
			// need an addressable name. Generators can preserve their
			// declared name with relation_name; otherwise use snake_case
			// of the struct field name.
			fieldName = cmp.Or(tags.RelationName, core.SnakeCase(field.GoName))
		}
		if !tags.Parse.Filter || fieldName == "" {
			if c, ok := policyColumn(g, tableName, field); ok {
				candidates = append(candidates, c)
			}
			continue
		}
		if _, exists := fields[fieldName]; exists {
			return nil, fmt.Errorf("field %s: duplicate filter field name %q", field.GoName, fieldName)
		}

		info, err := g.FieldType(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fieldType := info.Type

		// Geo tag overrides field type detection.
		var geoConfig *GeoFieldConfig
		if tags.Geo != "" {
			fieldType = core.FieldTypeGeo
			geoConfig, err = parseGeoTag(tags)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
		}

		expr, err := g.MappedExpr(tableName, fieldName, fieldType, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		var allowedOps []FilterOperator
		if opsTag := tags.FilterOps; opsTag != "" {
			if allowedOps, err = parseOperatorList(strings.Split(opsTag, ",")); err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
		} else {
			defaults, err := defaultFilterOpsFor(g, fieldType)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			allowedOps = defaults
		}

		fs := &FieldSchema{
			ExternalName:     fieldName,
			Table:            tableName,
			Expr:             expr,
			DataType:         fieldType,
			NumberKind:       info.NumberKind,
			Wire:             field.Wire(),
			Elem:             info.Elem,
			AllowedOperators: allowedOps,
			Indexed:          tags.Indexed,
			Immutable:        tags.Immutable,
			EnumValues:       slices.Clone(tags.Enum),
			Label:            tags.Label,
			Description:      tags.Description,
			Group:            tags.Group,
			Facetable:        tags.Parse.Facet,
			Searchable:       tags.Parse.Search,
			Includable:       tags.Parse.Include,
		}

		if tags.Relation == "" {
			if fs.Column, err = rowColumn(g, field, fieldName, expr); err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
		}

		if tags.Order != "" {
			n, err := strconv.Atoi(tags.Order)
			if err != nil {
				return nil, fmt.Errorf("field %s: order:%q is not an integer", field.GoName, tags.Order)
			}
			fs.Order = n
		}

		if fs.Facetable {
			if t := facetValueType(fs); unkeyedFacetTypes[t] {
				return nil, fmt.Errorf("field %s: %s values have no facet key every backend reads alike", field.GoName, t)
			}
			if tags.Facet != "" {
				facetSchema, err := ParseFacetTag(tags.Facet)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName, err)
				}
				if facetSchema != nil {
					facetSchema.Field = fieldName
					fs.FacetSchema = facetSchema
				}
			}
		}

		if tags.Relation != "" {
			rel, err := core.ParseRelationTag(tags.Relation)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			if info.Type != core.FieldTypeArray {
				return nil, fmt.Errorf("field %s: relation fields must be slices of scalar target IDs, got %s", field.GoName, field.Type)
			}
			// Relation operators compare target IDs, so values normalize
			// and advertise as the element type, not "array".
			fs.DataType = info.Elem
			fs.Elem = ""
			fs.Relation = rel
			if tags.FilterOps == "" {
				fs.AllowedOperators = []FilterOperator{OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull}
			}
		}

		if geoConfig != nil {
			fs.GeoConfig = geoConfig
		}

		if err := applyStorage(g, fs, field); err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		if err := checkOperators(fs); err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		if isScalarArray(fs) {
			fs.Element = newElement(fs)
		}
		fields[fieldName] = fs
		candidates = append(candidates, requestCandidate(field, fs))
		if core.IsDocument(fs.DataType) {
			paths, err := documentPathFields(g, field, fs)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			for _, p := range paths {
				if _, exists := fields[p.ExternalName]; exists {
					return nil, fmt.Errorf("field %s: path %q collides with another field", field.GoName, p.ExternalName)
				}
				fields[p.ExternalName] = p
				candidates = append(candidates, policyCandidate{goName: field.GoName, name: p.ExternalName, request: true, field: p})
			}
			elems, err := documentElements(g, field, fs)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			if elems != nil {
				fs.Elements = elems
				fs.AllowedOperators = append(fs.AllowedOperators, OpSome, OpNone)
			}
		} else if tags.Paths != "" || tags.Elements != "" {
			return nil, fmt.Errorf("field %s: paths are declared on json or object fields, not %s", field.GoName, fs.DataType)
		}
	}

	policyFields, refused := indexPolicyNames(candidates)
	return &ResourceSchema{Name: tableName, Fields: fields, PolicyFields: policyFields, policyRefused: refused}, nil
}

// GenerateSortSchema generates a SortSchema from struct tags on type T.
func GenerateSortSchema[T any](g *core.SchemaGenerator) (*SortSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generateSortSchema(g, t)
}

func generateSortSchema(g *core.SchemaGenerator, t reflect.Type) (*SortSchema, error) {
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
	fields := make(map[string]*SortFieldSchema)
	var defaultField string
	var defaultDir SortDirection

	for _, field := range walk.Fields {
		tags := &field.Tags
		info, err := g.FieldType(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		// : one sort grammar — scalar sortability opts in
		// via parse:"…,sort".
		// The legacy sort_field:"true" tag is deleted, not dual-read.
		if !tags.Parse.Excluded {
			if dt := info.Type; core.IsDocument(dt) {
				if err := addDocSortPaths(g, fields, tableName, field, dt); err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName, err)
				}
			}
		}
		if !tags.Parse.Sort {
			if tags.SortDefault != "" {
				return nil, fmt.Errorf("field %s: sort_default:%q on a field without the sort capability", field.GoName, tags.SortDefault)
			}
			continue
		}
		if tags.Relation != "" {
			// A relation has no row column to sort or resume a cursor on.
			return nil, fmt.Errorf("field %s: relation fields are not sortable", field.GoName)
		}
		fieldName := field.External
		if fieldName == "" {
			continue
		}
		ft := field.Type
		nullable := ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map || ft.Kind() == reflect.Interface
		fieldType := info.Type
		if !sortable(ft, fieldType) {
			// A cursor carries a scalar per key; a document, array or geo
			// value has no portable order, and an interval or network
			// value's text order is not its order.
			return nil, fmt.Errorf("field %s: %s fields are not sortable", field.GoName, fieldType)
		}
		expr, err := g.MappedExpr(tableName, fieldName, fieldType, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		column, err := g.ColumnName(fieldName, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		kind := core.NumericKind(ft)
		if core.IsDocument(fieldType) {
			// A struct the database holds as a scalar orders as that scalar,
			// as it filters (applyStorage).
			scalar, err := scalarStorage(g, field)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.GoName, err)
			}
			if scalar != "" {
				fieldType, kind = scalar, kindOf(scalar)
			}
		}
		fields[fieldName] = &SortFieldSchema{
			Table:      tableName,
			Column:     column,
			Expr:       expr,
			DataType:   fieldType,
			NumberKind: kind,
			Nullable:   nullable,
			Indexed:    tags.Indexed,
		}

		if defaultTag := tags.SortDefault; defaultTag != "" {
			if defaultField != "" {
				return nil, fmt.Errorf("field %s: sort_default:%q: %s already declares the default sort", field.GoName, defaultTag, defaultField)
			}
			switch SortDirection(defaultTag) {
			case SortAsc, SortDesc:
				defaultField, defaultDir = fieldName, SortDirection(defaultTag)
			default:
				return nil, fmt.Errorf("field %s: sort_default:%q is neither %q nor %q", field.GoName, defaultTag, SortAsc, SortDesc)
			}
		}
	}

	maxFields := g.SortMaxFields()
	if maxFields <= 0 {
		maxFields = 3 // default
	}
	return &SortSchema{
		Fields: fields, DefaultField: defaultField, DefaultDir: defaultDir, MaxFields: maxFields,
	}, nil
}

// --- Defaults ---

// defaultFilterOps maps FieldType to the operators a bare `parse:"filter"`
// field gets; each is a subset of typeOperators. Unexported per ;
// call DefaultFilterOps() for a defensive copy.
//
// `$in` and `$nin` come as a pair. JSON documents default to containment,
// and arrays to containment and element conditions (`$some` / `$none`),
// which every backend evaluates.
// Struct-typed (object) fields get no default: a struct the database holds
// as a scalar declares it with `storage:"scalar:<type>"` and takes that
// type's defaults, and a document opts in with filter_ops.
var defaultFilterOps = map[core.FieldType][]FilterOperator{
	core.FieldTypeString:    {OpEq, OpNe, OpContains, OpLike, OpILike, OpIn, OpNin},
	core.FieldTypeInt:       {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin},
	core.FieldTypeFloat:     {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin},
	core.FieldTypeDecimal:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin},
	core.FieldTypeBool:      {OpEq, OpNe},
	core.FieldTypeTimestamp: {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	core.FieldTypeDate:      {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	core.FieldTypeTime:      {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	core.FieldTypeInterval:  {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeInet:      {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeCIDR:      {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeMACAddr:   {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeUUID:      {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeJSON:      {OpEq, OpNe, OpContains, OpHasKey, OpExists, OpNull},
	core.FieldTypeArray:     {OpContains, OpSome, OpNone, OpNull},
	core.FieldTypeGeo:       {OpNear, OpWithin},
}

// defaultFilterOpsFor returns a fresh copy of the default operators for a
// field type: GeneratorOptions.DefaultFilterOps when it names the type,
// otherwise the package defaults. Every schema owns its slice, so
// configuring one resource's operators cannot change another's.
func defaultFilterOpsFor(g *core.SchemaGenerator, ft core.FieldType) ([]FilterOperator, error) {
	if names, ok := g.DefaultFilterOps(ft); ok {
		ops, err := parseOperatorList(names)
		if err != nil {
			return nil, fmt.Errorf("DefaultFilterOps[%s]: %w", ft, err)
		}
		return ops, nil
	}
	return slices.Clone(defaultFilterOps[ft]), nil
}

// DefaultFilterOps returns a defensive copy of the package's default
// FieldType -> operators map. Callers can mutate the returned map without
// affecting the package-level defaults or other callers.
func DefaultFilterOps() map[core.FieldType][]FilterOperator {
	out := make(map[core.FieldType][]FilterOperator, len(defaultFilterOps))
	for k, v := range defaultFilterOps {
		ops := make([]FilterOperator, len(v))
		copy(ops, v)
		out[k] = ops
	}
	return out
}

// unkeyedFacetTypes are the value types whose PostgreSQL text differs from
// their canonical text: interval follows the session's IntervalStyle, inet
// writes a host's /32, and both network types write an IPv4-compatible IPv6
// address in dotted form. A terms facet's key is that text.
var unkeyedFacetTypes = map[core.FieldType]bool{
	core.FieldTypeInterval: true, core.FieldTypeInet: true, core.FieldTypeCIDR: true,
}

// sortable reports whether a keyset can order and resume on a field: a scalar
// type, or a struct whose value reduces to one (driver.Valuer,
// encoding.TextMarshaler — see CursorValue).
func sortable(t reflect.Type, ft core.FieldType) bool {
	switch ft {
	case core.FieldTypeString, core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeDecimal,
		core.FieldTypeBool, core.FieldTypeTimestamp, core.FieldTypeDate, core.FieldTypeTime, core.FieldTypeUUID:
		return true
	case core.FieldTypeObject:
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		pt := reflect.PointerTo(t)
		return t.Implements(valuerType) || pt.Implements(valuerType) ||
			t.Implements(textMarshalerType) || pt.Implements(textMarshalerType)
	}
	return false
}

var (
	valuerType        = reflect.TypeFor[driver.Valuer]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// addDocSortPaths adds the path sort keys a document field declares.
func addDocSortPaths(g *core.SchemaGenerator, fields map[string]*SortFieldSchema, tableName string, field *core.Field, dt core.FieldType) error {
	name := field.External
	if name == "" {
		return nil
	}
	expr, err := g.MappedExpr(tableName, name, dt, field.Tags.Column)
	if err != nil {
		return err
	}
	column, err := g.ColumnName(name, field.Tags.Column)
	if err != nil {
		return err
	}
	keys, err := docSortPaths(g, field, dt, tableName, column, expr)
	if err != nil {
		return err
	}
	for n, k := range keys {
		if _, dup := fields[n]; dup {
			return fmt.Errorf("sort path %q collides with another sort field", n)
		}
		fields[n] = k
	}
	return nil
}

// enumField refuses an enum on a field whose values are not strings: the
// allowlist is text, compared with a string value or each element of an
// array of strings, and on any other type it would be read and never
// checked.
func enumField(g *core.SchemaGenerator, field *core.Field) error {
	if field.Tags.Geo != "" {
		return errEnumType(core.FieldTypeGeo, "")
	}
	info, err := g.FieldType(field)
	if err != nil {
		return err
	}
	if !enumApplies(info.Type, info.Elem) {
		return errEnumType(info.Type, info.Elem)
	}
	return nil
}

// enumApplies reports whether an enum constrains values of t (elements of
// elem for an array): string fields and arrays of strings.
func enumApplies(t, elem core.FieldType) bool {
	return t == core.FieldTypeString || t == core.FieldTypeArray && elem == core.FieldTypeString
}

func errEnumType(t, elem core.FieldType) error {
	if t == core.FieldTypeArray {
		t = core.FieldType("array of " + string(elem))
	}
	return fmt.Errorf("enum on a %s field; enum takes string fields and arrays of strings", t)
}
