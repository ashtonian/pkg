//go:build unit

package client

// The client against parse's recorded wire (parse's testdata/sdk_wire.json):
// the requests parse's handlers answered and their answers, recorded from
// the handlers themselves. Every answer decodes into its type here and
// encodes back to the recorded JSON, so a member the server adds, drops or
// spells differently fails these tests; every request the client builds is
// the recorded one.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type wireRequest struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Query       url.Values      `json:"query"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
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
	// Params are the server's list parameter names, by the name of their
	// constant in parse's filter package.
	Params    map[string]string   `json:"params"`
	PageSize  int                 `json:"page_size"`
	Sort      string              `json:"sort"`
	List      []wireCall          `json:"list"`
	Exchanges map[string]wireCall `json:"exchanges"`
}

func readWire(t *testing.T) sdkWire {
	t.Helper()
	var wire sdkWire
	decodeCorpus(t, sdkWirePath, &wire)
	if len(wire.List) == 0 || len(wire.Exchanges) == 0 {
		t.Fatalf("%s records no exchanges", sdkWirePath)
	}
	return wire
}

// The kinds of answer the server gives, each read by one type.
const (
	kindPage       = "page"
	kindFacets     = "facets"
	kindPatch      = "patch"
	kindError      = "error"
	kindSchema     = "schema"
	kindSchemaList = "schema list"
)

// answerKind is the kind of c's answer, from the route the request took
// (parse/sdk/ts/README.md's server contract).
func answerKind(t *testing.T, name string, c wireCall) string {
	t.Helper()
	switch {
	case c.Response.Status >= http.StatusBadRequest:
		return kindError
	case c.Request.Method == http.MethodOptions:
		return kindFacets
	case c.Request.Method == http.MethodPatch:
		return kindPatch
	case c.Request.Path == "/schemas":
		return kindSchemaList
	case strings.HasSuffix(c.Request.Path, "/schema"):
		return kindSchema
	case c.Request.Method == http.MethodGet, c.Request.Method == http.MethodPost:
		return kindPage
	}
	t.Fatalf("exchange %s: %s %s is no route this client reads the answer of", name, c.Request.Method, c.Request.Path)
	return ""
}

// calls are every recorded exchange, by name; the list walk's pages are
// list[0], list[1], ….
func (w sdkWire) calls() map[string]wireCall {
	all := maps.Clone(w.Exchanges)
	for i, c := range w.List {
		all[fmt.Sprintf("list[%d]", i)] = c
	}
	return all
}

// roundTrip decodes body into a new T, refusing members T does not hold,
// and fails unless T encodes back to the same JSON.
func roundTrip[T any](t *testing.T, name string, body json.RawMessage) T {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var v T
	err := dec.Decode(&v)
	if err != nil {
		t.Fatalf("%s: decoding %T: %v", name, v, err)
	}
	checkJSON(t, string(body), encode(t, v), name, "encoded back")
	return v
}

// Each decode type reads every recorded answer of its kind and encodes it
// back to the recorded JSON.
func TestWire_AnswersRoundTrip(t *testing.T) {
	wire := readWire(t)
	read := map[string]int{}
	for name, c := range wire.calls() {
		kind := answerKind(t, name, c)
		read[kind]++
		t.Run(name, func(t *testing.T) {
			body := c.Response.Body
			check(t, "application/json", c.Response.ContentType)
			switch kind {
			case kindPage:
				roundTrip[Page[json.RawMessage]](t, name, body)
			case kindFacets:
				roundTrip[FacetsAnswer](t, name, body)
			case kindPatch:
				roundTrip[PatchAnswer[json.RawMessage]](t, name, body)
			case kindSchema:
				roundTrip[Schema](t, name, body)
			case kindSchemaList:
				roundTrip[SchemaList](t, name, body)
			case kindError:
				refused := DecodeError(c.Response.Status, body)
				check(t, c.Response.Status, refused.Status)
				if refused.Code == "" {
					t.Fatalf("%s: %s holds no parse error body", name, body)
				}
				checkJSON(t, string(body), encode(t, refused), "encoded back")
			}
		})
	}
	for _, kind := range []string{kindPage, kindFacets, kindPatch, kindError, kindSchema, kindSchemaList} {
		if read[kind] == 0 {
			t.Errorf("%s records no %s answer", sdkWirePath, kind)
		}
	}
}

