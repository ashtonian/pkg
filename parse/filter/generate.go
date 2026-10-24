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

	for _, field := range walk.Fields {
		tags := &field.Tags
		if !tags.Parse.Filter {
			continue
		}
		fieldName := field.External
		if fieldName == "" {
			// : virtual relation fields conventionally carry
			// `json:"-"` to hide from response payloads. They still
			// need an addressable name. Generators can preserve their
			// declared name with relation_name; otherwise use snake_case
			// of the struct field name.
			if tags.Relation == "" {
				continue
			}
			fieldName = cmp.Or(tags.RelationName, core.ToSnakeCase(field.GoName))
		}
		if _, exists := fields[fieldName]; exists {
			return nil, fmt.Errorf("field %s: duplicate filter field name %q", field.GoName, fieldName)
		}

		info := core.TypeOf(field.Type)
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

		sqlExpr, err := g.ColumnExpr(tableName, fieldName, fieldType, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		var allowedOps []FilterOperator
		if opsTag := tags.FilterOps; opsTag != "" {
			for p := range strings.SplitSeq(opsTag, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				op := FilterOperator(p)
				if !ValidFilterOps[op] {
					return nil, fmt.Errorf("field %s: unknown filter operator %q", field.GoName, p)
				}
				allowedOps = append(allowedOps, op)
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
			SQLExpr:          sqlExpr,
			DataType:         fieldType,
			NumberKind:       info.NumberKind,
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

		if tags.Order != "" {
			if n, err := strconv.Atoi(tags.Order); err == nil {
				fs.Order = n
			}
		}

		if fs.Facetable {
			if tags.Facet != "" {
				facetSchema, err := ParseFacetTag(tags.Facet)
				if err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName, err)
				}
				if facetSchema != nil {
					facetSchema.Field = fieldName
					facetSchema.SQLExpr = sqlExpr
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

		if err := applyStorage(fs, tags); err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		if err := checkOperators(fs); err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fields[fieldName] = fs
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

	return &ResourceSchema{Name: tableName, Fields: fields}, nil
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

		// : one sort grammar — scalar sortability opts in
		// via parse:"…,sort".
		// The legacy sort_field:"true" tag is deleted, not dual-read.
		if !tags.Parse.Excluded {
			if dt := core.TypeOf(field.Type).Type; core.IsDocument(dt) {
				if err := addDocSortPaths(g, fields, tableName, field, dt); err != nil {
					return nil, fmt.Errorf("field %s: %w", field.GoName, err)
				}
			}
		}
		if !tags.Parse.Sort {
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
		fieldType := core.TypeOf(ft).Type
		if !sortable(ft, fieldType) {
			// A cursor carries a scalar per key; a document, array or geo
			// value has no portable order.
			return nil, fmt.Errorf("field %s: %s fields are not sortable", field.GoName, fieldType)
		}
		sqlExpr, err := g.ColumnExpr(tableName, fieldName, fieldType, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		column, err := g.ColumnName(fieldName, tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fields[fieldName] = &SortFieldSchema{
			SQLExpr:    sqlExpr,
			Column:     column,
			DataType:   fieldType,
			NumberKind: core.NumericKind(ft),
			Nullable:   nullable,
			Indexed:    tags.Indexed,
		}

		if defaultTag := tags.SortDefault; defaultTag != "" {
			defaultField = fieldName
			if defaultTag == "desc" {
				defaultDir = SortDesc
			} else {
				defaultDir = SortAsc
			}
		}
	}

	maxFields := g.Opts.SortMaxFields
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
// `$in` and `$nin` come as a pair. JSON documents and
// arrays default to containment, which every backend evaluates
// . Struct-typed (object) fields get no default: several are
// opaque scalars stored in their own column type (decimal.Decimal), not
// documents, so they opt in with filter_ops.
var defaultFilterOps = map[core.FieldType][]FilterOperator{
	core.FieldTypeString:    {OpEq, OpNe, OpContains, OpLike, OpILike, OpIn, OpNin},
	core.FieldTypeInt:       {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin},
	core.FieldTypeFloat:     {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin},
	core.FieldTypeBool:      {OpEq, OpNe},
	core.FieldTypeTimestamp: {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte},
	core.FieldTypeUUID:      {OpEq, OpNe, OpIn, OpNin},
	core.FieldTypeJSON:      {OpEq, OpNe, OpContains, OpHasKey, OpExists, OpNull},
	core.FieldTypeArray:     {OpContains, OpNull},
	core.FieldTypeGeo:       {OpNear, OpWithin},
}

// defaultFilterOpsFor returns a fresh copy of the default operators for a
// field type: GeneratorOptions.DefaultFilterOps when it names the type,
// otherwise the package defaults. Every schema owns its slice — sharing the
// package-level slice let configuring one resource rewrite another's
// operators.
func defaultFilterOpsFor(g *core.SchemaGenerator, ft core.FieldType) ([]FilterOperator, error) {
	if names, ok := g.Opts.DefaultFilterOps[ft]; ok {
		ops := make([]FilterOperator, 0, len(names))
		for _, name := range names {
			op := FilterOperator(strings.TrimSpace(name))
			if !ValidFilterOps[op] {
				return nil, fmt.Errorf("DefaultFilterOps[%s]: unknown filter operator %q", ft, name)
			}
			ops = append(ops, op)
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

// FilterMeta describes available filter fields for an entity.
type FilterMeta struct {
	Fields []FilterFieldMeta `json:"fields"`
}

// FilterFieldMeta describes a single filterable field.
type FilterFieldMeta struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Operators []string `json:"operators,omitempty"`
}

// FilterMetaFromSchema derives FilterMeta from a ResourceSchema.
func FilterMetaFromSchema(schema *ResourceSchema) *FilterMeta {
	if schema == nil {
		return &FilterMeta{}
	}
	meta := &FilterMeta{Fields: make([]FilterFieldMeta, 0, len(schema.Fields))}
	for _, fs := range schema.Fields {
		ops := make([]string, len(fs.AllowedOperators))
		for i, op := range fs.AllowedOperators {
			ops[i] = string(op)
		}
		meta.Fields = append(meta.Fields, FilterFieldMeta{
			Name: fs.ExternalName, Type: string(fs.DataType), Operators: ops,
		})
	}
	return meta
}

// sortable reports whether a keyset can order and resume on a field: a scalar
// type, or a struct whose value reduces to one (driver.Valuer,
// encoding.TextMarshaler — see CursorValue).
func sortable(t reflect.Type, ft core.FieldType) bool {
	switch ft {
	case core.FieldTypeString, core.FieldTypeInt, core.FieldTypeFloat,
		core.FieldTypeBool, core.FieldTypeTimestamp, core.FieldTypeUUID:
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

// RepositoryScope is the floor of a repository read: the request tenant when
// the table is tenant-scoped, and soft-delete masking.
type RepositoryScope struct {
	TenantColumn   string
	TenantID       string
	DeleteColumn   string
	IncludeDeleted bool
}

// ComposeConstraints is the policy tree a repository read applies, the same
// on every backend: governance when present — it scopes
// tenancy itself, grants included — otherwise the tenant floor; soft-delete
// masking; then authorization. The parts are ANDed under a new root, so
// neither input is modified and a cached governance tree is never extended.
func ComposeConstraints(scope RepositoryScope, governance, authz *PolicyConstraints) *PolicyConstraints {
	root := &PolicyConstraints{Source: "repository"}
	if governance != nil && !governance.IsEmpty() {
		root.And = append(root.And, governance)
		if scope.DeleteColumn != "" && !scope.IncludeDeleted {
			root.And = append(root.And, BuildBaseConstraints("", "", scope.DeleteColumn, false))
		}
	} else {
		root.And = append(root.And, BuildBaseConstraints(scope.TenantColumn, scope.TenantID, scope.DeleteColumn, scope.IncludeDeleted))
	}
	if authz != nil && !authz.IsEmpty() {
		root.And = append(root.And, authz)
	}
	return root
}

// BuildBaseConstraints builds PolicyConstraints for tenant isolation and soft-delete.
//
// Fail-closed tenant scoping: when tenantCol is set, the tenant
// equality predicate is ALWAYS emitted. An empty tenantID yields an impossible
// predicate (`tenant_col = ”` — zero rows, or a type error on UUID columns)
// instead of silently widening the query to every tenant. Repos additionally
// reject the empty-tenant case up front with a typed error; the predicate here
// is defense in depth for any caller that bypasses that gate.
func BuildBaseConstraints(tenantCol, tenantID, deleteCol string, includeSoftDeleted bool) *PolicyConstraints {
	pc := &PolicyConstraints{Source: "repository_base"}
	if tenantCol != "" {
		pc.Constraints = append(pc.Constraints, &PolicyConstraint{
			Field: tenantCol, Operator: OpEq, Value: tenantID,
		})
	}
	if deleteCol != "" && !includeSoftDeleted {
		pc.Constraints = append(pc.Constraints, &PolicyConstraint{
			Field: deleteCol, Operator: OpNull, Value: true,
		})
	}
	return pc
}

// ToAnySlice converts a []string to []any.
func ToAnySlice(ss []string) []any {
	result := make([]any, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}

// addDocSortPaths adds the path sort keys a document field declares.
func addDocSortPaths(g *core.SchemaGenerator, fields map[string]*SortFieldSchema, tableName string, field *core.Field, dt core.FieldType) error {
	name := field.External
	if name == "" {
		return nil
	}
	sqlExpr, err := g.ColumnExpr(tableName, name, dt, field.Tags.Column)
	if err != nil {
		return err
	}
	column, err := g.ColumnName(name, field.Tags.Column)
	if err != nil {
		return err
	}
	keys, err := docSortPaths(g, field, dt, sqlExpr, column)
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
