package httpx

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Lifecycle releases what client and chain builders construct for it:
// [NewClient], [NewStandard] and [MustNewStandard] given [WithLifecycle], and
// [Chain.BuildWithLifecycle] / [Chain.ClientWithLifecycle].
//
// A build registers only what it constructs: each CacheTransport its cache
// layer builds, and the base *http.Transport it builds when the caller supplies
// none. It never registers what the caller supplied or what it can merely reach
// through Unwrap: a WithTransport or Build base, a CacheStore, a TokenSource,
// an AuthProvider, a hook, or a layer a custom TransportFunc constructs.
//
// The zero value is ready to use, and one Lifecycle may own several clients.
// Close it once their callers have stopped; the clients still answer later
// requests, but their caches no longer refresh in the background.
type Lifecycle struct {
	mu     sync.Mutex
	closed bool
	builds []*buildOwner

	once sync.Once
	err  error
}

// WithLifecycle registers what NewClient or NewStandard constructs with l; see
// [Lifecycle]. A nil or closed Lifecycle fails the build with an error wrapping
// ErrInvalidConfig.
func WithLifecycle(l *Lifecycle) Option {
	return func(cfg *clientConfig) {
		cfg.lifecycle = l
		cfg.lifecycleSet = true
	}
}

// Close stops every registered build, newest first: it closes the build's
// caches outermost first, which cancels their background revalidations and
// waits for them, then closes the idle connections of the base transport it
// constructed.
// Close is idempotent and safe for concurrent use: every call returns once
// the work has stopped. Builds given l afterwards fail.
func (l *Lifecycle) Close() error {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		builds := l.builds
		l.builds = nil
		l.mu.Unlock()

		var errs []error
		for _, build := range slices.Backward(builds) {
			errs = append(errs, build.stop())
		}
		l.err = errors.Join(errs...)
	})
	return l.err
}

// register hands a completed build to l.
func (l *Lifecycle) register(b *buildOwner) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return invalidConfig("lifecycle is closed")
	}
	l.builds = append(l.builds, b)
	return nil
}

// buildOwner is what one build constructed and owns.
type buildOwner struct {
	caches []*CacheTransport
	base   idleConnectionsCloser // nil when the caller supplied the base
}

// ownBase records the base transport the build constructed.
func (o *buildOwner) ownBase(rt http.RoundTripper) {
	if c, ok := rt.(idleConnectionsCloser); ok {
		o.base = c
	}
}

// adopt takes ownership of layer, the result of wrapping input, when a
// builder layer constructed it and no build owns it yet. Every build adopts,
// with or without a Lifecycle, so no later build can claim a layer it finds
// beneath a base it was given.
func (o *buildOwner) adopt(layer, input http.RoundTripper) {
	c, ok := layer.(*CacheTransport)
	if !ok {
		return
	}
	if in, _ := input.(*CacheTransport); in == c {
		return // a pass-through layer handed back what it was given
	}
	if c.adoptable.CompareAndSwap(true, false) {
		o.caches = append(o.caches, c)
	}
}

// finish registers the build with l, when there is one.
func (o *buildOwner) finish(l *Lifecycle) error {
	if l == nil {
		return nil
	}
	return l.register(o)
}

// stop closes the caches outermost first, then the idle connections of the
// owned base: no layer stops while one above it may still use it.
func (o *buildOwner) stop() error {
	var errs []error
	for _, cache := range slices.Backward(o.caches) {
		errs = append(errs, cache.Close())
	}
	if o.base != nil {
		o.base.CloseIdleConnections()
	}
	return errors.Join(errs...)
}

// stopOnError releases what a failed build constructed.
func (o *buildOwner) stopOnError(err *error) {
	if *err != nil {
		_ = o.stop() // the build's own error is the one to report
	}
}

// newLayerCache constructs the cache of a builder layer: the build that
// receives it owns it.
func newLayerCache(base http.RoundTripper, store CacheStore, opts ...CacheOption) (http.RoundTripper, error) {
	c, err := NewCacheTransport(base, store, opts...)
	if err != nil {
		return nil, err
	}
	c.adoptable.Store(true)
	return c, nil
}

// BuildWithLifecycle is [Chain.Build], registering with l the base it
// constructs when base is nil and the caches its WithCacheT layers construct.
// A nil or closed Lifecycle fails the build with an error wrapping
// ErrInvalidConfig.
func (c Chain) BuildWithLifecycle(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
	if l == nil {
		return nil, invalidConfig("chain: lifecycle is nil")
	}
	return c.build(base, l)
}

// ClientWithLifecycle is [Chain.Client], registering what the chain constructs
// with l as [Chain.BuildWithLifecycle] does.
func (c Chain) ClientWithLifecycle(l *Lifecycle, base http.RoundTripper, timeout time.Duration) (*http.Client, error) {
	if l == nil {
		return nil, invalidConfig("chain: lifecycle is nil")
	}
	return c.client(base, timeout, l)
}
