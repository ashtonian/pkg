package httpx

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"
)

// State represents the circuit breaker state.
type State int

const (
	// StateClosed is the normal operating state.
	StateClosed State = iota
	// StateOpen is when the circuit is tripped and failing fast.
	StateOpen
	// StateHalfOpen is when the circuit is testing if the backend is healthy.
	StateHalfOpen
)

// String returns the string representation of the state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

func stateOf(s gobreaker.State) State {
	switch s {
	case gobreaker.StateOpen:
		return StateOpen
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	default:
		return StateClosed
	}
}

// Circuit breaker defaults.
const (
	DefaultFailureThreshold = 5
	DefaultSuccessThreshold = 2
	DefaultOpenTimeout      = 30 * time.Second
)

// Outcomes reported to a breaker. A nil outcome is a success.
var (
	errBreakerFailure  = errors.New("httpx: circuit breaker failure")
	errBreakerExcluded = errors.New("httpx: circuit breaker excluded")
)

type breaker = gobreaker.TwoStepCircuitBreaker[struct{}]

// CircuitTransport fails fast when an origin (scheme, host and port) is
// unhealthy. Each origin has its own breaker: consecutive failures open it,
// after the open timeout it admits up to the success threshold of probe
// requests, and that many consecutive successes close it again; any probe
// failure reopens it. A request cancelled by its caller counts as neither
// success nor failure, and an outcome that completes after its breaker
// changed state is ignored. A rejected request fails with ErrCircuitOpen,
// both while open and beyond the probe count while half-open. At most
// DefaultMaxOrigins breakers are kept
// (WithMaxBreakers); the least recently used origin is dropped beyond that
// and starts closed on its next request.
type CircuitTransport struct {
	base             http.RoundTripper
	failureThreshold int
	successThreshold int
	openTimeout      time.Duration
	maxBreakers      int
	isFailure        func(*http.Response, error) bool
	onStateChange    func(origin Origin, from, to State)

	// Serialize state operations and their synchronous callbacks. The
	// registry and individual breakers also lock internally, but alone
	// cannot keep an eviction/reset notification ahead of a replacement
	// breaker's transitions. Network requests never hold this lock.
	stateMu  sync.Mutex
	breakers *originStates[*breaker]
}

// CircuitOption configures a CircuitTransport.
type CircuitOption func(*CircuitTransport)

