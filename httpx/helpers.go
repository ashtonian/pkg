package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// replayBody gives every attempt of a request its body. With GetBody each
// attempt after the first gets a fresh copy from it. Without, the first
// attempt sends the body through a recorder capped at a byte limit, and a
// later attempt replays the recording — possible only once the first attempt
// has read the body to its end within the limit. Nothing is read ahead of the
// transport, so a stalled stream is cancelled by whatever cancels the
// attempt, and no goroutine is started.
type replayBody struct {
	getBody func() (io.ReadCloser, error)
	rec     *recordingBody // the recorder, without GetBody
	none    bool           // the request has no body
}

// newReplayBody prepares first — a clone the caller owns, whose Body may be
// replaced — for further attempts.
func newReplayBody(first *http.Request, limit int64) *replayBody {
	switch {
	case first.Body == nil || first.Body == http.NoBody:
		return &replayBody{none: true}
	case first.GetBody != nil:
		return &replayBody{getBody: first.GetBody}
	}
	rec := &recordingBody{body: first.Body, limit: limit}
	first.Body = rec
	return &replayBody{rec: rec}
}

// ready reports whether another attempt can carry the same body.
func (b *replayBody) ready() bool {
	return b.none || b.getBody != nil || b.rec.complete()
}

// next clones req for another attempt with a fresh body. Call it only after
// ready reported true.
func (b *replayBody) next(req *http.Request) (*http.Request, error) {
	next := req.Clone(req.Context())
	switch {
	case b.none:
	case b.getBody != nil:
		body, err := b.getBody()
		if err != nil {
			return nil, fmt.Errorf("replay request body: %w", err)
		}
		next.Body = body
	default:
		next.Body = io.NopCloser(bytes.NewReader(b.rec.recorded()))
	}
	return next, nil
}

// recordingBody passes a request body through to the transport and keeps a
// copy of what it read, up to limit bytes. The transport may read it from
// another goroutine, even after RoundTrip returns.
type recordingBody struct {
	body  io.ReadCloser
	limit int64

	mu   sync.Mutex
	buf  []byte
	over bool // more than limit bytes were read: no replay
	done bool // read to its end within limit
}

func (r *recordingBody) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.over {
		if int64(len(r.buf))+int64(n) > r.limit {
			r.over, r.buf = true, nil
		} else {
			r.buf = append(r.buf, p[:n]...)
		}
	}
	if errors.Is(err, io.EOF) && !r.over {
		r.done = true
	}
	return n, err
}

func (r *recordingBody) Close() error { return r.body.Close() }

func (r *recordingBody) complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// recorded returns the whole body; it does not change once complete.
func (r *recordingBody) recorded() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf
}

// prefixedBody reads a buffered prefix and then the rest of the original
// body, and closes the original.
type prefixedBody struct {
	io.Reader
	io.Closer
}

// cloneRequest creates a shallow clone of a request.
func cloneRequest(req *http.Request) *http.Request {
	return req.Clone(req.Context())
}

// closeRequestBody closes the request body when a transport rejects a
// request without sending it; the RoundTripper contract makes the transport
// responsible for closing it even on error.
func closeRequestBody(req *http.Request) {
	if req.Body != nil && req.Body != http.NoBody {
		_ = req.Body.Close() // the request failed before it was sent; nothing to report
	}
}

// drainAndClose reads remaining body and closes it for connection reuse.
func drainAndClose(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		// Synthetic transport responses may have no originating request.
		ctx := context.Background()
		if resp.Request != nil {
			ctx = resp.Request.Context()
		}
		// This discarded response cannot replace the retry/cache/hedge decision.
		DrainAndClose(ctx, resp.Body, 8192)
	}
}
