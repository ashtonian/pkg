//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// retryReply is one scripted answer from a timedUpstream.
type retryReply struct {
	status int
	header http.Header
}

// timedUpstream answers each attempt with the next scripted reply, the last
// repeating, and records when each attempt arrived on the bubble's clock.
type timedUpstream struct {
	start   time.Time
	replies []retryReply
	mu      sync.Mutex
	at      []time.Duration
}

func newTimedUpstream(replies ...retryReply) *timedUpstream {
	return &timedUpstream{start: time.Now(), replies: replies}
}

func (u *timedUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.at = append(u.at, time.Since(u.start))
	rep := u.replies[min(len(u.at), len(u.replies))-1]
	header := rep.header.Clone()
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: rep.status, Header: header, Body: http.NoBody, Request: r}, nil
}

// attemptTimes is when each attempt arrived, from the upstream's start.
func (u *timedUpstream) attemptTimes() []time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.at)
}

func status(code int) retryReply { return retryReply{status: code} }

func retryAfterReply(code int, value string) retryReply {
	return retryReply{status: code, header: http.Header{"Retry-After": {value}}}
}

func getThrough(t *testing.T, rt http.RoundTripper) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// Each retry waits exactly its backoff: the initial wait, multiplied per
// attempt and capped at the max wait, or what the server's Retry-After asks
// for, as delay-seconds or an HTTP-date.
func TestRetryWaitsItsBackoff(t *testing.T) {
	tests := []struct {
		name     string
		backoff  [2]time.Duration // initial, max
		attempts int
		replies  func() []retryReply
		want     []time.Duration
		status   int
	}{
		{
			name: "doubles from the initial wait", backoff: [2]time.Duration{100 * time.Millisecond, 10 * time.Second}, attempts: 4,
			replies: func() []retryReply { return []retryReply{status(503), status(503), status(503), status(200)} },
			want:    []time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond, 700 * time.Millisecond}, status: 200,
		},
		{
			name: "capped at the max wait", backoff: [2]time.Duration{time.Second, 3 * time.Second}, attempts: 5,
			replies: func() []retryReply { return []retryReply{status(503)} },
			want:    []time.Duration{0, time.Second, 3 * time.Second, 6 * time.Second, 9 * time.Second}, status: 503,
		},
		{
			name: "Retry-After seconds", backoff: [2]time.Duration{time.Millisecond, 10 * time.Second}, attempts: 3,
			replies: func() []retryReply { return []retryReply{retryAfterReply(429, "2"), status(200)} },
			want:    []time.Duration{0, 2 * time.Second}, status: 200,
		},
		{
			name: "Retry-After date", backoff: [2]time.Duration{time.Millisecond, 10 * time.Second}, attempts: 3,
			replies: func() []retryReply {
				return []retryReply{retryAfterReply(503, time.Now().Add(5*time.Second).UTC().Format(http.TimeFormat)), status(200)}
			},
			want: []time.Duration{0, 5 * time.Second}, status: 200,
		},
		{
			name: "Retry-After zero", backoff: [2]time.Duration{time.Second, 10 * time.Second}, attempts: 3,
			replies: func() []retryReply { return []retryReply{retryAfterReply(503, "0"), status(200)} },
			want:    []time.Duration{0, 0}, status: 200,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				up := newTimedUpstream(tt.replies()...)
				rt := must(NewRetryTransport(up, WithMaxAttempts(tt.attempts), WithJitter(0),
					WithBackoff(tt.backoff[0], tt.backoff[1], 2)))
				resp := getThrough(t, rt)
				if resp.StatusCode != tt.status {
					t.Errorf("status = %d, want %d", resp.StatusCode, tt.status)
				}
				if got := up.attemptTimes(); !slices.Equal(got, tt.want) {
					t.Errorf("attempts at %v, want %v", got, tt.want)
				}
			})
		})
	}
}

// Jitter moves each wait within its fraction of the nominal backoff, and
// does move it.
func TestRetryJitterStaysWithinItsRange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const jitter = 0.5
		up := newTimedUpstream(status(503))
		rt := must(NewRetryTransport(up, WithMaxAttempts(8), WithJitter(jitter),
			WithBackoff(100*time.Millisecond, time.Hour, 2)))
		getThrough(t, rt)

		at := up.attemptTimes()
		if len(at) != 8 {
			t.Fatalf("%d attempts, want 8", len(at))
		}
		moved := false
		for i := 1; i < len(at); i++ {
			nominal := 100 * time.Millisecond << (i - 1)
			wait := at[i] - at[i-1]
			lo, hi := time.Duration(float64(nominal)*(1-jitter)), time.Duration(float64(nominal)*(1+jitter))
			if wait < lo || wait > hi {
				t.Errorf("wait %d = %v, want within [%v, %v]", i, wait, lo, hi)
			}
			moved = moved || wait != nominal
		}
		if !moved {
			t.Error("every wait was its nominal backoff: jitter had no effect")
		}
	})
}

// Cancelling the request during a backoff ends the wait at once with the
// context's error, and no further attempt is sent.
func TestRetryBackoffEndsWithTheRequestContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := newTimedUpstream(status(503))
		rt := must(NewRetryTransport(up, WithMaxAttempts(10), WithJitter(0),
			WithBackoff(100*time.Millisecond, time.Second, 2)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(50*time.Millisecond, cancel)

		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", nil)
		if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed != 50*time.Millisecond {
			t.Errorf("returned %v after the start, want the 50ms the cancel came at", elapsed)
		}
		if n := len(up.attemptTimes()); n != 1 {
			t.Errorf("%d attempts, want 1", n)
		}
	})
}

// Which responses are retried: the default statuses, or those WithRetryOn or
// WithRetryIf name; never a client error by default. Each retry is reported
// to the hook, and the last response comes back once attempts run out.
func TestRetryConditions(t *testing.T) {
	retry409 := func(resp *http.Response, err error) bool { return err == nil && resp.StatusCode == http.StatusConflict }
	tests := []struct {
		name     string
		opts     []RetryOption
		replies  []retryReply
		attempts int
		status   int
	}{
		{"success is not retried", nil, []retryReply{status(200)}, 1, 200},
		{"503 is retried", nil, []retryReply{status(503), status(503), status(200)}, 3, 200},
		{"502, 504 and 429 are retried", nil, []retryReply{status(502), status(504), status(429), status(200)}, 4, 200},
		{"400 is not retried", nil, []retryReply{status(400)}, 1, 400},
		{"attempts run out", nil, []retryReply{status(503)}, 4, 503},
		{"WithRetryOn", []RetryOption{WithRetryOn(http.StatusConflict)}, []retryReply{status(409), status(200)}, 2, 200},
		{"WithRetryIf", []RetryOption{WithRetryIf(retry409)}, []retryReply{status(409), status(409), status(200)}, 3, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hook := &CountingHook{}
				up := newTimedUpstream(tt.replies...)
				opts := append([]RetryOption{WithMaxAttempts(4), WithRetryHook(hook)}, tt.opts...)
				resp := getThrough(t, must(NewRetryTransport(up, opts...)))
				if resp.StatusCode != tt.status {
					t.Errorf("status = %d, want %d", resp.StatusCode, tt.status)
				}
				if n := len(up.attemptTimes()); n != tt.attempts {
					t.Errorf("%d attempts, want %d", n, tt.attempts)
				}
				if got := hook.Retries.Load(); got != int64(tt.attempts-1) {
					t.Errorf("hook saw %d retries, want %d", got, tt.attempts-1)
				}
			})
		})
	}
}
