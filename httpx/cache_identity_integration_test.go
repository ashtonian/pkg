//go:build integration

package httpx

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCachePartitionsNonCanonicalWireAuthorization(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "max-age=60")
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	tr := must(NewCacheTransport(base, nil))
	defer tr.Close()
	client := &http.Client{Transport: tr}
	for _, identity := range []string{"Bearer A", "Bearer B", "Bearer A"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		req.Header["authorization"] = []string{identity}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != identity {
			t.Fatalf("request %q received %q: %v", identity, body, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("origin calls=%d, want one per identity", calls.Load())
	}
}
