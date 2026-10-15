package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache defaults.
const (
	DefaultMaxCacheBodyBytes        int64 = 1 << 20
	DefaultMaxRetention                   = 24 * time.Hour
	DefaultRevalidateTimeout              = 30 * time.Second
	DefaultMaxConcurrentRevalidates       = 8
)

// cacheStatusName is the cache identifier in Cache-Status (RFC 9211).
const cacheStatusName = "httpx"

// storableStatuses are the final statuses the cache stores: those RFC 9110
// §15.1 lists as heuristically cacheable. 206 is excluded — range requests
// bypass the cache.
var storableStatuses = map[int]bool{
	http.StatusOK: true, http.StatusNonAuthoritativeInfo: true, http.StatusNoContent: true,
	http.StatusMultipleChoices: true, http.StatusMovedPermanently: true, http.StatusPermanentRedirect: true,
	http.StatusNotFound: true, http.StatusMethodNotAllowed: true, http.StatusGone: true,
	http.StatusRequestURITooLong: true, http.StatusNotImplemented: true,
}

// CacheTransport is a private HTTP cache (RFC 9111) for GET requests.
//
// Stored responses are keyed by the effective request target — the URL with
// the request's Host as its authority — and partitioned by caller identity —
// the Authorization and Cookie headers plus any WithCacheIdentityHeaders.
// Callers must configure every additional identity header (including API-key
// headers) and use separate stores for identities hidden in a transport.
// These keys do not establish application authorization or tenant boundaries.
// Within a partition the cache honours Vary, corrected Age, the request directives
// no-store, no-cache, max-age, min-fresh, max-stale and only-if-cached, and
// the response directives no-store, no-cache, must-revalidate, max-age and
// Expires. A response without explicit freshness is stored only when it has
// a validator, and is revalidated on every use. Range, HEAD,
// caller-conditional and body-bearing requests bypass the cache; unsafe
// methods with a response below 400 invalidate the target URI within that
// request's partition, and a fetch or revalidation still in
// flight when they do never stores what it read.
type CacheTransport struct {
	base                 http.RoundTripper
	store                CacheStore
	hook                 MetricsHook
	identityHeaders      []string
	staleIfError         time.Duration
	staleWhileRevalidate time.Duration
	maxBodyBytes         int64
	maxRetention         time.Duration
	revalidateTimeout    time.Duration
	maxRevalidations     int

	// Background revalidation: one per key, at most maxRevalidations at a
	// time, all stopped by Close.
	closeCtx   context.Context
	stop       context.CancelFunc
	slots      chan struct{}
	mu         sync.Mutex
	revalidate map[string]struct{}
	wg         sync.WaitGroup

	// Stripes serialize publication with invalidation. A fetch that predates
	// an invalidation must never publish, even briefly. Sharing a stripe only
	// costs an unrelated key one store; no network or body reads hold a lock.
	stripes [invalidationStripes]cacheStripe
}

const invalidationStripes = 256

type cacheStripe struct {
	mu         sync.Mutex
	generation uint64
}

// CacheOption configures a CacheTransport.
type CacheOption func(*CacheTransport)

