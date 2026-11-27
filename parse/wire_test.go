//go:build unit

package parse_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/patch"
)

// The wire fixture the SDKs are held to: the requests a client sends and
// the answers parse's handlers gave them, recorded by serving a resource
// over SQLite with parsehttp and parsesql. The Go client's tests
// (parse/client, a module of its own) and the TypeScript client's
// (sdk/ts/src/client.test.ts) replay each exchange: the request they build
// must equal the recorded one, and they decode the recorded answer, so both
// SDKs and the handlers agree on every byte.
//
// Regenerate after an intentional wire change:
//
//	go test -tags=unit -run TestSDKWireFixture -update ./

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

// wireDB is the SQLite provider the fixture's server runs on, its database
// seeded.
func wireDB(t *testing.T) *parse.Provider {
	t.Helper()
	p, db := providertest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE wire_items (id TEXT PRIMARY KEY, name TEXT NOT NULL, labels TEXT NOT NULL, unit TEXT)`,
		`CREATE TABLE wire_parts (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, state TEXT NOT NULL)`,
		`CREATE TABLE wire_item_tags (item_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (item_id, tag_id))`,
		`CREATE TABLE wire_stock (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`,
		// Each column alone, and the SKU's text up to its slash, names
		// another row than the pair does.
		`INSERT INTO wire_stock VALUES ('w1', 'a/b', 0), ('w1', 'a', 0), ('w2', 'a/b', 0)`,
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	for i, name := range []string{"echo", "alpha", "delta", "bravo", "charlie"} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO wire_items (id, name, labels) VALUES (?1, ?2, '{}')`, string(rune('a'+i)), name)
		require.NoError(t, err)
	}
	for i := range wireTagsPerOwner {
		_, err := db.ExecContext(t.Context(), `INSERT INTO wire_item_tags VALUES ('b', ?1)`, fmt.Sprintf("t%03d", i+1))
		require.NoError(t, err)
	}
	_, err := db.ExecContext(t.Context(), `INSERT INTO wire_item_tags VALUES ('d', 't001'), ('d', 't002')`)
	require.NoError(t, err)
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

