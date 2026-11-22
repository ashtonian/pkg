package filter

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// operatorKind is what an operator applies to.
type operatorKind uint8

const (
	// kindComparison compares or tests one field's value.
	kindComparison operatorKind = iota
	// kindLogical combines conditions: `$and`, `$or`, `$not`.
	kindLogical
	// kindRelation tests a relation's related rows: `$some`, `$none`,
	// `$is`, `$is_not`. On a scalar array `$some` and `$none` test its
	// elements instead, which typeOperators lists for arrays.
	kindRelation
)

// operatorDef is one operator of the vocabulary. Its key in the JSON
// grammar is "$" followed by its name.
type operatorDef struct {
	op FilterOperator
	// suffix is its flat query-parameter suffix (`price_gte=10`); "" when
	// the flat form has none. A parameter without a suffix is `$eq`.
	suffix string
	kind   operatorKind
}

// operatorTable names every operator once, in the order the vocabulary is
// documented. The `$` keys, the flat suffixes and the operator classes all
// derive from it; typeOperators says which types take each one.
var operatorTable = []operatorDef{
	{op: OpEq, kind: kindComparison},
	{op: OpNe, suffix: "_ne", kind: kindComparison},
	{op: OpGt, suffix: "_gt", kind: kindComparison},
	{op: OpGte, suffix: "_gte", kind: kindComparison},
	{op: OpLt, suffix: "_lt", kind: kindComparison},
	{op: OpLte, suffix: "_lte", kind: kindComparison},
	{op: OpIn, suffix: "_in", kind: kindComparison},
	{op: OpNin, suffix: "_nin", kind: kindComparison},
	{op: OpLike, suffix: "_like", kind: kindComparison},
	{op: OpILike, suffix: "_ilike", kind: kindComparison},
	{op: OpContains, suffix: "_contains", kind: kindComparison},
	{op: OpHasKey, kind: kindComparison},
	{op: OpNull, suffix: "_null", kind: kindComparison},
	{op: OpNotNull, suffix: "_not_null", kind: kindComparison},
	{op: OpExists, kind: kindComparison},
	{op: OpNear, kind: kindComparison},
	{op: OpWithin, kind: kindComparison},
	{op: OpIntersects, kind: kindComparison},
	{op: OpSome, kind: kindRelation},
	{op: OpNone, kind: kindRelation},
	{op: OpIs, kind: kindRelation},
	{op: OpIsNot, kind: kindRelation},
	{op: OpAnd, kind: kindLogical},
	{op: OpOr, kind: kindLogical},
	{op: OpNot, kind: kindLogical},
}

// operatorDefs is operatorTable by operator.
var operatorDefs = func() map[FilterOperator]operatorDef {
	m := make(map[FilterOperator]operatorDef, len(operatorTable))
	for _, d := range operatorTable {
		m[d.op] = d
	}
	return m
}()

// ParseOperator returns the operator a JSON grammar key names ("$gte"),
// logical operators and relation predicates included.
func ParseOperator(key string) (FilterOperator, bool) {
	name, ok := strings.CutPrefix(key, "$")
	if !ok {
		return "", false
	}
	d, ok := operatorDefs[FilterOperator(name)]
	return d.op, ok
}

// fieldOperators are the operators some field type takes, in table order:
// the names filter_ops and GeneratorOptions.DefaultFilterOps may use.
var fieldOperators = func() []FilterOperator {
	var ops []FilterOperator
	for _, d := range operatorTable {
		for _, typed := range typeOperators {
			if slices.Contains(typed, d.op) {
				ops = append(ops, d.op)
				break
			}
		}
	}
	return ops
}()

// Operators returns every operator a field may declare, in the order the
// vocabulary is documented: those some field type takes (OperatorsFor).
// Which a given field may declare depends on its type.
func Operators() []FilterOperator { return slices.Clone(fieldOperators) }

// IsValidOperator reports whether op is an operator a field may declare
// (Operators); whether it applies to a given field depends on its type.
func IsValidOperator(op FilterOperator) bool { return slices.Contains(fieldOperators, op) }

