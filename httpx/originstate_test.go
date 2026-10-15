//go:build unit

package httpx

import (
	"fmt"
	"sync"
	"testing"
)

func TestOriginStatesEvictsLeastRecentlyUsed(t *testing.T) {
	created := 0
	var evicted []Origin
	s := newOriginStates(2, func(Origin) int { created++; return created }, func(o Origin, _ int) { evicted = append(evicted, o) })
	a, b, c := mustOrigin(originA), mustOrigin(originB), mustOrigin(originC)

	s.get(a)
	s.get(b)
	s.get(a) // b is now least recently used
	s.get(c)

	if len(evicted) != 1 || evicted[0] != b {
		t.Fatalf("evicted = %v, want [b]", evicted)
	}
	if v, ok := s.peek(a); !ok || v != 1 {
		t.Fatalf("a = %d, %v; its state must survive", v, ok)
	}
	if _, ok := s.peek(b); ok {
		t.Fatal("b should be gone")
	}
	if v := s.get(b); v != 4 {
		t.Fatalf("b comes back with fresh state, got %d", v)
	}
}

func TestOriginStatesReplace(t *testing.T) {
	n := 0
	s := newOriginStates(4, func(Origin) int { n++; return n }, nil)
	a := mustOrigin(originA)
	if _, ok := s.replace(a); ok {
		t.Fatal("replacing an absent origin reports nothing")
	}
	s.get(a)
	if old, ok := s.replace(a); !ok || old != 1 {
		t.Fatalf("replace returned %d, %v", old, ok)
	}
	if v := s.get(a); v != 2 {
		t.Fatalf("a = %d after replace, want fresh state 2", v)
	}
}

func TestOriginStatesConcurrentUse(t *testing.T) {
	s := newOriginStates(8, func(Origin) *sync.Mutex { return new(sync.Mutex) }, nil)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for j := range 100 {
				o := mustOrigin(fmt.Sprintf("https://h%d.example.com", (i+j)%16))
				s.get(o)
				s.peek(o)
				if j%10 == 0 {
					s.replace(o)
				}
			}
		})
	}
	wg.Wait()
	if n := s.len(); n > 8 {
		t.Fatalf("len = %d, over the bound", n)
	}
}

// len observes the retained state count in tests.
func (s *originStates[V]) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}
