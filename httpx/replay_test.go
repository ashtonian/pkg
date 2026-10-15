//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// scriptedUpstream answers attempt n with statuses[n-1] (the last status
// repeats) and records every body it receives.
type scriptedUpstream struct {
	mu       sync.Mutex
	statuses []int
	header   http.Header
	bodies   []string
}

func (u *scriptedUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.bodies = append(u.bodies, string(body))
	status := u.statuses[min(len(u.bodies), len(u.statuses))-1]
	return &http.Response{StatusCode: status, Header: u.header.Clone(), Body: http.NoBody, Request: r}, nil
}

func (u *scriptedUpstream) attempts() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

func fastRetry(up http.RoundTripper, opts ...RetryOption) *RetryTransport {
	return must(NewRetryTransport(up, append([]RetryOption{
		WithMaxAttempts(3),
		WithBackoff(time.Millisecond, 10*time.Millisecond, 2),
	}, opts...)...))
}

// opaqueBody hides the reader type so http.NewRequest cannot set GetBody.
func opaqueBody(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

func TestRetryReplaysOnlySafeRequests(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		key      string
		opts     []RetryOption
		attempts int
	}{
		{"POST is sent once", http.MethodPost, "", nil, 1},
		{"PATCH is sent once", http.MethodPatch, "", nil, 1},
		{"POST with Idempotency-Key is retried", http.MethodPost, "k-1", nil, 3},
		{"POST retried when explicitly allowed", http.MethodPost, "", []RetryOption{WithRetryNonIdempotent()}, 3},
		{"GET is retried", http.MethodGet, "", nil, 3},
		{"PUT is retried", http.MethodPut, "", nil, 3},
		{"DELETE is retried", http.MethodDelete, "", nil, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable}}
			rt := fastRetry(up, tt.opts...)
			req, _ := http.NewRequest(tt.method, "https://api.example.com/things", strings.NewReader("payload"))
			if tt.key != "" {
				req.Header.Set(IdempotencyKeyHeader, tt.key)
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := up.attempts(); got != tt.attempts {
				t.Fatalf("attempts = %d, want %d", got, tt.attempts)
			}
			for i, b := range up.bodies {
				if b != "payload" {
					t.Errorf("attempt %d body = %q", i+1, b)
				}
			}
		})
	}
}

func TestRetryBuffersBodiesWithoutGetBody(t *testing.T) {
	up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	rt := fastRetry(up, WithMaxReplayBytes(16))

	req, _ := http.NewRequest(http.MethodPut, "https://api.example.com/x", opaqueBody("small body"))
	if req.GetBody != nil {
		t.Fatal("test needs a request without GetBody")
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if up.attempts() != 2 || up.bodies[0] != "small body" || up.bodies[1] != "small body" {
		t.Fatalf("bodies = %q", up.bodies)
	}
}

func TestRetrySendsOversizedUnreplayableBodyOnce(t *testing.T) {
	up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable}}
	rt := fastRetry(up, WithMaxReplayBytes(4))

	req, _ := http.NewRequest(http.MethodPut, "https://api.example.com/x", opaqueBody("larger than four bytes"))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if up.attempts() != 1 || up.bodies[0] != "larger than four bytes" {
		t.Fatalf("an unreplayable body must be sent once and whole, got %q", up.bodies)
	}
}

func TestRetryDoesNotModifyTheCallersRequest(t *testing.T) {
	up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	rt := fastRetry(up)

	body := strings.NewReader("payload")
	req, _ := http.NewRequest(http.MethodPut, "https://api.example.com/x", body)
	origBody := req.Body
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Body != origBody {
		t.Error("the caller's request body was replaced")
	}
}

func TestRetryHonoursRetryAfterLimits(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		deadline   time.Duration
	}{
		{"longer than max wait", "60", 0},
		{"too large for a duration", "9223372037", 0},
		{"outlasts the request deadline", "1", 50 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := &scriptedUpstream{
				statuses: []int{http.StatusServiceUnavailable},
				header:   http.Header{"Retry-After": {tt.retryAfter}},
			}
			rt := must(NewRetryTransport(up, WithMaxAttempts(3), WithBackoff(time.Millisecond, 2*time.Second, 2)))

			ctx := context.Background()
			if tt.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.deadline)
				defer cancel()
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", nil)
			start := time.Now()
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable || up.attempts() != 1 {
				t.Fatalf("want the 503 back after one attempt, got %d after %d", resp.StatusCode, up.attempts())
			}
			if elapsed := time.Since(start); elapsed > 25*time.Millisecond {
				t.Fatalf("returned after %s; it should not wait", elapsed)
			}
		})
	}
}

