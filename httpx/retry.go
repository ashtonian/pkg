package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// DefaultMaxReplayBytes is the largest request body RetryTransport records,
// as the first attempt sends it, so it can re-send it. Requests that set
// GetBody are replayed without recording; a larger body without GetBody, or
// one the first attempt did not read to its end, is not retried.
const DefaultMaxReplayBytes int64 = 1 << 20

// IdempotencyKeyHeader is the request header that permits RetryTransport to
// replay a non-idempotent request. The server must implement deduplication
// for that key; setting it alone does not make a write safe.
const IdempotencyKeyHeader = "Idempotency-Key"

// RetryTransport implements exponential backoff with jitter.
//
// A request is eligible for replay when its
// method is idempotent (RFC 9110 §9.2.2: GET, HEAD, OPTIONS, TRACE, PUT,
// DELETE), it carries an Idempotency-Key header, or the transport was built
// with WithRetryNonIdempotent. The application must ensure the server makes
// replay safe; this transport cannot deduplicate writes. Retried attempts
// clone the request metadata, but still consume and close request bodies.
type RetryTransport struct {
	base           http.RoundTripper
	maxAttempts    int
	initialWait    time.Duration
	maxWait        time.Duration
	multiplier     float64
	jitter         float64
	retryIf        func(*http.Response, error) bool
	hook           MetricsHook
	nonIdempotent  bool
	maxReplayBytes int64
	configErrs     []error
}

// RetryOption configures a RetryTransport.
type RetryOption func(*RetryTransport)

// NewRetryTransport creates a new retry transport.
//
// Defaults: 3 attempts, 100ms initial wait, 10s max wait, 2x multiplier, 10% jitter,
// 1 MiB recording bound for bodies without GetBody. Retries on 502, 503, 504,
// 429, connection reset/refused/aborted, broken pipe, EOF and network timeouts;
// caller cancellation and deadline errors are excluded.
func NewRetryTransport(base http.RoundTripper, opts ...RetryOption) (*RetryTransport, error) {
	if base == nil {
		return nil, invalidConfig("retry: base transport is nil")
	}

	t := &RetryTransport{
		base:           base,
		maxAttempts:    3,
		initialWait:    100 * time.Millisecond,
		maxWait:        10 * time.Second,
		multiplier:     2.0,
		jitter:         0.1,
		maxReplayBytes: DefaultMaxReplayBytes,
	}
	t.retryIf = t.defaultRetryIf

	for _, opt := range opts {
		opt(t)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}

	t.hook = metricsHookOrNoop(t.hook)
	return t, nil
}

func (t *RetryTransport) validate() error {
	var errs []error
	if t.maxAttempts < 1 {
		errs = append(errs, invalidConfig("retry: WithMaxAttempts must be >= 1, got %d", t.maxAttempts))
	}
	if t.initialWait <= 0 {
		errs = append(errs, invalidConfig("retry: WithBackoff initial wait must be > 0, got %s", t.initialWait))
	}
	if t.maxWait < t.initialWait {
		errs = append(errs, invalidConfig("retry: WithBackoff max wait %s is below initial wait %s", t.maxWait, t.initialWait))
	}
	if !(t.multiplier >= 1) || math.IsInf(t.multiplier, 0) {
		errs = append(errs, invalidConfig("retry: WithBackoff multiplier must be a finite value >= 1, got %v", t.multiplier))
	}
	if !(t.jitter >= 0 && t.jitter <= 1) {
		errs = append(errs, invalidConfig("retry: WithJitter must be within [0, 1], got %v", t.jitter))
	}
	if t.retryIf == nil {
		errs = append(errs, invalidConfig("retry: WithRetryIf requires a non-nil function"))
	}
	if t.maxReplayBytes < 0 {
		errs = append(errs, invalidConfig("retry: WithMaxReplayBytes must be >= 0, got %d", t.maxReplayBytes))
	}
	errs = append(errs, t.configErrs...)
	return errors.Join(errs...)
}

// WithMaxAttempts sets the maximum number of attempts.
func WithMaxAttempts(n int) RetryOption {
	return func(t *RetryTransport) { t.maxAttempts = n }
}

// WithBackoff sets the backoff parameters. max also bounds a server's
// Retry-After: a response asking for a longer pause is returned to the
// caller instead of retried.
func WithBackoff(initial, max time.Duration, multiplier float64) RetryOption {
	return func(t *RetryTransport) {
		t.initialWait = initial
		t.maxWait = max
		t.multiplier = multiplier
	}
}

// WithJitter sets the jitter factor (0.0-1.0).
func WithJitter(jitter float64) RetryOption {
	return func(t *RetryTransport) { t.jitter = jitter }
}

// WithRetryOn sets the status codes that trigger a retry.
func WithRetryOn(codes ...int) RetryOption {
	return func(t *RetryTransport) {
		codeSet := make(map[int]bool, len(codes))
		for _, code := range codes {
			if code < 100 || code > 599 {
				t.configErrs = append(t.configErrs, invalidConfig("retry: WithRetryOn status %d is not an HTTP status code", code))
				continue
			}
			codeSet[code] = true
		}
		t.retryIf = func(resp *http.Response, err error) bool {
			if err != nil {
				return t.isRetryableError(err)
			}
			return codeSet[resp.StatusCode]
		}
	}
}

// WithRetryIf sets a custom retry condition.
func WithRetryIf(fn func(*http.Response, error) bool) RetryOption {
	return func(t *RetryTransport) { t.retryIf = fn }
}

