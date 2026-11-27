//go:build unit

package filter_test

import (
	"cmp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
)

// Evaluate and facet keys read every value of a type as its case orders
// and names it, in every spelling a row may hold it in.
func TestCanonicalOrder_Readers(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			field := &filter.FieldSchema{ExternalName: "v", Table: "t", Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind}
			ops := []filter.FilterOperator{filter.OpEq, filter.OpNe}
			if c.Ordered {
				ops = append(ops, filter.OpLt, filter.OpLte, filter.OpGt, filter.OpGte)
			}
			for i, x := range c.Values {
				for _, a := range x.Spellings {
					row := filter.ColumnRow(func(column string) (any, bool) { return a, column == "t.v" })
					for j, y := range c.Values {
						literal, err := c.Stored(y)
						require.NoError(t, err)
						for _, op := range ops {
							got, err := filter.Evaluate(&filter.FilterNode{Field: field, Op: op, Value: literal}, row)
							require.NoError(t, err, "%#v $%s %#v", a, op, literal)
							assert.Equal(t, truthOf(holds(op, cmp.Compare(i, j))), got, "%#v $%s %#v", a, op, literal)
						}
					}

					keys, err := filter.FacetKeys(a, field)
					if !c.Faceted {
						require.Error(t, err, "%s values have no facet key", c.Name)
						continue
					}
					require.NoError(t, err, "%#v", a)
					assert.Equal(t, []string{x.Text}, keys, "%#v", a)
				}
			}
		})
	}
}

// holds reports whether op holds for a value ordered c against the
// operand.
func holds(op filter.FilterOperator, c int) bool {
	switch op {
	case filter.OpEq:
		return c == 0
	case filter.OpNe:
		return c != 0
	case filter.OpLt:
		return c < 0
	case filter.OpLte:
		return c <= 0
	case filter.OpGt:
		return c > 0
	}
	return c >= 0
}
