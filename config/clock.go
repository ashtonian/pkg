package config

import "time"

// clock is where a Reloader reads the time and gets its debounce timer and
// its recheck and poll tickers.
type clock interface {
	Now() time.Time
	NewTimer(d time.Duration) timer
	NewTicker(d time.Duration) ticker
}

type timer interface {
	C() <-chan time.Time
	// ResetAt arms the timer to fire at t, at once when t has passed. A
	// deadline rather than a duration keeps it exact however long passes
	// between reading the clock and arming.
	ResetAt(t time.Time)
	Stop()
}

type ticker interface {
	C() <-chan time.Time
	Stop()
}

// withClock makes a Reloader keep time by c. Tests use it to drive a file
// watch: fsnotify's reader blocks in a system call, which keeps a
// testing/synctest bubble from ever going idle, so that time cannot be
// virtual.
func withClock(c clock) ReloadOption {
	return func(o *reloadOptions) { o.clock = c }
}

// systemClock is the time package's clock.
type systemClock struct{}

func (systemClock) Now() time.Time                   { return time.Now() }
func (systemClock) NewTimer(d time.Duration) timer   { return systemTimer{time.NewTimer(d)} }
func (systemClock) NewTicker(d time.Duration) ticker { return systemTicker{time.NewTicker(d)} }

type systemTimer struct{ t *time.Timer }

func (s systemTimer) C() <-chan time.Time { return s.t.C }
func (s systemTimer) ResetAt(t time.Time) { s.t.Reset(time.Until(t)) }
func (s systemTimer) Stop()               { s.t.Stop() }

type systemTicker struct{ t *time.Ticker }

func (s systemTicker) C() <-chan time.Time { return s.t.C }
func (s systemTicker) Stop()               { s.t.Stop() }
