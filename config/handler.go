package config

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ReloadHandler returns an http.Handler that triggers a config reload
// on POST requests. On success it returns 200 with the new (redacted)
// config as JSON. On failure it returns 500 with the error message;
// the previous config is preserved.
//
// Authentication and authorization are the caller's responsibility.
// Mount at your preferred path, e.g.:
//
//	mux.Handle("POST /admin/config/reload", reloader.ReloadHandler())
func (r *Reloader[T]) ReloadHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		cfg, err := r.Reload(req.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		m := ToMap(cfg, true) // redacted
		if err := json.NewEncoder(w).Encode(m); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	})
}
