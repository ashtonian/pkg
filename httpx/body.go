package httpx

import (
	"io"
	"net/http"
	"sync"
)

// releaseWhenBodyDone arranges for release to run exactly once when the
// caller is finished with resp: when its body reaches EOF or a read error,
// or is closed. A response without a body releases immediately. The
// response of a protocol upgrade (101) keeps the body's writer so the
// connection stays usable.
func releaseWhenBodyDone(resp *http.Response, release func()) *http.Response {
	if resp.Body == nil || resp.Body == http.NoBody {
		release()
		return resp
	}
	b := &releasingBody{body: resp.Body, release: sync.OnceFunc(release)}
	if w, ok := resp.Body.(io.Writer); ok {
		resp.Body = &releasingReadWriteBody{releasingBody: b, w: w}
	} else {
		resp.Body = b
	}
	return resp
}

type releasingBody struct {
	body    io.ReadCloser
	release func()
}

func (b *releasingBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *releasingBody) Close() error {
	err := b.body.Close()
	b.release()
	return err
}

type releasingReadWriteBody struct {
	*releasingBody
	w io.Writer
}

func (b *releasingReadWriteBody) Write(p []byte) (int, error) { return b.w.Write(p) }

// idleConnectionsCloser is implemented by transports that pool connections.
type idleConnectionsCloser interface {
	CloseIdleConnections()
}

// closeIdleConnections forwards to rt when it pools connections.
func closeIdleConnections(rt http.RoundTripper) {
	if c, ok := rt.(idleConnectionsCloser); ok {
		c.CloseIdleConnections()
	}
}
