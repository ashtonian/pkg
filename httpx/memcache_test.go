//go:build unit

package httpx

import (
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func stored(body string) *CachedResponse {
	return &CachedResponse{StatusCode: 200, Header: http.Header{}, Body: []byte(body)}
}

func TestMemoryCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := must(NewMemoryCache(WithMaxEntries(2)))
	c.Set("a", stored("1"), 0)
	c.Set("b", stored("2"), 0)
	c.Get("a") // a is now the most recently used
	c.Set("c", stored("3"), 0)

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s should still be stored", k)
		}
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d", c.Len())
	}
}

func TestMemoryCacheBoundsBytes(t *testing.T) {
	c := must(NewMemoryCache(WithMaxBytes(10)))
	c.Set("k1", stored("12345"), 0) // 2 + 5 = 7 bytes
	c.Set("k2", stored("12"), 0)    // 2 + 2 = 4 bytes, over the budget
	if _, ok := c.Get("k1"); ok {
		t.Error("k1 should have been evicted to fit k2")
	}
	if c.Bytes() > 10 {
		t.Errorf("Bytes = %d, over the cap", c.Bytes())
	}
	c.Set("big", stored("far more than ten bytes"), 0)
	if _, ok := c.Get("big"); ok {
		t.Error("an entry larger than the cap must not be stored")
	}
}

func TestMemoryCacheExpiresEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := must(NewMemoryCache())
		c.Set("k", stored("v"), time.Minute)
		time.Sleep(59 * time.Second)
		if _, ok := c.Get("k"); !ok {
			t.Fatal("entry expired early")
		}
		time.Sleep(2 * time.Second)
		if _, ok := c.Get("k"); ok {
			t.Fatal("entry outlived its TTL")
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry still accounted: len=%d bytes=%d", c.Len(), c.Bytes())
		}
	})
}

func TestMemoryCacheReplaceAndDelete(t *testing.T) {
	c := must(NewMemoryCache())
	c.Set("k", stored("old"), 0)
	c.Set("k", stored("new!"), 0)
	if v, _ := c.Get("k"); string(v.Body) != "new!" || c.Len() != 1 || c.Bytes() != 5 {
		t.Fatalf("replace: body=%q len=%d bytes=%d", v.Body, c.Len(), c.Bytes())
	}
	c.Delete("k")
	if _, ok := c.Get("k"); ok || c.Len() != 0 || c.Bytes() != 0 {
		t.Fatal("delete left the entry behind")
	}
}