// NewCacheTransport creates a cache transport over store; a nil store uses
// a default NewMemoryCache.
func NewCacheTransport(base http.RoundTripper, store CacheStore, opts ...CacheOption) (*CacheTransport, error) {
	if base == nil {
		return nil, invalidConfig("cache: base transport is nil")
	}
	if store == nil {
		mc, err := NewMemoryCache()
		if err != nil {
			return nil, err
		}
		store = mc
	}
	t := &CacheTransport{
		base:              base,
		store:             store,
		maxBodyBytes:      DefaultMaxCacheBodyBytes,
		maxRetention:      DefaultMaxRetention,
		revalidateTimeout: DefaultRevalidateTimeout,
		maxRevalidations:  DefaultMaxConcurrentRevalidates,
		revalidate:        make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	t.hook = metricsHookOrNoop(t.hook)
	t.slots = make(chan struct{}, t.maxRevalidations)
	t.closeCtx, t.stop = context.WithCancel(context.Background())
	return t, nil
}

func (t *CacheTransport) validate() error {
	var errs []error
	if t.staleIfError < 0 {
		errs = append(errs, invalidConfig("cache: WithStaleIfError must be >= 0, got %s", t.staleIfError))
	}
	if t.staleWhileRevalidate < 0 {
		errs = append(errs, invalidConfig("cache: WithStaleWhileRevalidate must be >= 0, got %s", t.staleWhileRevalidate))
	}
	if t.maxBodyBytes < 1 {
		errs = append(errs, invalidConfig("cache: WithMaxCacheBodyBytes must be >= 1, got %d", t.maxBodyBytes))
	}
	if t.maxRetention <= 0 {
		errs = append(errs, invalidConfig("cache: WithMaxRetention must be > 0, got %s", t.maxRetention))
	}
	if t.revalidateTimeout <= 0 {
		errs = append(errs, invalidConfig("cache: WithRevalidation timeout must be > 0, got %s", t.revalidateTimeout))
	}
	if t.maxRevalidations < 1 {
		errs = append(errs, invalidConfig("cache: WithRevalidation concurrency must be >= 1, got %d", t.maxRevalidations))
	}
	for _, h := range t.identityHeaders {
		if h == "" {
			errs = append(errs, invalidConfig("cache: WithCacheIdentityHeaders contains an empty header name"))
		}
	}
	return errors.Join(errs...)
}

// WithStaleIfError lets the cache serve a stale response for up to d when
// revalidation returns an inner transport error (including local admission
// refusal) or 5xx, unless the response says must-revalidate or no-cache.
// Caller cancellation and expired caller deadlines remain errors. A response's
// own stale-if-error directive also grants permission.
func WithStaleIfError(d time.Duration) CacheOption {
	return func(t *CacheTransport) { t.staleIfError = d }
}

// WithStaleWhileRevalidate lets the cache serve a stale response for up to d
// while it revalidates in the background, unless the response says
// must-revalidate or no-cache. A response's own stale-while-revalidate
// directive also grants permission.
func WithStaleWhileRevalidate(d time.Duration) CacheOption {
	return func(t *CacheTransport) { t.staleWhileRevalidate = d }
}

// WithCacheHook sets the metrics hook for hits and misses.
func WithCacheHook(hook MetricsHook) CacheOption {
	return func(t *CacheTransport) { t.hook = hook }
}

// WithCacheIdentityHeaders adds request headers that identify the caller
// (for example X-API-Key) to the partition key, alongside Authorization and
// Cookie. Other identity headers are not detected automatically.
func WithCacheIdentityHeaders(names ...string) CacheOption {
	return func(t *CacheTransport) {
		for _, n := range names {
			t.identityHeaders = append(t.identityHeaders, http.CanonicalHeaderKey(n))
		}
	}
}

// WithMaxCacheBodyBytes caps the size of a stored body; larger responses
// pass through unstored. Default DefaultMaxCacheBodyBytes.
func WithMaxCacheBodyBytes(n int64) CacheOption {
	return func(t *CacheTransport) { t.maxBodyBytes = n }
}

// WithMaxRetention caps how long any response is kept, including responses
// kept only for revalidation. Default DefaultMaxRetention.
func WithMaxRetention(d time.Duration) CacheOption {
	return func(t *CacheTransport) { t.maxRetention = d }
}

// WithRevalidation sets the timeout and the concurrency cap for background
// (stale-while-revalidate) revalidations. Defaults DefaultRevalidateTimeout
// and DefaultMaxConcurrentRevalidates.
func WithRevalidation(timeout time.Duration, concurrency int) CacheOption {
	return func(t *CacheTransport) {
		t.revalidateTimeout = timeout
		t.maxRevalidations = concurrency
	}
}

// Close stops background revalidations and waits for them to finish.
func (t *CacheTransport) Close() error {
	t.mu.Lock()
	t.stop()
	t.mu.Unlock()
	t.wg.Wait()
	return nil
}

// RoundTrip implements http.RoundTripper.
func (t *CacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		closeRequestBody(req)
		return nil, err
	}
	reqCC := parseCacheControl(req.Header)
	if !cacheableRequest(req, reqCC) {
		resp, err := t.base.RoundTrip(req)
		if err == nil && !isSafeMethod(req.Method) && resp.StatusCode < 400 {
			t.invalidate(req, resp)
		}
		return resp, err
	}

	key := t.key(req)
	entry, ok, gen := t.lookup(key)
	if err := req.Context().Err(); err != nil {
		closeRequestBody(req)
		return nil, err
	}
	if ok && !varyMatches(entry, req) {
		ok = false
	}
	if !ok {
		if reqCC.onlyIfCached {
			return gatewayTimeout(req), nil
		}
		t.hook.OnCacheMiss(req)
		return t.fetch(req, key, gen, "fwd=miss")
	}

	respCC := parseCacheControl(entry.Header)
	f := computeFreshness(entry, respCC, time.Now())
	if usableWithoutValidation(f, respCC, reqCC) {
		t.hook.OnCacheHit(req, f.fresh())
		return t.serve(req, entry, f, "hit"), nil
	}
	if reqCC.onlyIfCached {
		return gatewayTimeout(req), nil
	}
	if t.staleAllowed(f, respCC, reqCC, respCC.staleWhileRevalidate, t.staleWhileRevalidate) {
		t.hook.OnCacheHit(req, false)
		t.revalidateInBackground(req, key, gen, entry)
		return t.serve(req, entry, f, "hit; detail=stale-while-revalidate"), nil
	}
	return t.validateSync(req, key, gen, entry, f, respCC, reqCC)
}

