//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type cleanupBody struct {
	read     func([]byte) (int, error)
	reads    int
	closes   int
	bytes    int
	closeErr error
}

func (b *cleanupBody) Read(buffer []byte) (int, error) {
	b.reads++
	n, err := b.read(buffer)
	b.bytes += n
	return n, err
}

func (b *cleanupBody) Close() error { b.closes++; return b.closeErr }

func TestDrainAndCloseBoundsRemainingBytesWithoutProbing(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		budget  int64
		bytes   int64
		reads   int
		eof     bool
		invalid bool
	}{
		{name: "over budget", body: "abcde", budget: 3, bytes: 3, reads: 1},
		{name: "exact budget is unknown", body: "abc", budget: 3, bytes: 3, reads: 1},
		{name: "under budget proves EOF", body: "ab", budget: 3, bytes: 2, reads: 2, eof: true},
		{name: "empty reader proves EOF", budget: 1, reads: 1, eof: true},
		{name: "zero is close only", body: "abc"},
		{name: "negative is invalid", body: "abc", budget: -1, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := strings.NewReader(test.body)
			body := &cleanupBody{read: reader.Read}
			result := DrainAndClose(t.Context(), body, test.budget)
			if result.BytesDrained != test.bytes || result.EOF != test.eof || body.reads != test.reads || body.closes != 1 || body.bytes != int(test.bytes) {
				t.Fatalf("result=%+v reads=%d closes=%d bytes=%d", result, body.reads, body.closes, body.bytes)
			}
			if test.invalid {
				if !errors.Is(result.DrainErr, ErrInvalidConfig) {
					t.Fatalf("want ErrInvalidConfig, got %v", result.DrainErr)
				}
			} else if result.DrainErr != nil {
				t.Fatalf("unexpected drain error: %v", result.DrainErr)
			}
			if result.CloseErr != nil {
				t.Fatal(result.CloseErr)
			}
		})
	}
}

func TestDrainAndClosePreservesReadAndCloseErrorsAndEOFProof(t *testing.T) {
	readErr := errors.New("synthetic read failure")
	closeErr := errors.New("synthetic close failure")
	for _, test := range []struct {
		name string
		err  error
		eof  bool
	}{
		{name: "bytes with EOF", err: io.EOF, eof: true},
		{name: "bytes with failure", err: readErr},
		{name: "unexpected EOF is a failure", err: io.ErrUnexpectedEOF},
		{name: "joined EOF does not hide another error", err: errors.Join(io.EOF, readErr)},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &cleanupBody{read: func(buffer []byte) (int, error) { return copy(buffer, "abc"), test.err }, closeErr: closeErr}
			result := DrainAndClose(t.Context(), body, 3)
			if result.BytesDrained != 3 || result.EOF != test.eof || result.CloseErr != closeErr || body.reads != 1 || body.closes != 1 {
				t.Fatalf("result=%+v reads=%d closes=%d", result, body.reads, body.closes)
			}
			if test.eof {
				if result.DrainErr != nil {
					t.Fatal(result.DrainErr)
				}
			} else if result.DrainErr != test.err {
				t.Fatalf("read error identity lost: got %v want %v", result.DrainErr, test.err)
			}
		})
	}
}

func TestDrainAndCloseHandlesNilAndKnownEmptyBodies(t *testing.T) {
	var typedNil *cleanupBody
	for _, body := range []io.ReadCloser{nil, typedNil, http.NoBody} {
		result := DrainAndClose(t.Context(), body, 0)
		if !result.EOF || result.BytesDrained != 0 || result.DrainErr != nil || result.CloseErr != nil {
			t.Fatalf("body=%T result=%+v", body, result)
		}
	}
	body := &cleanupBody{read: func([]byte) (int, error) { t.Fatal("invalid context read the body"); return 0, nil }}
	result := DrainAndClose(nil, body, 1)
	if !errors.Is(result.DrainErr, ErrInvalidConfig) || body.reads != 0 || body.closes != 1 {
		t.Fatalf("result=%+v reads=%d closes=%d", result, body.reads, body.closes)
	}
}

