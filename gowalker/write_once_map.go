package gowalker

import "sync"

// writeOnceMap is a concurrent map whose entries are written once and never
// replaced or removed, so reads take no lock: the case sync.Map is built
// for, where an RWMutex would have every reader write its shared counter.
// Writers serialize on mu, which keeps the entry count, and so a capacity,
// exact.
type writeOnceMap[K comparable, V any] struct {
	entries  sync.Map
	mu       sync.Mutex
	held     int
	uncached uint64
}

func (m *writeOnceMap[K, V]) load(key K) (V, bool) {
	v, ok := m.entries.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return v.(V), true
}

// publish keeps v for key and returns it, unless a value is already held
// for key, which it returns instead, or keeps refuses one more entry, which
// the uncached count records.
func (m *writeOnceMap[K, V]) publish(key K, v V, keeps func(held int) bool) V {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.load(key)
	if ok {
		return held
	}
	if !keeps(m.held) {
		m.uncached++
		return v
	}
	m.entries.Store(key, v)
	m.held++
	return v
}

// counts returns the entries held and the values published but not kept.
func (m *writeOnceMap[K, V]) counts() (held int, uncached uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.held, m.uncached
}

// keepAll is the bound of a map that its key space bounds.
func keepAll(int) bool { return true }
