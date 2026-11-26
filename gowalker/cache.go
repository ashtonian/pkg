package gowalker

import "reflect"

// TypeCache computes a library's own metadata once per Go type — column lists,
// parsed tag options, help text — from an engine's schema, and shares it across
// calls and goroutines. Results, including errors, are retained for up to the
// engine's cache capacity of types, or for every type with
// [WithUnboundedCache]; beyond the capacity they are computed on each call,
// which [TypeCache.CacheStats] counts. A lookup of a kept type takes no
// lock.
type TypeCache[M any] struct {
	engine  *Engine
	build   func(*Schema) (M, error)
	entries writeOnceMap[reflect.Type, typeCacheEntry[M]]
}

// CacheStats reports the cache; Uncached counts builds not kept.
func (c *TypeCache[M]) CacheStats() CacheStats {
	return c.engine.settings.stats(&c.entries)
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
	c := &TypeCache[M]{engine: engine, build: build}
	return c
}

// For returns T's metadata.
func (c *TypeCache[M]) For[T any]() (M, error) {
	return c.Get(reflect.TypeFor[T]())
}

// Get returns the metadata of the exact type t. A schema compilation error is
// returned as is; an error from build is returned as build returned it.
func (c *TypeCache[M]) Get(t reflect.Type) (M, error) {
	entry, ok := c.entries.load(t)
	if ok {
		return entry.value, entry.err
	}
	schema, err := c.engine.CompileType(t)
	if err == nil {
		entry.value, entry.err = c.build(schema)
	} else {
		entry.err = err
	}
	entry = c.entries.publish(t, entry, c.engine.settings.retains)
	return entry.value, entry.err
}
