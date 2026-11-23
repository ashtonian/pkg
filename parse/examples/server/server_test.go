//go:build unit

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
