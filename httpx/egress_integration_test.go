//go:build integration

package httpx

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// dnsAnswers supplies deterministic A answers through the real Go resolver.
func dnsAnswers(t *testing.T, addresses ...[4]byte) *net.Resolver {
	t.Helper()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			var size [2]byte
			if _, err := io.ReadFull(server, size[:]); err != nil {
				return
			}
			query := make([]byte, binary.BigEndian.Uint16(size[:]))
			if _, err := io.ReadFull(server, query); err != nil {
				return
			}
			if len(query) < 17 {
				return
			}
			end := 12
			for end < len(query) && query[end] != 0 {
				end += int(query[end]) + 1
			}
			end++
			if end+4 > len(query) {
				return
			}
			qtype := binary.BigEndian.Uint16(query[end : end+2])
			answer := append([]byte(nil), query[:end+4]...)
			binary.BigEndian.PutUint16(answer[2:4], 0x8180)
			binary.BigEndian.PutUint16(answer[6:8], 0)
			binary.BigEndian.PutUint16(answer[10:12], 0)
			if qtype == 1 {
				binary.BigEndian.PutUint16(answer[6:8], uint16(len(addresses)))
				for _, addr := range addresses {
					answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
					answer = append(answer, addr[:]...)
				}
			}
			binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
			if _, err := server.Write(append(size[:], answer...)); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("DNS fixture: %v", err)
			}
		}()
		return client, nil
	}}
}

func TestEgressResolvedAddressesRedirectAndProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("proxy received guarded request") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, strings.Replace(serverURL(r), "127.0.0.1", "127.0.0.2", 1), http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	base, err := NewBaseTransport(WithEgressPolicy(NewEgressPolicy(netip.MustParsePrefix("127.0.0.1/32"))), WithDialer(&net.Dialer{Timeout: time.Second, Resolver: dnsAnswers(t, [4]byte{127, 0, 0, 2}, [4]byte{127, 0, 0, 1})}))
	if err != nil {
		t.Fatal(err)
	}
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: base, Timeout: 5 * time.Second}
	resp, err := client.Get("http://mixed.invalid:" + port + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	resp, err = client.Get(server.URL + "/redirect")
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrDestinationRefused) {
		t.Fatalf("redirect bypass: %v", err)
	}
	blocked, err := NewBaseTransport(WithEgressPolicy(NewEgressPolicy()), WithDialer(&net.Dialer{Timeout: time.Second, Resolver: dnsAnswers(t, [4]byte{127, 0, 0, 1})}))
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.CloseIdleConnections()
	for _, scheme := range []string{"http", "https"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, scheme+"://rebound.invalid:"+port, nil)
		resp, err = blocked.RoundTrip(req)
		if resp != nil {
			resp.Body.Close()
		}
		if !errors.Is(err, ErrDestinationRefused) {
			t.Fatalf("%s DNS/proxy bypass: %v", scheme, err)
		}
	}
}

func serverURL(r *http.Request) string { return "http://" + r.Host + "/private" }
