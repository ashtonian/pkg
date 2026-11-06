package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"slices"
)

// ContentTypeProblemJSON is the RFC 9457 JSON problem-details media type.
const ContentTypeProblemJSON = "application/problem+json"

// ErrNotProblem means the response media type was not accepted as problem details.
// Its body has not been read or closed.
var ErrNotProblem = errors.New("httpx: response is not problem details")

// ErrProblemExtensionNotFound means a problem has no extension by that name.
var ErrProblemExtensionNotFound = errors.New("httpx: problem extension not found")

// Problem is an RFC 9457 object. Status is advisory and never replaces the
// response's HTTP status. Extensions retain their JSON until explicitly decoded.
// All server-supplied text is untrusted, including the title returned by Error.
type Problem struct {
	Type       string                     `json:"type"`
	Title      string                     `json:"title,omitempty"`
	Status     int                        `json:"status,omitempty"`
	Detail     string                     `json:"detail,omitempty"`
	Instance   string                     `json:"instance,omitempty"`
	Extensions map[string]json.RawMessage `json:"-"`
}

// Error describes the problem without automatically including its detail,
// instance or extensions. Inspect those fields explicitly when appropriate.
func (p *Problem) Error() string {
	if p == nil {
		return "httpx: problem details"
	}
	title := p.Title
	if title == "" {
		title = http.StatusText(p.Status)
	}
	if title == "" {
		title = p.Type
	}
	if title == "" {
		title = "problem details"
	}
	if p.Status != 0 {
		return fmt.Sprintf("httpx: %s (status %d)", title, p.Status)
	}
	return "httpx: " + title
}

// Extension decodes one member into v using encoding/json. An absent member
// returns ErrProblemExtensionNotFound; an explicit null remains a JSON null.
func (p *Problem) Extension(name string, v any) error {
	if p != nil {
		if raw, ok := p.Extensions[name]; ok {
			if err := json.Unmarshal(raw, v); err != nil {
				return fmt.Errorf("problem extension %q: %w", name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrProblemExtensionNotFound, name)
}

// UnmarshalJSON reads one object, defaulting a missing type to about:blank.
// RFC 9457 requires standard members of the wrong JSON type to be ignored.
func (p *Problem) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if members == nil {
		return errors.New("httpx: problem details must be an object")
	}
	result := Problem{Type: "about:blank"}
	for name, raw := range members {
		var target *string
		switch name {
		case "type":
			target = &result.Type
		case "title":
			target = &result.Title
		case "detail":
			target = &result.Detail
		case "instance":
			target = &result.Instance
		case "status":
			// JSON permits integral numbers written with a fraction or
			// exponent. Only HTTP status codes have meaning in this member.
			var status float64
			if err := json.Unmarshal(raw, &status); err == nil && status >= 100 && status <= 599 && math.Trunc(status) == status {
				result.Status = int(status)
			}
			delete(members, name)
			continue
		default:
			continue
		}
		// null is not a string; leave the default in place.
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, target); err != nil {
				return err
			}
		}
		delete(members, name)
	}
	if len(members) != 0 {
		result.Extensions = members
	}
	*p = result
	return nil
}

// MarshalJSON writes extension members alongside standard members. Extensions
// cannot override any of the five reserved standard names.
func (p Problem) MarshalJSON() ([]byte, error) {
	type standard Problem
	data, err := json.Marshal(standard(p))
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	for name, raw := range p.Extensions {
		switch name {
		case "type", "title", "status", "detail", "instance":
			continue
		}
		members[name] = raw
	}
	return json.Marshal(members)
}

// WithProblemErrors makes typed request helpers return *Problem for a non-2xx
// problem response. Other media types and HEAD responses remain caller-owned.
// Malformed or oversized problem bodies return their decoding error instead.
func WithProblemErrors() DecodeOption {
	return func(c *decodeConfig) { c.problemErrors = true }
}

// WithProblemMediaTypes accepts additional exact JSON media types as problem
// documents. Values must be application/json or end in +json, without parameters.
// It does not classify every JSON error as a problem unless explicitly requested.
func WithProblemMediaTypes(mediaTypes ...string) DecodeOption {
	values := slices.Clone(mediaTypes)
	return func(c *decodeConfig) { c.problemMediaTypes = append(c.problemMediaTypes, values...) }
}

// DecodeProblem decodes an accepted problem media type, regardless of status.
// The default is application/problem+json, with optional media-type parameters.
// Other media types return ErrNotProblem without reading or closing the body.
// WithMaxResponseBytes and body retention/closure follow DecodeResponse: a fully
// read body is closed and replayed; an oversized body remains readable in full
// and must be closed by the caller. Relative Type/Instance URIs are resolved
// against resp.Request.URL when available, without fetching them.
func DecodeProblem(resp *http.Response, opts ...DecodeOption) (*Problem, error) {
	cfg, err := newDecodeConfig(opts)
	if err != nil {
		return nil, err
	}
	return decodeProblem(resp, cfg)
}

func decodeProblem(resp *http.Response, cfg decodeConfig) (*Problem, error) {
	if resp == nil {
		return nil, invalidConfig("DecodeProblem: response is nil")
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (mediaType != ContentTypeProblemJSON && !slices.Contains(cfg.problemMediaTypes, mediaType)) {
		return nil, ErrNotProblem
	}
	if resp.Body == nil {
		return nil, io.EOF
	}
	data, err := readBody(resp, cfg.maxBytes)
	if err != nil {
		return nil, err
	}
	var problem Problem
	if err := json.Unmarshal(data, &problem); err != nil {
		return nil, fmt.Errorf("decode problem details: %w", err)
	}
	if resp.Request != nil && resp.Request.URL != nil {
		for _, field := range []*string{&problem.Type, &problem.Instance} {
			if field == &problem.Instance && *field == "" {
				continue
			}
			if ref, err := url.Parse(*field); err == nil {
				*field = resp.Request.URL.ResolveReference(ref).String()
			}
		}
	}
	return &problem, nil
}
