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
	"os/exec"
	"strings"

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
			return
		}
	})
	mux.HandleFunc("/git/show/", func(w http.ResponseWriter, r *http.Request) {
		sha := strings.TrimPrefix(r.URL.Path, "/git/show/")
		if !isHexSHA(sha) {
			http.Error(w, "invalid sha", http.StatusBadRequest)
			return
		}
		// --stat + --patch keeps output bounded; we still cap below.
		out, err := exec.Command("git", "-C", projectDir, "show", "--stat", "--patch", sha).CombinedOutput()
		if err != nil {
			http.Error(w, "git show failed: "+strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		// Cap at 64KiB so a huge merge commit can't OOM the browser tab.
		const cap = 64 << 10
		if len(out) > cap {
			out = append(out[:cap], []byte("\n\n… (truncated; run `git show "+sha+"` to see the full diff)\n")...)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(out)
	})
	return mux, nil
}

// isHexSHA accepts 4..40 hex chars (matches both short and full git SHAs).
func isHexSHA(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// Run starts a blocking HTTP server. Listens on addr (e.g. "localhost:7100").
func Run(projectDir, addr string) error {
	h, err := Handler(projectDir)
	if err != nil {
		return err
	}
	return http.ListenAndServe(addr, h)
}
