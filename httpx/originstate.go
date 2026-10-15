package httpx

import (
	"container/list"
	"sync"
)

// DefaultMaxOrigins bounds how many origins CircuitTransport and
// RateLimitTransport keep state for.
const DefaultMaxOrigins = 1024

// originStates keeps one value per origin, created on first use and
// evicting the least recently used origin beyond max. It is a stateful
// registry, not a cache: an evicted origin starts again from fresh state.
type originStates[V any] struct {
	max     int
	create  func(Origin) V
	onEvict func(Origin, V) // called without the lock held; may be nil

	mu    sync.Mutex
	order *list.List // front = most recently used; values are *originEntry[V]
	items map[Origin]*list.Element
}

type originEntry[V any] struct {
	origin Origin
	value  V
}

func newOriginStates[V any](max int, create func(Origin) V, onEvict func(Origin, V)) *originStates[V] {
	return &originStates[V]{
		max:     max,
		create:  create,
		onEvict: onEvict,
		order:   list.New(),
		items:   make(map[Origin]*list.Element),
	}
}

// get returns the state for o, creating it when absent.
func (s *originStates[V]) get(o Origin) V {
	s.mu.Lock()
	if el, ok := s.items[o]; ok {
		s.order.MoveToFront(el)
		v := el.Value.(*originEntry[V]).value
		s.mu.Unlock()
		return v
	}
	v := s.create(o)
	s.items[o] = s.order.PushFront(&originEntry[V]{origin: o, value: v})
	var evicted *originEntry[V]
	if s.order.Len() > s.max {
		evicted = s.order.Remove(s.order.Back()).(*originEntry[V])
		delete(s.items, evicted.origin)
	}
	s.mu.Unlock()
	if evicted != nil && s.onEvict != nil {
		s.onEvict(evicted.origin, evicted.value)
	}
	return v
}

// peek returns the state for o without creating it or marking it used.
func (s *originStates[V]) peek(o Origin) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[o]
	if !ok {
		var zero V
		return zero, false
	}
	return el.Value.(*originEntry[V]).value, true
}

// replace swaps o's state for a fresh one and returns the previous state,
// if any.
func (s *originStates[V]) replace(o Origin) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[o]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*originEntry[V])
	old := e.value
	e.value = s.create(o)
	return old, true
}