func TestDrainAndCloseAlwaysClosesOnInvalidBudgetAndCancellation(t *testing.T) {
	closeErr := errors.New("synthetic close failure")
	for _, mode := range []string{"invalid budget", "pre-canceled", "expired", "between reads"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			budget := int64(4)
			wantErr := error(context.Canceled)
			wantReads := 0
			switch mode {
			case "invalid budget":
				budget, wantErr = -1, ErrInvalidConfig
			case "pre-canceled":
				cancel()
			case "expired":
				var expire context.CancelFunc
				ctx, expire = context.WithDeadline(ctx, time.Time{})
				defer expire()
				wantErr = context.DeadlineExceeded
			case "between reads":
				wantReads = 1
			}
			body := &cleanupBody{read: func(buffer []byte) (int, error) { cancel(); return copy(buffer, "a"), nil }, closeErr: closeErr}
			result := DrainAndClose(ctx, body, budget)
			if !errors.Is(result.DrainErr, wantErr) || result.CloseErr != closeErr || body.reads != wantReads || body.closes != 1 || result.EOF {
				t.Fatalf("result=%+v reads=%d closes=%d", result, body.reads, body.closes)
			}
		})
	}
}

func TestDrainAndCloseDoesNotSpinOnEmptyReads(t *testing.T) {
	body := &cleanupBody{read: func([]byte) (int, error) { return 0, nil }}
	result := DrainAndClose(t.Context(), body, 1)
	if !errors.Is(result.DrainErr, io.ErrNoProgress) || body.reads <= 1 || body.reads >= 1000 || body.closes != 1 || result.BytesDrained != 0 || result.EOF {
		t.Fatalf("result=%+v reads=%d closes=%d", result, body.reads, body.closes)
	}
}

func TestDrainAndCloseCannotInterruptAnUncooperativeReader(t *testing.T) {
	reading, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	body := &cleanupBody{read: func(buffer []byte) (int, error) { close(reading); <-unblock; return copy(buffer, "a"), nil }}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan CleanupResult, 1)
	go func() { result <- DrainAndClose(ctx, body, 2) }()
	<-reading
	cancel()
	select {
	case got := <-result:
		t.Fatalf("uncooperative read was abandoned: %+v", got)
	default:
	}
	release()
	got := <-result
	if !errors.Is(got.DrainErr, context.Canceled) || got.BytesDrained != 1 || body.closes != 1 {
		t.Fatalf("result=%+v closes=%d", got, body.closes)
	}
}

type cleanupTransport struct {
	body       io.ReadCloser
	idleCloses int
}

func (transport *cleanupTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {ContentTypeJSON}}, Body: transport.body, Request: request}, nil
}

func (transport *cleanupTransport) CloseIdleConnections() { transport.idleCloses++ }

func TestDrainAndCloseLeavesProtocolDecisionAndTransportOwnershipWithCaller(t *testing.T) {
	reader := strings.NewReader("unbounded response contents")
	body := &cleanupBody{read: reader.Read}
	transport := &cleanupTransport{body: body}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, itemEndpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := Do[named](client, request)
	if err != nil || response == nil || body.reads != 0 || body.closes != 0 {
		t.Fatalf("response=%v error=%v reads=%d closes=%d", response, err, body.reads, body.closes)
	}
	result := DrainAndClose(ctx, response.Body, 3)
	if result.BytesDrained != 3 || result.EOF || result.DrainErr != nil || result.CloseErr != nil || body.closes != 1 || transport.idleCloses != 0 || ctx.Err() != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("result=%+v bodyCloses=%d transportCloses=%d context=%v status=%d", result, body.closes, transport.idleCloses, ctx.Err(), response.StatusCode)
	}
}

func TestDrainAndCloseOwnsOnlyRemainingExposedBytesAfterBoundedRead(t *testing.T) {
	reader := strings.NewReader("abcdef")
	body := &cleanupBody{read: reader.Read}
	_, err := ReadAtMost(body, 2)
	if !errors.Is(err, ErrResponseTooLarge) || body.closes != 0 {
		t.Fatalf("bounded read error=%v closes=%d", err, body.closes)
	}
	before := body.bytes
	result := DrainAndClose(t.Context(), body, 2)
	if result.BytesDrained != 2 || body.bytes != before+2 || result.EOF || body.closes != 1 {
		t.Fatalf("result=%+v before=%d after=%d closes=%d", result, before, body.bytes, body.closes)
	}

	reader = strings.NewReader(`{"name":"oversized"}`)
	body = &cleanupBody{read: reader.Read}
	response := &http.Response{Body: body}
	var value named
	err = DecodeResponse(response, JSON, &value, WithMaxResponseBytes(4))
	if !errors.Is(err, ErrResponseTooLarge) || body.closes != 0 {
		t.Fatalf("decode error=%v closes=%d", err, body.closes)
	}
	before = body.bytes
	result = DrainAndClose(t.Context(), response.Body, 2)
	if result.BytesDrained != 2 || body.bytes != before || result.EOF || body.closes != 1 {
		t.Fatalf("buffered prefix not counted: result=%+v before=%d after=%d closes=%d", result, before, body.bytes, body.closes)
	}
}
