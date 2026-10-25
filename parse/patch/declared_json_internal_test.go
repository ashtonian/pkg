//go:build unit

package patch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

type declaredZero int

func (declaredZero) IsZero() bool { return true }

type declaredNonZeroMap map[string]string

func (declaredNonZeroMap) IsZero() bool { return false }

func TestDeclaredJSONReplacementOmissionMatchesJSON(t *testing.T) {
	type row struct {
		Any    any                        `json:"any,omitzero"`
		Zero   declaredZero               `json:"zero,omitzero"`
		Nil    interface{ IsZero() bool } `json:"nil,omitzero"`
		NilMap interface{ IsZero() bool } `json:"nil_map,omitzero"`
		Map    declaredNonZeroMap         `json:"map,omitzero"`
	}
	var nilZero *declaredZero
	value := row{Any: declaredZero(7), Zero: 7, Nil: nilZero, NilMap: declaredNonZeroMap(nil)}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	assert.JSONEq(t, `{"any":7,"nil_map":null,"map":null}`, string(raw))
	var emitted map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &emitted))
	fields, err := core.StructFieldsOf(reflect.TypeFor[row](), "json", "db")
	require.NoError(t, err)
	for _, field := range fields.Fields {
		fv, err := field.Value(reflect.ValueOf(value))
		require.NoError(t, err)
		_, present := emitted[field.External]
		assert.Equal(t, !present, jsonFieldOmitted(field, fv), field.External)
	}
}
