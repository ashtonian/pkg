//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Two resources over one table: the wide one serves secret, the narrow one
// withholds its column (ReadColumns).
type wideUser struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Secret   string `json:"secret"    db:"secret"    parse:"filter,sort,patch"`
}

type narrowUser struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Secret   string `json:"-"         db:"secret"`
}

func ownershipResources(t *testing.T, opts ...parse.ResourceOption) (*parse.Resource[wideUser], *parse.Resource[narrowUser]) {
	t.Helper()
	wide, err := parse.NewResource[wideUser]("users", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	narrow, err := parse.NewResource[narrowUser]("users", append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.ReadColumns("id", "tenant_id", "name"),
	}, opts...)...)
	require.NoError(t, err)
	return wide, narrow
}

var secretQuery = url.Values{filter.ParamFilter: {`{"secret":"x"}`}, filter.ParamSort: {"secret"}}

// A resource compiles only the list requests it parsed: another resource's
// over the same table would reach a column it withholds.
func TestBuildList_RefusesARequestItDidNotParse(t *testing.T) {
	wide, narrow := ownershipResources(t)
	scope := parse.Scope{Tenant: "t1"}

	_, err := narrow.ParseList(secretQuery)
	require.ErrorContains(t, err, "E2071", "the narrow resource has no secret field")

	wideReq, err := wide.ParseList(secretQuery)
	require.NoError(t, err)
	_, err = narrow.BuildList(filter.PostgresDialect{}, wideReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	assert.ErrorContains(t, err, "parse: users: list request")

	_, err = narrow.BuildList(filter.PostgresDialect{}, &parse.ListRequest{}, scope)
	require.ErrorIs(t, err, parse.ErrForeign, "a hand-built request")

	own, err := narrow.ParseList(url.Values{filter.ParamFilter: {`{"name":"a"}`}})
	require.NoError(t, err)
	plan, err := narrow.BuildList(filter.PostgresDialect{}, own, scope)
	require.NoError(t, err)
	assert.NotContains(t, plan.Page.SQL, "secret")
	_, err = narrow.BuildList(filter.PostgresDialect{}, nil, scope)
	require.NoError(t, err, "a nil request is a first page")
}

// A WithPaths view and its base each compile only their own list requests
// and filters; they share patch schemas, so each accepts the other's patches.
func TestWithPaths_ViewAndBaseKeepTheirOwnRequests(t *testing.T) {
	_, base := ownershipResources(t)
	view, err := base.WithPaths()
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	q := url.Values{filter.ParamFilter: {`{"name":"a"}`}}

	baseReq, err := base.ParseList(q)
	require.NoError(t, err)
	viewReq, err := view.ParseList(q)
	require.NoError(t, err)
	_, err = view.BuildList(filter.SQLiteDialect{}, viewReq, scope)
	require.NoError(t, err)
	_, err = view.BuildList(filter.SQLiteDialect{}, baseReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = base.BuildList(filter.SQLiteDialect{}, viewReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)

	baseFilter, err := base.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	p, err := base.ParseMergePatch([]byte(`{"name":"b"}`))
	require.NoError(t, err)
	_, err = view.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByFilter(baseFilter), scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = view.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("u1"), scope)
	require.NoError(t, err, "the view shares its base's patch schema")
}

// Updates and patch application take only the filters and patches the
// resource parsed.
func TestBuildUpdate_RefusesAFilterOrPatchItDidNotParse(t *testing.T) {
	wide, narrow := ownershipResources(t)
	scope := parse.Scope{Tenant: "t1"}
	d := filter.PostgresDialect{}

	ownPatch, err := narrow.ParseMergePatch([]byte(`{"name":"b"}`))
	require.NoError(t, err)
	widePatch, err := wide.ParseMergePatch([]byte(`{"secret":"s"}`))
	require.NoError(t, err)
	wideFilter, err := wide.ParseFilterMap(map[string]any{"secret": "x"})
	require.NoError(t, err)
	ast, err := filter.NewFilterParser(narrow.FilterSchema(), nil).ParseMap(map[string]any{"name": "a"})
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		patch  *patch.ParsedPatch
		target parse.Target
	}{
		"another resource's filter": {ownPatch, parse.ByFilter(wideFilter)},
		"an unmarked filter":        {ownPatch, parse.ByFilter(filter.NewParsedFilter(ast))},
		"another resource's patch":  {widePatch, parse.ByID("u1")},
		"a nil patch":               {nil, parse.ByID("u1")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := narrow.BuildUpdate(d, tc.patch, tc.target, scope)
			require.ErrorIs(t, err, parse.ErrForeign)
		})
	}

	_, err = narrow.ApplyValidatedPatch(widePatch, narrowUser{})
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = narrow.BuildUpdateBatch(d, []parse.RowUpdate{{Key: "u1", Patch: ownPatch}, {Key: "u2", Patch: widePatch}}, scope, parse.UpdateBatchLimits{})
	require.ErrorIs(t, err, parse.ErrForeign)
	assert.ErrorContains(t, err, "update batch row 1 patch")

	serverPatch, err := narrow.ServerMergePatchMap(map[string]any{"name": "c"})
	require.NoError(t, err)
	ownFilter, err := narrow.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	_, err = narrow.BuildUpdate(d, serverPatch, parse.ByFilter(ownFilter), scope)
	require.NoError(t, err, "the server patch schema is the resource's too")
}

