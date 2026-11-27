//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestSchemaRegistrySnapshotOwnership(t *testing.T) {
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	response := r.SchemaFor("items")
	response.Filter.Fields["name"].Operators[0] = filter.OpNe
	response.Filter.Fields["name"].EnumValues[0] = "changed"
	response.Patch.Paths["/name"].AllowedOps[0] = patch.PatchOpRemove
	got := r.SchemaFor("items")
	if got.Filter.Fields["name"].Operators[0] != filter.OpEq || got.Filter.Fields["name"].EnumValues[0] != "a" || got.Patch.Paths["/name"].AllowedOps[0] != patch.PatchOpReplace {
		t.Fatal("editing a discovery response changed registered validation metadata")
	}
}

// A name is registered once: a second registration under it, by type or by
// resource, is refused and leaves the first as it was.
func TestSchemaRegistry_RefusesATakenName(t *testing.T) {
	type plain struct {
		ID   string `json:"id"   db:"id"`
		Name string `json:"name" db:"name" parse:"filter,patch"`
	}
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	require.ErrorIs(t, parse.Register[plain](r), parse.ErrRegistered)
	items, err := parse.NewResource[plain](onSQLite, "items")
	require.NoError(t, err)
	err = parse.RegisterResource(r, items)
	require.ErrorIs(t, err, parse.ErrRegistered)
	assert.EqualError(t, err, "parse: items: already registered")
	require.Error(t, parse.Register[int](r))

	got := r.SchemaFor("items")
	require.NotNil(t, got)
	assert.NotNil(t, got.Sort, "the first registration's capabilities stay")
	assert.NotNil(t, got.Search)
	assert.Equal(t, []string{"items"}, r.ResourceNames())

	assert.PanicsWithError(t, "parse: items: already registered", func() { parse.MustRegister[plain](r) })
	assert.PanicsWithError(t, "parse: items: already registered", func() { parse.MustRegisterResource(r, items) })
}

// Two resources over one table register under one name: the registry holds
// the first, never the wider of the two.
func TestRegisterResource_TwoResourcesOverOneTable(t *testing.T) {
	type narrow struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Name string `json:"name" db:"name" parse:"filter"`
	}
	type wide struct {
		ID     string `json:"id"     db:"id"     parse:"filter"`
		Name   string `json:"name"   db:"name"   parse:"filter"`
		Secret string `json:"secret" db:"secret" parse:"filter"`
	}
	r := parse.NewSchemaRegistry()
	parse.MustRegisterResource(r, parse.MustResource[narrow](onSQLite, "users"))
	require.ErrorIs(t, parse.RegisterResource(r, parse.MustResource[wide](onSQLite, "users")), parse.ErrRegistered)
	assert.NotContains(t, r.SchemaFor("users").Filter.Fields, "secret")
}

// The registry serves a resource's search schema as a copy, like its other
// three; a view is not registered.
func TestSchemaRegistry_SearchSchemaAndViews(t *testing.T) {
	r := parse.NewSchemaRegistry()
	items := parse.MustResource[registrySnapshotEntity](onSQLite, "items")
	parse.MustRegisterResource(r, items)
	search, ok := r.SearchSchema("items")
	require.True(t, ok)
	require.Len(t, search.Fields, 1)
	search.Fields[0].Name = "changed"
	again, _ := r.SearchSchema("items")
	assert.Equal(t, "name", again.Fields[0].Name)
	_, ok = r.SearchSchema("unknown")
	assert.False(t, ok)

	view, err := items.WithPaths()
	require.NoError(t, err)
	assert.ErrorContains(t, parse.RegisterResource(parse.NewSchemaRegistry(), view), "parse: items: a view (WithPaths, In) is a value for one request and is not registered")
}
