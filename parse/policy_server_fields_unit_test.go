//go:build unit

package parse_test

import (
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestPolicy_NamesColumnsRequestsCannotFilter(t *testing.T) {
	docs := serverFieldDocuments(t)
	tests := []struct {
		name       string
		constraint *filter.PolicyConstraint
		wantSQL    string
		wantArg    any
	}{
		{"a hidden field by column", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: 7}, "documents.owner_id = $1", int64(7)},
		{"a field without the filter capability by name", &filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "eu"}, "documents.region_code = $1", "eu"},
		{"the same field by column", &filter.PolicyConstraint{Field: "region_code", Operator: filter.OpEq, Value: "us"}, "documents.region_code = $1", "us"},
		{"a filter field by column", &filter.PolicyConstraint{Field: "folder_id", Operator: filter.OpEq, Value: 3}, "documents.folder_id = $1", int64(3)},
		{"an operator the field's request operators lack", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpIn, Value: []any{7, 8}}, "documents.owner_id = ANY($1)", []int64{7, 8}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := docs.BuildList(filter.PostgresDialect{}, nil, policyOn(tt.constraint))
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, tt.wantSQL)
			assert.Equal(t, []any{tt.wantArg}, plan.Page.Args[:1])
		})
	}
}

func TestPolicy_ServerFieldsStayOutOfRequests(t *testing.T) {
	docs := serverFieldDocuments(t)
	for _, q := range []url.Values{
		{filter.ParamFilter: {`{"owner_id":7}`}},
		{filter.ParamFilter: {`{"region":"eu"}`}},
		{filter.ParamFilter: {`{"folder_id":3}`}},
		{"owner_id": {"7"}},
		{"region": {"eu"}},
	} {
		_, err := docs.ParseList(q)
		assert.ErrorIs(t, err, filter.ECodeUnknownField, q.Encode())
	}

	fields := map[string]bool{}
	for _, f := range filter.FilterMetaFromSchema(docs.FilterSchema()).Fields {
		fields[f.Name] = true
	}
	assert.Equal(t, map[string]bool{"id": true, "title": true, "folderId": true}, fields, "the schema document advertises request fields only")
}

func TestPolicy_ServerFieldsKeepTheirTypes(t *testing.T) {
	docs := serverFieldDocuments(t)

	_, err := docs.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpLike, Value: "7%"}))
	assert.ErrorIs(t, err, filter.ECodeInvalidOperator, "an operator the type has not")

	_, err = docs.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven"}))
	assert.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "a value the type refuses")

	_, err = docs.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "apac"}))
	assert.Error(t, err, "a value outside the field's enum")

	plan, err := docs.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven", UnknownIfInvalid: true}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "WHERE NULL", "an invalid value under UnknownIfInvalid admits no row")

	_, err = docs.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "owner", Operator: filter.OpEq, Value: 7}))
	assert.ErrorIs(t, err, filter.ECodeUnknownField, "a name no column has still fails closed")
}

// serverFieldRows are serverFieldFixture's rows as a memory backend holds
// them, by column.
var serverFieldRows = []map[string]any{
	{"id": 1, "owner_id": 7, "region_code": "eu", "title": "alpha", "folder_id": 1},
	{"id": 2, "owner_id": 7, "region_code": "us", "title": "beta", "folder_id": 1},
	{"id": 3, "owner_id": 8, "region_code": "eu", "title": "alpha", "folder_id": 2},
	{"id": 4, "owner_id": 7, "region_code": "eu", "title": "gamma", "folder_id": 2},
}

// TestPolicyServerFields_Memory evaluates the policy the SQLite and
// PostgreSQL cases run over the same rows, with the reference evaluator, as
// a memory backend does.
func TestPolicyServerFields_Memory(t *testing.T) {
	docs := serverFieldDocuments(t)
	ast, err := filter.ApplyPolicyConstraints(docs.FilterSchema(), nil, ownerEUPolicy().Policy)
	require.NoError(t, err)
	var ids []int
	for _, row := range serverFieldRows {
		truth, err := filter.Evaluate(ast.Root, filter.ColumnRow(func(column string) (any, bool) {
			v, ok := row[column[len("documents."):]]
			return v, ok
		}))
		require.NoError(t, err)
		if truth == filter.True {
			ids = append(ids, row["id"].(int))
		}
	}
	slices.Sort(ids)
	assert.Equal(t, []int{1, 4}, ids)
}

// A hidden field whose column is a request field's name leaves the name to
// the request field: the schema builds, and a policy on the name constrains
// the request field.
func TestPolicy_RequestFieldNamesWin(t *testing.T) {
	type shadow struct {
		ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
		Label string `json:"title" db:"label" parse:"filter"`
		Old   string `json:"-"     db:"title"`
	}
	r, err := parse.NewResource[shadow]("shadows")
	require.NoError(t, err)
	plan, err := r.BuildList(filter.PostgresDialect{}, nil, policyOn(&filter.PolicyConstraint{Field: "title", Operator: filter.OpEq, Value: "x"}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "shadows.label = $1")
}
