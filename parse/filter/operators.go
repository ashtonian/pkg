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
	core.FieldTypeArray:     {OpContains, OpNull, OpNotNull, OpExists},
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
