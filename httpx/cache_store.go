package httpx

import (
	"context"
	"errors"
)

// Cache storage failures.
var (
	ErrCacheCapacity = errors.New("httpx: cache entry exceeds capacity")
	ErrCacheDisabled = errors.New("httpx: cache stripe disabled after storage mutation failure")
)

// CacheOperation identifies storage work without including keys or URLs.
type CacheOperation string

const (
	CacheRead   CacheOperation = "read"
	CacheWrite  CacheOperation = "write"
	CacheDelete CacheOperation = "delete"
)

// CacheError reports best-effort maintenance failures. Its text excludes the
// backend's message, which may contain credentials or private cache keys.
// Unwrap permits errors.Is/As; do not log the unwrapped error without redaction.
type CacheError struct {
	Operation CacheOperation
	err       error
}

func (e *CacheError) Error() string { return "httpx: cache " + string(e.Operation) + " failed" }
func (e *CacheError) Unwrap() error { return e.err }

// WithOnCacheError observes storage failures synchronously. fn must be quick,
// concurrency-safe and must not call the cache again. No key, URL or body is
// exposed. Use bounded labels such as Operation and errors.Is classifications.
func WithOnCacheError(fn func(context.Context, *CacheError)) CacheOption {
	return func(t *CacheTransport) { t.onError = fn }
}

func (t *CacheTransport) observeError(ctx context.Context, op CacheOperation, err error) {
	if t.onError != nil {
		t.onError(ctx, &CacheError{Operation: op, err: err})
	}
}

func (s *cacheStripe) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *cacheStripe) unlock() { <-s.gate }
