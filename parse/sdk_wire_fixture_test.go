//go:build unit

package parse_test

// The wire fixture the SDKs are held to: requests built by the Go client
// (parse/client) and the answers parse's handlers gave them, recorded by
// serving a resource over SQLite with parsehttp and parsesql. The TypeScript
// client tests (sdk/ts/src/client.test.ts) replay each exchange: the request
// they build must equal the recorded one, and they decode the recorded
// answer, so the TypeScript SDK, the Go client and the handlers agree on
// every byte.
//
// Regenerate after an intentional wire change:
//
//	go test -tags=unit -run TestSDKWireFixture -update ./

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/client"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/parsesql"
)

const sdkWireFixturePath = "testdata/sdk_wire.json"

type wireItem struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Name   string         `json:"name"   db:"name"   parse:"filter,sort,patch,facet" filter_ops:"eq,ne,like,in" enum:"alpha,bravo,charlie,delta,echo" facet:"terms:5"`
	Labels map[string]any `json:"labels" db:"labels" parse:"filter,patch"`
	// Unit's name holds "/" and "~", which a JSON Pointer escapes: the SDK
	// must look it up as /unit~1si~0v.
	Unit *string  `json:"unit/si~v" db:"unit" parse:"patch"`
	Tags []string `json:"-" db:"-" parse:"filter,include" relation:"wire_item_tags,item_id,tag_id"`
}

// wirePart is the target of wireItem's declared relation "parts".
type wirePart struct {
	ID     string `json:"id"      db:"id"      parse:"filter"`
	ItemID string `json:"item_id" db:"item_id" parse:"filter"`
	State  string `json:"state"   db:"state"   parse:"filter" enum:"ok,worn"`
}

// wireStock is keyed by two columns, so its item URL holds a segment per
// column, each escaped on its own.
type wireStock struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id"`
	SKU         string `json:"sku"          db:"sku"`
	Qty         int    `json:"qty"          db:"qty" parse:"patch"`
}

// wireRequest is a request as the server receives it. Query holds every
// value of each parameter; Body is JSON.
type wireRequest struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	Query       map[string][]string `json:"query,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	Body        json.RawMessage     `json:"body,omitempty"`
}

type wireResponse struct {
	Status      int             `json:"status"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
}

type wireCall struct {
	Request  wireRequest  `json:"request"`
	Response wireResponse `json:"response"`
}

type sdkWire struct {
	// Params are the list request parameter names (filter.Param*), keyed by
	// constant, so the SDK's query encoding follows the server's names.
	Params   map[string]string `json:"params"`
	PageSize int               `json:"page_size"`
	Sort     string            `json:"sort"`
	// List is a keyset walk of the list route, a page per call, each
	// request carrying the end cursor of the page before it.
	List []wireCall `json:"list"`
	// Exchanges are the other requests the SDK sends, by name, with the
	// answers the handlers gave.
	Exchanges map[string]wireCall `json:"exchanges"`
}

func TestSDKWireFixture(t *testing.T) {
	got := buildSDKWire(t)
	if *updateRendered {
		require.NoError(t, os.WriteFile(sdkWireFixturePath, got, 0o644))
		return
	}
	want, err := os.ReadFile(sdkWireFixturePath)
	require.NoError(t, err, "missing fixture; regenerate with -update")
	require.JSONEq(t, string(want), string(got), "wire fixture is stale; regenerate with -update")
}

// wireTagsPerOwner is one more tag than an include returns for an owner,
// so the first owner's include is cut and named in _truncated.
const wireTagsPerOwner = filter.DefaultMaxIncludePerRelation + 1

func wireDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE wire_items (id TEXT PRIMARY KEY, name TEXT NOT NULL, labels TEXT NOT NULL, unit TEXT)`,
		`CREATE TABLE wire_parts (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, state TEXT NOT NULL)`,
		`CREATE TABLE wire_item_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id))`,
		`CREATE TABLE wire_stock (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`,
		// Each column alone, and the SKU's text up to its slash, names
		// another row than the pair does.
		`INSERT INTO wire_stock VALUES ('w1', 'a/b', 0), ('w1', 'a', 0), ('w2', 'a/b', 0)`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err)
	}
	for i, name := range []string{"echo", "alpha", "delta", "bravo", "charlie"} {
		_, err := db.Exec(`INSERT INTO wire_items (id, name, labels) VALUES (?1, ?2, '{}')`, string(rune('a'+i)), name)
		require.NoError(t, err)
	}
	for i := range wireTagsPerOwner {
		_, err := db.Exec(`INSERT INTO wire_item_tags VALUES ('b', ?1)`, fmt.Sprintf("t%03d", i+1))
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO wire_item_tags VALUES ('d', 't001'), ('d', 't002')`)
	require.NoError(t, err)
	return db
}

