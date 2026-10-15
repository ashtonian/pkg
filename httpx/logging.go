package httpx

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Redacted replaces the value of every sensitive header and query parameter
// in the snapshots a LoggingTransport hands to its hook.
const Redacted = "[REDACTED]"

// defaultSensitiveHeaders are always redacted. Names are canonical MIME
// header keys.
var defaultSensitiveHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"Cookie",
	"Set-Cookie",
	"X-Api-Key",
	"X-Auth-Token",
	"X-Access-Token",
	"X-Refresh-Token",
	"X-Csrf-Token",
	"X-Amz-Security-Token",
}

// defaultSensitiveQueryParams are always redacted; matching is
// case-insensitive. They cover OAuth tokens and codes, API keys and
// presigned-URL signatures.
var defaultSensitiveQueryParams = []string{
	"access_token",
	"id_token",
	"refresh_token",
	"token",
	"code",
	"client_secret",
	"api_key",
	"apikey",
	"key",
	"password",
	"secret",
	"sig",
	"signature",
	"x-amz-signature",
	"x-amz-credential",
	"x-amz-security-token",
	"x-goog-signature",
	"x-goog-credential",
}

// LoggingTransport reports each request and response to a LoggingHook.
//
// The hook never sees the live request or response: it receives detached
// snapshots built from an allowlist of fields — method, URL, protocol,
// header, host, length and transfer coding, and the request context — with
// no body, no redirect response and no TLS state. Sensitive header values and
// credential query parameters are replaced with [Redacted]; URL userinfo and
// fragments are removed, from the request URL and from URL-valued headers
// (Referer, Location, Content-Location) alike; and an error is reported with
// every such URL in its text redacted. The hook's error is detached from the
// original cause, which only the caller receives. Redaction is always on;
// WithSensitiveHeaders and WithSensitiveQueryParams extend the defaults.
//
// The guarantee covers what this transport hands its LoggingHook. Hooks of
// other transports — MetricsHook, RetryHook, the cache's hook — receive the
// live request, because they act on it rather than record it; do not log
// from them.
type LoggingTransport struct {
	base    http.RoundTripper
	hook    LoggingHook
	headers map[string]struct{} // canonical header keys
	params  map[string]struct{} // lower-case query parameter names
}

// LoggingOption configures a LoggingTransport.
type LoggingOption func(*LoggingTransport)

// NewLoggingTransport creates a new logging transport. A nil hook logs
// nothing.
func NewLoggingTransport(base http.RoundTripper, hook LoggingHook, opts ...LoggingOption) (*LoggingTransport, error) {
	if base == nil {
		return nil, invalidConfig("logging: base transport is nil")
	}
	t := &LoggingTransport{
		base:    base,
		hook:    loggingHookOrNoop(hook),
		headers: make(map[string]struct{}, len(defaultSensitiveHeaders)),
		params:  make(map[string]struct{}, len(defaultSensitiveQueryParams)),
	}
	WithSensitiveHeaders(defaultSensitiveHeaders...)(t)
	WithSensitiveQueryParams(defaultSensitiveQueryParams...)(t)
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// WithSensitiveHeaders redacts the named headers in addition to the
// defaults. Names are matched case-insensitively.
func WithSensitiveHeaders(names ...string) LoggingOption {
	return func(t *LoggingTransport) {
		for _, n := range names {
			t.headers[http.CanonicalHeaderKey(n)] = struct{}{}
		}
	}
}

// WithSensitiveQueryParams redacts the named query parameters in addition
// to the defaults. Names are matched case-insensitively.
func WithSensitiveQueryParams(names ...string) LoggingOption {
	return func(t *LoggingTransport) {
		for _, n := range names {
			t.params[strings.ToLower(n)] = struct{}{}
		}
	}
}

// RoundTrip implements http.RoundTripper.
func (t *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	snap := t.requestSnapshot(req)
	t.hook.LogRequest(snap)

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		t.hook.LogResponse(snap, nil, duration, t.redactError(err, req.URL))
		return nil, err
	}
	t.hook.LogResponse(snap, t.responseSnapshot(resp, snap), duration, nil)
	return resp, nil
}

func (t *LoggingTransport) requestSnapshot(req *http.Request) *http.Request {
	snap := &http.Request{
		Method:           req.Method,
		URL:              t.redactURL(req.URL),
		Proto:            req.Proto,
		ProtoMajor:       req.ProtoMajor,
		ProtoMinor:       req.ProtoMinor,
		Header:           t.redactHeader(req.Header),
		Body:             http.NoBody,
		ContentLength:    req.ContentLength,
		TransferEncoding: append([]string(nil), req.TransferEncoding...),
		Host:             req.Host,
	}
	return snap.WithContext(req.Context())
}

func (t *LoggingTransport) responseSnapshot(resp *http.Response, req *http.Request) *http.Response {
	return &http.Response{
		Status:           resp.Status,
		StatusCode:       resp.StatusCode,
		Proto:            resp.Proto,
		ProtoMajor:       resp.ProtoMajor,
		ProtoMinor:       resp.ProtoMinor,
		Header:           t.redactHeader(resp.Header),
		Body:             http.NoBody,
		ContentLength:    resp.ContentLength,
		TransferEncoding: append([]string(nil), resp.TransferEncoding...),
		Uncompressed:     resp.Uncompressed,
		Request:          req,
	}
}

