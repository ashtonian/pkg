package httpx

import (
	"fmt"
	"net/http"
	"time"
)

// NewClient creates a new http.Client with the configured transport stack.
//
// The stack order is fixed (see [NewClient]'s build order below), independent
// of the order options are passed. Every enabled layer validates its
// configuration; the first invalid layer's error is returned, wrapping
// [ErrInvalidConfig].
//
// Example:
//
//	client, err := httpx.NewClient(
//	    httpx.WithBaseTransport(httpx.WithHTTP2()),
//	    httpx.WithCompression(),
//	    httpx.WithRetry(httpx.WithMaxAttempts(3)),
//	    httpx.WithCircuitBreaker(),
//	)
func NewClient(opts ...Option) (*http.Client, error) {
	cfg := &clientConfig{}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg.build()
}

// clientLayer is one optional decorator in the fixed NewClient stack.
type clientLayer struct {
	name    string
	enabled bool
	wrap    func(http.RoundTripper) (http.RoundTripper, error)
}

func (cfg *clientConfig) build() (*http.Client, error) {
	if cfg.timeoutSet && cfg.timeout < 0 {
		return nil, invalidConfig("WithTimeout must be >= 0, got %s", cfg.timeout)
	}

	transport, err := cfg.baseTransport()
	if err != nil {
		return nil, err
	}

	// Layers are listed innermost to outermost:
	//   base -> compression -> circuit -> limiter -> ratelimit -> retry ->
	//   hedging -> cache -> auth -> metrics -> logging
	//
	// Every network attempt passes the breaker and both admission budgets:
	// the breaker is innermost so it tracks each attempt's outcome, and the
	// limiter and rate limit sit inside retry and hedging so a retried or
	// hedged attempt spends a slot and a token like the first — but outside
	// the breaker, so a local rejection (ErrRateLimited, ErrLimiterTimeout)
	// never counts as an origin failure. Neither error is retried: a retry
	// beyond the budget fails with it, or waits under WithWait.
	// The cache sits outside all of that, so a hit needs no slot or token
	// and is served even while the breaker is open (stale-if-error included),
	// while its refreshes take the protected path below it. Auth is outside
	// the cache so the cache partitions by the credential the request
	// actually carries.
	// A client-level metrics hook also receives the retry and cache layers'
	// events; a WithRetryHook or WithCacheHook passed to that layer is applied
	// after it and takes precedence.
	retryOpts, cacheOpts := cfg.retryOpts, cfg.cacheOpts
	if cfg.useMetrics && cfg.metricsHook != nil {
		retryOpts = append([]RetryOption{WithRetryHook(cfg.metricsHook)}, retryOpts...)
		cacheOpts = append([]CacheOption{WithCacheHook(cfg.metricsHook)}, cacheOpts...)
	}
	layers := []clientLayer{
		{"compression", cfg.useCompression, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewCompressionTransport(rt, cfg.compressionOpts...))
		}},
		{"circuit breaker", cfg.useCircuitBreaker, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewCircuitTransport(rt, cfg.circuitOpts...))
		}},
		{"limiter", cfg.useLimiter, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewLimiterTransport(rt, cfg.limiterMax, cfg.limiterOpts...))
		}},
		{"rate limit", cfg.useRateLimit, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewRateLimitTransport(rt, cfg.rateLimitRPS, cfg.rateLimitBurst, cfg.rateLimitOpts...))
		}},
		{"retry", cfg.useRetry, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewRetryTransport(rt, retryOpts...))
		}},
		{"hedging", cfg.useHedging, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewHedgingTransport(rt, cfg.hedgingDelay, cfg.hedgingOpts...))
		}},
		{"cache", cfg.useCache, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewCacheTransport(rt, cfg.cacheStore, cacheOpts...))
		}},
		{"auth", len(cfg.authBindings) > 0, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewAuthTransport(rt, cfg.authBindings...))
		}},
		{"metrics", cfg.useMetrics, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewMetricsTransport(rt, cfg.metricsHook))
		}},
		{"logging", cfg.useLogging, func(rt http.RoundTripper) (http.RoundTripper, error) {
			return asRoundTripper(NewLoggingTransport(rt, cfg.loggingHook, cfg.loggingOpts...))
		}},
	}
	for _, l := range layers {
		if !l.enabled {
			continue
		}
		if transport, err = l.wrap(transport); err != nil {
			return nil, fmt.Errorf("httpx: %s: %w", l.name, err)
		}
	}

	timeout := cfg.timeout
	if !cfg.timeoutSet {
		timeout = DefaultTimeout
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		Jar:       cfg.jar,
	}, nil
}