// WithFilter swaps in a filter the request's resource parsed, and nothing
// else.
func TestListRequest_WithFilterKeepsTheRequestSealed(t *testing.T) {
	wide, narrow := ownershipResources(t)
	req, err := narrow.ParseList(url.Values{filter.ParamPageSize: {"7"}})
	require.NoError(t, err)

	wideFilter, err := wide.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	_, err = req.WithFilter(wideFilter)
	require.ErrorIs(t, err, parse.ErrForeign)

	own, err := narrow.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	filtered, err := req.WithFilter(own)
	require.NoError(t, err)
	assert.Same(t, own, filtered.Filter())
	assert.Equal(t, 7, filtered.PageSize())
	assert.Nil(t, req.Filter(), "the original request is unchanged")
	_, err = narrow.BuildList(filter.SQLiteDialect{}, filtered, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
}

// What the accessors and the registry hand out are copies: changing them
// changes nothing the resource parses or the registry describes.
func TestSchemaAccessors_HandOutCopies(t *testing.T) {
	_, narrow := ownershipResources(t)
	reg := parse.NewSchemaRegistry()
	parse.RegisterResource(reg, narrow)
	secret := func() *filter.FieldSchema {
		return &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
	}

	narrow.FilterSchema().Fields["secret"] = secret()
	registered, ok := reg.FilterSchema("users")
	require.True(t, ok)
	registered.Fields["secret"] = secret()
	narrow.SortSchema().Fields["secret"] = &filter.SortFieldSchema{Table: "users", Column: "secret", DataType: core.FieldTypeString}
	require.NoError(t, narrow.PatchSchema().AddPath("/secret", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "secret"}))
	pathOf(narrow.ServerPatchSchema(), "/name").AllowedOps = nil

	_, err := narrow.ParseList(secretQuery)
	require.ErrorContains(t, err, "E2071")
	_, err = narrow.ParseList(url.Values{filter.ParamSort: {"secret"}})
	require.Error(t, err)
	_, err = narrow.ParseMergePatch([]byte(`{"secret":"s"}`))
	require.Error(t, err)
	_, err = narrow.ServerMergePatchMap(map[string]any{"name": "c"})
	require.NoError(t, err)
	assert.NotContains(t, reg.SchemaFor("users").Filter.Fields, "secret")
}

// Customization runs before the read-set checks, and a schema the function
// keeps changes nothing once the resource is built.
func TestCustomizeSchemas_IsHeldToTheReadSet(t *testing.T) {
	_, err := parse.NewResource[narrowUser]("users", parse.ReadColumns("id", "tenant_id", "name"), parse.CustomizeSchemas(func(s parse.Schemas) error {
		s.Filter.Fields["secret"] = &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
		return nil
	}))
	require.ErrorContains(t, err, `"secret"`, "a customized field over a withheld column is refused")

	_, err = parse.NewResource[narrowUser]("users", parse.CustomizeSchemas(func(parse.Schemas) error { return assert.AnError }))
	require.ErrorIs(t, err, assert.AnError)

	var kept parse.Schemas
	_, narrow := ownershipResources(t, parse.CustomizeSchemas(func(s parse.Schemas) error {
		kept = s
		return nil
	}))
	kept.Filter.Fields["secret"] = &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
	_, err = narrow.ParseList(secretQuery)
	require.ErrorContains(t, err, "E2071")
}
