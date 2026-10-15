//go:build unit

package httpx

import (
	"testing"
)

func TestBufferPool(t *testing.T) {
	pool := must(NewBufferPool(1024))

	// Get a buffer
	buf := pool.Get()
	if buf == nil {
		t.Fatal("expected non-nil buffer")
	}

	// Buffer should be empty
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got length %d", buf.Len())
	}

	// Write to buffer
	buf.WriteString("test data")
	if buf.Len() != 9 {
		t.Errorf("expected length 9, got %d", buf.Len())
	}

	// Put buffer back
	pool.Put(buf)

	// Get another buffer - should be reset
	buf2 := pool.Get()
	if buf2.Len() != 0 {
		t.Errorf("expected reset buffer, got length %d", buf2.Len())
	}
}

func TestBufferPool_LargeBuffer(t *testing.T) {
	pool := must(NewBufferPool(1024))

	// Get a buffer and grow it significantly
	buf := pool.Get()
	data := make([]byte, 10*1024) // 10KB
	buf.Write(data)

	// Put should discard oversized buffers
	pool.Put(buf)

	// Next buffer should be new (default size)
	buf2 := pool.Get()
	if buf2.Cap() > pool.Size()*4 {
		t.Error("expected new buffer with default capacity")
	}
}

func TestBufferPool_NilPut(t *testing.T) {
	pool := must(NewBufferPool(1024))

	// Should not panic
	pool.Put(nil)
}

func TestBufferPool_Size(t *testing.T) {
	pool := must(NewBufferPool(2048))
	if pool.Size() != 2048 {
		t.Errorf("expected size 2048, got %d", pool.Size())
	}
}

func TestDefaultBufferPool(t *testing.T) {
	pool := DefaultBufferPool()
	if pool == nil {
		t.Fatal("DefaultBufferPool() should not return nil")
	}

	// Default pool should have 32KB buffers
	if pool.Size() != 32*1024 {
		t.Errorf("expected default pool size 32KB, got %d", pool.Size())
	}
}