// wireServer serves wire_items as a parse server does: lists by GET and
// POST, facets by OPTIONS, merge patches of one row or of the rows id[]
// names, and the schema routes; and merge patches of one wire_stock row by
// its key of two columns.
func wireServer(t *testing.T, db *sql.DB) http.Handler {
	t.Helper()
	items := parse.MustResource[wireItem]("wire_items")
	parts := parse.MustResource[wirePart]("wire_parts")
	items.MustRelate("parts", parse.HasMany(parts, "item_id", parse.Indexed()))
	stock := parse.MustResource[wireStock]("wire_stock", parse.PrimaryKey("warehouse_id", "sku"))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, items))
	require.NoError(t, parse.RegisterResource(reg, parts))
	require.NoError(t, parse.RegisterResource(reg, stock))

	// handle answers a parse error with its body; any other error fails
	// the recording.
	handle := func(serve func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if err := serve(w, r); err != nil && !parsehttp.WriteError(w, r, err) {
				t.Fatalf("%s %s: %v", r.Method, r.URL, err)
			}
		}
	}

	mux := http.NewServeMux()
	routeItem(t, mux, db, items, handle)
	routeItem(t, mux, db, stock, handle)
	list := handle(func(w http.ResponseWriter, r *http.Request) error {
		req, err := parsehttp.ParseList(r, items)
		if err != nil {
			return err
		}
		plan, err := items.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
		if err != nil {
			return err
		}
		page, err := parsesql.QueryPage(r.Context(), db, items, plan)
		if err != nil {
			return err
		}
		return parsehttp.WriteList(w, r, req, page)
	})
	mux.Handle("GET /wire_items", list)
	mux.Handle("POST /wire_items", list)
	mux.Handle("OPTIONS /wire_items", handle(func(w http.ResponseWriter, r *http.Request) error {
		req, err := parsehttp.ParseFacets(r, items)
		if err != nil {
			return err
		}
		query, err := items.BuildFacets(filter.SQLiteDialect{}, req, parse.Scope{})
		if err != nil {
			return err
		}
		rows, err := db.QueryContext(r.Context(), query.SQL, query.Args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		facets, err := filter.ScanFacetUnion(rows)
		if err != nil {
			return err
		}
		return parsehttp.WriteJSON(w, r, http.StatusOK, parsehttp.FacetsAnswer{Facets: filter.FacetMap(facets)})
	}))
	mux.Handle("PATCH /wire_items", handle(func(w http.ResponseWriter, r *http.Request) error {
		q, err := parsehttp.Query(r)
		if err != nil {
			return err
		}
		ids := q["id[]"]
		updated, err := wireUpdate(r, db, items, parse.ByIDs(anys(ids)...))
		if err != nil {
			return err
		}
		answer := parsehttp.PatchAnswer[wireItem]{Updated: updated}
		for _, id := range ids {
			if !slices.ContainsFunc(updated, func(it wireItem) bool { return it.ID == id }) {
				answer.Failed = append(answer.Failed, parsehttp.PatchFailure{ID: id, Code: "E4004", Error: "wire item " + id + " not found"})
			}
		}
		return parsehttp.WriteJSON(w, r, http.StatusOK, answer)
	}))
	mux.Handle("GET /{resource}/schema", parsehttp.SchemaHandler(reg))
	mux.Handle("GET /schemas", parsehttp.SchemaListHandler(reg))
	return mux
}

