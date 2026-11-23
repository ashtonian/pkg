//go:build unit

package httpx

import (
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// eligibilityUpstream answers each attempt after latency(r), or fails it
// with err(r), and counts attempts.
type eligibilityUpstream struct {
	latency  func(*http.Request) time.Duration
	err      func(*http.Request) error
	attempts atomic.Int32
}

func (u *eligibilityUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.attempts.Add(1)
	select {
	case <-time.After(u.latency(r)):
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if u.err != nil {
		if err := u.err(r); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
}

// The adaptive delay is trained only by the requests it controls: hedged
// requests that got a response. Ten cheap POSTs, unhedged by default, or
// ten cheap failed GETs leave a later 40ms GET unhedged under the initial
// delay, as on a cold transport; ten cheap successful GETs, or POSTs a
// custom predicate hedges, lower the delay and the same GET is hedged.
func TestAdaptiveHedgingTrainsOnHedgedResponses(t *testing.T) {
	slowGet := func(r *http.Request) time.Duration {
		if r.Method == http.MethodGet && r.Header.Get("X-Warmup") == "" {
			return 40 * time.Millisecond
		}
		return time.Millisecond
	}
	failWarmup := func(r *http.Request) error {
		if r.Header.Get("X-Warmup") != "" {
			return errors.New("refused")
		}
		return nil
	}
	postsHedged := WithHedgeIf(func(r *http.Request) bool { return r.Method == http.MethodGet || r.Method == http.MethodPost })
	for _, tt := range []struct {
		name         string
		opts         []HedgingOption
		warmupMethod string
		warmupBody   func() io.Reader // nil: no body
		failWarmup   bool
		wantAttempts int32 // for the 40ms GET
	}{
		{name: "cold", wantAttempts: 1},
		{name: "after POSTs", warmupMethod: http.MethodPost, warmupBody: func() io.Reader { return strings.NewReader("x") }, wantAttempts: 1},
		{name: "after failed GETs", warmupMethod: http.MethodGet, failWarmup: true, wantAttempts: 1},
		{name: "after GETs", warmupMethod: http.MethodGet, wantAttempts: 2},
		{name: "after POSTs a predicate hedges", opts: []HedgingOption{postsHedged}, warmupMethod: http.MethodPost, warmupBody: func() io.Reader { return strings.NewReader("x") }, wantAttempts: 2},
		{name: "after unreplayable POSTs a predicate selects", opts: []HedgingOption{postsHedged}, warmupMethod: http.MethodPost, warmupBody: func() io.Reader { return io.MultiReader(strings.NewReader("x")) }, wantAttempts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				up := &eligibilityUpstream{latency: slowGet}
				if tt.failWarmup {
					up.err = failWarmup
				}
				rt := must(NewAdaptiveHedgingTransport(up, 0.95, tt.opts...))
				if tt.warmupMethod != "" {
					for range 10 {
						var body io.Reader
						if tt.warmupBody != nil {
							body = tt.warmupBody()
						}
						req, _ := http.NewRequest(tt.warmupMethod, "https://api.example.com/x", body)
						req.Header.Set("X-Warmup", "1")
						if resp, err := rt.RoundTrip(req); err == nil {
							resp.Body.Close()
						}
					}
				}
				synctest.Wait()
				up.attempts.Store(0)
				hedgedGet(t, rt, http.MethodGet)
				synctest.Wait()
				if got := up.attempts.Load(); got != tt.wantAttempts {
					t.Fatalf("the 40ms GET made %d attempts, want %d", got, tt.wantAttempts)
				}
			})
		})
	}
}

// The published delay is the exact nearest-rank percentile of the window
// at each recomputation, which happens every adaptiveRecomputeEvery
// samples; between them it holds.
func TestLatencyWindowPublishesThePercentile(t *testing.T) {
	const size = 100
	w := newLatencyWindow(0.9, size, time.Hour)
	exact := func(samples []time.Duration) time.Duration {
		window := slices.Clone(samples[max(0, len(samples)-size):])
		slices.Sort(window)
		return window[min(int(float64(len(window))*0.9), len(window)-1)]
	}
	var samples []time.Duration
	for n := 1; n <= 1000; n++ {
		d := time.Duration((n*7919)%997) * time.Microsecond // a scrambled sequence
		samples = append(samples, d)
		before := w.delay()
		w.record(d)
		switch {
		case n < adaptiveMinSamples:
			if w.delay() != time.Hour {
				t.Fatalf("after %d samples the delay is %v, want the initial delay", n, w.delay())
			}
		case n <= adaptiveRecomputeEvery || n%adaptiveRecomputeEvery == 0:
			if got, want := w.delay(), exact(samples); got != want {
				t.Fatalf("after %d samples the delay is %v, want %v", n, got, want)
			}
		default:
			if w.delay() != before {
				t.Fatalf("the delay changed between recomputations, at sample %d", n)
			}
		}
	}
}

// Concurrent recording keeps every published delay one of the recorded
// latencies, and never blocks a reader.
func TestLatencyWindowConcurrentRecording(t *testing.T) {
	w := newLatencyWindow(0.95, adaptiveWindow, adaptiveInitialDelay)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 2000 {
				w.record(time.Duration(1+(g*2000+i)%50) * time.Millisecond)
				if d := w.delay(); d != adaptiveInitialDelay && (d < time.Millisecond || d > 50*time.Millisecond || d%time.Millisecond != 0) {
					t.Errorf("published %v, not a recorded latency", d)
					return
				}
			}
		})
	}
	wg.Wait()
}
