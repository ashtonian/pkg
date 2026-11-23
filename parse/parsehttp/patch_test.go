//go:build unit

package parsehttp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsehttp"
	"github.com/ashtonian/pkg/parse/patch"
)

type note struct {
	ID   int    `json:"id"   db:"id"`
	Text string `json:"text" db:"text" parse:"patch"`
}

// ParsePatch reads the body in the grammar the request's Content-Type
// names.
func TestParsePatch_ReadsTheRequestsMediaType(t *testing.T) {
	notes, err := parse.NewResource[note]("notes")
	require.NoError(t, err)
	request := func(contentType, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPatch, "/notes/1", strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		return r
	}

	want, err := notes.ParseMergePatch([]byte(`{"text":"a"}`))
	require.NoError(t, err)
	got, err := parsehttp.ParsePatch(request("application/json", `{"text":"a"}`), notes)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	want, err = notes.ParsePatch([]byte(`[{"op":"replace","path":"/text","value":"a"}]`))
	require.NoError(t, err)
	got, err = parsehttp.ParsePatch(request(parse.MediaTypeJSONPatch, `[{"op":"replace","path":"/text","value":"a"}]`), notes)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	_, err = parsehttp.ParsePatch(request("", `{"text":"a"}`), notes)
	assert.ErrorIs(t, err, patch.ECodeInvalidJSON, "a body without a media type")
}
