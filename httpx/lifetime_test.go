//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestReleaseWhenBodyDone(t *testing.T) {
	t.Run("EOF releases once", func(t *testing.T) {
		var n atomic.Int32
		resp := releaseWhenBodyDone(&http.Response{Body: io.NopCloser(strings.NewReader("abc"))}, func() { n.Add(1) })
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if n.Load() != 1 {
			t.Fatalf("released %d times", n.Load())
		}
	})
	t.Run("close releases", func(t *testing.T) {
		var n atomic.Int32
		resp := releaseWhenBodyDone(&http.Response{Body: io.NopCloser(strings.NewReader("abc"))}, func() { n.Add(1) })
		if n.Load() != 0 {
			t.Fatal("released before the body was used")
		}
		resp.Body.Close()
		if n.Load() != 1 {
			t.Fatalf("released %d times", n.Load())
		}
	})
	t.Run("no body releases immediately", func(t *testing.T) {
		var n atomic.Int32
		releaseWhenBodyDone(&http.Response{Body: http.NoBody}, func() { n.Add(1) })
		if n.Load() != 1 {
			t.Fatalf("released %d times", n.Load())
		}
	})
	t.Run("upgrade body keeps its writer", func(t *testing.T) {
		rw := &readWriteBody{Reader: strings.NewReader("")}
		resp := releaseWhenBodyDone(&http.Response{StatusCode: http.StatusSwitchingProtocols, Body: rw}, func() {})
		w, ok := resp.Body.(io.Writer)
		if !ok {
			t.Fatal("upgrade body lost its writer")
		}
		if _, err := w.Write([]byte("x")); err != nil || rw.written != "x" {
			t.Fatalf("write did not reach the connection: %v %q", err, rw.written)
		}
	})
}

type readWriteBody struct {
	io.Reader
	written string
}

func (b *readWriteBody) Write(p []byte) (int, error) { b.written += string(p); return len(p), nil }
func (b *readWriteBody) Close() error                { return nil }

// ctxBody fails reads once its request context is cancelled, like a real
// response body whose connection was torn down.
type ctxBody struct {
	ctx    context.Context
	r      io.Reader
	closed chan struct{}
	once   sync.Once
}

func newCtxBody(ctx context.Context, s string) *ctxBody {
	return &ctxBody{ctx: ctx, r: strings.NewReader(s), closed: make(chan struct{})}
}

func (b *ctxBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.r.Read(p)
}

func (b *ctxBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestHedgingWinnerBodyOutlivesRoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: newCtxBody(r.Context(), "payload"), Request: r}, nil
		})
		ht := must(NewHedgingTransport(base, time.Hour)) // no hedge is ever launched

		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
		resp, err := ht.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != "payload" {
			t.Fatalf("winner body unreadable after RoundTrip returned: %q, %v", body, err)
		}
		resp.Body.Close()
	})
}

func TestHedgingClosesLosingResponses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu     sync.Mutex
			bodies []*ctxBody
		)
		base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			time.Sleep(10 * time.Millisecond) // both attempts are in flight together
			b := newCtxBody(context.Background(), "payload")
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
			return &http.Response{StatusCode: 200, Body: b, Request: r}, nil
		})
		ht := must(NewHedgingTransport(base, time.Millisecond))

		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
		resp, err := ht.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		time.Sleep(20 * time.Millisecond) // the losing attempt returns
		synctest.Wait()                   // and is cleaned up

		mu.Lock()
		defer mu.Unlock()
		if len(bodies) != 2 {
			t.Fatalf("expected 2 attempts, got %d", len(bodies))
		}
		for i, b := range bodies {
			select {
			case <-b.closed:
			default:
				t.Errorf("attempt %d body left open", i)
			}
		}
	})
}

func TestHedgingCancelsAttemptsWithTheCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cancelled atomic.Int32
		base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			cancelled.Add(1)
			return nil, r.Context().Err()
		})
		ht := must(NewHedgingTransport(base, time.Millisecond))

		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", nil)
		go func() {
			time.Sleep(5 * time.Millisecond) // after the hedge launched
			cancel()
		}()
		if _, err := ht.RoundTrip(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
		synctest.Wait()
		if cancelled.Load() != 2 {
			t.Fatalf("expected both attempts cancelled, got %d", cancelled.Load())
		}
	})
}

func TestLimiterHoldsSlotUntilBodyDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("stream")), Request: r}, nil
		})
		lt := must(NewLimiterTransport(base, 1, WithAcquireTimeout(time.Second)))

		get := func() (*http.Response, error) {
			req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
			return lt.RoundTrip(req)
		}
		first, err := get()
		if err != nil {
			t.Fatal(err)
		}
		if lt.InFlight() != 1 {
			t.Fatalf("in flight = %d while the body is open", lt.InFlight())
		}
		if _, err := get(); !errors.Is(err, ErrLimiterTimeout) {
			t.Fatalf("second request should wait out the timeout while the first body is open, got %v", err)
		}
		first.Body.Close()
		if lt.InFlight() != 0 {
			t.Fatalf("in flight = %d after close", lt.InFlight())
		}
		second, err := get()
		if err != nil {
			t.Fatal(err)
		}
		second.Body.Close()
	})
}

type idleCounter struct {
	http.RoundTripper
	closes atomic.Int32
}

func (c *idleCounter) CloseIdleConnections() { c.closes.Add(1) }

func TestCloseIdleConnectionsReachesTheBase(t *testing.T) {
	base := &idleCounter{RoundTripper: http.DefaultTransport}
	client := must(NewClient(
		WithTransport(base),
		WithCompression(),
		WithCache(nil),
		WithCircuitBreaker(),
		WithRetry(),
		WithHedging(time.Second),
		WithLimiter(4),
		WithRateLimit(10, 10),
		WithAuth("https://api.example.com", NewBearerTokenProvider("t")),
		WithMetrics(&CountingHook{}),
		WithLogging(nil),
	))
	client.CloseIdleConnections()
	if base.closes.Load() != 1 {
		t.Fatalf("CloseIdleConnections reached the base %d times", base.closes.Load())
	}
}