func buildSDKWire(t *testing.T) []byte {
	t.Helper()
	h := wireServer(t, wireDB(t))
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

// quotedItem's quoted fields travel as JSON strings, as encoding/json writes
// them; Count is a plain number. Key is the key, so the quoted int64 ID can
// be patched.
type quotedItem struct {
	Key   int64   `json:"key"          db:"key"   parse:"filter,sort"`
	ID    int64   `json:"id,string"    db:"id"    parse:"filter,sort,patch" patch_ops:"replace,test,copy"`
	Big   uint64  `json:"big,string"   db:"big"   parse:"filter,patch"`
	Ratio float64 `json:"ratio,string" db:"ratio" parse:"filter,patch"`
	On    bool    `json:"on,string"    db:"on"    parse:"filter,patch"`
	Opt   *int64  `json:"opt,string"   db:"opt"   parse:"filter,patch" filter_ops:"eq,in,nin,null" patch_ops:"add,replace,remove,copy"`
	Count int64   `json:"count"        db:"count" parse:"filter,patch" patch_ops:"replace,copy" max:"100"`
}

func quotedItems(t *testing.T) *parse.Resource[quotedItem] {
	t.Helper()
	r, err := parse.NewResource[quotedItem](onSQLite, "quoted_items", parse.PrimaryKey("key"))
	require.NoError(t, err)
	return r
}

func TestWireStringFilters(t *testing.T) {
	r := quotedItems(t)
	tests := []struct {
		name, filter string
		want         any
	}{
		{"int64 max", `{"id":"9223372036854775807"}`, int64(math.MaxInt64)},
		{"int64 min", `{"id":{"$gt":"-9223372036854775808"}}`, int64(math.MinInt64)},
		{"past 2^53, exactly", `{"id":{"$lte":"9007199254740993"}}`, int64(9007199254740993)},
		{"a set", `{"id":{"$in":["1","9007199254740993"]}}`, []any{int64(1), int64(9007199254740993)}},
		{"uint64 max", `{"big":"18446744073709551615"}`, uint64(math.MaxUint64)},
		{"a float", `{"ratio":{"$ne":"0.5"}}`, 0.5},
		{"a bool", `{"on":"true"}`, true},
		{"a bool's bare boolean, the same Go value", `{"on":false}`, false},
		{"an integral spelling", `{"id":"1.0e3"}`, int64(1000)},
		{"a pointer", `{"opt":{"$nin":["3"]}}`, []any{int64(3)}},
		{"an unquoted field keeps numbers", `{"count":7}`, int64(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := r.ParseFilter(tt.filter)
			require.NoError(t, err)
			assert.Equal(t, tt.want, parsed.AST().Root.Value)
		})
	}

	for name, f := range map[string]string{
		"a bare number":              `{"id":9223372036854775807}`,
		"a number past 2^53":         `{"id":9007199254740993}`,
		"a bare number in a set":     `{"id":{"$in":["1",2]}}`,
		"text that is no number":     `{"id":"seven"}`,
		"text with spaces":           `{"id":" 7"}`,
		"past int64":                 `{"id":"9223372036854775808"}`,
		"a negative unsigned":        `{"big":"-1"}`,
		"a fraction for an integer":  `{"id":"1.5"}`,
		"text for an unquoted field": `{"count":"7"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.ParseFilter(f)
			assert.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "%s: %v", f, err)
		})
	}
	_, err := r.ParseFilter(`{"id":9}`)
	require.ErrorContains(t, err, `json:",string"`)

	// Null tests and sorts take no field value.
	_, err = r.ParseFilter(`{"opt":{"$null":true}}`)
	require.NoError(t, err)
	_, err = r.ParseSort("-id")
	assert.NoError(t, err)
}

// A query parameter's text is the wire form already.
func TestWireStringQueryParams(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseRequestFilter(url.Values{"id": {"9223372036854775807"}})
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), parsed.AST().Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"id_in": {"1,9007199254740993"}})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(1), int64(9007199254740993)}, parsed.AST().Root.Value)

	parsed, err = r.ParseRequestFilter(url.Values{"on": {"false"}})
	require.NoError(t, err)
	assert.Equal(t, false, parsed.AST().Root.Value)

	// A bool reads its parameter as any bool parameter does.
	parsed, err = r.ParseRequestFilter(url.Values{"on": {"1"}})
	require.NoError(t, err)
	assert.Equal(t, true, parsed.AST().Root.Value)
	_, err = r.ParseRequestFilter(url.Values{"on": {"yes"}})
	assert.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "%v", err)
}

func TestWireStringPatches(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseMergePatch([]byte(`{"id":"9007199254740993","big":"18446744073709551615","on":"false","ratio":"2.5","opt":null}`))
	require.NoError(t, err)
	values := map[string]any{}
	for _, op := range parsed.Operations {
		values[op.Path] = op.Value
	}
	assert.Equal(t, map[string]any{
		"/id": int64(9007199254740993), "/big": uint64(math.MaxUint64), "/on": false, "/ratio": 2.5, "/opt": nil,
	}, values, "SQL binds the numbers")

	for name, body := range map[string]string{
		"a merge patch's bare number": `{"id":9007199254740993}`,
		"a merge patch past int64":    `{"id":"9223372036854775808"}`,
		"text for an unquoted field":  `{"count":"7"}`,
		"over an unquoted bound":      `{"count":101}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.ParseMergePatch([]byte(body))
			assert.ErrorIs(t, err, patch.ECodePatchValueInvalid, "%s: %v", body, err)
		})
	}

	parsed, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":"-9223372036854775808"},{"op":"add","path":"/opt","value":"4"}]`))
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), parsed.Operations[0].Value)
	assert.Equal(t, int64(4), parsed.Operations[1].Value)
	_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/id","value":5}]`))
	assert.ErrorIs(t, err, patch.ECodePatchValueInvalid, "%v", err)
}

