package main

import (
	"bytes"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"
)

const (
	// requestIDHeader carries a request's ID: in from a proxy that assigned
	// one, out on every answer.
	requestIDHeader = "X-Request-ID"
	// maxRequestIDLen bounds an incoming request ID the log repeats.
	maxRequestIDLen = 128
	// maxLoggedBody bounds each body a trace entry carries.
	maxLoggedBody = 4 << 10
)

// requestLog writes one entry per request, after it is answered: Info with
// the method, route, status, duration and request ID; at debug also the
// body sizes, the client address and the query parameter names; at trace
// also the request and response bodies, each capped at maxLoggedBody. The
// example's bodies hold no credentials or personal data; a service whose
// bodies do redacts them before they reach the entry.
type requestLog struct {
	log *slog.Logger
	// newID mints the ID of a request that arrives without a usable one.
	newID func() string
	// routes resolves a request's route template.
	routes *http.ServeMux
	// traceRoutes are routes logged at trace rather than Info.
	traceRoutes map[string]bool
}

// wrap serves next with each request's ID in its context and its answer's
// header, and logs the request once it is answered.
func (l requestLog) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(requestIDHeader)
		if !usableRequestID(id) {
			id = l.newID()
		}
		ctx := withRequestID(r.Context(), id)
		_, route := l.routes.Handler(r)
		level := slog.LevelInfo
		if l.traceRoutes[route] {
			level = levelTrace
		}
		w.Header().Set(requestIDHeader, id)
		tracing := l.log.Enabled(ctx, levelTrace)
		served := r.WithContext(ctx)
		body := r.Body
		if body == nil {
			body = http.NoBody
		}
		in := &countingBody{ReadCloser: body, keep: tracing}
		served.Body = in
		out := &recorder{ResponseWriter: w, keep: tracing}
		next.ServeHTTP(out, served)

		if !l.log.Enabled(ctx, level) {
			return
		}
		status := out.status
		if status == 0 {
			status = http.StatusOK // net/http's answer to a handler that wrote nothing
		}
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("route", route),
			slog.Int("status", status),
			slog.Float64("duration_seconds", time.Since(start).Seconds()),
		}
		if l.log.Enabled(ctx, slog.LevelDebug) {
			attrs = append(attrs,
				slog.Int64("request_bytes", in.n),
				slog.Int64("response_bytes", out.n),
				slog.String("client_addr", r.RemoteAddr),
				slog.Any("query_params", slices.Sorted(maps.Keys(r.URL.Query()))),
			)
		}
		if tracing {
			attrs = append(attrs,
				slog.String("request_body", in.kept.String()),
				slog.Bool("request_body_truncated", in.kept.truncated),
				slog.String("response_body", out.kept.String()),
				slog.Bool("response_body_truncated", out.kept.truncated),
			)
		}
		l.log.LogAttrs(ctx, level, "request", attrs...)
	})
}

// usableRequestID reports an incoming request ID the log and the answer
// may repeat: 1 to maxRequestIDLen letters, digits, '-', '_' or '.'.
func usableRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range []byte(id) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// cappedBuffer keeps the first maxLoggedBody bytes written to it.
type cappedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) add(p []byte) {
	room := maxLoggedBody - b.Len()
	if len(p) > room {
		p = p[:room]
		b.truncated = true
	}
	b.Write(p)
}

// countingBody counts the request body bytes the handler reads, keeping the
// first of them when keep is set.
type countingBody struct {
	io.ReadCloser
	keep bool
	n    int64
	kept cappedBuffer
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if b.keep {
		b.kept.add(p[:n])
	}
	return n, err
}

// recorder notes the status and counts the body bytes of an answer,
// keeping the first of them when keep is set.
type recorder struct {
	http.ResponseWriter
	keep   bool
	status int
	n      int64
	kept   cappedBuffer
}

func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *recorder) Write(p []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(p)
	rec.n += int64(n)
	if rec.keep {
		rec.kept.add(p[:n])
	}
	return n, err
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }
