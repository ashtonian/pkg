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

const (
	originA = "https://a.example.com"
	originB = "https://b.example.com"
	originC = "https://c.example.com"
)

// pathOrigin answers each request with the status mapped to its path
// (default 200). A request whose path has a gate blocks until the gate
// closes or its context ends.
type pathOrigin struct {
	mu     sync.Mutex
	status map[string]int
	gates  map[string]chan struct{}
	sent   map[string]int // by host
	panics map[string]bool
}

func newPathOrigin() *pathOrigin {
	return &pathOrigin{status: map[string]int{}, gates: map[string]chan struct{}{}, sent: map[string]int{}, panics: map[string]bool{}}
}

func (p *pathOrigin) respond(path string, status int) {
	p.mu.Lock()
	p.status[path] = status
	p.mu.Unlock()
}

func (p *pathOrigin) gate(path string) (open func()) {
	g := make(chan struct{})
	p.mu.Lock()
	p.gates[path] = g
	p.mu.Unlock()
	return func() { close(g) }
}

func (p *pathOrigin) RoundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.sent[r.URL.Host]++
	status := p.status[r.URL.Path]
	g := p.gates[r.URL.Path]
	boom := p.panics[r.URL.Path]
	p.mu.Unlock()
	if boom {
		panic("origin panicked")
	}
	if g != nil {
		select {
		case <-g:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: http.NoBody, Request: r}, nil
}

func (p *pathOrigin) count(host string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent[host]
}

func send(t *testing.T, rt http.RoundTripper, target string) error {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func sendAsync(ctx context.Context, rt http.RoundTripper, target string) <-chan error {
	out := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		out <- err
	}()
	return out
}

func mustOrigin(s string) Origin { return must(ParseOrigin(s)) }

func TestCircuitIsPerOrigin(t *testing.T) {
	p := newPathOrigin()
	p.respond("/fail", http.StatusInternalServerError)
	ct := must(NewCircuitTransport(p, WithFailureThreshold(2)))

	send(t, ct, originA+"/fail")
	send(t, ct, originA+"/fail")
	if err := send(t, ct, originA+"/ok"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("a should be open, got %v", err)
	}
	if p.count("a.example.com") != 2 {
		t.Errorf("an open circuit must not reach the origin, sent %d", p.count("a.example.com"))
	}
	if err := send(t, ct, originB+"/ok"); err != nil {
		t.Fatalf("b must be unaffected by a, got %v", err)
	}
	if a, b := ct.State(mustOrigin(originA)), ct.State(mustOrigin(originB)); a != StateOpen || b != StateClosed {
		t.Errorf("states: a=%s b=%s", a, b)
	}
}

func TestCircuitSuccessResetsConsecutiveFailures(t *testing.T) {
	p := newPathOrigin()
	p.respond("/fail", http.StatusInternalServerError)
	ct := must(NewCircuitTransport(p, WithFailureThreshold(2)))
	send(t, ct, originA+"/fail")
	send(t, ct, originA+"/ok")
	send(t, ct, originA+"/fail")
	if s := ct.State(mustOrigin(originA)); s != StateClosed {
		t.Fatalf("failures are consecutive, state %s", s)
	}
}

func TestCircuitHalfOpenAdmitsSuccessThresholdProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		p.respond("/fail", http.StatusInternalServerError)
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithSuccessThreshold(2), WithOpenTimeout(10*time.Second)))
		a := mustOrigin(originA)
		send(t, ct, originA+"/fail")

		time.Sleep(10 * time.Second)
		if err := send(t, ct, originA+"/ok"); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("still open until the timeout has passed, got %v", err)
		}
		time.Sleep(time.Second)

		open1, open2 := p.gate("/p1"), p.gate("/p2")
		p1 := sendAsync(context.Background(), ct, originA+"/p1")
		p2 := sendAsync(context.Background(), ct, originA+"/p2")
		synctest.Wait()
		if err := send(t, ct, originA+"/ok"); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("a third probe must be rejected while two are in flight, got %v", err)
		}
		open1()
		<-p1
		if s := ct.State(a); s != StateHalfOpen {
			t.Fatalf("one success of two must not close, state %s", s)
		}
		open2()
		<-p2
		if s := ct.State(a); s != StateClosed {
			t.Fatalf("two consecutive successes close the circuit, state %s", s)
		}
	})
}