// Applied to a struct, quoted values land in their fields, whether the
// patch runs on the struct or through its JSON document.
func TestWireStringApply(t *testing.T) {
	r := quotedItems(t)
	target := quotedItem{ID: 5, Big: 1, Ratio: 0.5, On: true, Count: 2}

	got, err := r.ApplyPatch([]byte(`[
		{"op":"test","path":"/id","value":"5"},
		{"op":"replace","path":"/id","value":"9007199254740993"},
		{"op":"replace","path":"/big","value":"18446744073709551615"},
		{"op":"replace","path":"/on","value":"false"},
		{"op":"copy","from":"/id","path":"/opt"}
	]`), target)
	require.NoError(t, err)
	assert.Equal(t, quotedItem{ID: 9007199254740993, Big: math.MaxUint64, Ratio: 0.5, On: false, Opt: new(int64(9007199254740993)), Count: 2}, got)

	_, err = r.ApplyPatch([]byte(`[{"op":"test","path":"/id","value":"4"}]`), target)
	require.ErrorIs(t, err, patch.ECodePatchValueInvalid, "a failed test: %v", err)
	_, err = r.ParsePatch([]byte(`[{"op":"copy","from":"/id","path":"/count"}]`))
	require.ErrorIs(t, err, patch.ECodePatchValueInvalid, "a quoted value into a number, refused at parse: %v", err)

	// A test compares canonical text: "5.0" is the 5 the field holds.
	_, err = r.ApplyPatch([]byte(`[{"op":"test","path":"/id","value":"5.0"}]`), target)
	require.NoError(t, err)

	merge, err := r.ParseMergePatch([]byte(`{"id":"-9223372036854775808","opt":"6"}`))
	require.NoError(t, err)
	got, err = r.ApplyValidatedPatch(merge, target)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), got.ID)
	assert.Equal(t, new(int64(6)), got.Opt)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"id":"-9223372036854775808"`, "responses follow encoding/json")
}

func TestWireStringSchemaDocument(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[quotedItem](reg))
	doc := reg.SchemaFor("quoted_items")
	require.NotNil(t, doc)
	for name, want := range map[string]string{"id": "string", "big": "string", "ratio": "string", "on": "string", "opt": "string", "count": ""} {
		assert.Equal(t, want, string(doc.Filter.Fields[name].Wire), "filter %s", name)
		assert.Equal(t, want, string(doc.Patch.Paths["/"+name].Wire), "patch %s", name)
	}
	data, err := json.Marshal(doc.Filter.Fields["id"])
	require.NoError(t, err)
	assert.Contains(t, string(data), `"wire":"string"`)
	data, err = json.Marshal(doc.Filter.Fields["count"])
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"wire"`)
}

// Code passes Go values, not the wire form: maps, operations and server
// patches take the number.
func TestWireStringGoValues(t *testing.T) {
	r := quotedItems(t)
	parsed, err := r.ParseFilterMap(map[string]any{"id": int64(9007199254740993), "on": true})
	require.NoError(t, err)
	assert.NotNil(t, parsed.AST().Root)

	merge, err := r.MergePatchMap(map[string]any{"id": int64(9007199254740993), "big": uint64(math.MaxUint64)})
	require.NoError(t, err)
	assert.Len(t, merge.Operations, 2)
	_, err = r.ServerMergePatchMap(map[string]any{"id": int64(7)})
	require.NoError(t, err)
	ops, err := r.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/id", Value: int64(math.MinInt64)}})
	require.NoError(t, err)
	assert.Equal(t, int64(math.MinInt64), ops.Operations[0].Value)

	// A JSON number decoded into the map, as a request body holds one, is
	// still the bare form.
	_, err = r.ParseFilterMap(map[string]any{"id": json.Number("5")})
	assert.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "%v", err)
}

