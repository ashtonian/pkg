package config

import (
	"sync"
	"testing"
	"time"
)

// Reloader tests keep time one of two ways. A test without a file watch runs
// in a testing/synctest bubble, where the time package is virtual and
// synctest.Wait says when every goroutine has settled. fsnotify's reader
// blocks in a system call, which keeps a bubble from ever going idle, so a
// test with a file watch runs on real time with a fakeClock instead, and
// moves it itself.

// fakeClock is a clock that moves only when a test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	// resets counts the times a timer was armed again with ResetAt.
	resets int
	// changed is closed, and replaced, whenever a timer is armed, stopped or
	// fires, or the time moves.
	changed chan struct{}
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), changed: make(chan struct{})}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) timer { return c.add(d, 0) }

func (c *fakeClock) NewTicker(d time.Duration) ticker {
	if d <= 0 {
		panic("fakeClock: non-positive ticker interval")
	}
	return c.add(d, d)
}

func (c *fakeClock) add(d, period time.Duration) *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, c: make(chan time.Time, 1), period: period}
	c.timers = append(c.timers, t)
	t.armAt(c.now.Add(d))
	return t
}

// Advance moves the clock forward by d, firing what falls due on the way in
// deadline order, each at its own deadline.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	end := c.now.Add(d)
	for {
		var next *fakeTimer
		for _, t := range c.timers {
			if t.active && !t.deadline.After(end) && (next == nil || t.deadline.Before(next.deadline)) {
				next = t
			}
		}
		if next == nil {
			break
		}
		c.now = next.deadline
		next.fire()
	}
	c.now = end
	c.notify()
}

// debounce reports when the armed one-shot timer is due, if one is: a
// Reloader's only one is the file watch's debounce.
func (c *fakeClock) debounce() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.timers {
		if t.active && t.period == 0 {
			return t.deadline, true
		}
	}
	return time.Time{}, false
}

func (c *fakeClock) resetCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resets
}

// changes is closed at the clock's next change.
func (c *fakeClock) changes() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.changed
}

// notify reports a change. The caller holds mu.
func (c *fakeClock) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// await waits for cond, checking it again at each change of the clock.
func (c *fakeClock) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		changed := c.changes()
		if cond() {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

// awaitDebounce waits for the file watch to arm its debounce and returns
// when it is due.
func (c *fakeClock) awaitDebounce(t *testing.T) time.Time {
	t.Helper()
	var due time.Time
	c.await(t, "the file watch to arm its debounce", func() bool {
		var ok bool
		due, ok = c.debounce()
		return ok
	})
	return due
}

// receive waits for a value on ch. Meanwhile it fires each debounce the file
// watch arms as soon as it is armed — what the test changed is on disk by
// then — so the watch settles without real time passing. Tickers move only
// as far as those debounces take the clock. An event that arrives after its
// write's burst settled, such as kqueue's Write after the Chmod of a
// truncate, starts a burst of its own, so a test that receives this way must
// not count reloads; coalescing is asserted on the file watch itself.
func receive[V any](t *testing.T, clk *fakeClock, ch <-chan V, what string) V {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		changed := clk.changes()
		if due, ok := clk.debounce(); ok {
			clk.Advance(due.Sub(clk.Now()))
			continue
		}
		select {
		case v := <-ch:
			return v
		case <-changed:
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

type fakeTimer struct {
	clock    *fakeClock
	c        chan time.Time
	period   time.Duration // a ticker's interval; zero for a timer
	deadline time.Time
	active   bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) ResetAt(at time.Time) {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.clock.resets++
	t.armAt(at)
}

func (t *fakeTimer) Stop() {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.drain()
	t.active = false
	t.clock.notify()
}

// armAt sets t to fire at deadline, at once when it has passed, as the time
// package's timers do. The caller holds the clock's lock.
func (t *fakeTimer) armAt(deadline time.Time) {
	t.drain() // as with the time package, no value from before is received after
	t.deadline = deadline
	t.active = true
	if !deadline.After(t.clock.now) {
		t.fire()
	}
	t.clock.notify()
}

// fire delivers the time, dropping it when the last is still unread as a
// ticker does, and arms a ticker for its next interval. The caller holds the
// clock's lock.
func (t *fakeTimer) fire() {
	select {
	case t.c <- t.clock.now:
	default:
	}
	if t.period > 0 {
		t.deadline = t.deadline.Add(t.period)
		return
	}
	t.active = false
}

func (t *fakeTimer) drain() {
	select {
	case <-t.c:
	default:
	}
}
