//go:build integration

package corpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// The wire fixture the SDKs are held to: the requests a client sends and
// the answers parse's handlers gave them, recorded by serving resources
// with parsehttp on each engine, whose answers all equal the one fixture.
// The Go client's tests (parse/client, a module of its own) and the
// TypeScript client's (sdk/ts/src/client.test.ts) replay each exchange: the
// request they build must equal the recorded one, and they decode the
// recorded answer, so both SDKs and the handlers agree on every byte.
//
// Regenerate after an intentional wire change, from parse (-update after the
// package, which go test passes to the test binary):
//
//	go test -tags=integration -run 'TestCorpus/SDKWire' ./parsesql -update

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

func sdkWireFixture(t *testing.T, engine Engine) {
	runOn(t, engine, runSDKWireFixture)
}

func runSDKWireFixture(t *testing.T, b sqlBackend) {
	path := repositoryFixture(t, "sdk_wire.json")
	got := buildSDKWire(t, b)
	if updating() {
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing fixture; regenerate with -update")
	require.JSONEq(t, string(want), string(got), "wire fixture is stale; regenerate with -update")
}

// repositoryFixture is the path of one of parse's testdata files, which in
// the repository lie beside the provider package whose tests run the
// corpus. A downloaded module has none beside it, and the case skips; in
// the repository, a missing fixture fails.
func repositoryFixture(t *testing.T, name string) string {
	t.Helper()
	const dir = "../testdata"
	_, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is parse's testdata, in the repository beside the provider; a module archive does not carry it", dir)
	}
	require.NoError(t, err)
	return filepath.Join(dir, name)
}

// updating reports whether the test binary runs with -update, which
// rewrites the fixtures a case records instead of comparing them. The
// corpus registers no flag: the provider test binary that runs it does, in
// its TestMain.
func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// wireTagsPerOwner is one more tag than an include returns for an owner,
// so the first owner's include is cut and named in _truncated.
const wireTagsPerOwner = filter.DefaultMaxIncludePerRelation + 1

