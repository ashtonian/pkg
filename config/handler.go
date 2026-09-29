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
// the previous config is preserved. A request that finds another reload in
// flight returns 409 without reloading — retry it to be sure the sources are
// read after that reload. A request after Stop returns 503.
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
			switch {
			case errors.Is(err, ErrReloadInProgress):
				status = http.StatusConflict
			case errors.Is(err, ErrReloaderStopped):
				status = http.StatusServiceUnavailable
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