func (cfg *clientConfig) baseTransport() (http.RoundTripper, error) {
	if cfg.transportSet {
		if cfg.transport == nil {
			return nil, invalidConfig("WithTransport requires a non-nil transport")
		}
		return cfg.transport, nil
	}
	t, err := NewBaseTransport(cfg.baseOpts...)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// asRoundTripper adapts a typed constructor result to the RoundTripper
// interface without turning a nil *T into a non-nil interface.
func asRoundTripper[T http.RoundTripper](t T, err error) (http.RoundTripper, error) {
	if err != nil {
		return nil, err
	}
	return t, nil
}

// WithTransport sets a custom base transport. The configured base
// transport options (WithBaseTransport) are ignored when this is set.
func WithTransport(t http.RoundTripper) Option {
	return func(cfg *clientConfig) {
		cfg.transport = t
		cfg.transportSet = true
	}
}

// WithBaseTransport configures the base http.Transport.
func WithBaseTransport(opts ...BaseOption) Option {
	return func(cfg *clientConfig) {
		cfg.baseOpts = opts
	}
}

// WithCompression enables automatic compression/decompression.
func WithCompression(opts ...CompressionOption) Option {
	return func(cfg *clientConfig) {
		cfg.useCompression = true
		cfg.compressionOpts = opts
	}
}

// WithCache enables the RFC 9111 private cache (see CacheTransport). A nil
// store uses a default NewMemoryCache.
func WithCache(store CacheStore, opts ...CacheOption) Option {
	return func(cfg *clientConfig) {
		cfg.useCache = true
		cfg.cacheStore = store
		cfg.cacheOpts = opts
	}
}

// WithRetry enables retry with exponential backoff.
func WithRetry(opts ...RetryOption) Option {
	return func(cfg *clientConfig) {
		cfg.useRetry = true
		cfg.retryOpts = opts
	}
}

// WithMetrics enables metrics collection.
func WithMetrics(hook MetricsHook) Option {
	return func(cfg *clientConfig) {
		cfg.useMetrics = true
		cfg.metricsHook = hook
	}
}

// WithLogging enables request/response logging.
func WithLogging(hook LoggingHook, opts ...LoggingOption) Option {
	return func(cfg *clientConfig) {
		cfg.useLogging = true
		cfg.loggingHook = hook
		cfg.loggingOpts = opts
	}
}

// WithLimiter enables concurrency limiting.
func WithLimiter(maxConcurrent int, opts ...LimiterOption) Option {
	return func(cfg *clientConfig) {
		cfg.useLimiter = true
		cfg.limiterMax = maxConcurrent
		cfg.limiterOpts = opts
	}
}

// WithRateLimit enables rate limiting.
func WithRateLimit(rps float64, burst int, opts ...RateLimitOption) Option {
	return func(cfg *clientConfig) {
		cfg.useRateLimit = true
		cfg.rateLimitRPS = rps
		cfg.rateLimitBurst = burst
		cfg.rateLimitOpts = opts
	}
}

// WithCircuitBreaker enables circuit breaker.
func WithCircuitBreaker(opts ...CircuitOption) Option {
	return func(cfg *clientConfig) {
		cfg.useCircuitBreaker = true
		cfg.circuitOpts = opts
	}
}

// WithHedging enables request hedging.
func WithHedging(delay time.Duration, opts ...HedgingOption) Option {
	return func(cfg *clientConfig) {
		cfg.useHedging = true
		cfg.hedgingDelay = delay
		cfg.hedgingOpts = opts
	}
}

// WithAuth binds provider to the origin (scheme, host and port) of
// originURL; any path is ignored. Credentials are added only to requests for
// that origin — never to another host, port or scheme, including redirect
// hops the client follows. Repeat WithAuth to bind further origins.
func WithAuth(originURL string, provider AuthProvider) Option {
	return func(cfg *clientConfig) {
		cfg.authBindings = append(cfg.authBindings, AuthBinding{URL: originURL, Provider: provider})
	}
}

// DefaultTimeout is the overall request deadline applied by NewClient when
// the caller passes no WithTimeout option. Style guide requires explicit
// timeouts on outbound HTTP calls; this default keeps zero-config callers
// safe. Set WithTimeout(0) to opt out (e.g., long-poll endpoints).
const DefaultTimeout = 30 * time.Second

// WithTimeout sets the overall client timeout (applied to the whole
// request including body read). Pass 0 to disable; default is
// [DefaultTimeout].
func WithTimeout(d time.Duration) Option {
	return func(cfg *clientConfig) {
		cfg.timeout = d
		cfg.timeoutSet = true
	}
}

// WithJar sets the cookie jar.
func WithJar(jar http.CookieJar) Option {
	return func(cfg *clientConfig) {
		cfg.jar = jar
	}
}

// TransportStack returns the transport stack for inspection.
func TransportStack(client *http.Client) []http.RoundTripper {
	var stack []http.RoundTripper
	t := client.Transport

	for t != nil {
		stack = append(stack, t)
		t = unwrapTransport(t)
	}

	return stack
}

// unwrapTransport gets the wrapped transport if there is one.
func unwrapTransport(t http.RoundTripper) http.RoundTripper {
	if w, ok := t.(wrappedTransport); ok {
		return w.Unwrap()
	}
	return nil
}
