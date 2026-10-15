package httpx

import (
	"net/http"
	"time"
)

// Encoding constants for compression.
const (
	EncodingBrotli   = "br"
	EncodingGzip     = "gzip"
	EncodingDeflate  = "deflate"
	EncodingZstd     = "zstd"
	EncodingIdentity = "identity"
)

// ContentType constants for codecs.
const (
	ContentTypeJSON        = "application/json"
	ContentTypeXML         = "application/xml"
	ContentTypeMsgpack     = "application/msgpack"
	ContentTypeProtobuf    = "application/protobuf"
	ContentTypeFormURL     = "application/x-www-form-urlencoded"
	ContentTypeOctetStream = "application/octet-stream"
)

// Option configures a client.
type Option func(*clientConfig)

// clientConfig holds configuration for building an http.Client.
type clientConfig struct {
	transport    http.RoundTripper
	transportSet bool // true when WithTransport() was applied
	timeout      time.Duration
	timeoutSet   bool // true when WithTimeout() was applied; zero means "default"
	jar          http.CookieJar

	// Transport stack configuration
	baseOpts        []BaseOption
	compressionOpts []CompressionOption
	cacheStore      CacheStore
	cacheOpts       []CacheOption
	retryOpts       []RetryOption
	metricsHook     MetricsHook
	loggingHook     LoggingHook
	loggingOpts     []LoggingOption
	limiterMax      int
	limiterOpts     []LimiterOption
	rateLimitRPS    float64
	rateLimitBurst  int
	rateLimitOpts   []RateLimitOption
	circuitOpts     []CircuitOption
	hedgingDelay    time.Duration
	hedgingOpts     []HedgingOption
	authBindings    []AuthBinding

	// Feature flags
	useCompression    bool
	useCache          bool
	useRetry          bool
	useMetrics        bool
	useLogging        bool
	useLimiter        bool
	useRateLimit      bool
	useCircuitBreaker bool
	useHedging        bool
}

// Codec handles encoding/decoding for a content type.
type Codec interface {
	ContentType() string
	Encode(v any) ([]byte, error)
	Decode(data []byte, v any) error
}

// MetricsHook receives transport events for observability.
type MetricsHook interface {
	OnRequest(req *http.Request)
	OnResponse(req *http.Request, resp *http.Response, duration time.Duration, err error)
	OnRetry(req *http.Request, attempt int, err error)
	OnCacheHit(req *http.Request, fresh bool)
	OnCacheMiss(req *http.Request)
}

// RetryCause classifies why a retry was attempted. Used as a label on
// labelled retry counters so an on-call engineer can distinguish
// network failures from server-side 5xx pressure from rate-limit
// throttling.
type RetryCause string

// RetryCause values.
const (
	// RetryCauseStatus — server returned a retryable HTTP status
	// (502/503/504/429).
	RetryCauseStatus RetryCause = "status"
	// RetryCauseNetwork — network-level error (connection reset,
	// connection refused, broken pipe, EOF).
	RetryCauseNetwork RetryCause = "network"
	// RetryCauseTimeout — net.Error.Timeout() was true.
	RetryCauseTimeout RetryCause = "timeout"
	// RetryCauseUnknown — retryIf returned true but the cause didn't
	// match any of the above. Caller's custom retry condition.
	RetryCauseUnknown RetryCause = "unknown"
)

// RetryCauseHook is an OPTIONAL extension to MetricsHook. RetryTransport
// type-asserts the hook and calls OnRetryCause in addition to OnRetry
// when the assertion succeeds. Implementations that don't care about
// per-cause labelling don't need to implement it.
//
// Splitting the cause from the legacy OnRetry signature avoids breaking
// every existing MetricsHook implementation while still letting modern
// observability stacks emit labelled counters.
type RetryCauseHook interface {
	OnRetryCause(req *http.Request, attempt int, cause RetryCause, err error)
}

// LoggingHook receives logging events from a LoggingTransport. Both methods
// receive redacted snapshots without bodies (see LoggingTransport); resp is
// nil and err non-nil when the request failed.
type LoggingHook interface {
	LogRequest(req *http.Request)
	LogResponse(req *http.Request, resp *http.Response, duration time.Duration, err error)
}

// CacheStore is the storage behind CacheTransport. Implementations must be
// safe for concurrent use and must treat stored values as immutable. ttl > 0
// bounds how long an entry may be returned; ttl <= 0 means no expiry.
type CacheStore interface {
	Get(key string) (*CachedResponse, bool)
	Set(key string, value *CachedResponse, ttl time.Duration)
	Delete(key string)
}

// CachedResponse is a response held by CacheTransport. Freshness is derived
// from Header (Cache-Control, Expires, Date, Age) and the request and
// response times (RFC 9111 §4.2).
type CachedResponse struct {
	StatusCode   int
	Header       http.Header
	Body         []byte
	RequestTime  time.Time   // when the request that produced it was sent
	ResponseTime time.Time   // when it was received
	VaryHeaders  http.Header // request header values named by the response's Vary
}

// AuthProvider adds authentication to requests.
type AuthProvider interface {
	AddAuth(req *http.Request) error
}

// wrappedTransport is implemented by transports that wrap another transport.
type wrappedTransport interface {
	Unwrap() http.RoundTripper
}

// noopMetricsHook is internal - pass nil to transports instead.
type noopMetricsHook struct{}

func (noopMetricsHook) OnRequest(*http.Request)                                        {}
func (noopMetricsHook) OnResponse(*http.Request, *http.Response, time.Duration, error) {}
func (noopMetricsHook) OnRetry(*http.Request, int, error)                              {}
func (noopMetricsHook) OnCacheHit(*http.Request, bool)                                 {}
func (noopMetricsHook) OnCacheMiss(*http.Request)                                      {}

// noopLoggingHook is internal - pass nil to transports instead.
type noopLoggingHook struct{}

func (noopLoggingHook) LogRequest(*http.Request)                                        {}
func (noopLoggingHook) LogResponse(*http.Request, *http.Response, time.Duration, error) {}

// metricsHookOrNoop returns the hook or a noop if nil.
func metricsHookOrNoop(h MetricsHook) MetricsHook {
	if h == nil {
		return noopMetricsHook{}
	}
	return h
}

// loggingHookOrNoop returns the hook or a noop if nil.
func loggingHookOrNoop(h LoggingHook) LoggingHook {
	if h == nil {
		return noopLoggingHook{}
	}
	return h
}