// WithRetryNonIdempotent also replays non-idempotent requests (POST, PATCH,
// ...) that carry no Idempotency-Key. Use it only when the server
// deduplicates repeated requests; otherwise an ambiguous failure can execute
// the operation more than once.
func WithRetryNonIdempotent() RetryOption {
	return func(t *RetryTransport) { t.nonIdempotent = true }
}

// WithMaxReplayBytes caps how much of a request body without GetBody is
// recorded, as the first attempt sends it, for replay. A larger body is sent
// once.
func WithMaxReplayBytes(n int64) RetryOption {
	return func(t *RetryTransport) { t.maxReplayBytes = n }
}

// WithRetryHook sets the metrics hook for retry events.
func WithRetryHook(hook MetricsHook) RetryOption {
	return func(t *RetryTransport) { t.hook = hook }
}

// RoundTrip implements http.RoundTripper.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.maxAttempts == 1 || !t.replaySafe(req) {
		return t.base.RoundTrip(req)
	}

	first := req.Clone(req.Context())
	body := newReplayBody(first, t.maxReplayBytes)

	attemptReq := first
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			var err error
			if attemptReq, err = body.next(req); err != nil {
				return nil, fmt.Errorf("httpx: retry: %w", err)
			}
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if !t.retryIf(resp, err) {
			return resp, err
		}
		if ctxErr := req.Context().Err(); ctxErr != nil {
			if resp != nil {
				drainAndClose(resp)
			}
			return nil, ctxErr
		}
		if attempt >= t.maxAttempts {
			return resp, err
		}
		if !body.ready() {
			// The first attempt did not read the whole body within the
			// replay limit — it was larger, or the server answered before
			// reading it — so there is nothing to send again.
			return resp, err
		}
		wait, ok := t.backoff(req.Context(), attempt, resp)
		if !ok {
			// The server asked for a longer pause than this transport or the
			// request deadline allows; hand the response back unretried.
			return resp, err
		}

		t.hook.OnRetry(req, attempt, err)
		if causeHook, ok := t.hook.(RetryCauseHook); ok {
			causeHook.OnRetryCause(req, attempt, classifyRetryCause(resp, err), err)
		}
		if resp != nil {
			drainAndClose(resp)
		}
		if err := sleep(req.Context(), wait); err != nil {
			return nil, err
		}
	}
}

// replaySafe reports whether req may be sent more than once.
func (t *RetryTransport) replaySafe(req *http.Request) bool {
	return t.nonIdempotent || isIdempotent(req.Method) || req.Header.Get(IdempotencyKeyHeader) != ""
}

func (t *RetryTransport) defaultRetryIf(resp *http.Response, err error) bool {
	if err != nil {
		return t.isRetryableError(err)
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, http.StatusTooManyRequests:
		return true
	}
	return false
}

func (t *RetryTransport) isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Typed comparisons: syscall.ECONNRESET / ECONNREFUSED / EPIPE cover the
	// kernel-level connection failures net/http surfaces wrapped, and
	// errors.Is unwraps through net.OpError → os.SyscallError → syscall.Errno.
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// backoff returns how long to wait before the next attempt. It reports
// false when the server's Retry-After exceeds the configured max wait, or
// when the wait would outlast the request's deadline.
func (t *RetryTransport) backoff(ctx context.Context, attempt int, resp *http.Response) (time.Duration, bool) {
	wait := t.exponential(attempt)
	if d, ok := retryAfter(resp); ok {
		if d > t.maxWait {
			return 0, false
		}
		wait = d
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
		return 0, false
	}
	return wait, true
}

func (t *RetryTransport) exponential(attempt int) time.Duration {
	wait := float64(t.initialWait) * math.Pow(t.multiplier, float64(attempt-1))
	if !(wait <= float64(t.maxWait)) { // also true for +Inf and NaN, which no conversion survives
		wait = float64(t.maxWait)
	}
	if t.jitter > 0 {
		jitterRange := wait * t.jitter
		wait = wait - jitterRange + (rand.Float64() * 2 * jitterRange)
	}
	if wait < 0 {
		wait = 0
	}
	if wait >= float64(t.maxWait) {
		// Large durations round up when converted to float64; MaxInt64
		// becomes 1<<63, which cannot be converted back to a Duration.
		// Return the original integer cap before converting the delay.
		return t.maxWait
	}
	return time.Duration(wait)
}

// retryAfter parses a Retry-After header (delay-seconds or HTTP-date). A
// date in the past means no wait; a delay too large for a time.Duration
// saturates rather than overflowing.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	seconds, err := strconv.ParseInt(v, 10, 64)
	switch {
	case err == nil && seconds <= 0:
		return 0, true
	case err == nil && seconds > int64(math.MaxInt64/time.Second),
		errors.Is(err, strconv.ErrRange) && v[0] >= '0' && v[0] <= '9':
		// Valid delay-seconds too large for a Duration: wait as long as
		// a Duration can, which no retry budget allows.
		return time.Duration(math.MaxInt64), true
	case err == nil:
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}

// classifyRetryCause buckets the (resp, err) pair into one of the
// RetryCause labels for hook reporting. Status checks are evaluated
// before err checks so a 502/503/504 with a downstream EOF still
// counts as a status retry, not a network retry.
func classifyRetryCause(resp *http.Response, err error) RetryCause {
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable,
			http.StatusGatewayTimeout, http.StatusTooManyRequests:
			return RetryCauseStatus
		}
	}
	if err == nil {
		return RetryCauseUnknown
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return RetryCauseTimeout
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return RetryCauseNetwork
	}
	return RetryCauseUnknown
}

// isIdempotent reports whether method is idempotent per RFC 9110 §9.2.2.
func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *RetryTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *RetryTransport) Unwrap() http.RoundTripper { return t.base }
