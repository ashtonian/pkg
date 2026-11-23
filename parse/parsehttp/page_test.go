//go:build unit

package parsehttp_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// listing's members exercise what an encoder decides: a renamed member,
// omitempty, a quoted number, a member type's MarshalJSON and text that
// encoding/json escapes.
type listing struct {
	ID      int       `json:"id"`
	Title   string    `json:"display_name"`
	Note    string    `json:"note,omitempty"`
	Count   int64     `json:"count,string"`
	Created time.Time `json:"created"`
	Body    string    `json:"body"`
}

func listingPage() *filter.Page[listing] {
	total := int64(9)
	created := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	page := &filter.Page[listing]{
		Items: []listing{
			{ID: 1, Title: "a", Count: 3, Created: created, Body: "<b>&"},
			{ID: 2, Title: "b", Note: "kept", Count: 4, Created: created, Body: "x"},
		},
		PageInfo:  filter.PageInfo{HasNextPage: true, HasPrevPage: true, StartCursor: "s", EndCursor: "e", TotalCount: &total},
		Relations: map[string]map[string][]string{"tags": {"1": {"red"}}},
		Truncated: map[string][]string{"tags": {"1"}},
		Facets:    map[string][]filter.FacetBucket{"note": {{Key: "kept", Count: 1}}},
	}
	return page
}

