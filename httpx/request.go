package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Get sends a GET request and decodes the response body into T.
//
// The codec is auto-detected from the response Content-Type using the global
// codec registry. Only 2xx responses are decoded; non-2xx responses return
// zero T with the response body intact and nil error. A body larger than
// WithMaxResponseBytes (default DefaultMaxResponseBytes) fails with
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
// If the request has no Accept header, one is set from registered codecs.
// Only 2xx responses are decoded; non-2xx responses return zero T with the
// response body intact and nil error. The body is read up to
// WithMaxResponseBytes: a larger one fails with ErrResponseTooLarge and the
// returned response still reads the whole body, which the caller must close.
// After decoding (or a decode error) resp.Body holds the bytes read. When T
// is a pointer type the codec decodes into a newly allocated pointee.
// HEAD, 204 and 205 responses return zero T without decoding, even when
// their headers include a Content-Type.
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

	// Set Accept header from registered codecs if not already set.
	if req.Header.Get("Accept") == "" {
		if accept := registeredAccept(); accept != "" {
			req.Header.Set("Accept", accept)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return zero, nil, err
	}

	// Only decode 2xx responses.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, resp, nil
	}
	// These successful responses carry no representation. Content-Type
	// can still describe the corresponding resource, especially for HEAD.
	if req.Method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusResetContent {
		return zero, resp, nil
	}

	// Find codec for response Content-Type.
	// If no Content-Type is set, return zero T without error.
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		return zero, resp, nil
	}
	if idx := strings.Index(contentType, ";"); idx != -1 {
		contentType = strings.TrimSpace(contentType[:idx])
	}

	codec, ok := GetCodec(contentType)
	if !ok {
		return zero, resp, fmt.Errorf("%w: %s", ErrNoCodec, contentType)
	}

	data, err := readBody(resp, cfg.maxBytes)
	if errors.Is(err, ErrResponseTooLarge) {
		return zero, resp, err
	}
	if err != nil {
		return zero, nil, err
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

// registeredAccept builds an Accept header value from all registered codecs.
func registeredAccept() string {
	registered := RegisteredCodecs()
	if len(registered) == 0 {
		return ""
	}
	types := make([]string, len(registered))
	for i, c := range registered {
		types[i] = c.ContentType()
	}
	return strings.Join(types, ", ")
}