// routeItem serves the merge patch of one row of res below its table's
// collection path, at a segment per key column (parsehttp.ItemPattern),
// read back as the row's key (parsehttp.ItemKey).
func routeItem[T any](t *testing.T, mux *http.ServeMux, db *sql.DB, res *parse.Resource[T],
	handle func(func(http.ResponseWriter, *http.Request) error) http.HandlerFunc,
) {
	t.Helper()
	pattern, err := parsehttp.ItemPattern(res)
	require.NoError(t, err)
	mux.Handle("PATCH /"+res.Table()+"/"+pattern, handle(func(w http.ResponseWriter, r *http.Request) error {
		key, err := parsehttp.ItemKey(r, res)
		if err != nil {
			return err
		}
		updated, err := wireUpdate(r, db, res, parse.ByKey(key))
		if err != nil {
			return err
		}
		return parsehttp.WriteJSON(w, r, http.StatusOK, parsehttp.PatchAnswer[T]{Updated: updated})
	}))
}

// wireUpdate applies r's merge patch to target's rows of res and returns
// them as the update left them.
func wireUpdate[T any](r *http.Request, db *sql.DB, res *parse.Resource[T], target parse.Target) ([]T, error) {
	parsed, err := parsehttp.ParsePatch(r, res)
	if err != nil {
		return nil, err
	}
	plan, err := res.BuildUpdate(filter.SQLiteDialect{}, parsed, target, parse.Scope{})
	if err != nil {
		return nil, err
	}
	updated, err := parsesql.Update(r.Context(), db, res, plan)
	return slices.Concat(updated.Changed, updated.Unchanged), err
}