// A test op's validators see the number, so a quoted field's bounds apply.
func TestWireStringTestValidates(t *testing.T) {
	type bounded struct {
		ID  int64 `json:"id"         db:"id"`
		Qty int64 `json:"qty,string" db:"qty" parse:"patch" patch_ops:"replace,test" min:"1" max:"9007199254740993"`
	}
	r, err := parse.NewResource[bounded](onSQLite, "bounded")
	require.NoError(t, err)
	_, err = r.ParsePatch([]byte(`[{"op":"test","path":"/qty","value":"9007199254740993"}]`))
	require.NoError(t, err)
	_, err = r.ParsePatch([]byte(`[{"op":"test","path":"/qty","value":"9007199254740994"}]`))
	assert.ErrorIs(t, err, patch.ECodePatchValueInvalid, "%v", err)
}

type wireAuthor struct {
	ID   int64  `json:"id,string" db:"id"`
	Name string `json:"name"      db:"name" parse:"filter"`
}

type wireBook struct {
	ID       int64  `json:"id,string"        db:"id"`
	AuthorID *int64 `json:"author_id,string" db:"author_id" parse:"filter"`
}

// A json:",string" key reaches the request as text even untagged for
// filtering, and a relation's shorthand compares its target's key that way.
func TestWireStringKeysAndRelations(t *testing.T) {
	authors, err := parse.NewResource[wireAuthor](onSQLite, "authors")
	require.NoError(t, err)
	books, err := parse.NewResource[wireBook](onSQLite, "books")
	require.NoError(t, err)
	require.NoError(t, books.Relate("author", parse.BelongsTo(authors, "author_id")))

	_, err = books.ParseFilter(`{"author":"9007199254740993"}`)
	require.NoError(t, err)
	_, err = books.ParseFilter(`{"author":{"$in":["1","2"]}}`)
	require.NoError(t, err)
	_, err = books.ParseFilter(`{"author":9007199254740993}`)
	require.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "a bare key: %v", err)
	_, err = books.ParseFilter(`{"author_id":{"$in":["1","2"]}}`)
	require.NoError(t, err)
	_, err = authors.ParseFilter(`{"id":"9007199254740993"}`)
	require.NoError(t, err, "the key, untagged for filtering")
	_, err = authors.ParseFilter(`{"id":9007199254740993}`)
	assert.ErrorIs(t, err, filter.ECodeValueTypeMismatch, "the bare key: %v", err)
}

type wireSpec struct {
	Qty int64 `json:"qty,string"`
	Max int64 `json:"max"`
}

// A path declared onto a quoted struct member, in a tag or at runtime, is
// refused: the document holds it as text its path reads as NULL.
func TestWireStringDeclaredMemberRefused(t *testing.T) {
	type tagged struct {
		ID   string   `json:"id"   db:"id"`
		Spec wireSpec `json:"spec" db:"spec" parse:"filter" paths:"qty:int"`
	}
	_, err := parse.NewResource[tagged](onSQLite, "tagged")
	require.ErrorContains(t, err, `json:",string"`)

	type plain struct {
		ID   string   `json:"id"   db:"id"`
		Spec wireSpec `json:"spec" db:"spec" parse:"filter"`
	}
	r, err := parse.NewResource[plain](onSQLite, "plain")
	require.NoError(t, err)
	_, err = r.WithPaths(filter.PathDecl{Path: "spec.qty", Type: core.FieldTypeInt})
	require.ErrorContains(t, err, `json:",string"`)
	_, err = r.WithPaths(filter.PathDecl{Path: "spec.max", Type: core.FieldTypeInt})
	assert.NoError(t, err)
}

