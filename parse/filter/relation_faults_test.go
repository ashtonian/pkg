//go:build unit

package filter_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type faultWidget struct {
	ID    string   `db:"id" json:"id" parse:"filter"`
	Tags  []string `db:"-" json:"-" parse:"filter,include" filter_ops:"eq,in,nin" relation:"widget_tags,widget_id,tag_id,id,tenant_id" indexed:"true"`
	Ranks []int64  `db:"-" json:"-" parse:"filter" filter_ops:"eq,gt" relation:"widget_ranks,widget_id,rank_id,id,-" indexed:"true"`
}

// What the server declares or wires so that a relation cannot be evaluated
// — no lookup outside SQL, a relation evaluated before it is resolved, a
// comparison only SQL evaluates, a relation without its target — is the
// server's fault: an error matching its sentinel, never a *core.ParseError a client
// would be answered with. The request is valid in every case.
func TestRelation_ServerFaultsAreSentinels(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[faultWidget](core.NewSchemaGenerator())
	require.NoError(t, err)
	// The owner's key, which a resource supplies (parse.NewResource).
	owner := []*filter.FieldSchema{{ExternalName: "id", Table: "fault_widgets", Column: "id", DataType: core.FieldTypeString}}
	schema.Fields["tags"].RelatedBy = owner
	schema.Fields["ranks"].RelatedBy = owner
	parse := func(t *testing.T, s *filter.ResourceSchema, f string) *filter.FilterAST {
		t.Helper()
		ast, err := filter.NewFilterParser(s, nil).Parse(f)
		require.NoError(t, err, "the request is valid")
		return ast
	}
	tenant := filter.ResolveRelationScope(filter.RelationScope{Tenant: "t1"})
	untargeted := schema.Clone()
	untargeted.Fields["tags"].AllowedOperators = append(untargeted.Fields["tags"].AllowedOperators, filter.OpSome)

	for _, tc := range []struct {
		name     string
		err      func(t *testing.T) error
		sentinel error
	}{
		{"memberships resolved without a lookup", func(t *testing.T) error {
			_, err := filter.ResolveMemberships(t.Context(), filter.NewParsedFilter(parse(t, schema, `{"tags":"red"}`)), nil, tenant)
			return err
		}, filter.ErrRelationUnresolvable},
		{"a relation evaluated before it is resolved", func(t *testing.T) error {
			_, err := filter.Evaluate(parse(t, schema, `{"tags":"red"}`).Root, filter.ColumnRow(func(string) (any, bool) { return nil, true }))
			return err
		}, filter.ErrRelationUnresolvable},
		{"an include resolved without a lookup", func(t *testing.T) error {
			_, err := filter.ResolveInclude(t.Context(), schema.Fields["tags"], []core.Key{{"w1"}}, nil, filter.WithIncludeRelationScope(filter.RelationScope{Tenant: "t1"}))
			return err
		}, filter.ErrRelationUnresolvable},
		{"a key comparison only SQL evaluates", func(t *testing.T) error {
			_, err := filter.ResolveMemberships(t.Context(), filter.NewParsedFilter(parse(t, schema, `{"ranks":{"$gt":3}}`)), noLookup{})
			return err
		}, filter.ErrRelationUnresolvable},
		{"a relation without a target resource", func(t *testing.T) error {
			_, err := filter.NewFilterParser(untargeted, nil).Parse(`{"tags":{"$some":{}}}`)
			return err
		}, filter.ErrRelationUnresolvable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err(t)
			require.ErrorIs(t, err, tc.sentinel)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError, "%v", err)
		})
	}
}
