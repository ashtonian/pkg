package httpx

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
)

// AuthTransport adds credentials to requests whose origin has a bound
// provider. The connection destination and effective Host authority must
// both match the binding. A request to any other origin — including a redirect
// hop the http.Client follows to another host, port or scheme — is sent without
// credentials, so a credential never leaves the origin it was issued for.
type AuthTransport struct {
	base      http.RoundTripper
	providers map[Origin]AuthProvider
}

// AuthBinding binds a provider to the origin of URL (its scheme, host and
// port; any path is ignored).
type AuthBinding struct {
	URL      string
	Provider AuthProvider
}

// NewAuthTransport creates a transport that applies each binding's provider
// only to requests for that binding's origin. At least one binding is
// required; origins must be distinct.
func NewAuthTransport(base http.RoundTripper, bindings ...AuthBinding) (*AuthTransport, error) {
	if base == nil {
		return nil, invalidConfig("auth: base transport is nil")
	}
	if len(bindings) == 0 {
		return nil, invalidConfig("auth: at least one origin binding is required")
	}
	providers := make(map[Origin]AuthProvider, len(bindings))
	for _, b := range bindings {
		if b.Provider == nil {
			return nil, invalidConfig("auth: provider is nil")
		}
		o, err := ParseOrigin(b.URL)
		if err != nil {
			return nil, invalidConfig("auth: %v", err)
		}
		if _, dup := providers[o]; dup {
			return nil, invalidConfig("auth: origin %s is bound twice", o)
		}
		providers[o] = b.Provider
	}
	return &AuthTransport{base: base, providers: providers}, nil
}

// RoundTrip implements http.RoundTripper.
func (t *AuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	o := OriginOf(req.URL)
	p, ok := t.providers[o]
	if !ok || requestAuthorityOrigin(req) != o {
		return t.base.RoundTrip(req)
	}

	clone := cloneRequest(req)
	if err := p.AddAuth(clone); err != nil {
		closeRequestBody(req)
		return nil, fmt.Errorf("httpx: auth for %s: %w", o, err)
	}
	return t.base.RoundTrip(clone)
}

// requestAuthorityOrigin accounts for virtual-host routing: URL.Host is the
// connection destination, while Request.Host is the authority sent to it.
// Both must match the binding before a provider's credentials are applied.
func requestAuthorityOrigin(req *http.Request) Origin {
	if req.URL == nil {
		return Origin{}
	}
	u := *req.URL
	if req.Host != "" {
		u.Host = req.Host
	}
	return OriginOf(&u)
}

// BearerTokenProvider adds a Bearer token to requests.
type BearerTokenProvider struct {
	mu          sync.RWMutex
	token       string
	RefreshFunc func() (string, error)
}

// NewBearerTokenProvider creates a new bearer token provider.
func NewBearerTokenProvider(token string) *BearerTokenProvider {
	return &BearerTokenProvider{token: token}
}

// NewRefreshableBearerTokenProvider creates a bearer token provider with auto-refresh.
func NewRefreshableBearerTokenProvider(token string, refresh func() (string, error)) *BearerTokenProvider {
	return &BearerTokenProvider{
		token:       token,
		RefreshFunc: refresh,
	}
}

// AddAuth implements AuthProvider.
func (p *BearerTokenProvider) AddAuth(req *http.Request) error {
	p.mu.RLock()
	token := p.token
	p.mu.RUnlock()

	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// SetToken updates the token.
func (p *BearerTokenProvider) SetToken(token string) {
	p.mu.Lock()
	p.token = token
	p.mu.Unlock()
}

// Refresh calls the refresh function and updates the token.
func (p *BearerTokenProvider) Refresh() error {
	if p.RefreshFunc == nil {
		return nil
	}

	token, err := p.RefreshFunc()
	if err != nil {
		return err
	}

	p.SetToken(token)
	return nil
}

// BasicAuthProvider adds HTTP Basic authentication.
type BasicAuthProvider struct {
	Username string
	Password string
}

// NewBasicAuthProvider creates a new basic auth provider.
func NewBasicAuthProvider(username, password string) *BasicAuthProvider {
	return &BasicAuthProvider{
		Username: username,
		Password: password,
	}
}

// AddAuth implements AuthProvider.
func (p *BasicAuthProvider) AddAuth(req *http.Request) error {
	auth := p.Username + ":" + p.Password
	encoded := base64.StdEncoding.EncodeToString([]byte(auth))
	req.Header.Set("Authorization", "Basic "+encoded)
	return nil
}

// APIKeyProvider adds an API key header.
type APIKeyProvider struct {
	Key    string
	Header string // Default: "X-API-Key"
}

// NewAPIKeyProvider creates a new API key provider.
func NewAPIKeyProvider(key string) *APIKeyProvider {
	return &APIKeyProvider{
		Key:    key,
		Header: "X-API-Key",
	}
}

// NewAPIKeyProviderWithHeader creates an API key provider with a custom header.
func NewAPIKeyProviderWithHeader(key, header string) *APIKeyProvider {
	return &APIKeyProvider{
		Key:    key,
		Header: header,
	}
}

// AddAuth implements AuthProvider.
func (p *APIKeyProvider) AddAuth(req *http.Request) error {
	header := p.Header
	if header == "" {
		header = "X-API-Key"
	}
	req.Header.Set(header, p.Key)
	return nil
}

// QueryParamAuthProvider adds authentication as a query parameter.
type QueryParamAuthProvider struct {
	Key   string // Query parameter name
	Value string // Query parameter value
}

// NewQueryParamAuthProvider creates a new query parameter auth provider.
func NewQueryParamAuthProvider(key, value string) *QueryParamAuthProvider {
	return &QueryParamAuthProvider{
		Key:   key,
		Value: value,
	}
}

// AddAuth implements AuthProvider.
func (p *QueryParamAuthProvider) AddAuth(req *http.Request) error {
	q := req.URL.Query()
	q.Set(p.Key, p.Value)
	req.URL.RawQuery = q.Encode()
	return nil
}

// ChainAuthProvider chains multiple auth providers.
type ChainAuthProvider struct {
	Providers []AuthProvider
}

// NewChainAuthProvider creates a new chain of auth providers.
func NewChainAuthProvider(providers ...AuthProvider) *ChainAuthProvider {
	return &ChainAuthProvider{Providers: providers}
}

// AddAuth implements AuthProvider by calling all providers in order.
func (p *ChainAuthProvider) AddAuth(req *http.Request) error {
	for _, provider := range p.Providers {
		if err := provider.AddAuth(req); err != nil {
			return err
		}
	}
	return nil
}

// FuncAuthProvider wraps a function as an AuthProvider.
type FuncAuthProvider func(*http.Request) error

// AddAuth implements AuthProvider.
func (f FuncAuthProvider) AddAuth(req *http.Request) error {
	return f(req)
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *AuthTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *AuthTransport) Unwrap() http.RoundTripper { return t.base }