// Each item keeps the masked members as its encoder wrote them, in its
// order: the wire name, a quoted number, a MarshalJSON's text, escaped
// text, and no member omitempty left out.
func TestMaskPage_NarrowsEachItemAsItEncodes(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
		want   []string
	}{
		{"renamed and quoted members", []string{"count", "display_name"}, []string{`{"display_name":"a","count":"3"}`, `{"display_name":"b","count":"4"}`}},
		{"a marshaler's text and escaped text", []string{"created", "body"}, []string{`{"created":"2026-11-23T08:00:00Z","body":"\u003cb\u003e\u0026"}`, `{"created":"2026-11-23T08:00:00Z","body":"x"}`}},
		{"a member omitempty left out", []string{"note"}, []string{`{}`, `{"note":"kept"}`}},
		{"a name no item has", []string{"Title"}, []string{`{}`, `{}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked, err := parsehttp.MaskPage(listingPage(), tt.fields)
			require.NoError(t, err)
			got := make([]string, len(masked.Items))
			for i, item := range masked.Items {
				got[i] = string(item)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// rawItem encodes as its own JSON text.
type rawItem string

func (r rawItem) MarshalJSON() ([]byte, error) { return []byte(r), nil }

// A marshaler's object is narrowed by its members' unquoted names, keeping
// each as written: an escaped name, a name written twice, and values whose
// strings and nesting hold names and braces of their own.
func TestMaskPage_NarrowsAMarshalersObject(t *testing.T) {
	page := &filter.Page[rawItem]{Items: []rawItem{
		` { "a" : "}{" , "b" : [ {"a":1} ], "a": 2 } `,
		`{"c":{"a":"x"},"a":"\"a\":"}`,
		`{"b":null}`,
	}}
	masked, err := parsehttp.MaskPage(page, []string{"a"})
	require.NoError(t, err)
	got := make([]string, len(masked.Items))
	for i, item := range masked.Items {
		got[i] = string(item)
	}
	assert.Equal(t, []string{`{"a":"}{","a":2}`, `{"a":"\"a\":"}`, `{}`}, got)
}

// Every member of the page but its items is the page's, whatever the mask:
// a member Page gains fails here until the fixture sets it.
func TestMaskPage_KeepsEveryPageMember(t *testing.T) {
	page := listingPage()
	masked, err := parsehttp.MaskPage(page, []string{"id"})
	require.NoError(t, err)
	src, dst := reflect.ValueOf(page).Elem(), reflect.ValueOf(masked).Elem()
	for i := range src.NumField() {
		name := src.Type().Field(i).Name
		if name == "Items" {
			continue
		}
		require.False(t, src.Field(i).IsZero(), "listingPage sets %s", name)
		assert.Equal(t, src.Field(i).Interface(), dst.FieldByName(name).Interface(), name)
	}
}

func TestMaskPage_EmptyAndNonObjectItems(t *testing.T) {
	for _, items := range [][]listing{nil, {}} {
		masked, err := parsehttp.MaskPage(&filter.Page[listing]{Items: items}, []string{"id"})
		require.NoError(t, err)
		body, err := json.Marshal(masked)
		require.NoError(t, err)
		assert.JSONEq(t, `{"items":[],"page_info":{"has_next_page":false,"has_prev_page":false}}`, string(body))
	}

	_, err := parsehttp.MaskPage(&filter.Page[int]{Items: []int{1}}, []string{"id"})
	require.ErrorContains(t, err, "masking list item 0: it encodes as number, not an object")
	_, err = parsehttp.MaskPage(&filter.Page[*listing]{Items: []*listing{{ID: 1}, nil}}, []string{"id"})
	require.ErrorContains(t, err, "masking list item 1: it encodes as null, not an object")
}

// WriteList answers the request's mask; without one it writes the page
// exactly as WriteJSON does.
func TestWriteList_AppliesTheRequestsFieldMask(t *testing.T) {
	res := gadgets(t)
	page := &filter.Page[gadget]{Items: []gadget{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}}, PageInfo: filter.PageInfo{HasNextPage: true, EndCursor: "e"}}
	write := func(t *testing.T, q url.Values) *httptest.ResponseRecorder {
		t.Helper()
		req, err := res.ParseList(q)
		require.NoError(t, err)
		w := httptest.NewRecorder()
		require.NoError(t, parsehttp.WriteList(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/gadgets", nil), req, page))
		return w
	}

	masked := write(t, url.Values{filter.ParamFields: {"name"}})
	assert.Equal(t, http.StatusOK, masked.Code)
	assert.JSONEq(t, `{"items":[{"name":"a"},{"name":"b"}],"page_info":{"has_next_page":true,"has_prev_page":false,"end_cursor":"e"}}`, masked.Body.String())

	whole := httptest.NewRecorder()
	require.NoError(t, parsehttp.WriteJSON(whole, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/gadgets", nil), http.StatusOK, page))
	assert.Equal(t, whole.Body.String(), write(t, url.Values{}).Body.String())
	unparsed := httptest.NewRecorder()
	require.NoError(t, parsehttp.WriteList(unparsed, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/gadgets", nil), nil, page))
	assert.Equal(t, whole.Body.String(), unparsed.Body.String())

	w := httptest.NewRecorder()
	err := parsehttp.WriteList(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/gadgets", nil), nil, &filter.Page[float64]{Items: []float64{math.NaN()}})
	require.Error(t, err)
	assert.Zero(t, w.Body.Len(), "nothing is written")
}

// A mask names fields under the resource's field-name tag; the response
// keeps the members encoding/json writes those fields under.
func TestWriteList_NarrowsToTheMasksJSONMembers(t *testing.T) {
	type record struct {
		ID    string `bson:"_id"   json:"id"  db:"id" parse:"filter,sort"`
		Title string `bson:"title"            db:"title"`
		Score int    `bson:"score" json:"-"  db:"score"`
	}
	records, err := parse.NewResource[record]("records", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	require.NoError(t, err)
	page := &filter.Page[record]{Items: []record{{ID: "r1", Title: "a", Score: 3}}}
	for _, tt := range []struct {
		mask, want string
	}{
		{"title", `[{"Title":"a"}]`},
		{"_id,score", `[{"id":"r1"}]`},
		{"score", `[{}]`},
	} {
		t.Run(tt.mask, func(t *testing.T) {
			req, err := records.ParseList(url.Values{filter.ParamFields: {tt.mask}})
			require.NoError(t, err)
			w := httptest.NewRecorder()
			require.NoError(t, parsehttp.WriteList(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/records", nil), req, page))
			var body struct{ Items json.RawMessage }
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.JSONEq(t, tt.want, string(body.Items))
		})
	}
}