func (t *LoggingTransport) redactHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, v := range h {
		ck := http.CanonicalHeaderKey(k)
		if _, sensitive := t.headers[ck]; sensitive {
			out[ck] = []string{Redacted}
			continue
		}
		if urlValuedHeaders[ck] {
			out[ck] = make([]string, len(v))
			for i, raw := range v {
				out[ck][i] = t.redactURLString(raw)
			}
			continue
		}
		out[ck] = append([]string(nil), v...)
	}
	return out
}

// urlValuedHeaders hold a URL whose query and userinfo carry credentials
// as readily as the request URL's: an OAuth callback's code in Location, a
// previous page's token in Referer.
var urlValuedHeaders = map[string]bool{"Referer": true, "Location": true, "Content-Location": true}

func (t *LoggingTransport) redactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Redacted
	}
	return t.redactURL(u).String()
}

// redactError returns err for a hook: its text with every URL it names —
// the request URL, and any *url.Error's — in redacted form. The caller keeps
// the original error; the hook gets only text, so unwrapping or type assertions
// cannot recover credential-bearing fields from the original cause.
func (t *LoggingTransport) redactError(err error, reqURL *url.URL) error {
	msg := err.Error()
	replacements := make(map[string]string)
	replace := func(raw string) {
		if raw == "" {
			return
		}
		safe := t.redactURLString(raw)
		replacements[raw] = safe
		if unquoted, uerr := url.PathUnescape(raw); uerr == nil && unquoted != raw {
			replacements[unquoted] = safe
		}
	}
	// Visit every branch, including relative URLs in a joined error; an
	// errors.As lookup alone sees only the first matching *url.Error.
	pending := []error{err}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if urlErr, ok := current.(*url.Error); ok {
			replace(urlErr.URL)
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			pending = append(pending, joined.Unwrap()...)
		} else if next := errors.Unwrap(current); next != nil {
			pending = append(pending, next)
		}
	}
	if reqURL != nil {
		replace(reqURL.String())
	}
	// Replace longer URLs first in a single pass: one credential-bearing
	// URL can be a prefix of another, especially in joined relative errors.
	raws := make([]string, 0, len(replacements))
	for raw := range replacements {
		raws = append(raws, raw)
	}
	slices.SortFunc(raws, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(raws))
	for _, raw := range raws {
		pairs = append(pairs, raw, replacements[raw])
	}
	if len(pairs) > 0 {
		msg = strings.NewReplacer(pairs...).Replace(msg)
	}
	// Error text can mention several URLs (errors.Join, proxy failures,
	// redirect validation) without exposing each as a *url.Error. Sanitize
	// every HTTP URL in the final text, not just the first error in a chain.
	msg = errorHTTPURL.ReplaceAllStringFunc(msg, t.redactURLString)
	return errors.New(msg)
}

var errorHTTPURL = regexp.MustCompile(`(?i)https?://[^\s<>"\x60]+`)

func (t *LoggingTransport) redactURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	c.User = nil
	c.Fragment, c.RawFragment = "", "" // implicit-flow tokens travel in fragments
	if c.RawQuery == "" {
		return &c
	}
	q, err := url.ParseQuery(c.RawQuery)
	if err != nil {
		// An unparseable query cannot be redacted parameter by parameter.
		c.RawQuery = Redacted
		return &c
	}
	changed := false
	for k := range q {
		if _, sensitive := t.params[strings.ToLower(k)]; sensitive {
			q[k] = []string{Redacted}
			changed = true
		}
	}
	if changed {
		c.RawQuery = q.Encode()
	}
	return &c
}

// StructuredLoggingHook is a LoggingHook that captures structured data.
type StructuredLoggingHook struct {
	LogFunc func(entry LogEntry)
}

// LogEntry represents a structured log entry.
type LogEntry struct {
	Type        string            `json:"type"` // "request" or "response"
	Method      string            `json:"method,omitempty"`
	URL         string            `json:"url,omitempty"`
	StatusCode  int               `json:"status_code,omitempty"`
	Duration    time.Duration     `json:"duration,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// LogRequest implements LoggingHook.
func (h *StructuredLoggingHook) LogRequest(req *http.Request) {
	if h.LogFunc == nil {
		return
	}
	h.LogFunc(LogEntry{
		Type:        "request",
		Method:      req.Method,
		URL:         req.URL.String(),
		Headers:     firstValues(req.Header),
		ContentType: req.Header.Get("Content-Type"),
	})
}

// LogResponse implements LoggingHook.
func (h *StructuredLoggingHook) LogResponse(req *http.Request, resp *http.Response, duration time.Duration, err error) {
	if h.LogFunc == nil {
		return
	}
	entry := LogEntry{
		Type:     "response",
		Method:   req.Method,
		URL:      req.URL.String(),
		Duration: duration,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if resp != nil {
		entry.StatusCode = resp.StatusCode
		entry.Headers = firstValues(resp.Header)
		entry.ContentType = resp.Header.Get("Content-Type")
	}
	h.LogFunc(entry)
}

func firstValues(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *LoggingTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *LoggingTransport) Unwrap() http.RoundTripper { return t.base }
