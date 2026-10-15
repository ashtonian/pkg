//go:build unit

package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestCheckResponseClosesAndChecksStatus(t *testing.T) {
	decodeErr := errors.New("decode failure")
	for _, tc := range []struct {
		name       string
		status     int
		requestErr error
		wantErr    bool
	}{
		{"success", 200, nil, false}, {"status error", 401, nil, true}, {"decode error with response", 200, decodeErr, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(`{"message":"denied"}`)}
			err := checkResponse(&http.Response{StatusCode: tc.status, Body: body}, tc.requestErr, http.StatusOK)
			if (err != nil) != tc.wantErr || !body.closed {
				t.Fatalf("error=%v, closed=%v", err, body.closed)
			}
			if tc.requestErr != nil && !errors.Is(err, tc.requestErr) {
				t.Fatal("lost original request error")
			}
		})
	}
}

func TestReadAPIError(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader(`{"code":"NOT_FOUND","message":"user not found"}`)}
	apiErr, err := readAPIError(&http.Response{StatusCode: 404, Body: body}, nil, http.StatusNotFound)
	if err != nil || apiErr.Code != "NOT_FOUND" || !body.closed {
		t.Fatalf("error body=%+v err=%v closed=%v", apiErr, err, body.closed)
	}
}
