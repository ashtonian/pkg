//go:build unit

package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadHandler_Post(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler := r.ReloadHandler()
	req := httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Response should be valid JSON with redacted config.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", body["host"])
	}
}

func TestReloadHandler_MethodNotAllowed(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler := r.ReloadHandler()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/admin/config/reload", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestReloadHandler_ReloadError(t *testing.T) {
	// Start with valid config, then make the file invalid.
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte("name: valid\n"), 0o644)

	r, err := NewReloader[validatedCfg](t.Context(), nil,
		WithMustConfigFile(cfgFile),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Delete the required file so reload fails.
	os.Remove(cfgFile)

	handler := r.ReloadHandler()
	req := httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestReloadHandler_RedactsSensitiveFields(t *testing.T) {
	type sensitiveCfg struct {
		Host     string `koanf:"host" default:"localhost"`
		Password string `koanf:"password" sensitive:"true" default:"secret123"`
	}

	r, err := NewReloader[sensitiveCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler := r.ReloadHandler()
	req := httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)

	if body["password"] != "[REDACTED]" {
		t.Errorf("password = %v, want [REDACTED]", body["password"])
	}
	if body["host"] != "localhost" {
		t.Errorf("host = %v, want localhost", body["host"])
	}
}
