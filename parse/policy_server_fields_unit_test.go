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
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

func TestPolicy_NamesColumnsRequestsCannotFilter(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
	tests := []struct {
		name       string
		constraint *filter.PolicyConstraint
		wantSQL    string
		wantArg    any
	}{
		{"a hidden field by column", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: 7}, `"documents"."owner_id" = $1`, int64(7)},
		{"a field without the filter capability by name", &filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "eu"}, `"documents"."region_code" = $1`, "eu"},
		{"the same field by column", &filter.PolicyConstraint{Field: "region_code", Operator: filter.OpEq, Value: "us"}, `"documents"."region_code" = $1`, "us"},
		{"a filter field by column", &filter.PolicyConstraint{Field: "folder_id", Operator: filter.OpEq, Value: 3}, `"documents"."folder_id" = $1`, int64(3)},
		{"an operator the field's request operators lack", &filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpIn, Value: []any{7, 8}}, `"documents"."owner_id" = ANY($1)`, []int64{7, 8}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := docs.BuildList(nil, parsetest.PolicyOn(tt.constraint))
			require.NoError(t, err)
			assert.Contains(t, plan.Page.SQL, tt.wantSQL)
			assert.Equal(t, []any{tt.wantArg}, plan.Page.Args[:1])
		})
	}
}

func TestPolicy_ServerFieldsStayOutOfRequests(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
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
	for _, f := range docs.FilterSchema().Fields {
		fields[f.ExternalName] = true
	}
	assert.Equal(t, map[string]bool{"id": true, "title": true, "folderId": true}, fields, "the schema document advertises request fields only")
}

func TestPolicy_ServerFieldsKeepTheirTypes(t *testing.T) {
	docs := parsetest.ServerFieldDocuments(t, onPostgres)

	_, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpLike, Value: "7%"}))
	assert.ErrorIs(t, err, filter.ErrPolicyInvalid, "an operator the type has not")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven"}))
	assert.ErrorIs(t, err, filter.ErrPolicyInvalid, "a value the type refuses")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "region", Operator: filter.OpEq, Value: "apac"}))
	assert.ErrorIs(t, err, filter.ErrPolicyInvalid, "a value outside the field's enum")

	plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner_id", Operator: filter.OpEq, Value: "seven", UnknownIfInvalid: true}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, "WHERE NULL", "an invalid value under UnknownIfInvalid admits no row")

	_, err = docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "owner", Operator: filter.OpEq, Value: 7}))
	assert.ErrorIs(t, err, filter.ErrPolicyInvalid, "a name no column has still fails closed")
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
	docs := parsetest.ServerFieldDocuments(t, onPostgres)
	ast, err := filter.ApplyPolicyConstraints(docs.FilterSchema(), nil, parsetest.OwnerEUPolicy().Policy)
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

// The region types hide a column named like a visible field without the
// filter capability, declared before it and after it.
type regionHiddenFirst struct {
	ID     int    `json:"id"     db:"id"`
	Legacy string `json:"-"      db:"region"`
	Region string `json:"region" db:"region_code"`
}

type regionHiddenLast struct {
	ID     int    `json:"id"     db:"id"`
	Region string `json:"region" db:"region_code"`
	Legacy string `json:"-"      db:"region"`
}

// The title types hide a column named like a request field.
type titleHiddenFirst struct {
	ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
	Old   string `json:"-"     db:"title"`
	Label string `json:"title" db:"label" parse:"filter"`
}