// The client's list parameter names are the server's.
func TestWire_ParamsAreTheServersNames(t *testing.T) {
	checkDeep(t, map[string]string{
		"ParamFilter": paramFilter, "ParamSort": paramSort, "ParamCursor": paramCursor,
		"ParamPageSize": paramPageSize, "ParamIncludeTotal": paramIncludeTotal,
		"ParamIncludeDeleted": paramIncludeDeleted, "ParamSearch": paramSearch,
		"ParamFacets": paramFacets, "ParamFields": paramFields, "ParamInclude": paramInclude,
	}, readWire(t).Params)
}

// wireItem is a row of the recorded resource, wire_items.
type wireItem struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Unit   *string           `json:"unit/si~v"`
}

// A keyset walk: each page's end cursor, sent back, is the next recorded
// request, and the walk ends where the recording does.
func TestWire_WalkFollowsTheDecodedCursors(t *testing.T) {
	wire := readWire(t)
	name := NewSortableText(wire.Sort)
	walk := ListParams{Sort: Sort{name.Asc()}, PageSize: wire.PageSize, Include: []string{"tags"}, Facets: []string{"name"}}
	var pages []Page[wireItem]
	for i, c := range wire.List {
		q, err := walk.Values()
		must(t, err)
		checkDeep(t, c.Request.Query, q, "request", i)
		var page Page[wireItem]
		must(t, json.Unmarshal(c.Response.Body, &page))
		pages = append(pages, page)
		if !page.PageInfo.HasNextPage {
			check(t, len(wire.List)-1, i, "the last page has no next page")
			break
		}
		if page.PageInfo.EndCursor == "" {
			t.Fatalf("page %d has a next page and no end cursor", i)
		}
		walk.Cursor = page.PageInfo.EndCursor
	}

	// The second page's start cursor fetches the page before it.
	back := walk
	back.Cursor = pages[1].PageInfo.StartCursor
	q, err := back.Values()
	must(t, err)
	checkDeep(t, wire.Exchanges["list_backward"].Request.Query, q)

	first := pages[0]
	check(t, "alpha", first.Items[0].Name)
	check(t, 2, len(first.Relations["tags"]["d"]))
	checkDeep(t, []string{"b"}, first.Truncated["tags"], "the first owner's tags are cut at the server's cap")
	check(t, 5, len(first.Facets["name"]))
}

// Every other request the client builds is the recorded one. The routes
// are the server application's: the client builds the parameters, an
// item's path below its collection and the patch body.
func TestWire_RequestsAreTheRecordedOnes(t *testing.T) {
	wire := readWire(t)
	call := func(name string) wireCall {
		c, ok := wire.Exchanges[name]
		if !ok {
			t.Fatalf("%s records no exchange %s", sdkWirePath, name)
		}
		return c
	}
	name := NewSortableText("name")
	filtered := ListParams{Filter: name.Ne("echo"), Sort: Sort{name.Desc()}, PageSize: wire.PageSize, IncludeTotal: true}
	masked := filtered
	masked.Fields = []string{"name"}
	queries := map[string]ListParams{
		"list_total":  filtered,
		"list_masked": masked,
		"list_empty":  {Filter: NewField[string]("id").Eq("zz"), IncludeTotal: true, Include: []string{"tags"}, Facets: []string{"name"}},
		"facets":      {Filter: name.Ne("echo"), Facets: []string{"name"}},
		"error":       {Filter: NewField[int]("nope").Eq(1)},
	}
	for exchange, params := range queries {
		q, err := params.Values()
		must(t, err)
		checkDeep(t, call(exchange).Request.Query, q, exchange)
	}

	body, err := filtered.Body()
	must(t, err)
	post := call("list_post").Request
	check(t, http.MethodPost, post.Method)
	checkJSON(t, string(post.Body), string(body), "list_post")

	type itemPatch struct {
		Labels Nullable[map[string]string] `json:"labels,omitzero"`
	}
	type stockPatch struct {
		Qty Nullable[int] `json:"qty,omitzero"`
	}
	patches := map[string]struct {
		key  []any
		body any
	}{
		"patch":               {[]any{"b"}, itemPatch{Labels: Set(map[string]string{"env": "prod"})}},
		"patch_composite_key": {[]any{"w1", "a/b"}, stockPatch{Qty: Set(3)}},
		// The batch names its rows in id[], the application's parameter.
		"patch_many": {nil, itemPatch{Labels: Set(map[string]string{"env": "test"})}},
	}
	for exchange, p := range patches {
		req := call(exchange).Request
		checkJSON(t, string(req.Body), encode(t, p.body), exchange)
		if p.key == nil {
			continue
		}
		item, err := ItemPath(p.key...)
		must(t, err)
		collection, _, _ := strings.Cut(strings.TrimPrefix(req.Path, "/"), "/")
		check(t, req.Path, "/"+collection+"/"+item, exchange)
	}
}