// NewCircuitTransport creates a per-origin circuit breaker transport.
// Defaults: DefaultFailureThreshold, DefaultSuccessThreshold,
// DefaultOpenTimeout, DefaultMaxOrigins breakers; any error, nil response or
// 5xx from the wrapped transport is a failure, except caller cancellation.
// Deadline errors count as failures. Put local admission layers outside the
// breaker to keep their refusals out of these outcomes.
func NewCircuitTransport(base http.RoundTripper, opts ...CircuitOption) (*CircuitTransport, error) {
	if base == nil {
		return nil, invalidConfig("circuit breaker: base transport is nil")
	}
	t := &CircuitTransport{
		base:             base,
		failureThreshold: DefaultFailureThreshold,
		successThreshold: DefaultSuccessThreshold,
		openTimeout:      DefaultOpenTimeout,
		maxBreakers:      DefaultMaxOrigins,
		isFailure:        serverFailure,
	}
	for _, opt := range opts {
		opt(t)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	t.breakers = newOriginStates(t.maxBreakers, t.newBreaker, t.evicted)
	return t, nil
}

func (t *CircuitTransport) validate() error {
	var errs []error
	if t.failureThreshold < 1 || uint64(t.failureThreshold) > math.MaxUint32 {
		errs = append(errs, invalidConfig("circuit breaker: WithFailureThreshold must be within [1, %d], got %d", uint64(math.MaxUint32), t.failureThreshold))
	}
	if t.successThreshold < 1 || uint64(t.successThreshold) > math.MaxUint32 {
		errs = append(errs, invalidConfig("circuit breaker: WithSuccessThreshold must be within [1, %d], got %d", uint64(math.MaxUint32), t.successThreshold))
	}
	if t.openTimeout <= 0 {
		errs = append(errs, invalidConfig("circuit breaker: WithOpenTimeout must be > 0, got %s", t.openTimeout))
	}
	if t.maxBreakers < 1 {
		errs = append(errs, invalidConfig("circuit breaker: WithMaxBreakers must be >= 1, got %d", t.maxBreakers))
	}
	if t.isFailure == nil {
		errs = append(errs, invalidConfig("circuit breaker: WithIsFailure requires a non-nil function"))
	}
	return errors.Join(errs...)
}

func serverFailure(resp *http.Response, err error) bool {
	return err != nil || resp == nil || resp.StatusCode >= 500
}

// WithFailureThreshold sets how many consecutive failures open an origin's
// circuit. n must be within [1, math.MaxUint32].
func WithFailureThreshold(n int) CircuitOption {
	return func(t *CircuitTransport) { t.failureThreshold = n }
}

// WithSuccessThreshold sets how many probes a half-open circuit admits and
// how many consecutive successes close it. n must be within [1, math.MaxUint32].
func WithSuccessThreshold(n int) CircuitOption {
	return func(t *CircuitTransport) { t.successThreshold = n }
}

// WithOpenTimeout sets how long a circuit stays open before admitting
// probes.
func WithOpenTimeout(d time.Duration) CircuitOption {
	return func(t *CircuitTransport) { t.openTimeout = d }
}

// WithMaxBreakers caps how many origins keep a breaker. The least recently
// used origin beyond the cap loses its breaker and starts again closed; a
// request already admitted by the dropped breaker still completes, so the
// cap bounds the origins retained, not the requests in flight.
func WithMaxBreakers(n int) CircuitOption {
	return func(t *CircuitTransport) { t.maxBreakers = n }
}

// WithIsFailure sets how a response or transport error is classified.
func WithIsFailure(fn func(*http.Response, error) bool) CircuitOption {
	return func(t *CircuitTransport) { t.isFailure = fn }
}

// WithOnStateChange sets a callback for an origin's state transitions,
// including Reset and eviction of a breaker that was not closed (both
// report a transition to closed). It runs synchronously, serialized with
// breaker state changes, so it must be quick and must not call back into the
// transport.
func WithOnStateChange(fn func(origin Origin, from, to State)) CircuitOption {
	return func(t *CircuitTransport) { t.onStateChange = fn }
}

func (t *CircuitTransport) newBreaker(o Origin) *breaker {
	var self *breaker
	st := gobreaker.Settings{
		Name:        o.String(),
		MaxRequests: uint32(t.successThreshold),
		Timeout:     t.openTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= uint32(t.failureThreshold)
		},
		IsSuccessful: func(err error) bool { return err == nil },
		IsExcluded:   func(err error) bool { return errors.Is(err, errBreakerExcluded) },
	}
	if fn := t.onStateChange; fn != nil {
		st.OnStateChange = func(_ string, from, to gobreaker.State) {
			// A request admitted before its breaker was evicted or Reset
			// completes against the old breaker; its transitions describe
			// a state the origin no longer has, so they are not reported.
			if current, ok := t.breakers.peek(o); ok && current == self {
				fn(o, stateOf(from), stateOf(to))
			}
		}
	}
	self = gobreaker.NewTwoStepCircuitBreaker[struct{}](st)
	return self
}

// evicted reports a dropped breaker's implicit return to closed.
func (t *CircuitTransport) evicted(o Origin, b *breaker) {
	if t.onStateChange == nil {
		return
	}
	if s := stateOf(b.State()); s != StateClosed {
		t.onStateChange(o, s, StateClosed)
	}
}

// RoundTrip implements http.RoundTripper.
func (t *CircuitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin := OriginOf(req.URL)
	done, err := t.admit(origin)
	if err != nil {
		closeRequestBody(req)
		return nil, fmt.Errorf("%w: %s", ErrCircuitOpen, origin)
	}
	defer func() {
		// A panicking base must still release a half-open probe slot.
		if p := recover(); p != nil {
			t.complete(done, errBreakerFailure)
			panic(p)
		}
	}()
	resp, err := t.base.RoundTrip(req)
	t.complete(done, t.outcome(req, resp, err))
	return resp, err
}

func (t *CircuitTransport) admit(origin Origin) (func(error), error) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.breakers.get(origin).Allow()
}

func (t *CircuitTransport) complete(done func(error), outcome error) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	done(outcome)
}

// outcome classifies a completed request for its breaker. A caller's
// cancellation says nothing about the origin's health; a caller's deadline
// expiring does, and counts as a failure.
func (t *CircuitTransport) outcome(req *http.Request, resp *http.Response, err error) error {
	if err != nil && errors.Is(req.Context().Err(), context.Canceled) {
		return errBreakerExcluded
	}
	if t.isFailure(resp, err) {
		return errBreakerFailure
	}
	return nil
}

// State returns origin's circuit state; an origin with no breaker is
// closed.
func (t *CircuitTransport) State(origin Origin) State {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	b, ok := t.breakers.peek(origin)
	if !ok {
		return StateClosed
	}
	return stateOf(b.State())
}

// Reset gives origin a fresh, closed breaker.
func (t *CircuitTransport) Reset(origin Origin) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	old, ok := t.breakers.replace(origin)
	if ok {
		t.evicted(origin, old)
	}
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *CircuitTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *CircuitTransport) Unwrap() http.RoundTripper { return t.base }