// Filter values and names as the wire carries them: JSON numbers within the
// domain every backend compares alike, field names that are no SQL
// identifier, and not_null in both grammars.

type docRow struct {
	ID   string         `json:"id" db:"id" parse:"sort"`
	Doc  map[string]any `json:"doc" db:"doc" parse:"filter"`
	Nums []uint64       `json:"nums" db:"nums" parse:"filter"`
}

// A JSON number some backend would round is rejected, not answered
// differently per backend.
func TestFilter_JSONNumbersStayInThePortableDomain(t *testing.T) {
	p, err := parse.NewResource[docRow](onSQLite, "doc_rows")
	require.NoError(t, err)

	for _, bad := range []string{
		`{"doc":{"n":18446744073709551615}}`,
		`{"doc":{"$contains":{"deep":[1, 9223372036854775808]}}}`,
		`{"doc":{"n":0.10000000000000001}}`,
		`{"nums":{"$contains":[18446744073709551615]}}`,
	} {
		_, err := p.ParseFilter(bad)
		requireCode(t, err, filter.ECodeValueTypeMismatch.String())
	}
	for _, ok := range []string{
		`{"doc":{"n":9223372036854775807}}`,
		`{"doc":{"n":-9223372036854775808}}`,
		`{"doc":{"n":0.1}}`,
		`{"doc":{"n":1.50}}`,
		`{"doc":{"n":2e3}}`,
		`{"nums":{"$contains":[9223372036854775807]}}`,
	} {
		_, err := p.ParseFilter(ok)
		assert.NoError(t, err, ok)
	}
}

func TestPortableDecimal(t *testing.T) {
	for lit, want := range map[string]bool{
		"0.1": true, "1.50": true, "2e3": true, "-0.0": true, "1.7976931348623157e308": true,
		"0.10000000000000001": false, "1e400": false, "1e-400": false,
		"9007199254740993.0": false, "3.141592653589793238": false,
	} {
		assert.Equal(t, want, core.PortableDecimal(lit), lit)
	}
}

// A facet over a field whose external name is not an SQL identifier
// executes.
func TestFacet_ExternalNameIsNotAnAlias(t *testing.T) {
	type labelled struct {
		Label string `json:"display-name" db:"label" parse:"filter,facet"`
	}
	s, err := filter.GenerateFilterSchema[labelled](core.NewSchemaGenerator())
	require.NoError(t, err)
	fq, err := filter.BuildFacetQuery(s, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "display-name"}})
	require.NoError(t, err)

	db := dbtest.SQLite(t)
	_, err = db.ExecContext(t.Context(), `CREATE TABLE labelleds (label TEXT); INSERT INTO labelleds VALUES ('a'), ('a'), ('b')`)
	require.NoError(t, err)
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
}

type nullable struct {
	ID    string  `json:"id,omitempty" db:"id"`
	Name  *string `json:"name"  parse:"filter" filter_ops:"not_null"`
	Other *string `json:"other" parse:"filter" filter_ops:"eq"`
}

// not_null has a wire spelling, and every NULL test is one capability.
func TestFilter_NotNullIsAWireOperator(t *testing.T) {
	p, err := parse.NewResource[nullable](onSQLite, "nullables")
	require.NoError(t, err)

	for filterJSON, want := range map[string]filter.FilterOperator{
		`{"name":{"$not_null":true}}`:  filter.OpNotNull,
		`{"name":{"$not_null":false}}`: filter.OpNull,
		`{"name":{"$null":false}}`:     filter.OpNotNull,
		`{"name":{"$exists":false}}`:   filter.OpNull,
	} {
		f, parseErr := p.ParseFilter(filterJSON)
		require.NoError(t, parseErr, filterJSON)
		assert.Equal(t, want, f.AST().Root.Op, filterJSON)
	}

	_, err = p.ParseFilter(`{"other":{"$not_null":true}}`)
	requireCode(t, err, filter.ECodeInvalidOperator.String())
}

