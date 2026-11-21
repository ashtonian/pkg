//go:build unit

package httpx

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// slowUpstream holds every request for latency and counts how many it holds
// at once.
type slowUpstream struct {
	latency time.Duration
	active  atomic.Int32
	peak    atomic.Int32
}

func (u *slowUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	n := u.active.Add(1)
	defer u.active.Add(-1)
	for old := u.peak.Load(); old < n && !u.peak.CompareAndSwap(old, n); old = u.peak.Load() {
	}
	select {
	case <-time.After(u.latency):
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func limitedGet(ctx context.Context, rt http.RoundTripper) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/x", nil)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func TestLimiterReportsItsSlots(t *testing.T) {
	lt := must(NewLimiterTransport(http.DefaultTransport, 5))
	if lt.Capacity() != 5 || lt.Available() != 5 || lt.InFlight() != 0 {
		t.Fatalf("capacity %d, available %d, in flight %d; want 5, 5, 0", lt.Capacity(), lt.Available(), lt.InFlight())
	}
}

// Ten requests through three slots run three at a time: they finish in four
// rounds of the upstream's latency, and every wait for a slot is reported.
func TestLimiterRunsAtMostItsCapacity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &slowUpstream{latency: 50 * time.Millisecond}
		var blocked atomic.Int32
		lt := must(NewLimiterTransport(up, 3, WithOnBlocked(func() { blocked.Add(1) })))

		start := time.Now()
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				if err := limitedGet(context.Background(), lt); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()

		if p := up.peak.Load(); p != 3 {
			t.Errorf("peak concurrency %d, want 3", p)
		}
		if took := time.Since(start); took != 200*time.Millisecond {
			t.Errorf("ten requests took %v, want four 50ms rounds", took)
		}
		if n := blocked.Load(); n != 7 {
			t.Errorf("%d waits for a slot reported, want 7", n)
		}
	})
}

// A request waiting for a slot gives up exactly at its acquire timeout, or
// when its own context ends first.
func TestLimiterWaitEnds(t *testing.T) {
	tests := []struct {
		name     string
		opts     []LimiterOption
		deadline time.Duration
		want     error
	}{
		{"at the acquire timeout", []LimiterOption{WithAcquireTimeout(50 * time.Millisecond)}, 0, ErrLimiterTimeout},
		{"with the request context", nil, 50 * time.Millisecond, context.DeadlineExceeded},
		{"with the request context before the acquire timeout", []LimiterOption{WithAcquireTimeout(time.Second)}, 50 * time.Millisecond, context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lt := must(NewLimiterTransport(&slowUpstream{latency: time.Hour}, 1, tt.opts...))
				holder, release := context.WithCancel(context.Background())
				held := make(chan error, 1)
				go func() { held <- limitedGet(holder, lt) }()
				synctest.Wait() // the slot is taken

				ctx := context.Background()
				if tt.deadline > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tt.deadline)
					defer cancel()
				}
				start := time.Now()
				if err := limitedGet(ctx, lt); !errors.Is(err, tt.want) {
					t.Errorf("err = %v, want %v", err, tt.want)
				}
				if took := time.Since(start); took != 50*time.Millisecond {
					t.Errorf("gave up after %v, want 50ms", took)
				}
				release()
				<-held
			})
		})
	}
}