// wireDB is the provider on b the fixture's server runs on, its database
// seeded.
func wireDB(t *testing.T, b sqlBackend) *parse.Provider {
	t.Helper()
	p, db := b.open(t)
	labels := b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})
	dbtest.Exec(t, db,
		`CREATE TABLE wire_items (id TEXT PRIMARY KEY, name TEXT NOT NULL, labels `+labels+` NOT NULL, unit TEXT)`,
		`CREATE TABLE wire_parts (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, state TEXT NOT NULL)`,
		`CREATE TABLE wire_item_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id))`,
		`CREATE TABLE wire_stock (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`,
		// Each column alone, and the SKU's text up to its slash, names
		// another row than the pair does.
		`INSERT INTO wire_stock VALUES ('w1', 'a/b', 0), ('w1', 'a', 0), ('w2', 'a/b', 0)`,
	)
	d := b.dialect
	for i, name := range []string{"echo", "alpha", "delta", "bravo", "charlie"} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO wire_items (id, name, labels) VALUES (`+d.Placeholder(1)+`, `+d.Placeholder(2)+`, '{}')`, string(rune('a'+i)), name)
		require.NoError(t, err)
	}
	for i := range wireTagsPerOwner {
		_, err := db.ExecContext(t.Context(), `INSERT INTO wire_item_tags VALUES ('b', `+d.Placeholder(1)+`)`, fmt.Sprintf("t%03d", i+1))
		require.NoError(t, err)
	}
	dbtest.Exec(t, db, `INSERT INTO wire_item_tags VALUES ('d', 't001'), ('d', 't002')`)
	return p
}

// wireServer serves wire_items as a parse server does: lists by GET and
// POST, facets by OPTIONS, merge patches of one row or of the rows id[]
// names, and the schema routes; and merge patches of one wire_stock row by
// its key of two columns.
func wireServer(t *testing.T, p *parse.Provider) http.Handler {
	t.Helper()
	items := parse.MustResource[wireItem](p, "wire_items")
	parts := parse.MustResource[wirePart](p, "wire_parts")
	items.MustRelate("parts", parse.HasMany(parts, "item_id", parse.Indexed()))
	stock := parse.MustResource[wireStock](p, "wire_stock", parse.PrimaryKey("warehouse_id", "sku"))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, items))
	require.NoError(t, parse.RegisterResource(reg, parts))
	require.NoError(t, parse.RegisterResource(reg, stock))

	// handle answers a parse error with its body; any other error fails
	// the recording.
	handle := func(serve func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			err := serve(w, r)
			if err != nil && !parsehttp.WriteError(w, r, err) {
				t.Fatalf("%s %s: %v", r.Method, r.URL, err)
			}
		}
	}

	mux := http.NewServeMux()
	routeItem(t, mux, items, handle)
	routeItem(t, mux, stock, handle)
	list := handle(func(w http.ResponseWriter, r *http.Request) error {
		req, err := parsehttp.ParseList(r, items)
		if err != nil {
			return err
		}
		page, err := items.List(r.Context(), req, parse.Scope{})
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
		facets, err := items.Facets(r.Context(), req, parse.Scope{})
		if err != nil {
			return err
		}
		return parsehttp.WriteJSON(w, r, http.StatusOK, parsehttp.FacetsAnswer{Facets: facets})
	}))
	mux.Handle("PATCH /wire_items", handle(func(w http.ResponseWriter, r *http.Request) error {
		q, err := parsehttp.Query(r)
		if err != nil {
			return err
		}
		ids := q["id[]"]
		updated, err := wireUpdate(r, items, parse.ByIDs(anys(ids)...))
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
func routeItem[T any](t *testing.T, mux *http.ServeMux, res *parse.Resource[T],
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
		updated, err := wireUpdate(r, res, parse.ByKey(key))
		if err != nil {
			return err
		}
		return parsehttp.WriteJSON(w, r, http.StatusOK, parsehttp.PatchAnswer[T]{Updated: updated})
	}))
}

// wireUpdate applies r's merge patch to target's rows of res and returns
// them as the update left them.
func wireUpdate[T any](r *http.Request, res *parse.Resource[T], target parse.Target) ([]T, error) {
	parsed, err := parsehttp.ParsePatch(r, res)
	if err != nil {
		return nil, err
	}
	updated, err := res.Update(r.Context(), parsed, target, parse.Scope{})
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
	call := wireCall{Request: req, Response: wireResponse{
		Status: rec.Code, ContentType: rec.Header().Get("Content-Type"), Body: bytes.TrimSpace(rec.Body.Bytes()),
	}}
	return call
}

// listGET is the GET of the list q's parameters ask for.
func listGET(q url.Values) wireRequest {
	request := wireRequest{Method: http.MethodGet, Path: "/wire_items", Query: q}
	return request
}

// The filters the recorded lists send, as the clients encode them: every
// condition in operator form.
const (
	wireNotEcho = `{"name":{"$ne":"echo"}}`
	wireNoRow   = `{"id":{"$eq":"zz"}}`
	wireUnknown = `{"nope":{"$eq":1}}`
)

// pageOf decodes the page of a list call.
func pageOf(t *testing.T, c wireCall) filter.Page[wireItem] {
	t.Helper()
	require.Equal(t, http.StatusOK, c.Response.Status, "%s", c.Response.Body)
	var page filter.Page[wireItem]
	require.NoError(t, json.Unmarshal(c.Response.Body, &page))
	return page
}

func buildSDKWire(t *testing.T, b sqlBackend) []byte {
	t.Helper()
	h := wireServer(t, wireDB(t, b))
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

	pageSize := strconv.Itoa(wire.PageSize)
	walk := url.Values{
		filter.ParamSort: {wire.Sort}, filter.ParamPageSize: {pageSize},
		filter.ParamInclude: {"tags"}, filter.ParamFacets: {"name"},
	}
	from := func(cursor filter.Cursor) url.Values {
		q := maps.Clone(walk)
		q.Set(filter.ParamCursor, string(cursor))
		return q
	}
	for q, n := walk, 0; ; n++ {
		require.Less(t, n, 10, "walk did not terminate")
		call := record(t, h, listGET(q))
		wire.List = append(wire.List, call)
		page := pageOf(t, call)
		if !page.PageInfo.HasNextPage {
			break
		}
		q = from(page.PageInfo.EndCursor)
	}
	require.Len(t, wire.List, 3)

	// The page before the walk's second: its start cursor seeks backwards.
	wire.Exchanges["list_backward"] = record(t, h, listGET(from(pageOf(t, wire.List[1]).PageInfo.StartCursor)))

	filtered := url.Values{
		filter.ParamFilter: {wireNotEcho}, filter.ParamSort: {"-name"},
		filter.ParamPageSize: {pageSize}, filter.ParamIncludeTotal: {"true"},
	}
	wire.Exchanges["list_total"] = record(t, h, listGET(filtered))
	body := json.RawMessage(`{"filter":` + wireNotEcho + `,"sort":"-name","page_size":` + pageSize + `,"include_total":true}`)
	wire.Exchanges["list_post"] = record(t, h, wireRequest{Method: http.MethodPost, Path: "/wire_items", ContentType: "application/json", Body: body})

	// The same list under a field mask: each item has the masked members
	// only, and the page info is the unmasked list's.
	masked := maps.Clone(filtered)
	masked.Set(filter.ParamFields, "name")
	wire.Exchanges["list_masked"] = record(t, h, listGET(masked))
	var maskedPage struct{ Items []map[string]any }
	require.NoError(t, json.Unmarshal(wire.Exchanges["list_masked"].Response.Body, &maskedPage))
	require.NotEmpty(t, maskedPage.Items)
	for _, item := range maskedPage.Items {
		require.Equal(t, []string{"name"}, slices.Sorted(maps.Keys(item)))
	}

	// A list that matches nothing: items [], with its total and facets.
	none := url.Values{
		filter.ParamFilter: {wireNoRow}, filter.ParamIncludeTotal: {"true"},
		filter.ParamInclude: {"tags"}, filter.ParamFacets: {"name"},
	}
	wire.Exchanges["list_empty"] = record(t, h, listGET(none))
	require.Contains(t, string(wire.Exchanges["list_empty"].Response.Body), `"items":[]`)

	facets := listGET(url.Values{filter.ParamFilter: {wireNotEcho}, filter.ParamFacets: {"name"}})
	facets.Method = http.MethodOptions
	wire.Exchanges["facets"] = record(t, h, facets)
	wire.Exchanges["error"] = record(t, h, listGET(url.Values{filter.ParamFilter: {wireUnknown}}))
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
	item := url.PathEscape("w1") + "/" + url.PathEscape("a/b")
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

// A facet's statement names the field by its external name, which is no
// SQL alias of the column it reads.
func facetExternalName(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		type labelled struct {
			Label string `json:"display-name" db:"label" parse:"filter,facet"`
		}
		s, err := filter.GenerateFilterSchema[labelled](core.NewSchemaGenerator())
		require.NoError(t, err)
		fq, err := filter.BuildFacetQuery(s, b.dialect, "1=1", nil, []filter.FacetRequest{{Field: "display-name"}})
		require.NoError(t, err)

		db := b.db(t)
		dbtest.Exec(t, db, `CREATE TABLE labelleds (label TEXT)`, `INSERT INTO labelleds VALUES ('a'), ('a'), ('b')`)
		rows, err := db.QueryContext(t.Context(), fq.SQL, fq.Args...)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		counts := map[string]int{}
		for rows.Next() {
			var field, key string
			var count int
			require.NoError(t, rows.Scan(&field, &key, &count))
			assert.Equal(t, "display-name", field)
			counts[key] = count
		}
		require.NoError(t, rows.Err())
		assert.Equal(t, map[string]int{"a": 2, "b": 1}, counts)
	})
}

type wireTag struct {
	ID   int64  `json:"id,string" db:"id"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type wirePlainTag struct {
	ID int64 `json:"id" db:"id"`
}

