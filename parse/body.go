package parse

import (
	"fmt"
	"io"
	"math"
	"mime"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/httpjson"
	"github.com/ashtonian/pkg/parse/patch"
)

// The patch media types ParsePatchRequest reads.
const (
	// MediaTypeMergePatch is a JSON Merge Patch (RFC 7396).
	MediaTypeMergePatch = "application/merge-patch+json"
	// MediaTypeJSONPatch is a JSON Patch (RFC 6902).
	MediaTypeJSONPatch = "application/json-patch+json"
)

// BodyReadError is a request body that could not be read to its end,
// usually a client that went away or sent less than it declared. It is no
// *core.ParseError, since no body arrived to be judged, and no defect of the
// server: a handler answers it as a bad request, or not at all when the
// client is gone. Err is the reader's error.
type BodyReadError struct {
	// Table is the resource's table.
	Table string
	// Request is the body's request: "list" or "patch".
	Request string
	Err     error
}

func (e *BodyReadError) Error() string {
	return fmt.Sprintf("parse: %s: reading %s body: %v", e.Table, e.Request, e.Err)
}

func (e *BodyReadError) Unwrap() error { return e.Err }

// ParsePatchRequest validates a patch request: the value of its
// Content-Type header and its body, for a transport that hands a handler
// those parts rather than an *http.Request (parsehttp.ParsePatch is this
// over one). The media type names the grammar: MediaTypeMergePatch, or
// application/json as clients send by default, is a JSON Merge Patch
// (ParseMergePatch), and MediaTypeJSONPatch a JSON Patch (ParsePatch). Any
// other media type, or none, is E2086, so a body is never read in a grammar
// its sender did not name.
//
// The body is read no further than MaxInputSize, the size the patch parsers
// hold a body to, and a longer one is E2063; a MaxInputSize of zero or less
// reads it whole. A read that fails is a *BodyReadError.
func (r *Resource[T]) ParsePatchRequest(contentType string, body io.Reader, opts ...PatchOption) (*patch.ParsedPatch, error) {
	var grammar func([]byte, ...PatchOption) (*patch.ParsedPatch, error)
	if media, _, err := mime.ParseMediaType(contentType); err == nil {
		switch media {
		case MediaTypeMergePatch, httpjson.MediaType:
			grammar = r.ParseMergePatch
		case MediaTypeJSONPatch:
			grammar = r.ParsePatch
		}
	}
	if grammar == nil {
		return nil, &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: patch.ECodeInvalidJSON.String(),
			Message: fmt.Sprintf("a patch body is %s, %s or %s", MediaTypeMergePatch, MediaTypeJSONPatch, httpjson.MediaType),
			Details: map[string]any{core.DetailParameter: "content-type"},
		}
	}
	data, err := r.readBody(body, r.cfg.MaxInputSize, "patch", core.ParseErrorKindPatch)
	if err != nil {
		return nil, err
	}
	return grammar(data, opts...)
}

// readBody reads body up to limit bytes, limit zero or less being none: one
// byte more tells a body that is too large without reading the rest of it,
// so the limit is held below MaxInt for that byte. A body over limit is
// E2063; a read that fails is a *BodyReadError.
func (r *Resource[T]) readBody(body io.Reader, limit int, request string, kind core.ParseErrorKind) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	if limit > 0 {
		body = io.LimitReader(body, int64(min(limit, math.MaxInt-1))+1)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, &BodyReadError{Table: r.table, Request: request, Err: err}
	}
	if limit > 0 && len(data) > limit {
		return nil, &core.ParseError{
			Kind: kind, Code: filter.ECodeInputTooLarge.String(),
			Message: fmt.Sprintf("%s body exceeds maximum %d bytes", request, limit),
		}
	}
	return data, nil
}
