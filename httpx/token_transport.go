package httpx

import (
	"fmt"
	"net/http"
)

// TokenBinding authorizes a token source for one URL origin and authority.
type TokenBinding struct {
	URL    string
	Source TokenSource
}

// TokenTransport attaches bearer credentials and replays one rejected request
// after a 401. Only replayable bodies are eligible; unlike retries of ambiguous
// failures, an explicit 401 permits replay of any method. Sources are borrowed.
type TokenTransport struct {
	base    http.RoundTripper
	sources map[Origin]TokenSource
}

// NewTokenTransport binds each source to a distinct HTTP(S) origin.
func NewTokenTransport(base http.RoundTripper, bindings ...TokenBinding) (*TokenTransport, error) {
	if base == nil || len(bindings) == 0 {
		return nil, invalidConfig("token auth: base and bindings are required")
	}
	t := &TokenTransport{base: base, sources: make(map[Origin]TokenSource, len(bindings))}
	for _, binding := range bindings {
		origin, err := ParseOrigin(binding.URL)
		if err != nil {
			return nil, invalidConfig("token auth: %v", err)
		}
		if binding.Source == nil {
			return nil, invalidConfig("token auth: source is nil")
		}
		if _, exists := t.sources[origin]; exists {
			return nil, invalidConfig("token auth: duplicate origin %s", origin)
		}
		t.sources[origin] = binding.Source
	}
	return t, nil
}

func (t *TokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin := OriginOf(req.URL)
	source, bound := t.sources[origin]
	if !bound || requestAuthorityOrigin(req) != origin {
		return t.base.RoundTrip(req)
	}
	token, _, err := source.Token(req.Context())
	if err != nil {
		closeRequestBody(req)
		return nil, &tokenRefreshError{cause: err}
	}
	clone := cloneRequest(req)
	clone.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.base.RoundTrip(clone)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || (req.Body != nil && req.Body != http.NoBody && req.GetBody == nil) {
		return resp, err
	}
	// The rejected response cannot answer the replay. Close without draining an
	// untrusted 401 body; cleanup failure does not change authentication outcome.
	DrainAndClose(req.Context(), resp.Body, 0)
	token, _, err = source.Refresh(req.Context(), token)
	if err != nil {
		return nil, &tokenRefreshError{cause: err}
	}
	replay := cloneRequest(req)
	if req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("httpx: token replay body: %w", err)
		}
		replay.Body = body
	}
	replay.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(replay)
}

func (t *TokenTransport) CloseIdleConnections()     { closeIdleConnections(t.base) }
func (t *TokenTransport) Unwrap() http.RoundTripper { return t.base }

// WithTokenAuth attaches credentials before cache lookup and replays a 401 once.
func WithTokenAuth(originURL string, src TokenSource) Option {
	return func(cfg *clientConfig) {
		cfg.tokenBindings = append(cfg.tokenBindings, TokenBinding{URL: originURL, Source: src})
	}
}

// WithTokenAuthT adds token authentication to a custom chain. Place it outside
// caching so the replay uses the new token's identity partition.
func WithTokenAuthT(bindings ...TokenBinding) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewTokenTransport(base, bindings...))
	}
}
