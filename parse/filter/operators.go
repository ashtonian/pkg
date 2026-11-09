package filter

import (
	"fmt"
	"slices"

	"github.com/ashtonian/pkg/parse/core"
)

// typeOperators is every operator a data type can be filtered with — the set
// filter_ops and GeneratorOptions.DefaultFilterOps choose from. Each pair
// executes with one meaning on every dialect and the memory backend
// ; a schema cannot advertise a combination the builders
// cannot run. Geo shapes a dialect cannot evaluate are the one exception,
// rejected at build with E2115.
var typeOperators = map[core.FieldType][]FilterOperator{
	core.FieldTypeString:    {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpLike, OpILike, OpContains, OpNull, OpNotNull, OpExists},
	core.FieldTypeInt:       {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeFloat:     {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeDecimal:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeTimestamp: {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeDate:      {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeUUID:      {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeBool:      {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeJSON:      {OpEq, OpNe, OpContains, OpHasKey, OpNull, OpNotNull, OpExists},
	core.FieldTypeObject:    {OpEq, OpNe, OpContains, OpHasKey, OpNull, OpNotNull, OpExists},
	core.FieldTypeArray:     {OpContains, OpSome, OpNone, OpNull, OpNotNull, OpExists},
	core.FieldTypeGeo:       {OpNear, OpWithin, OpIntersects, OpNull, OpNotNull, OpExists},
	core.FieldTypeAny:       {OpNull, OpNotNull, OpExists},
}

// OperatorsFor returns the operators a field of data type dt may declare.
// A relation field compares target IDs of type dt; `$contains` has no
// relation meaning (membership is `$eq`), so it is excluded.
func OperatorsFor(dt core.FieldType, relation bool) []FilterOperator {
	ops := slices.Clone(typeOperators[dt])
	if relation {
		ops = slices.DeleteFunc(ops, func(op FilterOperator) bool { return op == OpContains })
	}
	return ops
}

// checkOperators rejects operators that do not apply to the field's type.
func checkOperators(fs *FieldSchema) error {
	allowed := OperatorsFor(fs.DataType, fs.Relation != nil)
	for _, op := range fs.AllowedOperators {
		if !slices.Contains(allowed, op) {
			kind := string(fs.DataType)
			if fs.Relation != nil {
				kind = "relation (" + kind + " target)"
			}
			return fmt.Errorf("operator %q does not apply to %s fields (allowed: %v)", op, kind, allowed)
		}
	}
	return nil
}

// elementOperators are the operators a scalar array's element conditions
// take: the element type's, without the NULL tests. An element is a value,
// so a condition on it is never a NULL test: an array path's elements of
// another kind are left out of the array, and `$some` / `$none` with no
// condition ask whether the array has an element.
func elementOperators(elem core.FieldType) []FilterOperator {
	return slices.DeleteFunc(slices.Clone(typeOperators[elem]), isNullTest)
}

// newElement is the element field of array, a scalar array: elementColumn
// stands for the element until the builder or an evaluator binds it to one.
func newElement(array *FieldSchema) *FieldSchema {
	return &FieldSchema{
		ExternalName:     array.ExternalName,
		SQLExpr:          elementColumn,
		DataType:         array.Elem,
		NumberKind:       array.NumberKind,
		AllowedOperators: elementOperators(array.Elem),
		EnumValues:       slices.Clone(array.EnumValues),
	}
}

// isScalarArray reports an array of scalars, an array column or an array
// path: its elements take `$some` / `$none` conditions (elementOf).
func isScalarArray(f *FieldSchema) bool {
	return f.DataType == core.FieldTypeArray && f.Relation == nil && f.Elem != ""
}

// elementOf is a scalar array's element field: its Element, or the element
// its Elem implies for a schema built without one.
func elementOf(array *FieldSchema) *FieldSchema {
	if array.Element != nil {
		return array.Element
	}
	return newElement(array)
}

// isElement reports the element field of a scalar array (newElement).
func isElement(f *FieldSchema) bool {
	return f.Path == nil && f.SQLExpr == elementColumn
}