func TestQueryParams_NotNullSuffix(t *testing.T) {
	type row struct {
		ID      string  `json:"id,omitempty" db:"id"`
		Name    *string `json:"name"     parse:"filter" filter_ops:"not_null"`
		NameNot *string `json:"name_not" parse:"filter" filter_ops:"null"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	require.NoError(t, err)
	qp := queryParams(t, p.FilterSchema())

	ast, err := qp.Parse(url.Values{"name_not_null": {"true"}})
	require.NoError(t, err)
	assert.Equal(t, "name", ast.Root.Field.ExternalName, "the split naming a real field wins")
	assert.Equal(t, filter.OpNotNull, ast.Root.Op)
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

// A membership's keys travel as the target's key does: a json:",string"
// key's as JSON strings, as responses write them and the relation's filter
// shorthands take them, a plain key's as numbers.
func TestRelationKeysTakeTheTargetKeysWire(t *testing.T) {
	posts := wirePosts(t, onPostgres)

	parsed, err := posts.ParseMergePatch([]byte(`{"tags":["5","9007199254740993"]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5), int64(9007199254740993)}, parsed.Operations[0].Value)
	_, err = posts.ParseMergePatch([]byte(`{"tags":[5]}`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	for _, body := range []string{
		`[{"op":"add","path":"/tags","value":["5"]}]`,
		`[{"op":"remove","path":"/tags","value":["5"]}]`,
		`[{"op":"replace","path":"/tags","value":["5","6"]}]`,
	} {
		_, err := posts.ParsePatch([]byte(body))
		require.NoError(t, err, body)
	}
	_, err = posts.ParsePatch([]byte(`[{"op":"add","path":"/tags","value":[5]}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	// A Go value code passes is the key, as for a scalar path.
	parsed, err = posts.MergePatchMap(map[string]any{"tags": []int64{7}})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(7)}, parsed.Operations[0].Value)

	// A target key without the option is unchanged.
	parsed, err = posts.ParseMergePatch([]byte(`{"labels":[5]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5)}, parsed.Operations[0].Value)
	_, err = posts.ParseMergePatch([]byte(`{"labels":["5"]}`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	// The schema document says each key is quoted.
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, posts))
	paths := reg.SchemaFor("posts").Patch.Paths
	assert.Equal(t, "string", string(paths["/tags"].Wire))
	assert.Empty(t, paths["/labels"].Wire)
}

// parse/client's Relation[Quoted[K]] sends the keys the server takes
// (testdata/client_requests.json), and the relation statements bind them as
// the target's integers: the junction's untyped column keeps what is bound.
func TestClientQuotedRelationOperationsParse(t *testing.T) {
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db,
		`CREATE TABLE posts (id INTEGER PRIMARY KEY)`, `INSERT INTO posts VALUES (1)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO tags VALUES (5, 'five'), (6, 'six')`,
		`CREATE TABLE labels (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE post_tags (post_id, tag_id)`, `INSERT INTO post_tags VALUES (1, 6)`,
		`CREATE TABLE post_labels (post_id, label_id)`)
	posts := wirePosts(t, provider)
	sent := readClientRequests(t).QuotedRelation
	parsed, err := posts.ParsePatch(sent.JSONPatch)
	require.NoError(t, err, "%s", sent.JSONPatch)
	assert.Equal(t, []any{int64(5), int64(9007199254740993)}, parsed.Operations[0].Value)

	parsed, err = posts.ParseMergePatch(sent.MergePatch)
	require.NoError(t, err, "%s", sent.MergePatch)
	res, err := posts.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	assert.Positive(t, res.RelationRows)
	assert.Equal(t, [][]any{{int64(5), "integer"}}, dbtest.QueryRows[[]any](t, db, `SELECT tag_id, typeof(tag_id) FROM post_tags WHERE post_id = 1`))
}
