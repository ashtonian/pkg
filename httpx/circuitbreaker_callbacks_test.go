//go:build unit

package httpx

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A retired breaker's last callback must arrive before any transitions from
// the replacement. Otherwise a synchronous metrics hook can report closed
// while the current breaker is open.
func TestCircuitEvictionCallbacksPrecedeReplacement(t *testing.T) {
	var gauge atomic.Int32
	ct := callbackCircuit(t, func(_ Origin, _, to State) { gauge.Store(int32(to)) })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, originA, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := OriginOf(req.URL)
	if err := send(t, ct, originA); err != nil {
		t.Fatal(err)
	}

	// Pause eviction after the registry removes A, before its callback.
	evicting, release := make(chan struct{}), make(chan struct{})
	original := ct.breakers.onEvict
	ct.breakers.onEvict = func(origin Origin, b *breaker) {
		if origin == o {
			close(evicting)
			<-release
		}
		original(origin, b)
	}
	evicted := circuitAttempt(t.Context(), ct, originB)
	<-evicting
	replaced := circuitAttempt(t.Context(), ct, originA)
	releaseCircuitTransition(t, release, replaced)
	if err := <-evicted; err != nil {
		t.Fatal(err)
	}
	if got, want := State(gauge.Load()), ct.State(o); got != want || want != StateOpen {
		t.Fatalf("last callback = %s, retained breaker = %s; want open", got, want)
	}
}

func TestCircuitResetCallbacksPrecedeReplacement(t *testing.T) {
	var gauge atomic.Int32
	resetting, release := make(chan struct{}), make(chan struct{})
	ct := callbackCircuit(t, func(_ Origin, _, to State) {
		if to == StateClosed {
			close(resetting)
			<-release
		}
		gauge.Store(int32(to))
	})
	if err := send(t, ct, originA); err != nil {
		t.Fatal(err)
	}
	o, err := ParseOrigin(originA)
	if err != nil {
		t.Fatal(err)
	}
	reset := make(chan struct{})
	go func() {
		ct.Reset(o)
		close(reset)
	}()
	<-resetting
	replaced := circuitAttempt(t.Context(), ct, originA)
	releaseCircuitTransition(t, release, replaced)
	<-reset
	if got, want := State(gauge.Load()), ct.State(o); got != want || want != StateOpen {
		t.Fatalf("last callback = %s, retained breaker = %s; want open", got, want)
	}
}

func callbackCircuit(t *testing.T, hook func(Origin, State, State)) *CircuitTransport {
	t.Helper()
	return must(NewCircuitTransport(stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if r.URL.Host == "a.example.com" {
			status = http.StatusServiceUnavailable
		}
		return &http.Response{StatusCode: status, Body: http.NoBody, Request: r}, nil
	}), WithFailureThreshold(1), WithMaxBreakers(1), WithOnStateChange(hook)))
}

func circuitAttempt(ctx context.Context, ct *CircuitTransport, target string) <-chan error {
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			done <- err
			return
		}
		resp, err := ct.RoundTrip(req)
		if resp != nil {
			err = errors.Join(err, resp.Body.Close())
		}
		done <- err
	}()
	return done
}

func releaseCircuitTransition(t *testing.T, release chan<- struct{}, attempt <-chan error) {
	t.Helper()
	// The correct implementation blocks replacement while the old state
	// notification is paused. Give an unsynchronized replacement a chance
	// to complete before releasing it, reproducing the obsolete callback.
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-attempt:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-timer.C:
		close(release)
		if err := <-attempt; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCircuitCallbacksMatchStateAfterConcurrentChurn(t *testing.T) {
	var mu sync.Mutex
	states := make(map[Origin]State)
	ct := must(NewCircuitTransport(stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Request: r}, nil
	}), WithMaxBreakers(2), WithFailureThreshold(1), WithOnStateChange(func(o Origin, _, to State) {
		mu.Lock()
		states[o] = to
		mu.Unlock()
	})))
	origins := []string{originA, originB, originC}
	var wg sync.WaitGroup
	for _, target := range origins {
		wg.Go(func() {
			o, err := ParseOrigin(target)
			if err != nil {
				t.Error(err)
				return
			}
			for range 100 {
				ct.Reset(o)
				resp, err := ct.RoundTrip(must(http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)))
				if resp != nil {
					if closeErr := resp.Body.Close(); closeErr != nil {
						t.Error(closeErr)
					}
				}
				if err != nil && !errors.Is(err, ErrCircuitOpen) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	for _, target := range origins {
		o := must(ParseOrigin(target))
		if got, want := states[o], ct.State(o); got != want {
			t.Errorf("%s: last callback = %s, retained breaker = %s", o, got, want)
		}
	}
}
