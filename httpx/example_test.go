//go:build integration

package httpx_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

func ExampleNewClient() {
	client, err := httpx.NewClient(
		httpx.WithCompression(),
		httpx.WithRetry(httpx.WithMaxAttempts(3)),
		httpx.WithCircuitBreaker(),
		httpx.WithTimeout(30*time.Second),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	// client is a standard *http.Client with resilience built in
	defer client.CloseIdleConnections()
	fmt.Println("client created")
	// Output: client created
}

func ExampleNewClient_full() {
	client, err := httpx.NewClient(
		httpx.WithCompression(),
		httpx.WithCache(nil,
			httpx.WithStaleIfError(5*time.Minute),
		),
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(100*time.Millisecond, 5*time.Second, 2.0),
		),
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(5),
			httpx.WithOpenTimeout(30*time.Second),
		),
		httpx.WithLimiter(10),
		httpx.WithRateLimit(100, 200),
		httpx.WithAuth("https://api.example.com", httpx.NewBearerTokenProvider("my-token")),
		httpx.WithMetrics(&httpx.CountingHook{}),
		httpx.WithTimeout(30*time.Second),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	fmt.Println("full client created")
	// Output: full client created
}

func ExampleNewChain() {
	chain := httpx.NewChain(
		httpx.WithCompressionT(),
		httpx.WithCircuitBreakerT(), // Counts each network attempt.
		httpx.WithRetryT(httpx.WithMaxAttempts(3)),
	)
	client, err := chain.Client(nil, 30*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	fmt.Println("chain client created")
	// Output: chain client created
}

func ExampleDefaultChain() {
	client, err := httpx.DefaultChain().Client(nil, 30*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	fmt.Println("default chain client created")
	// Output: default chain client created
}

func ExampleResilientChain() {
	client, err := httpx.ResilientChain().Client(nil, 30*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	fmt.Println("resilient chain client created")
	// Output: resilient chain client created
}

func ExampleAPIClientChain() {
	client, err := httpx.APIClientChain().Client(nil, 30*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	fmt.Println("API client chain created")
	// Output: API client chain created
}

func ExampleGet() {
	type User struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Alice"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, resp, err := httpx.Get[User](ctx, srv.Client(), srv.URL)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	// A non-2xx response has nil err and a zero User; check the status.
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("name: %s\n", user.Name)

	// Output: name: Alice
}

func ExamplePost() {
	type CreateReq struct {
		Name string `json:"name"`
	}
	type User struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Bob"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user, resp, err := httpx.Post[User](ctx, srv.Client(), srv.URL, CreateReq{Name: "Bob"})
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	// A non-2xx response has nil err and a zero User; check the status.
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("created: %s\n", user.Name)

	// Output: created: Bob
}

func ExampleDo() {
	type User struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Charlie"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	user, resp, err := httpx.Do[User](srv.Client(), req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("name: %s\n", user.Name)

	// Output: name: Charlie
}

func ExampleNewAuthTransport() {
	echo := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "auth=%q", r.Header.Get("Authorization"))
	}
	api := httptest.NewServer(http.HandlerFunc(echo))
	defer api.Close()
	other := httptest.NewServer(http.HandlerFunc(echo))
	defer other.Close()

	// The token is bound to the api server's origin only.
	transport, err := httpx.NewAuthTransport(
		api.Client().Transport,
		httpx.AuthBinding{URL: api.URL, Provider: httpx.NewBearerTokenProvider("my-secret-token")},
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	for _, u := range []string{api.URL, other.URL} {
		resp, err := client.Get(u)
		if err != nil {
			fmt.Println(err)
			return
		}
		body, readErr := httpx.ReadAtMost(resp.Body, 1<<20)
		closeErr := resp.Body.Close()
		if readErr != nil {
			fmt.Println(readErr)
			return
		}
		if closeErr != nil {
			fmt.Println(closeErr)
			return
		}
		if resp.StatusCode != http.StatusOK {
			fmt.Println("unexpected status:", resp.Status)
			return
		}
		fmt.Println(string(body))
	}

	// Output:
	// auth="Bearer my-secret-token"
	// auth=""
}

func ExampleNewRetryTransport() {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	transport, err := httpx.NewRetryTransport(
		srv.Client().Transport,
		httpx.WithMaxAttempts(3),
		httpx.WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	resp, err := client.Get(srv.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	body, err := httpx.ReadAtMost(resp.Body, 1<<20)
	if err != nil {
		fmt.Println(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("status: %d, body: %s, attempts: %d\n", resp.StatusCode, body, attempts)

	// Output: status: 200, body: ok, attempts: 3
}

func ExampleCountingHook() {
	hook := &httpx.CountingHook{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	client, err := httpx.NewClient(httpx.WithMetrics(hook), httpx.WithTimeout(5*time.Second))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	for range 3 {
		resp, err := client.Get(srv.URL)
		if err != nil {
			fmt.Println(err)
			return
		}
		_, readErr := httpx.ReadAtMost(resp.Body, 1<<20)
		closeErr := resp.Body.Close()
		if readErr != nil {
			fmt.Println(readErr)
			return
		}
		if closeErr != nil {
			fmt.Println(closeErr)
			return
		}
		if resp.StatusCode != http.StatusOK {
			fmt.Println("unexpected status:", resp.Status)
			return
		}
	}

	fmt.Printf("requests: %d\n", hook.Requests.Load())

	// Output: requests: 3
}

func ExampleTransportStack() {
	client, err := httpx.NewClient(
		httpx.WithCompression(),
		httpx.WithRetry(httpx.WithMaxAttempts(3)),
		httpx.WithCircuitBreaker(),
		httpx.WithTimeout(5*time.Second),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	defer client.CloseIdleConnections()
	stack := httpx.TransportStack(client)
	fmt.Printf("stack depth: %d\n", len(stack))

	// Output: stack depth: 4
}

func ExampleEncodeRequest() {
	type Payload struct {
		Name string `json:"name"`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/api", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := httpx.EncodeRequest(req, httpx.JSON, Payload{Name: "Alice"}); err != nil {
		fmt.Println(err)
		return
	}
	// This example inspects the encoded request without sending it.
	defer req.Body.Close()

	fmt.Println(req.Header.Get("Content-Type"))

	// Output: application/json
}

func ExampleChain_Append() {
	base := httpx.DefaultChain()
	withAuth := base.Append(
		httpx.WithAuthT("https://api.example.com", httpx.NewBearerTokenProvider("token")),
	)

	client, err := withAuth.Client(nil, 30*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	fmt.Println("appended auth to default chain")
	// Output: appended auth to default chain
}

func ExampleNewCacheTransport() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		fmt.Fprint(w, "cached response")
	}))
	defer srv.Close()

	transport, err := httpx.NewCacheTransport(srv.Client().Transport, nil) // nil: default bounded memory store
	if err != nil {
		fmt.Println(err)
		return
	}
	defer transport.Close() // Stops any background cache revalidation.
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	// First request: cache miss
	resp, err := client.Get(srv.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	body, err := httpx.ReadAtMost(resp.Body, 1<<20)
	if err != nil {
		fmt.Println(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("first: %s\n", body)

	// Second request: cache hit
	resp, err = client.Get(srv.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	body, err = httpx.ReadAtMost(resp.Body, 1<<20)
	if err != nil {
		fmt.Println(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Printf("second: %s, Cache-Status: %s\n", body, resp.Header.Get("Cache-Status"))

	// Output:
	// first: cached response
	// second: cached response, Cache-Status: httpx; hit
}