// trackedBody records whether it was closed.
type trackedBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *trackedBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func (b *trackedBody) wasClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func TestRejectionsCloseTheRequestBody(t *testing.T) {
	ok := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	failing := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: http.NoBody, Request: r}, nil
	})
	tripped := must(NewCircuitTransport(failing, WithFailureThreshold(1), WithOpenTimeout(time.Hour)))
	if err := send(t, tripped, "https://api.example.com/trip"); err != nil {
		t.Fatal(err)
	}
	limited := must(NewRateLimitTransport(ok, 1, 1))
	if err := send(t, limited, "https://api.example.com/spend"); err != nil { // spend the only token
		t.Fatal(err)
	}

	full := must(NewLimiterTransport(ok, 1, WithAcquireTimeout(time.Millisecond)))
	full.sem <- struct{}{} // occupy the only slot

	failingAuth := must(NewAuthTransport(ok, AuthBinding{URL: "https://api.example.com", Provider: FuncAuthProvider(func(*http.Request) error {
		return errors.New("no token")
	})}))

	for name, rt := range map[string]http.RoundTripper{
		"circuit open":  tripped,
		"rate limited":  limited,
		"limiter full":  full,
		"auth provider": failingAuth,
	} {
		t.Run(name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader("payload")}
			req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/x", body)
			if _, err := rt.RoundTrip(req); err == nil {
				t.Fatal("expected a rejection")
			}
			if !body.wasClosed() {
				t.Error("rejected request body was not closed")
			}
		})
	}
}

// EncodeRequest replaces the whole body — Body, GetBody, ContentLength — so a
// retry and a 307 redirect both carry the new payload, not one the request
// was built with.
func TestEncodeRequestReplaysTheNewPayload(t *testing.T) {
	up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	req, _ := http.NewRequest(http.MethodPut, "https://api.example.com/x", strings.NewReader(`"old"`))
	if err := EncodeRequest(req, JSON, "new"); err != nil {
		t.Fatal(err)
	}
	resp, err := fastRetry(up).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(up.bodies) != 2 || up.bodies[0] != `"new"` || up.bodies[1] != `"new"` {
		t.Fatalf("retry bodies = %q, want the new payload twice", up.bodies)
	}

	var redirected []string
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		redirected = append(redirected, string(b))
		mu.Unlock()
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	req, _ = http.NewRequest(http.MethodPut, origin.URL, strings.NewReader(`"old"`))
	if err := EncodeRequest(req, JSON, "new"); err != nil {
		t.Fatal(err)
	}
	resp, err = origin.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(redirected) != 1 || redirected[0] != `"new"` {
		t.Fatalf("307 replay body = %q, want the new payload", redirected)
	}
}

// A body without GetBody is never read ahead of the transport: a stalled
// stream is cut off by the client's timeout like any request, under retry and
// under hedging alike.
func TestReplayDoesNotReadAheadOfTheTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, hedge := range []bool{false, true} {
			pr, pw := io.Pipe()
			up := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done() // the stream never produces; wait to be cancelled
				_ = r.Body.Close()
				return nil, r.Context().Err()
			})
			opts := []Option{WithTransport(up), WithTimeout(time.Second)}
			if hedge {
				opts = append(opts, WithHedging(time.Hour))
			} else {
				opts = append(opts, WithRetry())
			}
			c := must(NewClient(opts...))
			done := make(chan error, 1)
			go func() {
				req, _ := http.NewRequest(http.MethodGet, "https://example.test", pr)
				_, err := c.Do(req)
				done <- err
			}()
			time.Sleep(2 * time.Second)
			synctest.Wait()
			select {
			case err := <-done:
				if err == nil {
					t.Errorf("hedge=%v: a stalled request succeeded", hedge)
				}
			default:
				t.Errorf("hedge=%v: the request outlived the client timeout reading a stalled body", hedge)
				_ = pw.Close()
				<-done
			}
			_ = pw.Close()
		}
	})
}

// The replay limit is a byte count compared without arithmetic that could
// wrap: the largest limit still records and replays.
func TestRetryRecordsUnderTheLargestLimit(t *testing.T) {
	up := &scriptedUpstream{statuses: []int{http.StatusServiceUnavailable, http.StatusOK}}
	rt := fastRetry(up, WithMaxReplayBytes(math.MaxInt64))
	req, _ := http.NewRequest(http.MethodPut, "https://api.example.com/x", opaqueBody("payload"))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(up.bodies) != 2 || up.bodies[0] != "payload" || up.bodies[1] != "payload" {
		t.Fatalf("bodies = %q", up.bodies)
	}
}
