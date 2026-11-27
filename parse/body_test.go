//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func limitedDocs(t *testing.T, opts ...core.Option) *parse.Resource[limitedDoc] {
	t.Helper()
	r, err := parse.NewResource[limitedDoc](onSQLite, "limited", parse.ParserOptions(opts...))
	require.NoError(t, err)
	return r
}

// The media type names the grammar, and nothing else is read: a merge patch
// for application/merge-patch+json or application/json, a JSON Patch for
// application/json-patch+json.
func TestParsePatchRequest_TheMediaTypeNamesTheGrammar(t *testing.T) {
	docs := limitedDocs(t)
	merge, err := docs.ParseMergePatch([]byte(`{"name":"a"}`))
	require.NoError(t, err)
	jsonPatch, err := docs.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"a"}]`))
	require.NoError(t, err)

	for _, tt := range []struct {
		contentType, body string
		want              *patch.ParsedPatch
	}{
		{parse.MediaTypeMergePatch, `{"name":"a"}`, merge},
		{"application/json", `{"name":"a"}`, merge},
		{"Application/JSON; charset=utf-8", `{"name":"a"}`, merge},
		{parse.MediaTypeJSONPatch, `[{"op":"replace","path":"/name","value":"a"}]`, jsonPatch},
	} {
		got, err := docs.ParsePatchRequest(tt.contentType, strings.NewReader(tt.body))
		require.NoError(t, err, tt.contentType)
		assert.Equal(t, tt.want, got, tt.contentType)
	}

	_, err = docs.ParsePatchRequest(parse.MediaTypeMergePatch, strings.NewReader(`[{"op":"replace","path":"/name","value":"a"}]`))
	assert.ErrorIs(t, err, patch.ECodeInvalidJSON, "a JSON Patch under the merge patch type is read as a merge patch")

	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json-patch", "not a media type;;"} {
		read := false
		body := readerFunc(func(p []byte) (int, error) { read = true; return 0, io.EOF })
		_, err := docs.ParsePatchRequest(contentType, body)
		require.ErrorIs(t, err, patch.ECodeInvalidJSON, contentType)
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok)
		assert.Equal(t, "content-type", pe.Details[core.DetailParameter], contentType)
		assert.False(t, read, "a body of another media type is not read: %q", contentType)
	}
}

// The body is read no further than MaxInputSize, one byte past it telling a
// body too large without reading the rest; with MaxInputSize unlimited, no
// further than 1 MiB, as a list body.
func TestParsePatchRequest_TheBodyIsBoundedByTheInputSize(t *testing.T) {
	body := func(n int) string { return `{"name":"` + strings.Repeat("x", n-len(`{"name":""}`)) + `"}` }

	docs := limitedDocs(t, core.WithMaxInputSize(64))
	_, err := docs.ParsePatchRequest(parse.MediaTypeMergePatch, strings.NewReader(body(64)))
	assert.NoError(t, err, "a body of exactly the limit")
	r := &countingReader{r: strings.NewReader(body(64) + strings.Repeat(" ", 1<<20))}
	_, err = docs.ParsePatchRequest(parse.MediaTypeJSONPatch, r)
	require.ErrorIs(t, err, patch.ECodeInputTooLarge)
	assert.ErrorContains(t, err, "patch body exceeds maximum 64 bytes")
	assert.Equal(t, 65, r.n, "read one byte past the limit and no further")

	for _, size := range []int{0, -1} {
		unlimited := limitedDocs(t, core.WithMaxInputSize(size))
		_, err := unlimited.ParsePatchRequest(parse.MediaTypeMergePatch, strings.NewReader(body(1<<20)))
		require.NoError(t, err, "input size %d reads a body of 1 MiB", size)
		r := &countingReader{r: strings.NewReader(body(4 << 20))}
		_, err = unlimited.ParsePatchRequest(parse.MediaTypeMergePatch, r)
		require.ErrorIs(t, err, patch.ECodeInputTooLarge, "input size %d", size)
		require.ErrorContains(t, err, "patch body exceeds maximum 1048576 bytes")
		assert.Equal(t, 1<<20+1, r.n, "input size %d reads one byte past 1 MiB and no further", size)
	}
}

// A body that cannot be read to its end is a *BodyReadError: neither the
// request's fault nor the server's.
func TestParsePatchRequest_AReadThatFails(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, errors.New("connection reset")} {
		_, err := limitedDocs(t).ParsePatchRequest(parse.MediaTypeMergePatch, io.MultiReader(strings.NewReader(`{"name":`), iotest.ErrReader(cause)))
		require.ErrorIs(t, err, cause)
		readErr, ok := errors.AsType[*parse.BodyReadError](err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, parse.BodyReadError{Table: "limited", Request: "patch", Err: cause}, *readErr)
		assert.Equal(t, "parse: limited: reading patch body: "+cause.Error(), err.Error())
		_, isParse := errors.AsType[*core.ParseError](err)
		assert.False(t, isParse)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// a request document that names a member twice, or holds
// invalid UTF-8, is refused, so a request cannot have one of two values win.
func TestRequestDocuments_RefuseDuplicateNamesAndInvalidUTF8(t *testing.T) {
	r := widgets(t)
	listFilter := func(doc string) error {
		_, err := r.ParseList(url.Values{"filter": {doc}})
		return err
	}
	mergePatch := func(doc string) error { _, err := r.ParseMergePatch([]byte(doc)); return err }
	jsonPatch := func(doc string) error { _, err := r.ParsePatch([]byte(doc)); return err }
	for name, tc := range map[string]struct {
		parse func(string) error
		doc   string
		code  string
	}{
		"filter member twice":         {listFilter, `{"name":"a","name":"b"}`, filter.ECodeInvalidJSON.String()},
		"nested filter member twice":  {listFilter, `{"$or":[{"name":"a","name":"b"}]}`, filter.ECodeInvalidJSON.String()},
		"filter invalid UTF-8":        {listFilter, "{\"name\":\"\xff\"}", filter.ECodeInvalidJSON.String()},
		"merge patch member twice":    {mergePatch, `{"name":"a","name":"b"}`, patch.ECodeInvalidJSON.String()},
		"JSON Patch operation twice":  {jsonPatch, `[{"op":"replace","op":"remove","path":"/name","value":"a"}]`, patch.ECodeInvalidJSON.String()},
		"JSON Patch value invalid":    {jsonPatch, "[{\"op\":\"replace\",\"path\":\"/name\",\"value\":\"\xff\"}]", patch.ECodeInvalidJSON.String()},
		"data after the filter value": {listFilter, `{"name":"a"} {"name":"b"}`, filter.ECodeInvalidJSON.String()},
	} {
		t.Run(name, func(t *testing.T) {
			var pe *core.ParseError
			require.ErrorAs(t, tc.parse(tc.doc), &pe)
			assert.Equal(t, tc.code, pe.Code)
		})
	}
}

// Numbers keep their literal through the stricter decoder.
func TestStdlibJSON_KeepsNumbersExact(t *testing.T) {
	var v map[string]any
	require.NoError(t, core.StdlibJSON{}.Decode([]byte(`{"n":9007199254740993,"f":1.50,"a":[{"m":-0}]}`), &v))
	assert.Equal(t, json.Number("9007199254740993"), v["n"])
	assert.Equal(t, json.Number("1.50"), v["f"])
	array, arrayOK := v["a"].([]any)
	require.True(t, arrayOK)
	require.Len(t, array, 1)
	object, objectOK := array[0].(map[string]any)
	require.True(t, objectOK)
	assert.Equal(t, json.Number("-0"), object["m"])
}

type retryPolicy struct {
	Attempts int           `json:"attempts"`
	Backoff  time.Duration `json:"backoff"`
}

type retryingJob struct {
	ID     string      `json:"id"     db:"id"`
	Policy retryPolicy `json:"policy" db:"policy" parse:"patch"`
}

// Why StdlibJSON decodes with encoding/json's options: a typed document
// round-trips through the codec, and responses write a time.Duration as
// nanoseconds, which encoding/json/v2's defaults refuse to read back.
func TestStdlibJSON_ReadsDocumentsAsResponsesWriteThem(t *testing.T) {
	r, err := parse.NewResource[retryingJob](onSQLite, "jobs")
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"policy":{"attempts":5}}`))
	require.NoError(t, err)
	got, err := r.ApplyValidatedPatch(p, retryingJob{ID: "j", Policy: retryPolicy{Attempts: 1, Backoff: 2 * time.Second}})
	require.NoError(t, err)
	assert.Equal(t, retryPolicy{Attempts: 5, Backoff: 2 * time.Second}, got.Policy)
}

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
			require.ErrorContains(t, err, "list body exceeds")
			assert.Equal(t, tt.limit+1, body.n, "read one byte past the limit and no further")
		})
	}
}

