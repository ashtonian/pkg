//go:build unit

package filter_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type parityItem struct {
	Status string    `json:"status" db:"status" parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,like,ilike,contains,null,not_null"`
	Count  int64     `json:"count"  db:"count"  parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin,null,not_null"`
	At     time.Time `json:"at"     db:"at"     parse:"filter" filter_ops:"eq,gt,null"`
}

// paritySchema is parityItem's filter schema with the system fields a
// resource declares: its key, tenant and soft-delete columns.
func paritySchema(t *testing.T) *filter.ResourceSchema {
	t.Helper()
	s, err := filter.GenerateFilterSchema[parityItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	s.PrimaryKey = "id"
	s.SystemFields = map[string]*filter.FieldSchema{
		"id": {ExternalName: "id", Table: "parity_items", Column: "id", DataType: core.FieldTypeString,
			AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte, filter.OpIn}},
		"tenant_id": {ExternalName: "tenant_id", Table: "parity_items", Column: "tenant_id", DataType: core.FieldTypeString,
			AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn}},
		"deleted_at": {ExternalName: "deleted_at", Table: "parity_items", Column: "deleted_at", DataType: core.FieldTypeTimestamp,
			AllowedOperators: []filter.FilterOperator{filter.OpNull, filter.OpNotNull}},
	}
	return s
}

// The JSON grammar and flat parameters accept the same fields and
// operators, system fields included, and compile what both accept to the
// same SQL.
func TestRequestFields_JSONAndFlatAgree(t *testing.T) {
	schema := paritySchema(t)
	sample := map[core.FieldType]struct {
		flat string
		json any
	}{
		core.FieldTypeString:    {"a", "a"},
		core.FieldTypeInt:       {"7", 7},
		core.FieldTypeTimestamp: {"2026-01-02T03:04:05Z", "2026-01-02T03:04:05Z"},
	}
	typeOf := map[string]core.FieldType{
		"status": core.FieldTypeString, "count": core.FieldTypeInt, "at": core.FieldTypeTimestamp,
		"id": core.FieldTypeString, "tenant_id": core.FieldTypeString, "deleted_at": core.FieldTypeTimestamp,
		"missing": core.FieldTypeString,
	}
	suffixes := map[filter.FilterOperator]string{filter.OpEq: ""}
	for _, op := range filter.Operators() {
		switch op {
		case filter.OpNe, filter.OpGt, filter.OpGte, filter.OpLt, filter.OpLte, filter.OpIn, filter.OpNin,
			filter.OpLike, filter.OpILike, filter.OpContains, filter.OpNull, filter.OpNotNull:
			suffixes[op] = "_" + string(op)
		}
	}
	jsonParser := filter.NewFilterParser(schema, nil)
	flatParser := filter.NewQueryParamParser(schema)
	for name, ft := range typeOf {
		for op, suffix := range suffixes {
			t.Run(name+suffix+"/"+string(op), func(t *testing.T) {
				v := sample[ft]
				flatValue, jsonValue := v.flat, v.json
				switch op {
				case filter.OpIn, filter.OpNin:
					jsonValue = []any{v.json}
				case filter.OpNull, filter.OpNotNull:
					flatValue, jsonValue = "true", true
				}
				doc, err := json.Marshal(map[string]any{name: map[string]any{"$" + string(op): jsonValue}})
				require.NoError(t, err)
				fromJSON, jsonErr := jsonParser.Parse(string(doc))
				fromFlat, flatErr := flatParser.Parse(url.Values{name + suffix: {flatValue}})
				require.Equal(t, jsonErr == nil, flatErr == nil, "json: %v, flat: %v", jsonErr, flatErr)
				if jsonErr != nil {
					assert.Equal(t, errorCode(t, jsonErr), errorCode(t, flatErr), "json: %v, flat: %v", jsonErr, flatErr)
					return
				}
				builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{})
				a, err := builder.Build(fromJSON)
				require.NoError(t, err)
				b, err := builder.Build(fromFlat)
				require.NoError(t, err)
				assert.Equal(t, a.Where, b.Where)
				assert.Equal(t, a.Args, b.Args)
			})
		}
	}
}

func errorCode(t *testing.T, err error) string {
	t.Helper()
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "%v is no *core.ParseError", err)
	return pe.Code
}

// A request names the key; the tenant and soft-delete columns stay policy
// fields, in both grammars.
func TestRequestFields_SystemFields(t *testing.T) {
	schema := paritySchema(t)
	for _, name := range []string{"tenant_id", "deleted_at"} {
		_, err := filter.NewFilterParser(schema, nil).Parse(`{"` + name + `":{"$null":true}}`)
		assert.Equal(t, filter.ECodeUnknownField.String(), errorCode(t, err), name)
		_, err = filter.NewQueryParamParser(schema).Parse(url.Values{name + "_null": {"true"}})
		assert.Equal(t, filter.ECodeUnknownField.String(), errorCode(t, err), name)
	}
	f, ok := schema.RequestField("id")
	require.True(t, ok)
	assert.Same(t, schema.SystemFields["id"], f)
	_, ok = schema.RequestField("tenant_id")
	assert.False(t, ok)
}

// Two parameters setting one operator are refused, naming both, instead of
// one dropping the other's predicate; different operators are ANDed.
func TestQueryParamParser_OneOperatorFromTwoParameters(t *testing.T) {
	qp := filter.NewQueryParamParser(testSchema())
	_, err := qp.Parse(url.Values{"status": {"active", "inactive"}, "status_in": {"suspended"}})
	require.Error(t, err)
	assert.Equal(t, filter.ECodeInvalidOperator.String(), errorCode(t, err))
	assert.ErrorContains(t, err, "parameters 'status' and 'status_in' both set $in on field 'status'")

	ast, err := qp.Parse(url.Values{"status": {"active"}, "status_in": {"inactive"}})
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(testSchema(), filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Equal(t, `("items"."status" = $1 AND "items"."status" = ANY($2))`, res.Where)
}
