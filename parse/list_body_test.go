//go:build unit

package parse_test

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestParseListBody_Refusals(t *testing.T) {
	res := widgets(t)
	tests := []struct {
		name, body string
		code       core.Code
		param      string
	}{
		{"an array", `[]`, filter.ECodeInvalidJSON, ""},
		{"a string", `"filter"`, filter.ECodeInvalidJSON, ""},
		{"a member named twice", `{"sort":"name","sort":"rank"}`, filter.ECodeInvalidJSON, ""},
		{"data after the object", `{} {}`, filter.ECodeInvalidJSON, ""},
		{"an unknown member", `{"filtr":{"name":"beta"}}`, filter.ECodeInvalidListParam, "filtr"},
		{"a flat field filter", `{"name":"beta"}`, filter.ECodeInvalidListParam, "name"},
		{"a filter as text", `{"filter":"{\"name\":\"beta\"}"}`, filter.ECodeInvalidListParam, "filter"},
		{"a filter as an array", `{"filter":[{"name":"beta"}]}`, filter.ECodeInvalidListParam, "filter"},
		{"a page size as text", `{"page_size":"5"}`, filter.ECodeInvalidListParam, "page_size"},
		{"a fractional page size", `{"page_size":2.5}`, filter.ECodeInvalidListParam, "page_size"},
		{"a zero page size", `{"page_size":0}`, filter.ECodeInvalidListParam, "page_size"},
		{"a negative page size", `{"page_size":-1}`, filter.ECodeInvalidListParam, "page_size"},
		{"a flag as text", `{"include_total":"true"}`, filter.ECodeInvalidListParam, "include_total"},
		{"names as one string", `{"facets":"name"}`, filter.ECodeInvalidListParam, "facets"},
		{"a sort as an array", `{"sort":["name"]}`, filter.ECodeInvalidListParam, "sort"},
		{"a filter naming no field", `{"filter":{"nope":1}}`, filter.ECodeUnknownField, ""},
		{"a body over the input limit", `{"search":"` + strings.Repeat("x", 70_000) + `"}`, filter.ECodeInputTooLarge, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := res.ParseListBody(strings.NewReader(tt.body))
			require.ErrorIs(t, err, tt.code)
			if tt.param != "" {
				pe, ok := errors.AsType[*core.ParseError](err)
				require.True(t, ok)
				assert.Equal(t, tt.param, pe.Details["parameter"])
			}
		})
	}
}

func TestParseListBody_NullMembersAreAbsent(t *testing.T) {
	res := widgets(t)
	got, err := res.ParseListBody(strings.NewReader(`{"filter":null,"sort":null,"page_size":null,"facets":null}`))
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestParseListBody_ReadErrorsAreNotRequestErrors(t *testing.T) {
	cause := errors.New("connection reset")
	_, err := widgets(t).ParseListBody(iotest.ErrReader(cause))
	assert.ErrorIs(t, err, cause)
	_, isParse := errors.AsType[*core.ParseError](err)
	assert.False(t, isParse, "the request is not at fault")
}