// An input size the headroom would carry past MaxInt still bounds the read
// rather than wrapping its limit negative and reading nothing.
func TestParseListBody_AnInputSizeAtMaxInt(t *testing.T) {
	got, err := limitedWidgets(t, core.WithMaxInputSize(math.MaxInt)).ParseListBody(strings.NewReader(`{"sort":"-rank"}`))
	require.NoError(t, err)
	require.NotNil(t, got.Sort())
}

func TestNewResource_RefusesANegativeListBodySize(t *testing.T) {
	_, err := parse.NewResource[widget](onSQLite, "widgets", parse.ParserOptions(core.WithMaxListBodySize(-1)))
	assert.ErrorContains(t, err, "parse: widgets: parser options: WithMaxListBodySize(-1): a limit is never negative")
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
			require.ErrorContains(t, err, "widgets: reading list body")
			_, isParse := errors.AsType[*core.ParseError](err)
			assert.False(t, isParse, "the request is not at fault")
			readErr, ok := errors.AsType[*parse.BodyReadError](err)
			require.True(t, ok)
			assert.Equal(t, parse.BodyReadError{Table: "widgets", Request: "list", Err: cause}, *readErr)
		})
	}
}

// ParseListPost is a POST's list from its parts, for a framework that hands
// a handler those rather than an *http.Request.
func TestParseListPost(t *testing.T) {
	res, err := parse.NewResource[gadgetRow](onSQLite, "gadgets", parse.AcceptParams("tenant"), atOneInstant())
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{Sort: "-id"})
	require.NoError(t, err)

	got, err := res.ParseListPost(url.Values{"tenant": {"t1"}}, "application/json; charset=utf-8", strings.NewReader(`{"sort":"-id"}`))
	require.NoError(t, err)
	assert.Equal(t, want, got, "the endpoint's own parameter stays in the query")
}