func anys[T any](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

// record sends req to h and returns it with h's answer.
func record(t *testing.T, h http.Handler, req wireRequest) wireCall {
	t.Helper()
	target := req.Path
	if len(req.Query) > 0 {
		target += "?" + url.Values(req.Query).Encode()
	}
	r := httptest.NewRequestWithContext(t.Context(), req.Method, target, bytes.NewReader(req.Body))
	if req.ContentType != "" {
		r.Header.Set("Content-Type", req.ContentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	require.True(t, json.Valid(rec.Body.Bytes()), "%s %s answered %d %s", req.Method, target, rec.Code, rec.Body)
	return wireCall{Request: req, Response: wireResponse{
		Status: rec.Code, ContentType: rec.Header().Get("Content-Type"), Body: bytes.TrimSpace(rec.Body.Bytes()),
	}}
}

// listGET is the GET a Go client sends for p.
func listGET(t *testing.T, p client.ListParams) wireRequest {
	t.Helper()
	q, err := p.Values()
	require.NoError(t, err)
	return wireRequest{Method: http.MethodGet, Path: "/wire_items", Query: q}
}

// pageOf decodes the page of a list call.
func pageOf(t *testing.T, c wireCall) filter.Page[wireItem] {
	t.Helper()
	require.Equal(t, http.StatusOK, c.Response.Status, "%s", c.Response.Body)
	var page filter.Page[wireItem]
	require.NoError(t, json.Unmarshal(c.Response.Body, &page))
	return page
}

func buildSDKWire(t *testing.T) []byte {
	t.Helper()
	h := wireServer(t, wireDB(t))
	name := client.NewSortableText("name")
	wire := sdkWire{
		Params: map[string]string{
			"ParamFilter": filter.ParamFilter, "ParamSort": filter.ParamSort, "ParamCursor": filter.ParamCursor,
			"ParamPageSize": filter.ParamPageSize, "ParamIncludeTotal": filter.ParamIncludeTotal,
			"ParamIncludeDeleted": filter.ParamIncludeDeleted, "ParamSearch": filter.ParamSearch,
			"ParamFacets": filter.ParamFacets, "ParamFields": filter.ParamFields, "ParamInclude": filter.ParamInclude,
		},
		PageSize:  2,
		Sort:      "name",
		Exchanges: map[string]wireCall{},
	}

	walk := client.ListParams{Sort: client.Sort{name.Asc()}, PageSize: wire.PageSize, Include: []string{"tags"}, Facets: []string{"name"}}
	for n := 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		call := record(t, h, listGET(t, walk))
		wire.List = append(wire.List, call)
		page := pageOf(t, call)
		if !page.PageInfo.HasNextPage {
			break
		}
		walk.Cursor = string(page.PageInfo.EndCursor)
	}
	require.Len(t, wire.List, 3)

	// The page before the walk's second: its start cursor seeks backwards.
	back := walk
	back.Cursor = string(pageOf(t, wire.List[1]).PageInfo.StartCursor)
	wire.Exchanges["list_backward"] = record(t, h, listGET(t, back))

	filtered := client.ListParams{Filter: name.Ne("echo"), Sort: client.Sort{name.Desc()}, PageSize: 2, IncludeTotal: true}
	wire.Exchanges["list_total"] = record(t, h, listGET(t, filtered))
	body, err := filtered.Body()
	require.NoError(t, err)
	wire.Exchanges["list_post"] = record(t, h, wireRequest{Method: http.MethodPost, Path: "/wire_items", ContentType: "application/json", Body: body})

	// The same list under a field mask: each item has the masked members
	// only, and the page info is the unmasked list's.
	masked := filtered
	masked.Fields = []string{"name"}
	wire.Exchanges["list_masked"] = record(t, h, listGET(t, masked))
	var maskedPage struct{ Items []map[string]any }
	require.NoError(t, json.Unmarshal(wire.Exchanges["list_masked"].Response.Body, &maskedPage))
	require.NotEmpty(t, maskedPage.Items)
	for _, item := range maskedPage.Items {
		require.Equal(t, []string{"name"}, slices.Sorted(maps.Keys(item)))
	}

	// A list that matches nothing: items [], with its total and facets.
	none := client.ListParams{Filter: client.NewField[string]("id").Eq("zz"), IncludeTotal: true, Include: []string{"tags"}, Facets: []string{"name"}}
	wire.Exchanges["list_empty"] = record(t, h, listGET(t, none))
	require.Contains(t, string(wire.Exchanges["list_empty"].Response.Body), `"items":[]`)

	facets := listGET(t, client.ListParams{Filter: name.Ne("echo"), Facets: []string{"name"}})
	facets.Method = http.MethodOptions
	wire.Exchanges["facets"] = record(t, h, facets)
	wire.Exchanges["error"] = record(t, h, listGET(t, client.ListParams{Filter: client.NewField[int]("nope").Eq(1)}))
	wire.Exchanges["schema"] = record(t, h, wireRequest{Method: http.MethodGet, Path: "/wire_items/schema"})
	wire.Exchanges["schemas"] = record(t, h, wireRequest{Method: http.MethodGet, Path: "/schemas"})
	wire.Exchanges["schema_not_found"] = record(t, h, wireRequest{Method: http.MethodGet, Path: "/gadgets/schema"})

	// Patches last: they change the rows the lists read.
	wire.Exchanges["patch"] = record(t, h, wireRequest{Method: http.MethodPatch, Path: "/wire_items/b", ContentType: "application/json", Body: json.RawMessage(`{"labels":{"env":"prod"}}`)})
	wire.Exchanges["patch_many"] = record(t, h, wireRequest{
		Method: http.MethodPatch, Path: "/wire_items", Query: url.Values{"id[]": {"d", "zz"}},
		ContentType: "application/json", Body: json.RawMessage(`{"labels":{"env":"test"}}`),
	})
	// The row keyed (w1, a/b): a segment per key column, each escaped on
	// its own, so the SKU's slash stays in its segment.
	item, err := client.ItemPath("w1", "a/b")
	require.NoError(t, err)
	wire.Exchanges["patch_composite_key"] = record(t, h, wireRequest{
		Method: http.MethodPatch, Path: "/wire_stock/" + item, ContentType: "application/json", Body: json.RawMessage(`{"qty":3}`),
	})

	for name, call := range wire.Exchanges {
		wantStatus := http.StatusOK
		switch name {
		case "error":
			wantStatus = http.StatusUnprocessableEntity
		case "schema_not_found":
			wantStatus = http.StatusNotFound
		}
		require.Equal(t, wantStatus, call.Response.Status, "%s: %s", name, call.Response.Body)
	}
	require.Equal(t, []string{"error", "facets", "list_backward", "list_empty", "list_masked", "list_post", "list_total", "patch", "patch_composite_key", "patch_many", "schema", "schema_not_found", "schemas"},
		slices.Sorted(maps.Keys(wire.Exchanges)))
	var stocked parsehttp.PatchAnswer[wireStock]
	require.NoError(t, json.Unmarshal(wire.Exchanges["patch_composite_key"].Response.Body, &stocked))
	require.Equal(t, []wireStock{{WarehouseID: "w1", SKU: "a/b", Qty: 3}}, stocked.Updated, "the pair names one row")

	out, err := json.MarshalIndent(wire, "", "  ")
	require.NoError(t, err)
	return append(out, '\n')
}
