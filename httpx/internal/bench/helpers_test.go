//go:build integration

package bench

import "net/http"

// must unwraps a constructor result in benchmarks whose configuration is
// known to be valid.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
