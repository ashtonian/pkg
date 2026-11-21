//go:build unit

package httpx

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// The whole client's timing, on a bubble's clock: what the integration tests
// once checked against a real server by waiting.

// countingOrigin answers every request with status(), a body naming the
// request's number, the current Date and its cache headers.
type countingOrigin struct {
	requests atomic.Int32
	status   func() int
	header   []string // key, value pairs
}

func (o *countingOrigin) RoundTrip(r *http.Request) (*http.Response, error) {
	n := o.requests.Add(1)
	h := http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}}
	for i := 0; i+1 < len(o.header); i += 2 {
		h.Set(o.header[i], o.header[i+1])
	}
	code := http.StatusOK
	if o.status != nil {
		code = o.status()
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader("response-" + strconv.Itoa(int(n)))), Request: r}, nil
}

// clientGet returns the status, Cache-Status and body of a GET through c, or
// its error.
func clientGet(c *http.Client) (int, string, string, error) {
	resp, err := c.Get("https://api.example.com/x")
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Cache-Status"), string(body), err
}

// The breaker opens after its failures, stays open for its timeout, then
// closes again after its successes.
func TestClientCircuitRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failing atomic.Bool
		failing.Store(true)
		up := &countingOrigin{status: func() int {
			if failing.Load() {
				return http.StatusInternalServerError
			}
			return http.StatusOK
		}}
		c := must(NewClient(WithTransport(up), WithCircuitBreaker(
			WithFailureThreshold(2), WithSuccessThreshold(2), WithOpenTimeout(50*time.Millisecond))))

		for range 2 {
			clientGet(c)
		}
		if _, _, _, err := clientGet(c); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("after two failures: err = %v, want ErrCircuitOpen", err)
		}
		time.Sleep(25 * time.Millisecond)
		if _, _, _, err := clientGet(c); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("halfway through the open timeout: err = %v, want ErrCircuitOpen", err)
		}

		failing.Store(false)
		time.Sleep(26 * time.Millisecond) // past the open timeout
		for i := range 3 {
			if code, _, _, err := clientGet(c); err != nil || code != http.StatusOK {
				t.Fatalf("request %d after the open timeout: %d, %v", i, code, err)
			}
		}
		if n := up.requests.Load(); n != 5 {
			t.Errorf("origin saw %d requests, want the 2 failures and 3 after recovery", n)
		}
	})
}

// A stale response within stale-while-revalidate is served at once, while
// one revalidation refreshes the cache behind it.
func TestClientServesStaleWhileRevalidating(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &countingOrigin{header: []string{"Cache-Control", "max-age=1"}}
		c := must(NewClient(WithTransport(up), WithCache(nil, WithStaleWhileRevalidate(10*time.Second))))

		_, _, first, err := clientGet(c)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second) // stale, within the revalidation window

		start := time.Now()
		_, _, stale, err := clientGet(c)
		if err != nil {
			t.Fatal(err)
		}
		if stale != first || time.Since(start) != 0 {
			t.Errorf("got %q after %v, want the stale %q at once", stale, time.Since(start), first)
		}
		synctest.Wait()
		if n := up.requests.Load(); n != 2 {
			t.Fatalf("origin saw %d requests, want one revalidation", n)
		}
		if _, _, fresh, _ := clientGet(c); fresh != "response-2" {
			t.Errorf("after revalidation got %q, want the refreshed response-2", fresh)
		}
	})
}

// A stale response within stale-if-error answers for an origin that fails.
func TestClientServesStaleIfError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failing atomic.Bool
		up := &countingOrigin{
			header: []string{"Cache-Control", "max-age=1"},
			status: func() int {
				if failing.Load() {
					return http.StatusBadGateway
				}
				return http.StatusOK
			},
		}
		c := must(NewClient(WithTransport(up), WithCache(nil, WithStaleIfError(time.Minute))))

		_, _, first, err := clientGet(c)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		failing.Store(true)

		code, cacheStatus, body, err := clientGet(c)
		if err != nil || code != http.StatusOK || body != first {
			t.Fatalf("got %d %q, %v; want the stale 200 %q", code, body, err, first)
		}
		if !strings.HasPrefix(cacheStatus, "httpx; hit") {
			t.Errorf("Cache-Status = %q, want a stale hit", cacheStatus)
		}
	})
}

// The client's retry waits exactly the Retry-After the origin asks for.
func TestClientRetryHonoursRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := newTimedUpstream(retryAfterReply(http.StatusTooManyRequests, "1"), status(http.StatusOK))
		c := must(NewClient(WithTransport(up), WithRetry(WithMaxAttempts(3),
			WithBackoff(time.Millisecond, 2*time.Second, 1)))) // max wait admits Retry-After: 1

		code, _, _, err := clientGet(c)
		if err != nil || code != http.StatusOK {
			t.Fatalf("got %d, %v; want 200 after the retry", code, err)
		}
		if got := up.attemptTimes(); len(got) != 2 || got[1] != time.Second {
			t.Errorf("attempts at %v, want [0s 1s]", got)
		}
	})
}