// The answers decode into the types an SDK declares for them.
func TestWire_AnswersDecodeIntoTheirTypes(t *testing.T) {
	wire := readWire(t)

	var masked Page[struct {
		Name string `json:"name"`
	}]
	must(t, json.Unmarshal(wire.Exchanges["list_masked"].Response.Body, &masked))
	check(t, "delta", masked.Items[0].Name)
	if masked.PageInfo.TotalCount == nil || *masked.PageInfo.TotalCount != 4 {
		t.Errorf("list_masked's total is %v, want 4", masked.PageInfo.TotalCount)
	}

	var empty Page[wireItem]
	must(t, json.Unmarshal(wire.Exchanges["list_empty"].Response.Body, &empty))
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Errorf("an empty page's items are %#v, want []", empty.Items)
	}
	checkJSON(t, string(wire.Exchanges["list_empty"].Response.Body), encode(t, Page[wireItem]{PageInfo: empty.PageInfo}), "a nil Items encodes as []")

	var facets FacetsAnswer
	must(t, json.Unmarshal(wire.Exchanges["facets"].Response.Body, &facets))
	checkDeep(t, FacetBucket{Key: "alpha", Count: 1}, facets.Facets["name"][0])
	checkJSON(t, `{"facets":{}}`, encode(t, FacetsAnswer{}), "a nil Facets encodes as {}")

	var many PatchAnswer[wireItem]
	must(t, json.Unmarshal(wire.Exchanges["patch_many"].Response.Body, &many))
	checkDeep(t, map[string]string{"env": "test"}, many.Updated[0].Labels)
	if len(many.Failed) != 1 || many.Failed[0].ID != "zz" || many.Failed[0].Code == "" {
		t.Errorf("patch_many's failures are %#v, want the key zz with its code", many.Failed)
	}
	checkJSON(t, `{"updated":[]}`, encode(t, PatchAnswer[wireItem]{}), "a nil Updated encodes as []")

	var schemas SchemaList
	must(t, json.Unmarshal(wire.Exchanges["schemas"].Response.Body, &schemas))
	var schema Schema
	must(t, json.Unmarshal(wire.Exchanges["schema"].Response.Body, &schema))
	resources := make([]string, 0, len(schemas.Schemas))
	for _, s := range schemas.Schemas {
		resources = append(resources, s.Resource)
	}
	if !slices.Contains(resources, schema.Resource) {
		t.Errorf("the schema list holds %v, not %s", resources, schema.Resource)
	}
	checkDeep(t, []string{"id"}, schema.Key)
	if !reflect.DeepEqual(schema.Filter.Fields["name"].EnumValues, []string{"alpha", "bravo", "charlie", "delta", "echo"}) {
		t.Errorf("wire_items' name enumerates %v", schema.Filter.Fields["name"].EnumValues)
	}
}

// A refused request is an *Error for errors.As, and its code an errors.Is
// target, through the wrapping an SDK adds.
func TestWire_ErrorsMatchByCode(t *testing.T) {
	wire := readWire(t)
	for exchange, code := range map[string]Code{"error": CodeUnknownField, "schema_not_found": CodeSchemaNotFound} {
		c := wire.Exchanges[exchange]
		err := fmt.Errorf("listing wire_items: %w", DecodeError(c.Response.Status, c.Response.Body))
		refused, ok := errors.AsType[*Error](err)
		if !ok {
			t.Fatalf("%s: %v is no *Error", exchange, err)
		}
		check(t, code, refused.Code, exchange)
		check(t, c.Response.Status, refused.Status, exchange)
		checkErrorIs(t, err, code, exchange)
		if errors.Is(err, CodeInvalidJSON) {
			t.Errorf("%s: %v matches another code", exchange, err)
		}
	}
	refused := DecodeError(http.StatusUnprocessableEntity, wire.Exchanges["error"].Response.Body)
	checkDeep(t, map[string]any{"field": "nope"}, refused.Details)
}
