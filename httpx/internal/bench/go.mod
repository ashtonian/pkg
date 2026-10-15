module github.com/ashtonian/pkg/httpx/internal/bench

go 1.26.0

require (
	github.com/ashtonian/pkg/httpx v0.1.0
	github.com/go-resty/resty/v2 v2.16.5
	github.com/hashicorp/go-retryablehttp v0.7.7
	github.com/sony/gobreaker v1.0.0
)

require (
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	golang.org/x/net v0.33.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)

// The benchmarks measure the working tree, never a release: this module is
// development-only and is not published.
replace github.com/ashtonian/pkg/httpx => ../..
