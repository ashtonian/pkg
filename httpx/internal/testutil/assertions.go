package testutil

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// AssertStatus checks that the response has the expected status code.
func AssertStatus(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		t.Errorf("expected status %d, got %d", expected, resp.StatusCode)
	}
}

// AssertHeader checks that the response has the expected header value.
func AssertHeader(t *testing.T, resp *http.Response, key, expected string) {
	t.Helper()
	actual := resp.Header.Get(key)
	if actual != expected {
		t.Errorf("expected header %s=%q, got %q", key, expected, actual)
	}
}

// AssertHeaderContains checks that the response header contains the expected value.
func AssertHeaderContains(t *testing.T, resp *http.Response, key, expected string) {
	t.Helper()
	actual := resp.Header.Get(key)
	if !strings.Contains(actual, expected) {
		t.Errorf("expected header %s to contain %q, got %q", key, expected, actual)
	}
}

// AssertBody checks that the response body matches the expected value.
func AssertBody(t *testing.T, resp *http.Response, expected string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if string(body) != expected {
		t.Errorf("expected body %q, got %q", expected, string(body))
	}
}

// AssertBodyContains checks that the response body contains the expected value.
func AssertBodyContains(t *testing.T, resp *http.Response, expected string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}
	if !strings.Contains(string(body), expected) {
		t.Errorf("expected body to contain %q, got %q", expected, string(body))
	}
}

// AssertNoError checks that no error occurred.
func AssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// AssertError checks that an error occurred.
func AssertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Error("expected an error, got nil")
	}
}

// AssertErrorContains checks that an error occurred with a message containing the expected string.
func AssertErrorContains(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Error("expected an error, got nil")
		return
	}
	if !strings.Contains(err.Error(), expected) {
		t.Errorf("expected error containing %q, got %q", expected, err.Error())
	}
}

// AssertEqual checks that two values are equal.
func AssertEqual(t *testing.T, expected, actual any) {
	t.Helper()
	if !reflect.DeepEqual(expected, actual) {
		t.Errorf("expected %v, got %v", expected, actual)
	}
}

// AssertTrue checks that a value is true.
func AssertTrue(t *testing.T, value bool, msg string) {
	t.Helper()
	if !value {
		t.Error(msg)
	}
}

// AssertFalse checks that a value is false.
func AssertFalse(t *testing.T, value bool, msg string) {
	t.Helper()
	if value {
		t.Error(msg)
	}
}

// AssertRequestHeader checks that a request has the expected header value.
func AssertRequestHeader(t *testing.T, req *http.Request, key, expected string) {
	t.Helper()
	actual := req.Header.Get(key)
	if actual != expected {
		t.Errorf("expected request header %s=%q, got %q", key, expected, actual)
	}
}

// AssertRequestHeaderContains checks that a request header contains the expected value.
func AssertRequestHeaderContains(t *testing.T, req *http.Request, key, expected string) {
	t.Helper()
	actual := req.Header.Get(key)
	if !strings.Contains(actual, expected) {
		t.Errorf("expected request header %s to contain %q, got %q", key, expected, actual)
	}
}

// AssertNil checks that a value is nil.
func AssertNil(t *testing.T, v any) {
	t.Helper()
	if v != nil && !reflect.ValueOf(v).IsNil() {
		t.Errorf("expected nil, got %v", v)
	}
}

// AssertNotNil checks that a value is not nil.
func AssertNotNil(t *testing.T, v any) {
	t.Helper()
	if v == nil || reflect.ValueOf(v).IsNil() {
		t.Error("expected non-nil value")
	}
}