// staleAllowed reports whether a stale response may be served under a
// stale-* permission: the larger of the origin's directive and the client's
// option, never when the response or request forbids serving stale.
func (t *CacheTransport) staleAllowed(f freshness, respCC, reqCC cacheDirectives, originWindow, clientWindow time.Duration) bool {
	if respCC.mustRevalidate || respCC.noCache || reqCC.noCache {
		return false
	}
	return f.staleness() <= max(originWindow, clientWindow) && max(originWindow, clientWindow) > 0
}

// validateSync refreshes a stored response the cache may not serve as-is:
// a conditional request when it has validators, otherwise a plain one. A
// transport error or 5xx falls back to the stored response when stale-if-error
// permits, unless the caller's context has ended.
func (t *CacheTransport) validateSync(req *http.Request, key string, gen uint64, entry *CachedResponse, f freshness, respCC, reqCC cacheDirectives) (*http.Response, error) {
	requestTime := time.Now()
	resp, err := t.base.RoundTrip(conditionalRequest(req.Clone(req.Context()), entry))
	if ctxErr := req.Context().Err(); ctxErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, ctxErr
	}
	if err != nil || resp.StatusCode >= 500 {
		if t.staleAllowed(f, respCC, reqCC, respCC.staleIfError, t.staleIfError) {
			if resp != nil {
				drainAndClose(resp)
			}
			t.hook.OnCacheHit(req, false)
			return t.serve(req, entry, f, "hit; detail=stale-if-error"), nil
		}
		return resp, err
	}
	if resp.StatusCode == http.StatusNotModified {
		drainAndClose(resp)
		updated := refreshEntry(entry, resp, req, requestTime)
		t.putRefreshed(key, gen, updated)
		t.hook.OnCacheHit(req, true)
		return t.serve(req, updated, computeFreshness(updated, parseCacheControl(updated.Header), time.Now()), "fwd=stale; fwd-status=304"), nil
	}
	t.hook.OnCacheMiss(req)
	gen = t.retire(key, gen)
	return t.storeResponse(req, key, gen, resp, requestTime, "fwd=stale"), nil
}

// putRefreshed stores an entry a 304 updated — or, when the 304's header
// fields now forbid storing it (no-store, Vary: *), removes the old one: the
// updated response still answers this request, but must not be reused.
func (t *CacheTransport) putRefreshed(key string, gen uint64, updated *CachedResponse) {
	if !storableHeader(updated.StatusCode, updated.Header) {
		t.retire(key, gen)
		return
	}
	t.put(key, gen, updated)
}

// fetch forwards a request the cache cannot answer and stores the response
// when it may.
func (t *CacheTransport) fetch(req *http.Request, key string, gen uint64, status string) (*http.Response, error) {
	requestTime := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	return t.storeResponse(req, key, gen, resp, requestTime, status), nil
}

