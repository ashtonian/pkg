//go:build integration

package httpx

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// A stale-while-revalidate refresh started by a NewStandard client survives
// CloseIdleConnections, and its Lifecycle cancels it.
func TestLifecycleStopsLoopbackRefresh(t *testing.T) {
	var calls atomic.Int32
	var startOnce sync.Once
	started, cancelled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Cache-Control", "max-age=1, stale-while-revalidate=600")
			w.Header().Set("Age", "5") // stale on arrival
			w.Header().Set("ETag", `"v1"`)
			fmt.Fprint(w, "v1")
			return
		}
		startOnce.Do(func() { close(started) })
		<-r.Context().Done()
		close(cancelled)
	}))
	defer srv.Close()

	var life Lifecycle
	defer life.Close()
	client := must(NewStandard(WithCache(nil), WithLifecycle(&life)))
	for _, want := range []string{"httpx; fwd=miss; stored", "httpx; hit; detail=stale-while-revalidate"} {
		resp := must(client.Get(srv.URL))
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(body) != "v1" || resp.Header.Get("Cache-Status") != want {
			t.Fatalf("got %q (%v), Cache-Status %q; want v1, %q", body, err, resp.Header.Get("Cache-Status"), want)
		}
	}
	<-started
	client.CloseIdleConnections()
	select {
	case <-cancelled:
		t.Fatal("CloseIdleConnections cancelled the refresh")
	default:
	}
	if err := life.Close(); err != nil {
		t.Fatal(err)
	}
	<-cancelled
}

// Close drops the idle connections of a base the builder constructed, and
// leaves those of a supplied base: the next request dials anew only in the
// first case.
func TestLifecycleClosesOnlyOwnedBaseConnections(t *testing.T) {
	supplied := http.DefaultTransport.(*http.Transport).Clone()
	defer supplied.CloseIdleConnections()
	for _, tt := range []struct {
		name      string
		build     func(*Lifecycle) (*http.Client, error)
		wantConns int32
	}{
		{"NewClient base", func(l *Lifecycle) (*http.Client, error) { return NewClient(WithLifecycle(l)) }, 2},
		{"Chain base", func(l *Lifecycle) (*http.Client, error) {
			return NewChain(WithCompressionT()).ClientWithLifecycle(l, nil, 0)
		}, 2},
		{"WithTransport base", func(l *Lifecycle) (*http.Client, error) {
			return NewClient(WithTransport(supplied), WithLifecycle(l))
		}, 1},
		{"Chain supplied base", func(l *Lifecycle) (*http.Client, error) {
			return NewChain(WithCompressionT()).ClientWithLifecycle(l, supplied, 0)
		}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var conns atomic.Int32
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "ok")
			}))
			srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			srv.Start()
			defer srv.Close()

			var life Lifecycle
			client := must(tt.build(&life))
			defer client.CloseIdleConnections()
			fetch := func() {
				resp := must(client.Get(srv.URL))
				_, _ = io.Copy(io.Discard, resp.Body) // EOF returns the connection to the pool
				resp.Body.Close()
			}
			fetch()
			if err := life.Close(); err != nil {
				t.Fatal(err)
			}
			fetch()
			if got := conns.Load(); got != tt.wantConns {
				t.Errorf("server accepted %d connections, want %d", got, tt.wantConns)
			}
		})
	}
}