type titleHiddenLast struct {
	ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"title" db:"label" parse:"filter"`
	Old   string `json:"-"     db:"title"`
}

// tagsRelation has a relation field and a column under one name.
type tagsRelation struct {
	ID      int      `json:"id"   db:"id"`
	Tags    []string `json:"-"    db:"-"    parse:"filter" filter_ops:"eq,in" relation:"document_tags,document_id,tag_id"`
	TagText string   `json:"-"    db:"tags"`
}

// policySQL compiles c as the policy of a list of T stored in documents.
func policySQL[T any](t *testing.T, c *filter.PolicyConstraint) (string, error) {
	t.Helper()
	r, err := parse.NewResource[T](onPostgres, "documents")
	require.NoError(t, err)
	plan, err := r.BuildList(nil, parsetest.PolicyOn(c))
	if err != nil {
		return "", err
	}
	return plan.Page.SQL, nil
}

// A policy name that is one field's name and another field's column is
// refused whichever field is declared first, naming both columns.
func TestPolicy_AmbiguousNamesAreRefused(t *testing.T) {
	tests := []struct {
		name    string
		compile func(*testing.T, *filter.PolicyConstraint) (string, error)
		field   string
		wantErr []string
		wantSQL string
	}{
		{"a visible field's name and a hidden column, hidden first", policySQL[regionHiddenFirst], "region", []string{"field Region (column region_code)", "field Legacy (column region)"}, ""},
		{"a visible field's name and a hidden column, hidden last", policySQL[regionHiddenLast], "region", []string{"field Region (column region_code)", "field Legacy (column region)"}, ""},
		{"the visible field by its column", policySQL[regionHiddenFirst], "region_code", nil, `"documents"."region_code" = $1`},
		{"a request field's name and a hidden column, hidden first", policySQL[titleHiddenFirst], "title", []string{"field Label (column label)", "field Old (column title)"}, ""},
		{"a request field's name and a hidden column, hidden last", policySQL[titleHiddenLast], "title", []string{"field Label (column label)", "field Old (column title)"}, ""},
		{"the request field by its column", policySQL[titleHiddenLast], "label", nil, `"documents"."label" = $1`},
		{"a relation's name and a hidden column", policySQL[tagsRelation], "tags", []string{"relation field Tags", "field TagText (column tags)"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, err := tt.compile(t, &filter.PolicyConstraint{Field: tt.field, Operator: filter.OpEq, Value: "x"})
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Contains(t, sql, tt.wantSQL)
				return
			}
			require.ErrorIs(t, err, filter.ErrAmbiguousPolicyField)
			for _, want := range tt.wantErr {
				assert.ErrorContains(t, err, want)
			}
		})
	}

	t.Run("a request still filters by the request field", func(t *testing.T) {
		r, err := parse.NewResource[titleHiddenFirst](onPostgres, "documents")
		require.NoError(t, err)
		req, err := r.ParseList(url.Values{"title": {"x"}})
		require.NoError(t, err)
		plan, err := r.BuildList(req, parse.Scope{})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.SQL, `"documents"."label" = $1`)
	})
}

// untypedPolicyFields has fields without the filter capability whose types
// a filter field could not take.
type untypedPolicyFields struct {
	ID   int      `json:"id"   db:"id"   parse:"filter"`
	Tags []string `json:"tags" db:"tags" storage:"jsonb"`
	Spot string   `json:"-"    db:"spot" geo:"plane"`
}

// A field a filter field could not type does not stop the resource from
// building; a policy that names it gets the field's error.
func TestPolicy_UntypedFieldsKeepTheirErrors(t *testing.T) {
	docs, err := parse.NewResource[untypedPolicyFields](onPostgres, "documents")
	require.NoError(t, err)

	tests := []struct {
		name    string
		field   string
		wantErr string
	}{
		{"a visible field by name", "tags", "field Tags: storage applies to json and object fields, not array"},
		{"a hidden field by column", "spot", `field Spot: geo storage "plane"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: tt.field, Operator: filter.OpNull}))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}

	plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: "id", Operator: filter.OpEq, Value: 1}))
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `"documents"."id" = $1`, "the resource's other fields still resolve")
}

type serverFieldComment struct {
	ID         int `json:"id"         db:"id"`
	DocumentID int `json:"documentId" db:"document_id"`
}

// A relation may not take a name policies resolve: it would change what a
// policy on that name constrains.
func TestRelate_RefusesPolicyNames(t *testing.T) {
	comments := parse.MustResource[serverFieldComment](onPostgres, "comments")
	for _, name := range []string{"owner_id", "region", "region_code", "folder_id"} {
		t.Run(name, func(t *testing.T) {
			docs := parsetest.ServerFieldDocuments(t, onPostgres)
			assert.ErrorContains(t, docs.Relate(name, parse.HasMany(comments, "document_id")), "server policies")

			plan, err := docs.BuildList(nil, parsetest.PolicyOn(&filter.PolicyConstraint{Field: name, Operator: filter.OpNotNull}))
			require.NoError(t, err)
			assert.NotContains(t, plan.Page.SQL, "EXISTS", "a policy on the name still constrains the column")
		})
	}

	t.Run("an ambiguous name", func(t *testing.T) {
		r, err := parse.NewResource[regionHiddenFirst](onPostgres, "documents")
		require.NoError(t, err)
		assert.ErrorContains(t, r.Relate("region", parse.HasMany(comments, "document_id")), "server policies")
	})

	t.Run("a name no field has", func(t *testing.T) {
		assert.NoError(t, parsetest.ServerFieldDocuments(t, onPostgres).Relate("comments", parse.HasMany(comments, "document_id")))
	})
}