// storeResponse stores resp when it is storable and returns the response
// to hand the caller, whose body reads exactly as it did upstream.
// requestTime is when the request was sent (RFC 9111 §4.2.3).
func (t *CacheTransport) storeResponse(req *http.Request, key string, gen uint64, resp *http.Response, requestTime time.Time, status string) *http.Response {
	if !storableResponse(resp) {
		return resp
	}

	body, complete, readErr := readUpTo(resp.Body, t.maxBodyBytes)
	switch {
	case readErr != nil:
		// A truncated body must not look like a successful empty one.
		resp.Body = prefixedBody{Reader: io.MultiReader(bytes.NewReader(body), errReader{readErr}), Closer: resp.Body}
		return resp
	case !complete:
		resp.Body = prefixedBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
		return resp
	}
	_ = resp.Body.Close() // fully read; a close error cannot change what was received
	resp.Body = io.NopCloser(bytes.NewReader(body))

	entry := &CachedResponse{
		StatusCode:   resp.StatusCode,
		Header:       resp.Header.Clone(),
		Body:         body,
		RequestTime:  requestTime,
		ResponseTime: time.Now(),
		VaryHeaders:  varyValues(resp.Header, req),
	}
	if t.put(key, gen, entry) {
		resp.Header.Set("Cache-Status", cacheStatusName+"; "+status+"; stored")
	}
	return resp
}

// lookup observes the entry and its generation together, before sending a
// request whose response may be stored.
func (t *CacheTransport) lookup(key string) (*CachedResponse, bool, uint64) {
	s := &t.stripes[stripeOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := t.store.Get(key)
	return entry, ok, s.generation
}

// retire removes a representation that a full validation response has
// superseded. Concurrent validations of that old representation may no
// longer restore it. If an unsafe request already invalidated this fetch,
// its obsolete generation is retained so the result cannot be stored.
func (t *CacheTransport) retire(key string, gen uint64) uint64 {
	s := &t.stripes[stripeOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != gen {
		return gen
	}
	s.generation++
	t.store.Delete(key)
	return s.generation
}

func stripeOf(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key)) // hash.Hash writes never fail
	return int(h.Sum32() % invalidationStripes)
}

// put stores entry for as long as it can be useful, capped by maxRetention,
// unless key was invalidated after gen was noted. The generation check and
// publication share the invalidation lock, so an obsolete result is never
// visible to another lookup. It reports whether the entry was stored.
func (t *CacheTransport) put(key string, gen uint64, entry *CachedResponse) bool {
	cc := parseCacheControl(entry.Header)
	ttl := t.maxRetention
	if !hasValidator(entry.Header) {
		f := computeFreshness(entry, cc, entry.ResponseTime)
		window := max(cc.staleIfError, cc.staleWhileRevalidate, t.staleIfError, t.staleWhileRevalidate, 0)
		ttl = min(ttl, f.lifetime-f.age+window)
	}
	if ttl <= 0 {
		return false
	}
	s := &t.stripes[stripeOf(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != gen {
		return false
	}
	t.store.Set(key, entry, ttl)
	return true
}

// serve builds the response for a stored entry.
func (t *CacheTransport) serve(req *http.Request, entry *CachedResponse, f freshness, status string) *http.Response {
	h := entry.Header.Clone()
	h.Set("Age", strconv.FormatInt(int64(f.age/time.Second), 10))
	h.Set("Cache-Status", cacheStatusName+"; "+status)
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", entry.StatusCode, http.StatusText(entry.StatusCode)),
		StatusCode:    entry.StatusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(entry.Body)),
		ContentLength: int64(len(entry.Body)),
		Request:       req,
	}
}