func TestParseListPost_Refusals(t *testing.T) {
	res, err := parse.NewResource[gadgetRow](onSQLite, "gadgets", parse.AcceptParams("tenant"))
	require.NoError(t, err)
	tests := []struct {
		name        string
		query       url.Values
		contentType string
		param       string
	}{
		{"a list parameter in the query", url.Values{"sort": {"-id"}}, "application/json", "sort"},
		{"a field filter in the query", url.Values{"name": {"a"}}, "application/json", "name"},
		{"a body of another type", nil, "text/plain", "content-type"},
		{"a body without a type", nil, "", "content-type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := res.ParseListPost(tt.query, tt.contentType, strings.NewReader(`{}`))
			require.ErrorIs(t, err, filter.ECodeInvalidListParam)
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok)
			assert.Equal(t, tt.param, pe.Details["parameter"])
		})
	}
}

type gadgetRow struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
}

// limitedWidgets is widgets under parser options.
func limitedWidgets(t *testing.T, opts ...core.Option) *parse.Resource[widget] {
	t.Helper()
	r, err := parse.NewResource[widget](onSQLite, "widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.PageSize(2, 3), atOneInstant(), parse.ParserOptions(opts...))
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

// A patch body over MaxInputSize is E2063, the code the error table
// documents for every oversized body, filter or patch.
func TestPatch_OversizedBodyIsInputTooLarge(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.MaxInputSize = 16
	schema := newSchema("", 0, map[string]*patch.PathSchema{"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}}})
	body := []byte(`{"name":"` + strings.Repeat("x", 32) + `"}`)

	_, err := patch.ParseMergePatch(body, schema, cfg)
	requireCode(t, err, patch.ECodeInputTooLarge.String())

	_, err = patch.NewParser(schema, cfg).Parse([]byte(`[{"op":"replace","path":"/name","value":"` + strings.Repeat("x", 32) + `"}]`))
	requireCode(t, err, patch.ECodeInputTooLarge.String())
}
