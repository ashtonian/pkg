package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

func main() {
	if err := run(context.Background(), os.Getenv("API_URL"), os.Stdout); err != nil {
		slog.Error("request", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, out io.Writer) (err error) {
	if url == "" {
		url = "https://httpbin.org/etag/httpx-demo"
	}
	// Layers are listed from innermost to outermost.
	transport, err := httpx.NewChain(
		httpx.WithCompressionT(),
		httpx.WithRetryT(httpx.WithMaxAttempts(3),
			httpx.WithBackoff(100*time.Millisecond, time.Second, 2)),
	).Build(http.DefaultTransport.(*http.Transport).Clone())
	if err != nil {
		return err
	}
	cache, err := httpx.NewCacheTransport(transport, nil) // Bounded private cache.
	if err != nil {
		return err
	}
	client := &http.Client{Transport: cache, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	defer func() { err = errors.Join(err, cache.Close()) }()

	for range 2 { // The server's cache headers decide: fresh hit or revalidation.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, readErr := httpx.ReadAtMost(resp.Body, 1<<20)
		if err := errors.Join(readErr, resp.Body.Close()); err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if _, err := fmt.Fprintf(out, "HTTP %d; %s\n%s\n", resp.StatusCode, resp.Header.Get("Cache-Status"), body); err != nil {
			return err
		}
	}
	return nil
}