// revalidateInBackground refreshes a stale entry after it was served —
// conditionally when it has validators. The request is cloned before this
// returns, because the caller may reuse it.
func (t *CacheTransport) revalidateInBackground(req *http.Request, key string, gen uint64, entry *CachedResponse) {
	t.mu.Lock()
	if _, busy := t.revalidate[key]; busy || t.closeCtx.Err() != nil {
		t.mu.Unlock()
		return
	}
	select {
	case t.slots <- struct{}{}:
	default:
		t.mu.Unlock()
		return // at capacity: a later request will revalidate
	}
	t.revalidate[key] = struct{}{}
	t.wg.Add(1)
	t.mu.Unlock()

	// Detached from the caller's cancellation (it has its response), bounded
	// by the revalidation timeout and stopped by Close.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), t.revalidateTimeout)
	stopOnClose := context.AfterFunc(t.closeCtx, cancel)
	clone := conditionalRequest(req.Clone(ctx), entry)

	go func() {
		defer func() {
			stopOnClose()
			cancel()
			t.mu.Lock()
			delete(t.revalidate, key)
			t.mu.Unlock()
			<-t.slots
			t.wg.Done()
		}()
		requestTime := time.Now()
		resp, err := t.base.RoundTrip(clone)
		if err != nil {
			return // the stale entry stays until its retention ends
		}
		if resp.StatusCode == http.StatusNotModified {
			drainAndClose(resp)
			t.putRefreshed(key, gen, refreshEntry(entry, resp, clone, requestTime))
			return
		}
		if resp.StatusCode >= 500 {
			drainAndClose(resp)
			return // a failed refresh does not supersede the stale representation
		}
		gen = t.retire(key, gen)
		drainAndClose(t.storeResponse(clone, key, gen, resp, requestTime, "fwd=stale"))
	}()
}

// invalidate removes stored responses a successful unsafe request made
// stale: the target URI and same-origin Location / Content-Location
// (RFC 9111 §4.4), within the caller's identity partition.
func (t *CacheTransport) invalidate(req *http.Request, resp *http.Response) {
	targets := []*url.URL{req.URL}
	for _, h := range []string{"Location", "Content-Location"} {
		if v := resp.Header.Get(h); v != "" {
			if u, err := req.URL.Parse(v); err == nil && OriginOf(u) == OriginOf(req.URL) {
				targets = append(targets, u)
			}
		}
	}
	for _, u := range targets {
		get := req.Clone(req.Context())
		get.Method = http.MethodGet
		get.URL = u
		key := t.key(get)
		s := &t.stripes[stripeOf(key)]
		s.mu.Lock()
		s.generation++
		t.store.Delete(key)
		s.mu.Unlock()
	}
}

// effectiveHost is the authority a request is sent to: its Host field when
// set, which Go sends instead of the URL's host.
func effectiveHost(req *http.Request) string {
	if req.Host != "" {
		return req.Host
	}
	return req.URL.Host
}

// key is the primary cache key: method, the effective target URI and the
// caller's identity.
func (t *CacheTransport) key(req *http.Request) string {
	var id strings.Builder
	for _, h := range append([]string{"Authorization", "Cookie"}, t.identityHeaders...) {
		for _, v := range headerValuesFold(req.Header, h) {
			id.WriteString(h)
			id.WriteByte(':')
			id.WriteString(v)
			id.WriteByte('\n')
		}
	}
	target := *req.URL
	target.Host = effectiveHost(req)
	key := req.Method + " " + target.String()
	if id.Len() > 0 {
		sum := sha256.Sum256([]byte(id.String()))
		key += " id:" + hex.EncodeToString(sum[:])
	}
	return key
}

// cacheableRequest reports whether the cache may answer req at all.
func cacheableRequest(req *http.Request, cc cacheDirectives) bool {
	if req.Method != http.MethodGet || cc.noStore {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody {
		return false // a GET body has no defined meaning; send it to the origin as is
	}
	for _, h := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
		if len(headerValuesFold(req.Header, h)) != 0 {
			return false // range and caller-conditional requests go to the origin untouched
		}
	}
	return true
}

func storableResponse(resp *http.Response) bool {
	return storableHeader(resp.StatusCode, resp.Header)
}

// storableHeader reports whether a response with this status and header may
// be stored (RFC 9111 §3): a heuristically cacheable status, no no-store, no
// Vary containing *, and a validator or explicit freshness.
func storableHeader(status int, h http.Header) bool {
	if !storableStatuses[status] {
		return false
	}
	cc := parseCacheControl(h)
	if cc.noStore || varyWildcard(h) {
		return false
	}
	if hasValidator(h) || cc.maxAge >= 0 {
		return true
	}
	_, err := http.ParseTime(h.Get("Expires"))
	return err == nil
}