// querySuffixes are the flat query-parameter suffixes, longest first, so a
// suffix that ends another (`_null`, `_not_null`) is tried after it.
var querySuffixes = func() []operatorDef {
	var out []operatorDef
	for _, d := range operatorTable {
		if d.suffix != "" {
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b operatorDef) int { return cmp.Compare(len(b.suffix), len(a.suffix)) })
	return out
}()

// parseOperatorList reads a comma-separated list of operator names, as
// filter_ops writes one, refusing a name no field type takes.
func parseOperatorList(names []string) ([]FilterOperator, error) {
	var ops []FilterOperator
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		op := FilterOperator(name)
		if !IsValidOperator(op) {
			return nil, fmt.Errorf("unknown filter operator %q", name)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// isLogical reports `$and`, `$or` and `$not`.
func isLogical(op FilterOperator) bool {
	d, ok := operatorDefs[op]
	return ok && d.kind == kindLogical
}

// isRelationPredicate reports `$some`, `$none`, `$is` and `$is_not`.
func isRelationPredicate(op FilterOperator) bool {
	d, ok := operatorDefs[op]
	return ok && d.kind == kindRelation
}

// typeOperators is every operator a data type can be filtered with — the set
// filter_ops and GeneratorOptions.DefaultFilterOps choose from. Each pair
// executes with one meaning on every dialect and the memory backend, so a
// schema cannot advertise a combination the builders cannot run. Geo
// operators on native geography or geometry storage are the one exception:
// only PostGIS evaluates them, SQLite and Evaluate refuse them with E2115.
var typeOperators = map[core.FieldType][]FilterOperator{
	core.FieldTypeString:    {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpLike, OpILike, OpContains, OpNull, OpNotNull, OpExists},
	core.FieldTypeInt:       {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeFloat:     {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeDecimal:   {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeTimestamp: {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeDate:      {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeTime:      {OpEq, OpNe, OpGt, OpGte, OpLt, OpLte, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeInterval:  {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeInet:      {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeCIDR:      {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
	core.FieldTypeMACAddr:   {OpEq, OpNe, OpIn, OpNin, OpNull, OpNotNull, OpExists},
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

// elementRole is what a field of an element condition stands for until it
// is bound to one element.
type elementRole uint8

const (
	// roleNone is a field read from a row: a column or a path.
	roleNone elementRole = iota
	// roleElement is a scalar array's element (newElement).
	roleElement
	// roleElementPath is a path into one element of a JSON array document
	// (documentElements).
	roleElementPath
)

// newElement is the element field of array, a scalar array.
func newElement(array *FieldSchema) *FieldSchema {
	return &FieldSchema{
		ExternalName:     array.ExternalName,
		SQLExpr:          elementColumn,
		DataType:         array.Elem,
		NumberKind:       array.NumberKind,
		AllowedOperators: elementOperators(array.Elem),
		EnumValues:       slices.Clone(array.EnumValues),
		role:             roleElement,
	}
}

// isScalarArray reports an array of scalars, an array column or an array
// path: its elements take `$some` / `$none` conditions (elementOf).
func isScalarArray(f *FieldSchema) bool {
	return f.DataType == core.FieldTypeArray && f.Relation == nil && f.Elem != ""
}

// elementOf is a scalar array's element field: its Element, or the element
// its Elem implies for a schema built without one. A hand-built Element is
// marked on a copy, leaving the shared schema unchanged.
func elementOf(array *FieldSchema) *FieldSchema {
	e := array.Element
	if e == nil {
		return newElement(array)
	}
	if e.role != roleElement {
		c := *e
		c.role = roleElement
		return &c
	}
	return e
}

// isElement reports the element field of a scalar array (newElement).
func isElement(f *FieldSchema) bool { return f.role == roleElement }

// isElementPath reports an element path of a JSON array document
// (documentElements), which reads one element rather than a column.
func isElementPath(f *FieldSchema) bool { return f.role == roleElementPath }

// bound is f, an element field or element path, bound to an element the SQL
// reads by alias: a field read from a row again.
func bound(f *FieldSchema) FieldSchema {
	c := *f
	c.role = roleNone
	return c
}
