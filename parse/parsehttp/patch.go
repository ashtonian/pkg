package parsehttp

import (
	"net/http"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/patch"
)

// ParsePatch reads a patch request from r: its body in the grammar its
// Content-Type names, a JSON Merge Patch for application/merge-patch+json or
// application/json and a JSON Patch for application/json-patch+json, read no
// further than the resource's MaxInputSize, or 1 MiB when it is unlimited
// (Resource.ParsePatchRequest over an *http.Request). Another media type is
// E2086 and a longer body E2063; a body that could not be read is a
// *parse.BodyReadError, which WriteError declines.
func ParsePatch[T any](r *http.Request, res *parse.Resource[T], opts ...parse.PatchOption) (*patch.ParsedPatch, error) {
	return res.ParsePatchRequest(r.Header.Get("Content-Type"), r.Body, opts...)
}
