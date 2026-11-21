//go:build unit

package httpx

import (
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// hedgedUpstream answers attempt n, counted from zero across every request,
// after latency(n) with the body "n", or with its context's error once that
// ends. It records when each attempt was launched on the bubble's clock.
type hedgedUpstream struct {
	start   time.Time
	latency func(attempt int) time.Duration
	mu      sync.Mutex
	at      []time.Duration
}

func newHedgedUpstream(latency func(attempt int) time.Duration) *hedgedUpstream {
	return &hedgedUpstream{start: time.Now(), latency: latency}
}

func (u *hedgedUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	n := len(u.at)
	u.at = append(u.at, time.Since(u.start))
	u.mu.Unlock()
	select {
	case <-time.After(u.latency(n)):
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strconv.Itoa(n))), Request: r}, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func (u *hedgedUpstream) launches() []time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.at)
}

// hedgedGet sends method through rt and returns the winning body and how
// long the round trip took.
func hedgedGet(t *testing.T, rt http.RoundTripper, method string) (string, time.Duration) {
	t.Helper()
	req, _ := http.NewRequest(method, "https://api.example.com/x", nil)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body), took
}

// A hedge is launched exactly one delay after each attempt still pending, up
// to the concurrency cap, and the first response wins.
func TestHedgingLaunchesAfterItsDelay(t *testing.T) {
	slowFirst := func(n int) time.Duration {
		if n == 0 {
			return time.Second
		}
		return 0
	}
	allSlow := func(int) time.Duration { return 500 * time.Millisecond }
	tests := []struct {
		name     string
		delay    time.Duration
		opts     []HedgingOption
		method   string
		latency  func(int) time.Duration
		launches []time.Duration
		winner   string
		took     time.Duration
	}{
		{"a fast attempt is not hedged", time.Hour, nil, http.MethodGet,
			func(int) time.Duration { return 10 * time.Millisecond }, []time.Duration{0}, "0", 10 * time.Millisecond},
		{"a slow attempt is hedged and the hedge wins", 50 * time.Millisecond, nil, http.MethodGet,
			slowFirst, []time.Duration{0, 50 * time.Millisecond}, "1", 50 * time.Millisecond},
		{"two attempts by default", 10 * time.Millisecond, nil, http.MethodGet,
			allSlow, []time.Duration{0, 10 * time.Millisecond}, "0", 500 * time.Millisecond},
		{"WithMaxHedgedRequests raises the cap", 10 * time.Millisecond, []HedgingOption{WithMaxHedgedRequests(3)}, http.MethodGet,
			allSlow, []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond}, "0", 500 * time.Millisecond},
		{"POST is not hedged by default", 10 * time.Millisecond, nil, http.MethodPost,
			slowFirst, []time.Duration{0}, "0", time.Second},
		{"WithHedgeIf hedges POST", 10 * time.Millisecond, []HedgingOption{WithHedgeIf(func(r *http.Request) bool { return r.Method == http.MethodPost })}, http.MethodPost,
			slowFirst, []time.Duration{0, 10 * time.Millisecond}, "1", 10 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				up := newHedgedUpstream(tt.latency)
				winner, took := hedgedGet(t, must(NewHedgingTransport(up, tt.delay, tt.opts...)), tt.method)
				synctest.Wait()
				if winner != tt.winner || took != tt.took {
					t.Errorf("attempt %s won after %v, want attempt %s after %v", winner, took, tt.winner, tt.took)
				}
				if got := up.launches(); !slices.Equal(got, tt.launches) {
					t.Errorf("attempts launched at %v, want %v", got, tt.launches)
				}
			})
		})
	}
}

// The adaptive transport hedges after its initial delay until it has ten
// samples, then after the configured percentile of the latencies it saw.
func TestAdaptiveHedgingDelayFollowsObservedLatency(t *testing.T) {
	t.Run("the initial delay before ten samples", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			up := newHedgedUpstream(func(n int) time.Duration {
				if n == 0 {
					return time.Second
				}
				return 0
			})
			_, took := hedgedGet(t, must(NewAdaptiveHedgingTransport(up, 0.95)), http.MethodGet)
			if took != 100*time.Millisecond {
				t.Errorf("hedged after %v, want the initial 100ms", took)
			}
		})
	})
	t.Run("the percentile after ten samples", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			up := newHedgedUpstream(func(n int) time.Duration {
				switch {
				case n < 10:
					return 30 * time.Millisecond // ten quick requests, under the initial delay
				case n == 10:
					return time.Second // the next request's first attempt
				}
				return 0
			})
			rt := must(NewAdaptiveHedgingTransport(up, 0.95))
			for range 10 {
				hedgedGet(t, rt, http.MethodGet)
			}
			winner, took := hedgedGet(t, rt, http.MethodGet)
			synctest.Wait()
			if winner != "11" || took != 30*time.Millisecond {
				t.Errorf("attempt %s won after %v, want the hedge after the observed 30ms", winner, took)
			}
		})
	})
}
