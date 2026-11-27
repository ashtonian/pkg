//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A key field takes equality, the range operators and $in, those its type
// takes: a key of a type without an order, such as an inet or an interval,
// offers no range.
func TestKeyField_OperatorsFollowItsType(t *testing.T) {
	ordered := []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte, filter.OpIn}
	unordered := []filter.FilterOperator{filter.OpEq, filter.OpIn}
	tests := []struct {
		name string
		ops  func(t *testing.T) []filter.FilterOperator
		want []filter.FilterOperator
	}{
		{"string", keyOperators[struct {
			ID string `json:"id" db:"id"`
		}], ordered},
		{"int", keyOperators[struct {
			ID int64 `json:"id" db:"id"`
		}], ordered},
		{"decimal", keyOperators[struct {
			ID decimal.Decimal `json:"id" db:"id"`
		}], ordered},
		{"timestamp", keyOperators[struct {
			ID time.Time `json:"id" db:"id"`
		}], ordered},
		{"uuid", keyOperators[struct {
			ID uuid.UUID `json:"id" db:"id"`
		}], ordered},
		{"bool", keyOperators[struct {
			ID bool `json:"id" db:"id"`
		}], unordered},
		{"inet", keyOperators[struct {
			ID core.Inet `json:"id" db:"id"`
		}], unordered},
		{"cidr", keyOperators[struct {
			ID core.CIDR `json:"id" db:"id"`
		}], unordered},
		{"macaddr", keyOperators[struct {
			ID core.MACAddr `json:"id" db:"id"`
		}], unordered},
		{"interval", keyOperators[struct {
			ID core.Interval `json:"id" db:"id"`
		}], unordered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ops(t))
		})
	}
}

// keyOperators are the operators the key field of a resource of T takes.
func keyOperators[T any](t *testing.T) []filter.FilterOperator {
	r, err := parse.NewResource[T]("keyed")
	require.NoError(t, err)
	key, ok := r.FilterSchema().RequestField("id")
	require.True(t, ok)
	return key.AllowedOperators
}

type inetKeyed struct {
	ID   core.Inet `json:"id"   db:"id"`
	Name string    `json:"name" db:"name" parse:"filter"`
}

// An inet key's $gt is refused as the field's operator: SQLite would
// compare its text (10.0.0.10 < 10.0.0.9) and PostgreSQL its address.
func TestKeyField_RefusesAnUnorderedTypesRange(t *testing.T) {
	r, err := parse.NewResource[inetKeyed]("inet_keyed")
	require.NoError(t, err)
	_, err = r.ParseListInput(parse.ListInput{Filter: `{"id":{"$gt":"10.0.0.9"}}`})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidOperator.String(), pe.Code)
	_, err = r.ParseListInput(parse.ListInput{FieldFilters: map[string][]string{"id_gt": {"10.0.0.9"}}})
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidOperator.String(), pe.Code)
	_, err = r.ParseListInput(parse.ListInput{Filter: `{"id":{"$in":["10.0.0.9","10.0.0.10"]}}`})
	require.NoError(t, err, "an inet key keeps equality and $in")
}