type wirePost struct {
	ID int64 `json:"id,string" db:"id"`
}

// wirePosts relates posts to quoted-key tags and to plain-key labels, both
// writable, on p.
func wirePosts(t *testing.T, p *parse.Provider) *parse.Resource[wirePost] {
	t.Helper()
	tags, err := parse.NewResource[wireTag](p, "tags")
	require.NoError(t, err)
	labels, err := parse.NewResource[wirePlainTag](p, "labels")
	require.NoError(t, err)
	posts, err := parse.NewResource[wirePost](p, "posts")
	require.NoError(t, err)
	require.NoError(t, posts.Relate("tags", parse.ManyToMany(tags, parse.Through("post_tags", "post_id", "tag_id"), parse.Writable())))
	require.NoError(t, posts.Relate("labels", parse.ManyToMany(labels, parse.Through("post_labels", "post_id", "label_id"), parse.Writable())))
	return posts
}

// A relation key that travels as a JSON string (json:",string"), as
// parse/client's Relation[Quoted[K]] sends it (testdata/client_requests.json),
// binds as the target's integer: on SQLite the junction's untyped column
// keeps the type bound.
func quotedRelationKeys(t *testing.T, engine Engine) {
	var sent struct {
		QuotedRelation struct {
			MergePatch json.RawMessage `json:"merge_patch"`
		} `json:"quoted_relation"`
	}
	data, err := os.ReadFile(repositoryFixture(t, "client_requests.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &sent))
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := b.open(t)
		pair := "post_id, tag_id"
		labelPair := "post_id, label_id"
		if b.engine == Postgres {
			pair, labelPair = "post_id BIGINT, tag_id BIGINT", "post_id BIGINT, label_id BIGINT"
		}
		dbtest.Exec(t, db,
			`CREATE TABLE posts (id BIGINT PRIMARY KEY)`, `INSERT INTO posts VALUES (1)`,
			`CREATE TABLE tags (id BIGINT PRIMARY KEY, name TEXT)`, `INSERT INTO tags VALUES (5, 'five'), (6, 'six')`,
			`CREATE TABLE labels (id BIGINT PRIMARY KEY)`,
			`CREATE TABLE post_tags (`+pair+`)`, `INSERT INTO post_tags VALUES (1, 6)`,
			`CREATE TABLE post_labels (`+labelPair+`)`)
		posts := wirePosts(t, provider)
		parsed, err := posts.ParseMergePatch(sent.QuotedRelation.MergePatch)
		require.NoError(t, err, "%s", sent.QuotedRelation.MergePatch)
		res, err := posts.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{})
		require.NoError(t, err)
		assert.Positive(t, res.RelationRows)
		pairs := dbtest.QueryRows[[]any](t, db, `SELECT tag_id FROM post_tags WHERE post_id = 1`)
		assert.Equal(t, [][]any{{int64(5)}}, normalizeInts(t, pairs))
		if b.engine == SQLite {
			assert.Equal(t, []string{"integer"}, dbtest.QueryRows[string](t, db, `SELECT typeof(tag_id) FROM post_tags WHERE post_id = 1`))
		}
	})
}