func TestCircuitProbeFailureReopens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		p.respond("/fail", http.StatusInternalServerError)
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithOpenTimeout(10*time.Second)))
		send(t, ct, originA+"/fail")
		time.Sleep(11 * time.Second) // past the open timeout
		if err := send(t, ct, originA+"/fail"); err != nil {
			t.Fatalf("the probe should be sent, got %v", err)
		}
		if s := ct.State(mustOrigin(originA)); s != StateOpen {
			t.Fatalf("a failed probe reopens, state %s", s)
		}
	})
}

func TestCircuitIgnoresOutcomesFromAnEarlierState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		p.respond("/fail", http.StatusInternalServerError)
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithSuccessThreshold(1), WithOpenTimeout(10*time.Second)))
		a := mustOrigin(originA)

		release := p.gate("/slow")
		slow := sendAsync(context.Background(), ct, originA+"/slow") // admitted while closed
		synctest.Wait()
		send(t, ct, originA+"/fail") // opens the circuit
		time.Sleep(11 * time.Second) // past the open timeout
		if s := ct.State(a); s != StateHalfOpen {
			t.Fatalf("setup: want half-open, got %s", s)
		}

		release()
		if err := <-slow; err != nil {
			t.Fatal(err)
		}
		if s := ct.State(a); s != StateHalfOpen {
			t.Fatalf("a success admitted before the circuit opened must not close it, state %s", s)
		}
	})
}

func TestCircuitCallerCancellationIsNotAFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1)))
		a := mustOrigin(originA)
		p.gate("/slow")

		ctx, cancel := context.WithCancel(context.Background())
		res := sendAsync(ctx, ct, originA+"/slow")
		synctest.Wait()
		cancel()
		if err := <-res; !errors.Is(err, context.Canceled) {
			t.Fatalf("want the cancellation back, got %v", err)
		}
		if s := ct.State(a); s != StateClosed {
			t.Fatalf("a cancelled request says nothing about the origin, state %s", s)
		}

		ctx, cancelTimeout := context.WithTimeout(context.Background(), time.Second)
		defer cancelTimeout()
		if err := <-sendAsync(ctx, ct, originA+"/slow"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want the deadline back, got %v", err)
		}
		if s := ct.State(a); s != StateOpen {
			t.Fatalf("an origin too slow for the deadline is failing, state %s", s)
		}
	})
}

type transition struct {
	origin   Origin
	from, to State
}

type transitions struct {
	mu  sync.Mutex
	got []transition
}

func (r *transitions) record(o Origin, from, to State) {
	r.mu.Lock()
	r.got = append(r.got, transition{o, from, to})
	r.mu.Unlock()
}

func (r *transitions) list() []transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]transition(nil), r.got...)
}

func TestCircuitReportsTransitions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		p.respond("/fail", http.StatusInternalServerError)
		var rec transitions
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithSuccessThreshold(1),
			WithOpenTimeout(10*time.Second), WithOnStateChange(rec.record)))
		a := mustOrigin(originA)

		send(t, ct, originA+"/fail")
		time.Sleep(11 * time.Second) // past the open timeout
		send(t, ct, originA+"/ok")

		want := []transition{{a, StateClosed, StateOpen}, {a, StateOpen, StateHalfOpen}, {a, StateHalfOpen, StateClosed}}
		got := rec.list()
		if len(got) != len(want) {
			t.Fatalf("transitions = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("transitions = %v, want %v", got, want)
			}
		}
	})
}

