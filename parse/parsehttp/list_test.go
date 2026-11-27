//go:build unit

package parsehttp_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

type gadget struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
}

// gadgets parses at a fixed instant, so two parses of one request are equal:
// a list request records the instant its relative times resolve at.
func gadgets(t *testing.T) *parse.Resource[gadget] {
	t.Helper()
	clock := func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	r, err := parse.NewResource[gadget](onSQLite, "gadgets", parse.AcceptParams("tenant"), parse.ParserOptions(core.WithClock(clock)))
	require.NoError(t, err)
	return r
}

func TestParseList_ReadsEitherTransport(t *testing.T) {
	res := gadgets(t)
	want, err := res.ParseListInput(parse.ListInput{Filter: `{"name":"a"}`, Sort: "-id", PageSize: 5})
	require.NoError(t, err)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := httptest.NewRequest(method, `/gadgets?filter=%7B%22name%22%3A%22a%22%7D&sort=-id&page_size=5&tenant=t1`, nil)
		got, err := parsehttp.ParseList(r, res)
		require.NoError(t, err, method)
		assert.Equal(t, want, got, method)
	}

	for _, contentType := range []string{"application/json", "application/json; charset=utf-8"} {
		r := httptest.NewRequest(http.MethodPost, "/gadgets?tenant=t1", strings.NewReader(`{"filter":{"name":"a"},"sort":"-id","page_size":5}`))
		r.Header.Set("Content-Type", contentType)
		got, err := parsehttp.ParseList(r, res)
		require.NoError(t, err, contentType)
		assert.Equal(t, want, got, "a POST body is the same request; the endpoint's own parameter stays in the query")
	}
}

func TestParseList_Refusals(t *testing.T) {
	res := gadgets(t)
	tests := []struct {
		name, method, target, contentType, body, param string
	}{
		{"a list parameter in a POST's query", http.MethodPost, "/gadgets?sort=-id", "application/json", `{}`, "sort"},
		{"a field filter in a POST's query", http.MethodPost, "/gadgets?name=a", "application/json", `{}`, "name"},
		{"a POST body of another type", http.MethodPost, "/gadgets", "application/x-www-form-urlencoded", `filter=%7B%7D`, "content-type"},
		{"a POST body without a type", http.MethodPost, "/gadgets", "", `{}`, "content-type"},
		{"another method", http.MethodPut, "/gadgets", "application/json", `{}`, "method"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			_, err := parsehttp.ParseList(r, res)
			require.ErrorIs(t, err, filter.ECodeInvalidListParam)
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok)
			assert.Equal(t, tt.param, pe.Details["parameter"])
		})
	}
}

// A query string that does not decode is refused whole as E2067, on every
// transport: url.URL.Query would drop the pair and keep the rest, and a list
// that named a filter would answer every row.
func TestParseList_AQueryStringThatDoesNotDecode(t *testing.T) {
	res := gadgets(t)
	for _, target := range []string{
		"/gadgets?name=%zz",
		"/gadgets?name=a%",
		"/gadgets?name=a;x=1",
		"/gadgets?filter=%7B%22name%22%3A%22a%22%7D%",
		"/gadgets?tenant=%zz",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
			r := httptest.NewRequest(method, target, strings.NewReader(`{"filter":{"name":"a"}}`))
			r.Header.Set("Content-Type", "application/json")
			_, err := parsehttp.ParseList(r, res)
			require.ErrorIs(t, err, filter.ECodeInvalidListParam, "%s %s", method, target)
			assert.ErrorContains(t, err, "the query string does not decode", "%s %s", method, target)
		}
		_, err := parsehttp.Query(httptest.NewRequest(http.MethodPatch, target, nil))
		assert.ErrorIs(t, err, filter.ECodeInvalidListParam, target)
	}

	q, err := parsehttp.Query(httptest.NewRequest(http.MethodGet, "/gadgets?name=a&name=b&filter=%7B%7D", nil))
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"name": {"a", "b"}, "filter": {"{}"}}, map[string][]string(q))
}
