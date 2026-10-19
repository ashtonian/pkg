package gowalker

import (
	"reflect"
	"sync"
)

// TypeCache computes a library's own metadata once per Go type — column lists,
// parsed tag options, help text — from an engine's schema, and shares it across
// calls and goroutines. Results, including errors, are retained for up to the
// engine's cache capacity of types; beyond it they are computed on each call.
type TypeCache[M any] struct {
	engine *Engine
	build  func(*Schema) (M, error)

	mu      sync.RWMutex
	entries map[reflect.Type]typeCacheEntry[M]
}

type typeCacheEntry[M any] struct {
	value M
	err   error
}

// NewTypeCache returns a cache that builds each type's metadata with build,
// from engine's compiled schema. build must be deterministic and safe for
// concurrent use; concurrent first requests for one type can each run it, and
// one result is kept. build may request other types from the cache, but must
// guard against recursion for a type that contains itself. NewTypeCache panics
// if engine or build is nil.
func NewTypeCache[M any](engine *Engine, build func(*Schema) (M, error)) *TypeCache[M] {
	if engine == nil || build == nil {
		panic("gowalker: NewTypeCache requires an engine and a build function")
	}
	return &TypeCache[M]{engine: engine, build: build, entries: make(map[reflect.Type]typeCacheEntry[M])}
}

// For returns T's metadata.
func (c *TypeCache[M]) For[T any]() (M, error) {
	return c.Get(reflect.TypeFor[T]())
}

// Get returns the metadata of the exact type t. A schema compilation error is
// returned as is; an error from build is returned as build returned it.
func (c *TypeCache[M]) Get(t reflect.Type) (M, error) {
	c.mu.RLock()
	entry, ok := c.entries[t]
	c.mu.RUnlock()
	if ok {
		return entry.value, entry.err
	}
	schema, err := c.engine.CompileType(t)
	if err == nil {
		entry.value, entry.err = c.build(schema)
	} else {
		entry.err = err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.entries[t]; ok {
		return cached.value, cached.err
	}
	if len(c.entries) < c.engine.settings.capacity {
		c.entries[t] = entry
	}
	return entry.value, entry.err
}
