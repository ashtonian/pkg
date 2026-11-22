//go:build unit

package filter

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A schema cannot advertise an operator its type has no meaning for.
func TestGenerateFilterSchema_RejectsInapplicableOperators(t *testing.T) {
	type likeOnInt struct {
		N int `json:"n" parse:"filter" filter_ops:"eq,like"`
	}
	_, err := GenerateFilterSchema[likeOnInt](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, `operator "like" does not apply to int fields`)

	type eqOnArray struct {
		Tags []string `json:"tags" parse:"filter" filter_ops:"eq"`
	}
	_, err = GenerateFilterSchema[eqOnArray](core.NewSchemaGenerator())
	assert.Error(t, err)

	type containsOnRelation struct {
		Tags []string `json:"-" parse:"filter" filter_ops:"contains" relation:"item_tags,item_id,tag_id"`
	}
	_, err = GenerateFilterSchema[containsOnRelation](core.NewSchemaGenerator())
	assert.ErrorContains(t, err, "relation (string target)")

	type bad struct {
		N int `json:"n" parse:"filter"`
	}
	g := core.NewSchemaGenerator(func(o *core.GeneratorOptions) {
		o.DefaultFilterOps = map[core.FieldType][]string{core.FieldTypeInt: {"eq", "contains"}}
	})
	_, err = GenerateFilterSchema[bad](g)
	assert.Error(t, err, "DefaultFilterOps are held to the same table")
}

func TestGenerateFilterSchema_DefaultsFollowTypeModel(t *testing.T) {
	type doc struct {
		Labels  map[string]any  `json:"labels"  parse:"filter"`
		Tags    []string        `json:"tags"    parse:"filter"`
		Tiers   []any           `json:"tiers"   parse:"filter"`
		Counts  []int64         `json:"counts"  parse:"filter"`
		Profile struct{ A int } `json:"profile" parse:"filter"`
	}
	s, err := GenerateFilterSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeJSON, s.Fields["labels"].DataType)
	assert.Equal(t, []FilterOperator{OpEq, OpNe, OpContains, OpHasKey, OpExists, OpNull}, s.Fields["labels"].AllowedOperators)
	assert.Equal(t, core.FieldTypeArray, s.Fields["tags"].DataType)
	assert.Equal(t, core.FieldTypeString, s.Fields["tags"].Elem)
	assert.Equal(t, []FilterOperator{OpContains, OpSome, OpNone, OpNull}, s.Fields["tags"].AllowedOperators)
	assert.Equal(t, core.FieldTypeJSON, s.Fields["tiers"].DataType, "[]any is a document, not a native array")
	assert.Equal(t, core.FieldTypeInt, s.Fields["counts"].Elem)
	assert.Empty(t, s.Fields["profile"].AllowedOperators, "struct fields opt in explicitly")
}

// Every operator a type admits is a name filter_ops and DefaultFilterOps
// accept: the declarable names derive from the per-type table.
func TestOperators_EveryTypeOperatorIsDeclarable(t *testing.T) {
	for ft := range typeOperators {
		for _, relation := range []bool{false, true} {
			for _, op := range OperatorsFor(ft, relation) {
				assert.True(t, IsValidOperator(op), "%s on %s (relation %v)", op, ft, relation)
			}
		}
	}
	for _, op := range []FilterOperator{OpAnd, OpOr, OpNot, OpIs, OpIsNot, OpRawSQL, OpMember, OpUnknown, "regex"} {
		assert.False(t, IsValidOperator(op), op)
	}
	assert.NotSame(t, &Operators()[0], &Operators()[0], "a caller cannot write the table")
}

func TestGenerateFilterSchema_ArrayElementOperators(t *testing.T) {
	type declared struct {
		Tags []string `json:"tags" parse:"filter" filter_ops:"some,none"`
	}
	s, err := GenerateFilterSchema[declared](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, []FilterOperator{OpSome, OpNone}, s.Fields["tags"].AllowedOperators)

	type defaulted struct {
		Tags []string `json:"tags" parse:"filter"`
	}
	g := core.NewSchemaGenerator(func(o *core.GeneratorOptions) {
		o.DefaultFilterOps = map[core.FieldType][]string{core.FieldTypeArray: {"contains", "some"}}
	})
	s, err = GenerateFilterSchema[defaulted](g)
	require.NoError(t, err)
	assert.Equal(t, []FilterOperator{OpContains, OpSome}, s.Fields["tags"].AllowedOperators)
}

// Every operator has one `$` key, and the grammar reads each.
func TestParseOperator(t *testing.T) {
	for _, d := range operatorTable {
		op, ok := ParseOperator("$" + string(d.op))
		assert.True(t, ok, d.op)
		assert.Equal(t, d.op, op)
	}
	for _, key := range []string{"eq", "$regex", "$raw_sql", "$member", "$unknown", ""} {
		_, ok := ParseOperator(key)
		assert.False(t, ok, key)
	}
}

// The package documentation lists the operators and suffixes the table
// holds, in its order.
func TestDocListsTheOperatorTable(t *testing.T) {
	doc, err := os.ReadFile("doc.go")
	require.NoError(t, err)
	text := strings.Join(strings.Fields(strings.ReplaceAll(string(doc), "//", "")), " ")
	list := func(pattern string) []string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(text)
		require.Len(t, m, 2, pattern)
		return strings.Split(m[1], ", ")
	}

	var ops []string
	for _, op := range Operators() {
		ops = append(ops, string(op))
	}
	assert.Equal(t, ops, list(`Field operators \(\[Operators\]\): ([a-z_, ]+)\.`))

	var suffixes []string
	for _, d := range operatorTable {
		if d.suffix != "" {
			suffixes = append(suffixes, d.suffix)
		}
	}
	assert.Equal(t, suffixes, list(`suffix conventions: ([a-z_, ]+)\. No suffix`))

	var relations, logical []string
	for _, d := range operatorTable {
		switch d.kind {
		case kindRelation:
			relations = append(relations, "$"+string(d.op))
		case kindLogical:
			logical = append(logical, "$"+string(d.op))
		}
	}
	assert.Equal(t, logical, list(`Logical operators: ([$a-z_, ]+)\.`))
	assert.Equal(t, relations, list(`Relation predicates: ([$a-z_, ]+)\.`))
	assert.True(t, slices.IsSortedFunc(querySuffixes, func(a, b operatorDef) int { return len(b.suffix) - len(a.suffix) }))
}
