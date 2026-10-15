//go:build unit || integration

package httpx_test

// must unwraps a constructor result in tests where the configuration is
// known to be valid.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
