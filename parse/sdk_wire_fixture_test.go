//go:build unit

package parse_test

// Go-emitted wire fixture for the TypeScript SDK. The
// pages are a real keyset walk on SQLite serialized exactly as filter.Page
// marshals; the schema is the registry's response. The TS client tests
// (sdk/ts/src/client.test.ts) serve these bodies from a fake fetch, so the
// SDK is held to the server's wire shape rather than to its own types.
//
// Regenerate after an intentional wire change:
//
//	go test -tags=unit -run TestSDKWireFixture -update ./

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

const sdkWireFixturePath = "testdata/sdk_wire.json"

type wireItem struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Name   string         `json:"name"   db:"name"   parse:"filter,sort,patch,facet" filter_ops:"eq,ne,like,in" enum:"alpha,bravo,charlie,delta,echo" facet:"terms:5"`
	Labels map[string]any `json:"labels" db:"labels" parse:"filter,patch"`
	// Unit's name holds "/" and "~", which a JSON Pointer escapes: the SDK
	// must look it up as /unit~1si~0v (review C07).
	Unit *string  `json:"unit/si~v" db:"unit" parse:"patch"`
	Tags []string `json:"-" db:"-" parse:"filter,include" relation:"wire_item_tags,item_id,tag_id"`
}

// wirePart is the target of wireItem's declared relation "parts".
type wirePart struct {
	ID     string `json:"id"      db:"id"      parse:"filter"`
	ItemID string `json:"item_id" db:"item_id" parse:"filter"`
	State  string `json:"state"   db:"state"   parse:"filter" enum:"ok,worn"`
}

type wireExchange struct {
	Cursor   string          `json:"cursor"`
	Response json.RawMessage `json:"response"`
}

type sdkWire struct {
	// Params are the list request parameter names (filter.Param*), keyed by
	// constant, so the SDK's query encoding follows the server's names.
	Params   map[string]string     `json:"params"`
	PageSize int                   `json:"page_size"`
	Sort     string                `json:"sort"`
	List     []wireExchange        `json:"list"`
	Schema   *parse.SchemaResponse `json:"schema"`
	// Related are the schemas of the relation targets Schema names, by
	// resource, as the registry serves them.
	Related map[string]*parse.SchemaResponse `json:"related"`
}

func TestSDKWireFixture(t *testing.T) {
	got := buildSDKWire(t)
	if *updateConformance {
		require.NoError(t, os.WriteFile(sdkWireFixturePath, got, 0o644))
		return
	}
	want, err := os.ReadFile(sdkWireFixturePath)
	require.NoError(t, err, "missing fixture; regenerate with -update")
	require.JSONEq(t, string(want), string(got), "wire fixture is stale; regenerate with -update")
}

func buildSDKWire(t *testing.T) []byte {
	t.Helper()
	db := dbtest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE wire_items (id TEXT PRIMARY KEY, name TEXT NOT NULL, labels TEXT, unit TEXT)`)
	require.NoError(t, err)
	for i, name := range []string{"echo", "alpha", "delta", "bravo", "charlie"} {
		_, listErr := db.Exec(`INSERT INTO wire_items (id, name, labels) VALUES (?1, ?2, '{}')`, string(rune('a'+i)), name)
		require.NoError(t, listErr)
	}

	p, err := parse.NewResource[wireItem]("wire_items")
	require.NoError(t, err)
	wire := sdkWire{
		Params: map[string]string{
			"ParamFilter": filter.ParamFilter, "ParamSort": filter.ParamSort, "ParamCursor": filter.ParamCursor,
			"ParamPageSize": filter.ParamPageSize, "ParamIncludeTotal": filter.ParamIncludeTotal,
			"ParamIncludeDeleted": filter.ParamIncludeDeleted, "ParamSearch": filter.ParamSearch,
			"ParamFacets": filter.ParamFacets, "ParamFields": filter.ParamFields, "ParamInclude": filter.ParamInclude,
		},
		PageSize: 2, Sort: "name",
	}
	cursor := ""
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		opts := []filter.ListOption{filter.ListSort(wire.Sort), filter.ListLimit(wire.PageSize), filter.ListUnscopedRelations()}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		} else {
			opts = append(opts, filter.ListFacets("name"))
		}
		res, listErr := p.ListQuery(filter.SQLiteDialect{}).Build(opts...)
		require.NoError(t, listErr)
		rows, listErr := db.Query("SELECT id, name FROM wire_items "+res.Fragment, res.Args...)
		require.NoError(t, listErr)
		var items []wireItem
		for rows.Next() {
			var it wireItem
			require.NoError(t, rows.Scan(&it.ID, &it.Name))
			it.Labels = map[string]any{}
			items = append(items, it)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())

		page, listErr := filter.NewPage(items, res.Limit, res.Cursor, func(it wireItem, col string) (any, error) {
			return filter.CursorValueByTag(it, "db", col)
		})
		require.NoError(t, listErr)
		if n == 0 {
			// An `include=tags` projection whose first owner was cut at the
			// cap, as a repository attaches it, and the `facets=name` counts
			// the facet query returns.
			page.Relations = map[string]map[string][]string{"tags": {page.Items[0].ID: {"t1", "t2"}}}
			page.Truncated = map[string][]string{"tags": {page.Items[0].ID}}
			facetRows, facetErr := db.Query(res.Facets.SQL, res.Facets.Args...)
			require.NoError(t, facetErr)
			facets, facetErr := filter.ScanFacetUnion(facetRows)
			require.NoError(t, errors.Join(facetErr, facetRows.Close()))
			page.Facets = filter.FacetMap(facets)
		}
		body, listErr := json.Marshal(page)
		require.NoError(t, listErr)
		wire.List = append(wire.List, wireExchange{Cursor: cursor, Response: body})
		if !page.PageInfo.HasNextPage {
			break
		}
		cursor = string(page.PageInfo.EndCursor)
	}

	items := parse.MustResource[wireItem]("wire_items")
	parts := parse.MustResource[wirePart]("wire_parts")
	items.MustRelate("parts", parse.HasMany(parts, "item_id", parse.Indexed()))
	reg := parse.NewSchemaRegistry()
	parse.RegisterResource(reg, items)
	parse.RegisterResource(reg, parts)
	wire.Schema = reg.SchemaFor("wire_items")
	require.NotNil(t, wire.Schema)
	wire.Related = map[string]*parse.SchemaResponse{"wire_parts": reg.SchemaFor("wire_parts")}

	out, err := json.MarshalIndent(wire, "", "  ")
	require.NoError(t, err)
	return append(out, '\n')
}
