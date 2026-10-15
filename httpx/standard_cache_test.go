//go:build unit

package httpx_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ashtonian/pkg/httpx"
)

// TestNewStandard_NoDefaultResponseCache guards RFC-0502 A1: NewStandard
// must NOT wire a response-body cache by default. The prior default keyed
// on method+URL only and was shared across 33 authenticated/multi-tenant
// call sites, so a cacheable upstream body could bleed between tenants.
// Two GETs to the same cacheable URL must both reach the server.
func TestNewStandard_NoDefaultResponseCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300") // cacheable
		fmt.Fprintf(w, "resp-%d", n)
	}))
	defer srv.Close()

	c := must(httpx.NewStandard())
	get := func() string {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	b1, b2 := get(), get()
	if b1 == b2 {
		t.Fatalf("NewStandard served a cached body by default (both %q) — A1 default response cache must be removed", b1)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expected 2 server hits (no default caching), got %d", got)
	}
}
