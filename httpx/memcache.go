package httpx

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// MemoryCache defaults.
const (
	DefaultMemoryCacheMaxEntries       = 1024
	DefaultMemoryCacheMaxBytes   int64 = 64 << 20
)

// MemoryCache is a bounded in-memory CacheStore: it keeps at most maxEntries
// responses and maxBytes of stored bodies and headers, evicting the least
// recently used entry first, and drops entries whose TTL has passed.
type MemoryCache struct {
	maxEntries int
	maxBytes   int64

	mu    sync.Mutex
	bytes int64
	order *list.List // front = most recently used
	items map[string]*list.Element
}

type memoryCacheEntry struct {
	key     string
	value   *CachedResponse
	size    int64
	expires time.Time // zero: no expiry
}

// MemoryCacheOption configures a MemoryCache.
type MemoryCacheOption func(*MemoryCache)

// WithMaxEntries caps the number of stored responses.
func WithMaxEntries(n int) MemoryCacheOption {
	return func(c *MemoryCache) { c.maxEntries = n }
}

// WithMaxBytes caps the total size of stored bodies and headers. A single
// response larger than the cap is not stored.
func WithMaxBytes(n int64) MemoryCacheOption {
	return func(c *MemoryCache) { c.maxBytes = n }
}

// NewMemoryCache creates a bounded in-memory cache. Defaults:
// DefaultMemoryCacheMaxEntries entries, DefaultMemoryCacheMaxBytes bytes.
func NewMemoryCache(opts ...MemoryCacheOption) (*MemoryCache, error) {
	c := &MemoryCache{
		maxEntries: DefaultMemoryCacheMaxEntries,
		maxBytes:   DefaultMemoryCacheMaxBytes,
		order:      list.New(),
		items:      make(map[string]*list.Element),
	}
	for _, opt := range opts {
		opt(c)
	}
	var errs []error
	if c.maxEntries < 1 {
		errs = append(errs, invalidConfig("memory cache: WithMaxEntries must be >= 1, got %d", c.maxEntries))
	}
	if c.maxBytes < 1 {
		errs = append(errs, invalidConfig("memory cache: WithMaxBytes must be >= 1, got %d", c.maxBytes))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// Get returns the stored response for key, if it has not expired.
func (c *MemoryCache) Get(ctx context.Context, key string) (*CachedResponse, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	el, ok := c.items[key]
	if !ok {
		return nil, false, nil
	}
	e := el.Value.(*memoryCacheEntry)
	if !e.expires.IsZero() && time.Now().After(e.expires) {
		c.remove(el)
		return nil, false, nil
	}
	c.order.MoveToFront(el)
	return e.value, true, nil
}

// Set stores value under key for ttl; ttl <= 0 means no expiry (the entry
// still leaves on eviction). Stored values must not be modified afterwards.
func (c *MemoryCache) Set(ctx context.Context, key string, value *CachedResponse, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	size := responseSize(key, value)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	if size > c.maxBytes {
		return ErrCacheCapacity
	}
	e := &memoryCacheEntry{key: key, value: value, size: size}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	c.items[key] = c.order.PushFront(e)
	c.bytes += size
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.remove(c.order.Back())
	}
	return nil
}

// Delete removes key.
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
	return nil
}

// Len returns the number of stored responses.
func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Bytes returns the accounted size of stored responses.
func (c *MemoryCache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

func (c *MemoryCache) remove(el *list.Element) {
	e := c.order.Remove(el).(*memoryCacheEntry)
	delete(c.items, e.key)
	c.bytes -= e.size
}

// responseSize approximates the memory a stored response retains: key,
// body and header bytes.
func responseSize(key string, r *CachedResponse) int64 {
	n := int64(len(key) + len(r.Body))
	for _, h := range []map[string][]string{r.Header, r.VaryHeaders} {
		for k, vs := range h {
			n += int64(len(k))
			for _, v := range vs {
				n += int64(len(v))
			}
		}
	}
	return n
}
