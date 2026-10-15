//go:build unit

package httpx

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Accepted numeric configuration and header values never wrap around: the
// largest budgets keep every byte, the largest backoff stays at its cap, and
// a delay too large for a Duration saturates instead of being ignored.
func TestNumericBoundsSaturate(t *testing.T) {
	t.Run("largest duration cap", func(t *testing.T) {
		// Large durations lose precision as float64; return the exact
		// integer cap instead of a rounded or negative delay.
		for _, limit := range []time.Duration{math.MaxInt64, math.MaxInt64 - 512} {
			rt := must(NewRetryTransport(http.DefaultTransport, WithBackoff(time.Second, limit, math.MaxFloat64), WithJitter(0)))
			if got := rt.exponential(2); got != limit {
				t.Errorf("max wait %d: backoff = %d, want %d", limit, got, limit)
			}
		}
	})
	t.Run("huge finite multiplier", func(t *testing.T) {
		rt := must(NewRetryTransport(http.DefaultTransport, WithBackoff(time.Nanosecond, time.Second, math.MaxFloat64), WithJitter(0.1)))
		for attempt := 2; attempt <= 4; attempt++ {
			if d := rt.exponential(attempt); d <= 0 || d > time.Second {
				t.Errorf("attempt %d backoff = %s, want within (0, 1s]", attempt, d)
			}
		}
	})
	t.Run("Retry-After beyond int64", func(t *testing.T) {
		resp := &http.Response{Header: http.Header{"Retry-After": {"9223372036854775808"}}}
		if d, ok := retryAfter(resp); !ok || d != time.Duration(math.MaxInt64) {
			t.Errorf("retryAfter = (%s, %v), want the longest Duration", d, ok)
		}
	})
	t.Run("largest decode budget", func(t *testing.T) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(`"payload"`))}
		var out string
		if err := DecodeResponse(resp, JSON, &out, WithMaxResponseBytes(math.MaxInt64)); err != nil || out != "payload" {
			t.Errorf("DecodeResponse = (%q, %v)", out, err)
		}
	})
	t.Run("negative buffer pool", func(t *testing.T) {
		if _, err := NewBufferPool(-1); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("NewBufferPool(-1) = %v, want ErrInvalidConfig", err)
		}
	})
}

func TestCircuitRejectsThresholdsThatOverflowItsCounters(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("an int on this architecture cannot exceed a uint32")
	}
	for name, option := range map[string]func(int) CircuitOption{
		"failures":  WithFailureThreshold,
		"successes": WithSuccessThreshold,
	} {
		t.Run(name, func(t *testing.T) {
			maxThreshold := uint64(math.MaxUint32)
			if _, err := NewCircuitTransport(http.DefaultTransport, option(int(maxThreshold))); err != nil {
				t.Fatalf("largest supported threshold: %v", err)
			}
			for _, n := range []uint64{maxThreshold + 1, maxThreshold + 2} {
				if _, err := NewCircuitTransport(http.DefaultTransport, option(int(n))); !errors.Is(err, ErrInvalidConfig) {
					t.Errorf("threshold %d: error = %v, want ErrInvalidConfig", n, err)
				}
			}
		})
	}
}

type itemPtr *named

// A defined pointer type decodes like the pointer it names.
func TestDecodeIntoDefinedPointer(t *testing.T) {
	got, err := decodeInto[itemPtr](JSON, []byte(`{"name":"ok"}`))
	if err != nil || got == nil || got.Name != "ok" {
		t.Fatalf("decodeInto[itemPtr] = (%v, %v)", got, err)
	}
}
