//go:build unit

package parse_test

import (
	"errors"
	"io"
	"math"
	"net/url"
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
		{"a filter over the input limit", `{"filter":` + nameFilter(64<<10+1) + `}`, filter.ECodeInputTooLarge, ""},
		{"a body over the list body limit", searchBody(80<<10 + 1), filter.ECodeInputTooLarge, ""},
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

// A filter MaxInputSize admits in a query string is admitted in a body
// beside the other members: the body limit leaves room for them, and the
// filter member is bounded as the filter parameter is.
func TestParseListBody_AFilterAtTheInputLimitBesideTheOtherMembers(t *testing.T) {
	cursor := strings.Repeat("c", 4096)
	tests := []struct {
		name       string
		opts       []core.Option
		filterSize int
	}{
		{"the default input size", nil, 64 << 10},
		{"a raised input size", []core.Option{core.WithMaxInputSize(256 << 10)}, 256 << 10},
		{"an unlimited input size", []core.Option{core.WithMaxInputSize(0)}, 512 << 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := limitedWidgets(t, tt.opts...)
			doc := nameFilter(tt.filterSize)
			want, err := res.ParseList(url.Values{
				filter.ParamFilter: {doc}, filter.ParamSort: {"-rank,name"}, filter.ParamCursor: {cursor},
				filter.ParamPageSize: {"2"}, filter.ParamSearch: {"beta"},
			})
			require.NoError(t, err, "the filter fits the input limit")

			got, err := res.ParseListBody(strings.NewReader(
				`{"filter":` + doc + `,"sort":"-rank,name","cursor":"` + cursor + `","page_size":2,"search":"beta"}`))
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// The list body limit bounds the whole document, even when MaxInputSize
// leaves the filter unlimited, and a body over it is read no further than
// the byte that tells it is over.
func TestParseListBody_BodyLimit(t *testing.T) {
	tests := []struct {
		name  string
		opts  []core.Option
		limit int
	}{
		{"the default input size with headroom", nil, 80 << 10},
		{"an unlimited input size", []core.Option{core.WithMaxInputSize(0)}, 1 << 20},
		{"a set list body size", []core.Option{core.WithMaxListBodySize(1024)}, 1024},
		{"a set list body size beside an unlimited input size", []core.Option{core.WithMaxInputSize(0), core.WithMaxListBodySize(1024)}, 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := limitedWidgets(t, tt.opts...)

			_, err := res.ParseListBody(strings.NewReader(searchBody(tt.limit)))
			require.NoError(t, err, "a body at the limit")

			body := &countingReader{r: strings.NewReader(searchBody(4 * tt.limit))}
			_, err = res.ParseListBody(body)
			require.ErrorIs(t, err, filter.ECodeInputTooLarge)
			assert.ErrorContains(t, err, "list body exceeds")
			assert.Equal(t, tt.limit+1, body.n, "read one byte past the limit and no further")
		})
	}
}

// An input size the headroom would carry past MaxInt still bounds the read
// rather than wrapping its limit negative and reading nothing.
func TestParseListBody_AnInputSizeAtMaxInt(t *testing.T) {
	got, err := limitedWidgets(t, core.WithMaxInputSize(math.MaxInt)).ParseListBody(strings.NewReader(`{"sort":"-rank"}`))
	require.NoError(t, err)
	require.NotNil(t, got.Sort)
}

func TestNewResource_RefusesANegativeListBodySize(t *testing.T) {
	_, err := parse.NewResource[widget]("widgets", parse.ParserOptions(core.WithMaxListBodySize(-1)))
	assert.ErrorContains(t, err, "list body limit -1 must not be negative")
}

func TestParseListBody_NullMembersAreAbsent(t *testing.T) {
	res := widgets(t)
	got, err := res.ParseListBody(strings.NewReader(`{"filter":null,"sort":null,"page_size":null,"facets":null}`))
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{})
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// A read error is the transport's, not the request's, and carries the
// resource and operation whatever the limits.
func TestParseListBody_ReadErrorsAreNotRequestErrors(t *testing.T) {
	tests := []struct {
		name string
		opts []core.Option
	}{
		{"a bounded input size", nil},
		{"an unlimited input size", []core.Option{core.WithMaxInputSize(0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause := errors.New("connection reset")
			_, err := limitedWidgets(t, tt.opts...).ParseListBody(iotest.ErrReader(cause))
			require.ErrorIs(t, err, cause)
			assert.ErrorContains(t, err, "widgets: reading list body")
			_, isParse := errors.AsType[*core.ParseError](err)
			assert.False(t, isParse, "the request is not at fault")
		})
	}
}

// ParseListPost is a POST's list from its parts, for a framework that hands
// a handler those rather than an *http.Request.
func TestParseListPost(t *testing.T) {
	res, err := parse.NewResource[gadgetRow]("gadgets", parse.AcceptParams("tenant"))
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{Sort: "-id"})
	require.NoError(t, err)

	got, err := res.ParseListPost(url.Values{"tenant": {"t1"}}, "application/json; charset=utf-8", strings.NewReader(`{"sort":"-id"}`))
	require.NoError(t, err)
	assert.Equal(t, want, got, "the endpoint's own parameter stays in the query")

	for _, tt := range []struct {
		query       url.Values
		contentType string
		param       string
	}{
		{url.Values{"sort": {"-id"}}, "application/json", "sort"},
		{url.Values{"name": {"a"}}, "application/json", "name"},
		{nil, "text/plain", "content-type"},
		{nil, "", "content-type"},
	} {
		_, err := res.ParseListPost(tt.query, tt.contentType, strings.NewReader(`{}`))
		require.ErrorIs(t, err, filter.ECodeInvalidListParam, tt.param)
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok)
		assert.Equal(t, tt.param, pe.Details["parameter"])
	}
}

type gadgetRow struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
}

// limitedWidgets is widgets under parser options.
func limitedWidgets(t *testing.T, opts ...core.Option) *parse.Resource[widget] {
	t.Helper()
	r, err := parse.NewResource[widget]("widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.PageSize(2, 3), parse.ParserOptions(opts...))
	require.NoError(t, err)
	return r
}

// nameFilter is a filter document of size bytes: {"name":"xx…"}.
func nameFilter(size int) string {
	return `{"name":"` + strings.Repeat("x", size-len(`{"name":""}`)) + `"}`
}

// searchBody is a list body of size bytes: {"search":"xx…"}.
func searchBody(size int) string {
	return `{"search":"` + strings.Repeat("x", size-len(`{"search":""}`)) + `"}`
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}