// varyWildcard reports a Vary field naming *, in any position of any line:
// such a response varies on something no request reveals.
func varyWildcard(h http.Header) bool {
	for _, field := range h.Values("Vary") {
		for name := range strings.SplitSeq(field, ",") {
			if strings.TrimSpace(name) == "*" {
				return true
			}
		}
	}
	return false
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// conditionalRequest turns req into a validation request for entry.
func conditionalRequest(req *http.Request, entry *CachedResponse) *http.Request {
	if etag := entry.Header.Get("ETag"); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lm := entry.Header.Get("Last-Modified"); lm != "" {
		req.Header.Set("If-Modified-Since", lm)
	}
	return req
}

// notModifiedExcluded are the header fields a 304 does not update
// (RFC 9111 §3.2).
var notModifiedExcluded = map[string]bool{
	"Content-Length": true, "Content-Encoding": true, "Transfer-Encoding": true, "Content-Range": true,
}

// refreshEntry returns entry updated with a 304's header fields and times;
// requestTime is when the validation request was sent.
func refreshEntry(entry *CachedResponse, notModified *http.Response, req *http.Request, requestTime time.Time) *CachedResponse {
	updated := *entry
	updated.Header = entry.Header.Clone()
	for k, v := range notModified.Header {
		if !notModifiedExcluded[k] {
			updated.Header[k] = append([]string(nil), v...)
		}
	}
	updated.RequestTime = requestTime
	updated.ResponseTime = time.Now()
	updated.VaryHeaders = varyValues(updated.Header, req)
	return &updated
}

// varyValues records the request header values a response varies on.
func varyValues(respHeader http.Header, req *http.Request) http.Header {
	var out http.Header
	for _, field := range respHeader.Values("Vary") {
		for name := range strings.SplitSeq(field, ",") {
			name = http.CanonicalHeaderKey(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if out == nil {
				out = http.Header{}
			}
			out[name] = requestValues(req, name)
		}
	}
	return out
}

// requestValues returns the values of request header name; Host lives in
// its own field, not the header map.
func requestValues(req *http.Request, name string) []string {
	if name == "Host" {
		return []string{effectiveHost(req)}
	}
	return headerValuesFold(req.Header, name)
}

// varyMatches reports whether req selects entry under its stored Vary.
func varyMatches(entry *CachedResponse, req *http.Request) bool {
	for name, stored := range entry.VaryHeaders {
		got := requestValues(req, name)
		if len(got) != len(stored) {
			return false
		}
		for i := range got {
			if strings.TrimSpace(got[i]) != strings.TrimSpace(stored[i]) {
				return false
			}
		}
	}
	return true
}

// gatewayTimeout is the only-if-cached answer when nothing usable is stored
// (RFC 9111 §5.2.1.7).
func gatewayTimeout(req *http.Request) *http.Response {
	h := http.Header{"Cache-Status": {cacheStatusName + "; fwd=miss; detail=only-if-cached"}}
	return &http.Response{
		Status: "504 Gateway Timeout", StatusCode: http.StatusGatewayTimeout,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: http.NoBody, Request: req,
	}
}

// readUpTo reads r to its end when that is at most limit bytes. Otherwise
// complete is false and buf holds what was read — up to limit+1 bytes — for
// the caller to put back in front of the rest. No arithmetic on limit can
// overflow, whatever its value.
func readUpTo(r io.Reader, limit int64) (buf []byte, complete bool, err error) {
	buf, err = io.ReadAll(io.LimitReader(r, limit))
	if err != nil || int64(len(buf)) < limit {
		return buf, err == nil, err
	}
	var one [1]byte
	n, err := io.ReadFull(r, one[:])
	switch {
	case n == 1:
		return append(buf, one[0]), false, nil
	case errors.Is(err, io.EOF):
		return buf, true, nil
	}
	return buf, false, err
}

// errReader returns err after the buffered prefix of a failed body.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// CloseIdleConnections forwards to the wrapped transport.
func (t *CacheTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *CacheTransport) Unwrap() http.RoundTripper { return t.base }
