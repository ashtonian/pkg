//go:build unit

package httpx

import (
	"net/http"
	"testing"
	"time"
)

// BenchmarkHedgingRoundTrip measures the hedging layer's own cost over an
// upstream that answers at once, so no hedge fires: the per-request work
// of choosing the delay and recording a sample, at full concurrency, with a
// full sample window.
func BenchmarkHedgingRoundTrip(b *testing.B) {
	upstream := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	for _, tt := range []struct {
		name string
		rt   http.RoundTripper
	}{
		{"static", must(NewHedgingTransport(upstream, time.Hour))},
		{"adaptive", must(NewAdaptiveHedgingTransport(upstream, 0.95))},
	} {
		b.Run(tt.name, func(b *testing.B) {
			req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
			for range 2000 {
				resp, err := tt.rt.RoundTrip(req)
				if err != nil {
					b.Fatal(err)
				}
				resp.Body.Close()
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					resp, err := tt.rt.RoundTrip(req)
					if err != nil {
						b.Error(err)
						return
					}
					resp.Body.Close()
				}
			})
		})
	}
}
