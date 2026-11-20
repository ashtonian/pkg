//go:build unit

package parsehttp_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func gadgets(t *testing.T) *parse.Resource[gadget] {
	t.Helper()
	r, err := parse.NewResource[gadget]("gadgets", parse.AcceptParams("tenant"))
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
