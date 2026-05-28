// Package serve hosts the cockpit dashboard UI and a /state.json endpoint
// backed by store.Load. Phase 1: HTTP polling. Phase 2 will add SSE so the
// UI reacts to .sync/ changes in <200ms.
package serve

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"

	"cockpit/internal/store"
)

//go:embed ui
var uiFS embed.FS

// Handler returns the HTTP handler that serves the embedded UI at / and
// the project state at /state.json. Exposed (vs only Run) so tests can mount
// it on httptest.NewServer.
func Handler(projectDir string) (http.Handler, error) {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return nil, fmt.Errorf("locate embedded ui: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/state.json", func(w http.ResponseWriter, r *http.Request) {
		state, err := store.Load(projectDir)
		if err != nil {
			http.Error(w, "load state: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := json.NewEncoder(w).Encode(state); err != nil {
			// Headers already sent; best effort.
			return
		}
	})
	return mux, nil
}

// Run starts a blocking HTTP server. Listens on addr (e.g. "localhost:7100").
func Run(projectDir, addr string) error {
	h, err := Handler(projectDir)
	if err != nil {
		return err
	}
	return http.ListenAndServe(addr, h)
}
