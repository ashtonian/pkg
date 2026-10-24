//go:build unit

package parse_test

import (
	"net/url"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// GEN-15: a server type that embeds its wire type is the same
// resource as the type written flat.

type deviceFlat struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
	Hash     string `json:"-"         db:"hash"      parse:"server"`
}

type DeviceWire struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
}

type deviceEmbedded struct {
	DeviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

type DeviceMiddle struct{ DeviceWire }

type deviceTwoDeep struct {
	DeviceMiddle
	Hash string `json:"-" db:"hash" parse:"server"`
}

type devicePointer struct {
	*DeviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

type deviceWire struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
}

type deviceUnexported struct {
	deviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

// resourceView is everything a resource derives from its type.
type resourceView struct {
	Columns    []string
	Filter     map[string]string // name -> SQL expression and operators
	Sort       map[string]string
	Search     []string
	Patch      map[string]string
	Server     map[string]string
	EndCursor  string
	Key        any
	Patched    map[string]any // the patched row's fields, by column
	FilterSQL  string
	FilterArgs []any
}

func viewOf[T any](t *testing.T, row func(id, name string, rank int64, hash string) T) resourceView {
	t.Helper()
	r, err := parse.NewResource[T]("devices", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	v := resourceView{Columns: r.Columns(filter.PostgresDialect{}), Filter: map[string]string{}, Sort: map[string]string{}, Patch: map[string]string{}, Server: map[string]string{}}
	for name, f := range r.FilterSchema().Fields {
		v.Filter[name] = f.SQLExpr + " " + string(f.DataType)
	}
	for name, f := range r.SortSchema().Fields {
		v.Sort[name] = f.SQLExpr + " " + f.Column
	}
	for _, f := range r.SearchSchema().Fields {
		v.Search = append(v.Search, f.Name+" "+f.SQLExpr)
	}
	for path, p := range r.PatchSchema().AllowedPaths {
		v.Patch[path] = p.SQLColumn
	}
	for path, p := range r.ServerPatchSchema().AllowedPaths {
		v.Server[path] = p.SQLColumn
	}

	req, err := r.ParseList(url.Values{"name": {"pump"}, "sort": {"-rank,name"}, "page_size": {"2"}})
	require.NoError(t, err, "a promoted field is filterable and sortable")
	plan, err := r.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	v.FilterSQL, v.FilterArgs = plan.Page.SQL, plan.Page.Args
	rows := []T{row("a", "pump", 3, "x"), row("b", "pump", 2, "y"), row("c", "pump", 1, "z")}
	page, err := r.Page(rows, plan)
	require.NoError(t, err)
	v.EndCursor = string(page.PageInfo.EndCursor)
	v.Key, err = r.Key(rows[0])
	require.NoError(t, err)

	patch, err := r.ParseMergePatch([]byte(`{"name":"valve","rank":9}`))
	require.NoError(t, err)
	patched, err := r.ApplyValidatedPatch(patch, rows[0])
	require.NoError(t, err)
	server, err := r.ParseServerMergePatch([]byte(`{"hash":"rotated"}`))
	require.NoError(t, err)
	patched, err = r.ApplyValidatedPatch(server, patched)
	require.NoError(t, err)
	v.Patched = map[string]any{}
	for _, col := range []string{"id", "name", "rank", "hash"} {
		got, err := filter.CursorValueByTag(patched, "db", col)
		require.NoError(t, err)
		v.Patched[col] = got
	}
	slices.Sort(v.Search)
	return v
}

func TestEmbeddedWireType_IsTheFlatResource(t *testing.T) {
	want := viewOf(t, func(id, name string, rank int64, hash string) deviceFlat {
		return deviceFlat{ID: id, TenantID: "t1", Name: name, Rank: rank, Hash: hash}
	})
	assert.NotEmpty(t, want.Filter["name"])
	for name, got := range map[string]resourceView{
		"depth 1": viewOf(t, func(id, name string, rank int64, hash string) deviceEmbedded {
			return deviceEmbedded{DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
		}),
		"depth 2": viewOf(t, func(id, name string, rank int64, hash string) deviceTwoDeep {
			return deviceTwoDeep{DeviceMiddle{DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}}, hash}
		}),
		"pointer embed": viewOf(t, func(id, name string, rank int64, hash string) devicePointer {
			return devicePointer{&DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
		}),
		"unexported embedded type": viewOf(t, func(id, name string, rank int64, hash string) deviceUnexported {
			return deviceUnexported{deviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
		}),
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, want, got) })
	}
}

// A patch through a nil embedded pointer allocates it, as encoding/json does
// when decoding; the caller's value is untouched.
func TestEmbeddedWireType_PatchAllocatesANilEmbed(t *testing.T) {
	r, err := parse.NewResource[devicePointer]("devices")
	require.NoError(t, err)
	patch, err := r.ParseMergePatch([]byte(`{"name":"valve"}`))
	require.NoError(t, err)
	original := devicePointer{Hash: "h"}
	got, err := r.ApplyValidatedPatch(patch, original)
	require.NoError(t, err)
	require.NotNil(t, got.DeviceWire)
	assert.Equal(t, "valve", got.Name)
	assert.Nil(t, original.DeviceWire)
	_, err = r.Key(original)
	assert.ErrorContains(t, err, "nil pointer", "a key under a nil embed is not there")
	assert.Equal(t, reflect.TypeFor[devicePointer](), reflect.TypeOf(got))
}
