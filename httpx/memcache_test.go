//go:build unit

package httpx

import (
	"errors"
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
	if err := c.Set(t.Context(), "a", stored("1"), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(t.Context(), "b", stored("2"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(t.Context(), "a"); err != nil {
		t.Fatal(err)
	} // a is now the most recently used
	if err := c.Set(t.Context(), "c", stored("3"), 0); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := c.Get(t.Context(), "b"); ok || err != nil {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok, err := c.Get(t.Context(), k); !ok || err != nil {
			t.Errorf("%s should still be stored", k)
		}
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d", c.Len())
	}
}

func TestMemoryCacheBoundsBytes(t *testing.T) {
	c := must(NewMemoryCache(WithMaxBytes(10)))
	if err := c.Set(t.Context(), "k1", stored("12345"), 0); err != nil {
		t.Fatal(err)
	} // 2 + 5 = 7 bytes
	if err := c.Set(t.Context(), "k2", stored("12"), 0); err != nil {
		t.Fatal(err)
	} // 2 + 2 = 4 bytes, over the budget
	if _, ok, err := c.Get(t.Context(), "k1"); ok || err != nil {
		t.Error("k1 should have been evicted to fit k2")
	}
	if c.Bytes() > 10 {
		t.Errorf("Bytes = %d, over the cap", c.Bytes())
	}
	if err := c.Set(t.Context(), "big", stored("far more than ten bytes"), 0); !errors.Is(err, ErrCacheCapacity) {
		t.Fatal(err)
	}
	if _, ok, err := c.Get(t.Context(), "big"); ok || err != nil {
		t.Error("an entry larger than the cap must not be stored")
	}
}

func TestMemoryCacheExpiresEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := must(NewMemoryCache())
		if err := c.Set(t.Context(), "k", stored("v"), time.Minute); err != nil {
			t.Fatal(err)
		}
		time.Sleep(59 * time.Second)
		if _, ok, err := c.Get(t.Context(), "k"); !ok || err != nil {
			t.Fatal("entry expired early")
		}
		time.Sleep(2 * time.Second)
		if _, ok, err := c.Get(t.Context(), "k"); ok || err != nil {
			t.Fatal("entry outlived its TTL")
		}
		if c.Len() != 0 || c.Bytes() != 0 {
			t.Fatalf("expired entry still accounted: len=%d bytes=%d", c.Len(), c.Bytes())
		}
	})
}

func TestMemoryCacheReplaceAndDelete(t *testing.T) {
	c := must(NewMemoryCache())
	if err := c.Set(t.Context(), "k", stored("old"), 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(t.Context(), "k", stored("new!"), 0); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := c.Get(t.Context(), "k"); err != nil || !ok || string(v.Body) != "new!" || c.Len() != 1 || c.Bytes() != 5 {
		t.Fatalf("replace: entry=%v error=%v len=%d bytes=%d", v, err, c.Len(), c.Bytes())
	}
	if err := c.Delete(t.Context(), "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Get(t.Context(), "k"); err != nil || ok || c.Len() != 0 || c.Bytes() != 0 {
		t.Fatal("delete left the entry behind")
	}
}
