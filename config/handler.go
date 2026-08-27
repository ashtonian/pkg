package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ReloadHandler returns an http.Handler that triggers a config reload
// on POST requests. On success it returns 200 with the new (redacted)
// config as JSON. On failure it returns 500 with the error message;
// the previous config is preserved. A request that races another in-flight
// reload returns 409 — the other reload reads the same source state, so the
// caller should retry the read rather than treat this as breakage.
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
			status := http.StatusInternalServerError
			if errors.Is(err, ErrReloadInProgress) {
				status = http.StatusConflict
			}
			http.Error(w, fmt.Sprintf("reload failed: %v", err), status)
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
