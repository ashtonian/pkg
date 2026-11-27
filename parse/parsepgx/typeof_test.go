//go:build unit

package parsepgx_test

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
)

// pgx's two-field pgtype types are nullable wrappers: a column holds the
// value, typed as the value's type, and a document holds the wrapper's
// fields, as core's TestTypeOf_NullableWrapper holds database/sql's to.
func TestTypeOf_PgtypeWrappers(t *testing.T) {
	tests := []struct {
		name   string
		typ    reflect.Type
		column core.TypeInfo
		member core.TypeInfo
	}{
		{"pgtype.Int8", reflect.TypeFor[pgtype.Int8](), core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"pgtype.Text", reflect.TypeFor[pgtype.Text](), core.TypeInfo{Type: core.FieldTypeString}, core.TypeInfo{Type: core.FieldTypeObject}},
		{"pgtype.Bool", reflect.TypeFor[pgtype.Bool](), core.TypeInfo{Type: core.FieldTypeBool}, core.TypeInfo{Type: core.FieldTypeObject}},
		// More than a value and its flag: no one value.
		{"pgtype.Timestamptz", reflect.TypeFor[pgtype.Timestamptz](), core.TypeInfo{Type: core.FieldTypeObject}, core.TypeInfo{Type: core.FieldTypeObject}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.column, core.TypeOf(tt.typ), "column")
			assert.Equal(t, tt.member, core.MemberTypeOf(tt.typ), "member")
		})
	}
	// pgtype.UUID is a wrapper whose value is a bare [16]byte, which is
	// binary, not a UUID type: the README states the limit.
	assert.Equal(t, core.TypeInfo{Type: core.FieldTypeAny}, core.TypeOf(reflect.TypeFor[pgtype.UUID]()), "pgtype.UUID")
}
