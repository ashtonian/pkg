//go:build unit

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every request is served under the configured request timeout.
func TestServer_RequestsCarryTheRequestTimeout(t *testing.T) {
	const timeout = time.Hour
	var deadline time.Time
	var ok bool
	s := &server{requestTimeout: timeout}
	h := s.withDeadline(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok = r.Context().Deadline()
	}))
	before := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/products", nil))
	after := time.Now()
	assert.True(t, ok, "the request has a deadline")
	assert.False(t, deadline.Before(before.Add(timeout)), "the deadline is the request timeout from when the request is served")
	assert.False(t, deadline.After(after.Add(timeout)))
}

func TestLogger_NamesTheTraceLevel(t *testing.T) {
	level, err := parseLevel("trace")
	require.NoError(t, err)
	assert.Equal(t, levelTrace, level)
	var logs bytes.Buffer
	newLogger(&logs, levelTrace).Log(t.Context(), levelTrace, "detail")
	assert.Contains(t, logs.String(), `"level":"TRACE"`)
}

// logEntries are the JSON entries of logs, one per line.
func logEntries(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	for line := range strings.Lines(logs.String()) {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		entries = append(entries, entry)
	}
	return entries
}

// requestLogged serves one request through a requestLog at level and
// returns the answer and the entries logged.
func requestLogged(t *testing.T, level slog.Level, req *http.Request) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	log := newLogger(&logs, level)
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		log.WarnContext(r.Context(), "inside the handler")
		w.WriteHeader(http.StatusAccepted)
		_, err = w.Write(append([]byte("got "), body...))
		assert.NoError(t, err)
	})
	mux.HandleFunc(schemasRoute, func(w http.ResponseWriter, _ *http.Request) {
		_, err := io.WriteString(w, "{}")
		assert.NoError(t, err)
	})
	l := requestLog{log: log, newID: func() string { return "minted" }, routes: mux, traceRoutes: map[string]bool{schemasRoute: true}}
	rec := httptest.NewRecorder()
	l.wrap(mux).ServeHTTP(rec, req)
	return rec, logEntries(t, &logs)
}

// Each request has an ID, in its answer and on every entry logged under
// it, and one Info entry once answered with its method, route template,
// status and duration.
func TestRequestLog_OneEntryPerRequest(t *testing.T) {
	for _, tt := range []struct{ name, header, want string }{
		{"an ID is minted", "", "minted"},
		{"a proxy's ID is kept", "proxy-7.a_b", "proxy-7.a_b"},
		{"an ID the log should not repeat is replaced", "a b\nc", "minted"},
		{"an ID past its bound is replaced", strings.Repeat("a", maxRequestIDLen+1), "minted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/items/7?b=1&a=2", strings.NewReader(`{"x":1}`))
			if tt.header != "" {
				req.Header.Set(requestIDHeader, tt.header)
			}
			rec, entries := requestLogged(t, slog.LevelInfo, req)
			assert.Equal(t, tt.want, rec.Header().Get(requestIDHeader))
			require.Len(t, entries, 2)
			assert.Equal(t, "inside the handler", entries[0]["msg"])
			assert.Equal(t, tt.want, entries[0]["request_id"], "an entry inside the request names it")
			entry := entries[1]
			assert.Equal(t, "request", entry["msg"])
			assert.Equal(t, "INFO", entry["level"])
			assert.Equal(t, tt.want, entry["request_id"])
			assert.Equal(t, "PATCH", entry["method"])
			assert.Equal(t, "PATCH /api/items/{id}", entry["route"])
			assert.InDelta(t, http.StatusAccepted, entry["status"], 0)
			assert.Contains(t, entry, "duration_seconds")
			assert.NotContains(t, entry, "request_bytes", "sizes are debug detail")
		})
	}
}

// Debug adds the sizes, the client address and the query parameter names;
// trace adds the bodies, capped.
func TestRequestLog_LowerLevelsAddDetail(t *testing.T) {
	body := `{"x":"` + strings.Repeat("y", maxLoggedBody) + `"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/items/7?b=1&a=2", strings.NewReader(body))
	_, entries := requestLogged(t, slog.LevelDebug, req)
	entry := entries[len(entries)-1]
	assert.InDelta(t, len(body), entry["request_bytes"], 0)
	assert.InDelta(t, len("got ")+len(body), entry["response_bytes"], 0)
	assert.Equal(t, req.RemoteAddr, entry["client_addr"])
	assert.Equal(t, []any{"a", "b"}, entry["query_params"])
	assert.NotContains(t, entry, "request_body", "bodies are trace detail")

	req = httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/items/7", strings.NewReader(body))
	_, entries = requestLogged(t, levelTrace, req)
	entry = entries[len(entries)-1]
	assert.Equal(t, body[:maxLoggedBody], entry["request_body"])
	assert.Equal(t, ("got " + body)[:maxLoggedBody], entry["response_body"])
	for _, key := range []string{"request_body_truncated", "response_body_truncated"} {
		truncated, ok := entry[key].(bool)
		assert.True(t, ok && truncated, key)
	}
}

// Schema discovery, which clients poll, logs at trace.
func TestRequestLog_SchemaRoutesLogAtTrace(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/schemas", nil)
	_, entries := requestLogged(t, slog.LevelDebug, req)
	assert.Empty(t, entries)
	_, entries = requestLogged(t, levelTrace, req)
	require.Len(t, entries, 1)
	assert.Equal(t, "TRACE", entries[0]["level"])
	assert.Equal(t, schemasRoute, entries[0]["route"])
}

// testSettings are the defaults with addr.
func testSettings(t *testing.T, addr string) *serverSettings {
	t.Helper()
	settings, err := settingsFrom(t, "--addr", addr)
	require.NoError(t, err)
	return settings
}

// The server logs the address it bound, after binding it, serves there,
// and once its context ends releases the signals, logs its shutdown and
// returns.
func TestServe_ListensThenShutsDown(t *testing.T) {
	out, in := io.Pipe()
	log := newLogger(in, slog.LevelInfo)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, func() { close(stopped) }, testSettings(t, "127.0.0.1:0"), handler, log)
		assert.NoError(t, in.Close())
	}()
	lines := bufio.NewScanner(out)
	next := func() map[string]any {
		require.True(t, lines.Scan(), "another entry")
		var entry map[string]any
		require.NoError(t, json.Unmarshal(lines.Bytes(), &entry), lines.Text())
		return entry
	}

	listening := next()
	assert.Equal(t, "listening", listening["msg"])
	addr, ok := listening["addr"].(string)
	require.True(t, ok)
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	assert.NotEqual(t, "0", port, "the entry names the port bound")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/", nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())
	assert.Equal(t, http.StatusNoContent, res.StatusCode)

	cancel()
	assert.Equal(t, "shutting down", next()["msg"])
	<-stopped
	require.NoError(t, <-served)
}

// An address it cannot bind fails the server before it says it listens.
func TestServe_AnAddressInUseIsNoListening(t *testing.T) {
	var lc net.ListenConfig
	taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { assert.NoError(t, taken.Close()) }()
	var logs bytes.Buffer
	err = serve(t.Context(), func() {}, testSettings(t, taken.Addr().String()), http.NotFoundHandler(), newLogger(&logs, slog.LevelInfo))
	require.ErrorContains(t, err, "listening on "+taken.Addr().String())
	assert.Empty(t, logs.String())
}