func TestCircuitResetClearsFailures(t *testing.T) {
	p := newPathOrigin()
	p.respond("/fail", http.StatusInternalServerError)
	var rec transitions
	ct := must(NewCircuitTransport(p, WithFailureThreshold(2), WithOnStateChange(rec.record)))
	a := mustOrigin(originA)

	send(t, ct, originA+"/fail")
	ct.Reset(a)
	send(t, ct, originA+"/fail")
	if s := ct.State(a); s != StateClosed {
		t.Fatalf("Reset must clear the failure count, state %s", s)
	}

	send(t, ct, originA+"/fail")
	if s := ct.State(a); s != StateOpen {
		t.Fatalf("setup: want open, got %s", s)
	}
	ct.Reset(a)
	if s := ct.State(a); s != StateClosed {
		t.Fatalf("Reset closes an open circuit, state %s", s)
	}
	if err := send(t, ct, originA+"/ok"); err != nil {
		t.Fatalf("a reset circuit admits requests, got %v", err)
	}
	if got := rec.list(); len(got) != 2 || got[1] != (transition{a, StateOpen, StateClosed}) {
		t.Fatalf("Reset of an open circuit must report open→closed, got %v", got)
	}
}

func TestCircuitBoundsBreakers(t *testing.T) {
	p := newPathOrigin()
	p.respond("/fail", http.StatusInternalServerError)
	var rec transitions
	ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithMaxBreakers(2), WithOnStateChange(rec.record)))
	a := mustOrigin(originA)

	send(t, ct, originA+"/fail")
	send(t, ct, originB+"/ok")
	send(t, ct, originC+"/ok") // a is least recently used and is dropped

	if n := ct.breakers.len(); n != 2 {
		t.Fatalf("breakers = %d, want 2", n)
	}
	if got := rec.list(); len(got) != 2 || got[1] != (transition{a, StateOpen, StateClosed}) {
		t.Fatalf("dropping an open breaker must report open→closed, got %v", got)
	}
	if err := send(t, ct, originA+"/ok"); err != nil || p.count("a.example.com") != 2 {
		t.Fatalf("a dropped origin starts closed, err %v sent %d", err, p.count("a.example.com"))
	}
}

func TestCircuitPanickingOriginReleasesTheProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		p.respond("/fail", http.StatusInternalServerError)
		p.panics["/boom"] = true
		ct := must(NewCircuitTransport(p, WithFailureThreshold(1), WithSuccessThreshold(1), WithOpenTimeout(10*time.Second)))

		send(t, ct, originA+"/fail")
		time.Sleep(11 * time.Second) // past the open timeout
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("the origin's panic must propagate")
				}
			}()
			send(t, ct, originA+"/boom")
		}()

		time.Sleep(11 * time.Second) // past the open timeout
		if err := send(t, ct, originA+"/ok"); err != nil {
			t.Fatalf("the panicked probe must count as a failure, not hold the slot: %v", err)
		}
	})
}

func TestStateString(t *testing.T) {
	for state, want := range map[State]string{StateClosed: "closed", StateOpen: "open", StateHalfOpen: "half-open", State(99): "unknown"} {
		if got := state.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", state, got, want)
		}
	}
}

// A request admitted by a breaker that has since been evicted completes
// without reporting transitions of a state the origin no longer has.
func TestCircuitEvictedBreakerReportsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var opens atomic.Int32
		o := &origin{respond: func(n int, _ *http.Request) *http.Response {
			if n == 1 {
				close(started)
				<-release
				return reply(503, "")
			}
			return reply(200, "")
		}}
		r, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		a := OriginOf(r.URL)
		ct := must(NewCircuitTransport(o, WithMaxBreakers(1), WithFailureThreshold(1), WithOnStateChange(func(origin Origin, _, to State) {
			if origin == a && to == StateOpen {
				opens.Add(1)
			}
		})))
		go func() {
			resp, _ := ct.RoundTrip(r)
			if resp != nil {
				resp.Body.Close()
			}
			close(done)
		}()
		<-started
		get(t, ct, "https://other.example/items") // evicts a's breaker
		get(t, ct, itemsURL)                      // a gets a fresh one
		close(release)
		<-done
		if opens.Load() != 0 || ct.State(a) != StateClosed {
			t.Fatalf("the evicted breaker reported %d open transition(s); current state %s", opens.Load(), ct.State(a))
		}
	})
}
