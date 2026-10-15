package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	if err := run(context.Background(), os.Getenv("API_URL"), os.Getenv("API_TOKEN"), os.Stdout); err != nil {
		slog.Error("fetch user", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, baseURL, token string, out io.Writer) (err error) {
	if baseURL == "" {
		baseURL = "https://api.example.com"
	}
	client, err := httpx.NewStandard(
		httpx.WithAuth(baseURL, httpx.NewBearerTokenProvider(token)),
		httpx.WithCircuitBreaker(),
		httpx.WithLimiter(16),
		httpx.WithTimeout(10*time.Second),
	)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()

	user, resp, err := httpx.Get[User](ctx, client,
		strings.TrimRight(baseURL, "/")+"/users/1",
		httpx.WithMaxResponseBytes(1<<20),
	)
	if resp != nil {
		defer func() { err = errors.Join(err, resp.Body.Close()) }()
	}
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get user: HTTP %d", resp.StatusCode)
	}
	_, err = fmt.Fprintf(out, "%s (HTTP %d)\n", user.Name, resp.StatusCode)
	return err
}
