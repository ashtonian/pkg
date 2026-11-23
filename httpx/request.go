package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Get sends a GET request and decodes the response body into T.
//
// The codec is auto-detected from the response Content-Type using the global
// codec registry. Only 2xx responses are decoded; non-2xx responses return
// zero T with the response body intact and nil error unless WithProblemErrors
// is enabled. A body larger than WithMaxResponseBytes (default
// DefaultMaxResponseBytes) fails with
// ErrResponseTooLarge.
//
// All transport decorators on the client (retry, auth, cache, compression, etc.)
// apply transparently.
//
//	user, resp, err := httpx.Get[User](ctx, client, "https://api.example.com/users/1")
func Get[T any](ctx context.Context, client *http.Client, url string, opts ...DecodeOption) (T, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		var zero T
		return zero, nil, err
	}
	return Do[T](client, req, opts...)
}

// Post sends a POST request with an encoded body and decodes the response into T.
//
// The body is encoded using the JSON codec by default. Pass nil for no body.
// If body implements [io.Reader], it is used directly without encoding.
//
//	created, resp, err := httpx.Post[User](ctx, client, url, CreateUserReq{Name: "Alice"})
func Post[T any](ctx context.Context, client *http.Client, url string, body any, opts ...DecodeOption) (T, *http.Response, error) {
	return doWithBody[T](ctx, client, http.MethodPost, url, body, opts)
}

// Put sends a PUT request with an encoded body and decodes the response into T.
//
//	updated, resp, err := httpx.Put[User](ctx, client, url, UpdateUserReq{Name: "Bob"})
func Put[T any](ctx context.Context, client *http.Client, url string, body any, opts ...DecodeOption) (T, *http.Response, error) {
	return doWithBody[T](ctx, client, http.MethodPut, url, body, opts)
}

// Patch sends a PATCH request with an encoded body and decodes the response into T.
func Patch[T any](ctx context.Context, client *http.Client, url string, body any, opts ...DecodeOption) (T, *http.Response, error) {
	return doWithBody[T](ctx, client, http.MethodPatch, url, body, opts)
}

// Delete sends a DELETE request. The response body is not decoded.
func Delete(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// Do executes a request and decodes the response body into T.
//
// This is the lowest-level generic helper. Use it when you need full control
// over the request (custom headers, custom codec via [EncodeRequest], etc.).
//
// If the request has no Accept header, Do sends a copy of it that lists the
// media types it can decode (see WithResponseCodecs), so req itself is left
// unchanged for reuse. Only 2xx responses are decoded, by the codec for the
// response's media type, matched case-insensitively and ignoring parameters;
// a malformed Content-Type is ErrNoCodec. Non-2xx responses return zero T
// with the response body intact and nil error, unless WithProblemErrors is
// enabled. That option returns *Problem for accepted problem media types.
// The body is read up to WithMaxResponseBytes: a larger one fails with
// ErrResponseTooLarge and the returned response still reads the whole body,
// which the caller must close.
// After decoding (or a decode error) resp.Body holds the bytes read. A body
// read failure returns the response with that error: its status and headers
// arrived, the helper has closed the body, and resp.Body replays the bytes
// received and then fails with the same error. T is decoded through a *T, as
// DecodeResponse would: a JSON null leaves a pointer T nil. HEAD, 204 and 205
// responses return zero T without decoding, even when their headers include
// a Content-Type.
//
//	req, _ := http.NewRequest("POST", url, nil)
//	httpx.EncodeRequest(req, msgpack.Codec, body)
//	user, resp, err := httpx.Do[User](client, req)
func Do[T any](client *http.Client, req *http.Request, opts ...DecodeOption) (T, *http.Response, error) {
	var zero T
	cfg, err := newDecodeConfig(opts)
	if err != nil {
		closeRequestBody(req)
		return zero, nil, err
	}

	resp, err := client.Do(withAccept(req, &cfg))
	if err != nil {
		return zero, nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if cfg.problemErrors && req.Method != http.MethodHead {
			problem, err := decodeProblem(resp, cfg)
			if err == nil {
				return zero, resp, problem
			}
			if !errors.Is(err, ErrNotProblem) {
				return zero, resp, err
			}
		}
		return zero, resp, nil
	}
	// These successful responses carry no representation. Content-Type
	// can still describe the corresponding resource, especially for HEAD.
	if req.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusResetContent {
		return zero, resp, nil
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		return zero, resp, nil
	}
	mediaType, err := mediaTypeKey(contentType)
	if err != nil {
		return zero, resp, fmt.Errorf("%w: Content-Type %q: %w", ErrNoCodec, contentType, err)
	}
	codec, ok := cfg.codecFor(mediaType)
	if !ok {
		return zero, resp, fmt.Errorf("%w: %s", ErrNoCodec, mediaType)
	}

	data, err := readBody(resp, cfg.maxBytes)
	if err != nil {
		return zero, resp, err
	}
	result, err := decodeInto[T](codec, data)
	if err != nil {
		return zero, resp, err
	}
	return result, resp, nil
}

// doWithBody creates a request with an encoded body and calls Do[T].
func doWithBody[T any](ctx context.Context, client *http.Client, method, url string, body any, opts []DecodeOption) (T, *http.Response, error) {
	var zero T

	var reqBody io.Reader
	var contentType string

	switch v := body.(type) {
	case nil:
		// No body.
	case io.Reader:
		// Use directly without encoding.
		reqBody = v
	default:
		// Encode with JSON codec.
		data, err := JSON.Encode(body)
		if err != nil {
			return zero, nil, err
		}
		reqBody = bytes.NewReader(data)
		contentType = JSON.ContentType()
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return zero, nil, err
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	return Do[T](client, req, opts...)
}

// withAccept returns the request Do sends: req itself when it names an
// Accept or there is none to add, otherwise a copy carrying the helper's.
// Writing it into req would make a later call on the same request take it
// for the caller's own preference.
func withAccept(req *http.Request, cfg *decodeConfig) *http.Request {
	if req.Header.Get("Accept") != "" {
		return req
	}
	accept := cfg.accept()
	if accept == "" {
		return req
	}
	sent := req.WithContext(req.Context())
	sent.Header = req.Header.Clone()
	if sent.Header == nil {
		sent.Header = make(http.Header, 1)
	}
	sent.Header.Set("Accept", accept)
	return sent
}
