package httpx

import (
	"context"
	"errors"
	"sync"
	"time"
)

// TokenSource supplies credentials and refreshes a rejected token. Implementations
// must be safe for concurrent callers, honor context cancellation and coalesce
// refreshes of the same stale token. Token may return an empty token to defer
// acquisition until rejection; Refresh must return a nonempty token on success.
// Expiry may be zero when unknown.
type TokenSource interface {
	Token(context.Context) (string, time.Time, error)
	Refresh(context.Context, string) (string, time.Time, error)
}

// RefreshFunc obtains a token. A source never runs it concurrently. A nonempty
// token returned with an error is retained, but the error still reaches waiters;
// this supports a rotated credential whose persistence failed.
type RefreshFunc func(context.Context) (string, time.Time, error)

// Token refresh defaults.
const (
	DefaultRefreshMargin  = 30 * time.Second
	DefaultRefreshTimeout = 30 * time.Second
)

// ErrTokenSourceClosed reports a Token or Refresh call after Close.
var ErrTokenSourceClosed = errors.New("httpx: token source is closed")

// RefreshingTokenSource shares refresh work across callers. Each caller can
// cancel its own wait. The refresh retains the first waiter's context values,
// has its own timeout, and is canceled when every waiter leaves or Close runs.
type RefreshingTokenSource struct {
	mu                sync.Mutex
	token             string
	expiry, refreshAt time.Time
	refresh           RefreshFunc
	margin, timeout   time.Duration
	now               func() time.Time
	onRefresh         func(context.Context, error)
	flight            *tokenFlight
	closed            bool
	deferAcquisition  bool
}

type tokenFlight struct {
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int
	abandoned bool
	token     string
	expiry    time.Time
	err       error
}

// TokenOption configures a RefreshingTokenSource.
type TokenOption func(*RefreshingTokenSource)

// WithDeferredAcquisition permits an empty initial token until Refresh is called.
// Token joins an active refresh, but otherwise returns empty without acquiring.
// Once a token is installed, its normal expiry and refresh rules apply.
func WithDeferredAcquisition() TokenOption {
	return func(s *RefreshingTokenSource) { s.deferAcquisition = true }
}

// WithRefreshMargin sets the proactive margin, capped at 10% of the remaining
// token lifetime when installed. Zero refreshes only at expiry or after a 401.
func WithRefreshMargin(d time.Duration) TokenOption {
	return func(s *RefreshingTokenSource) { s.margin = d }
}

// WithRefreshTimeout bounds a shared refresh independently of individual waits.
func WithRefreshTimeout(d time.Duration) TokenOption {
	return func(s *RefreshingTokenSource) { s.timeout = d }
}

// WithTokenClock sets the clock used to evaluate token expiry.
func WithTokenClock(now func() time.Time) TokenOption {
	return func(s *RefreshingTokenSource) { s.now = now }
}

// WithOnRefresh observes each completed callback before waiters resume. fn must
// be quick and must not call this source. The error text is redacted; errors.Is/As retain its cause.
func WithOnRefresh(fn func(context.Context, error)) TokenOption {
	return func(s *RefreshingTokenSource) { s.onRefresh = fn }
}

// NewTokenSource owns a serialized refresh callback and an optional initial
// token. An empty token triggers acquisition on the first Token call unless
// WithDeferredAcquisition is set.
func NewTokenSource(token string, expiry time.Time, refresh RefreshFunc, opts ...TokenOption) (*RefreshingTokenSource, error) {
	s := &RefreshingTokenSource{refresh: refresh, margin: DefaultRefreshMargin, timeout: DefaultRefreshTimeout, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	if refresh == nil || s.now == nil || s.margin < 0 || s.timeout <= 0 {
		return nil, invalidConfig("token source: refresh and clock are required, margin must be nonnegative and timeout positive")
	}
	s.install(token, expiry)
	return s, nil
}

func (s *RefreshingTokenSource) install(token string, expiry time.Time) {
	s.token, s.expiry = token, expiry
	s.refreshAt = expiry
	if !expiry.IsZero() {
		margin := min(s.margin, max(expiry.Sub(s.now()), 0)/10)
		s.refreshAt = expiry.Add(-margin)
	}
}

// Token returns a usable token, refreshing before expiry when necessary.
func (s *RefreshingTokenSource) Token(ctx context.Context) (string, time.Time, error) {
	return s.get(ctx, "", false)
}

// Refresh replaces stale after rejection, or returns a newer usable token
// already installed by another caller.
func (s *RefreshingTokenSource) Refresh(ctx context.Context, stale string) (string, time.Time, error) {
	return s.get(ctx, stale, true)
}

func (s *RefreshingTokenSource) get(ctx context.Context, stale string, force bool) (string, time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", time.Time{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return "", time.Time{}, ErrTokenSourceClosed
		}
		if s.flight == nil && s.token == "" && s.deferAcquisition && !force {
			s.mu.Unlock()
			return "", time.Time{}, nil
		}
		if s.flight == nil && s.token != "" && (!force || s.token != stale) && (s.expiry.IsZero() || s.now().Before(s.refreshAt)) {
			token, expiry := s.token, s.expiry
			s.mu.Unlock()
			return token, expiry, nil
		}
		f := s.flight
		if f != nil && f.abandoned {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-f.done:
				continue
			}
		}
		if f == nil {
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
			f = &tokenFlight{done: make(chan struct{}), cancel: cancel}
			s.flight = f
			go s.run(refreshCtx, f)
		}
		f.waiters++
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			f.waiters--
			if f.waiters == 0 && s.flight == f {
				f.abandoned = true
				f.cancel()
			}
			s.mu.Unlock()
			return "", time.Time{}, ctx.Err()
		case <-f.done:
			if err := ctx.Err(); err != nil {
				return "", time.Time{}, err
			}
			return f.token, f.expiry, f.err
		}
	}
}

func (s *RefreshingTokenSource) run(ctx context.Context, f *tokenFlight) {
	defer f.cancel()
	token, expiry, err := s.refresh(ctx)
	if err == nil && token == "" {
		err = errors.New("empty access token")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		err = &tokenRefreshError{cause: err}
	}
	if s.onRefresh != nil {
		s.onRefresh(ctx, err)
	}
	s.mu.Lock()
	if token != "" && !s.closed {
		s.install(token, expiry)
	}
	f.token, f.expiry, f.err = token, expiry, err
	s.flight = nil
	close(f.done)
	s.mu.Unlock()
}

// Close cancels refresh and waits for its cooperative callback to finish.
// A transport borrows its sources; the owner closes each shared source once.
func (s *RefreshingTokenSource) Close() error {
	s.mu.Lock()
	s.closed = true
	f := s.flight
	if f != nil {
		f.cancel()
	}
	s.mu.Unlock()
	if f != nil {
		<-f.done
	}
	return nil
}

type tokenRefreshError struct{ cause error }

func (e *tokenRefreshError) Error() string        { return ErrAuthRefresh.Error() }
func (e *tokenRefreshError) Unwrap() error        { return e.cause }
func (e *tokenRefreshError) Is(target error) bool { return target == ErrAuthRefresh }
