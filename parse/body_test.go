//go:build unit

package parse_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

func limitedDocs(t *testing.T, opts ...core.Option) *parse.Resource[limitedDoc] {
	t.Helper()
	r, err := parse.NewResource[limitedDoc]("limited", parse.ParserOptions(opts...))
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
// body too large without reading the rest; zero or less reads it whole.
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
		_, err := limitedDocs(t, core.WithMaxInputSize(size)).ParsePatchRequest(parse.MediaTypeMergePatch, strings.NewReader(body(1<<20)))
		assert.NoError(t, err, "input size %d reads the body whole", size)
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
