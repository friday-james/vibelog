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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// /prompt/<id>/diff?path=foo.go — prompt-based diff (NOT git diff).
	// Compares the file's snapshot at iter-<id> against the most recent prior
	// iter that has a snapshot for the same path. If none, diff vs /dev/null
	// (shows the entire file as added by that prompt).
	mux.HandleFunc("/prompt/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/prompt/"), "/")
		if len(parts) < 2 || parts[1] != "diff" {
			http.Error(w, "expected /prompt/<id>/diff?path=...", http.StatusBadRequest)
			return
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil || id < 1 {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" || strings.Contains(path, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		snapsRoot := filepath.Join(projectDir, ".sync", "snapshots")
		currentSnap := filepath.Join(snapsRoot, fmt.Sprintf("iter-%d", id), path)
		if _, err := os.Stat(currentSnap); err != nil {
			http.Error(w, fmt.Sprintf("no snapshot of %q at iter #%d — prompt-diff requires snapshots captured at hook time; older entries predate snapshotting", path, id), http.StatusNotFound)
			return
		}
		// Walk back to find the most recent prior iter that snapshotted this path.
		var prevSnap string
		prevID := 0
		for prev := id - 1; prev >= 1; prev-- {
			cand := filepath.Join(snapsRoot, fmt.Sprintf("iter-%d", prev), path)
			if _, err := os.Stat(cand); err == nil {
				prevSnap = cand
				prevID = prev
				break
			}
		}
		// Use -L to override the file labels in the unified-diff header.
		// Otherwise diff emits the snapshot filesystem paths (e.g.
		// `.sync/snapshots/iter-29/internal/...`) which read like the user
		// edited snapshot files.
		newLabel := fmt.Sprintf("%s @ iter-%d", path, id)
		var cmd *exec.Cmd
		if prevSnap == "" {
			cmd = exec.Command("diff", "-u",
				"-L", "(no prior snapshot)",
				"-L", newLabel,
				"/dev/null", currentSnap)
		} else {
			oldLabel := fmt.Sprintf("%s @ iter-%d", path, prevID)
			cmd = exec.Command("diff", "-u",
				"-L", oldLabel,
				"-L", newLabel,
				prevSnap, currentSnap)
		}
		out, _ := cmd.Output() // diff exits 1 on differences; not an error.
		const cap = 96 << 10
		if len(out) > cap {
			out = append(out[:cap], []byte("\n\n… (truncated)\n")...)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if len(out) == 0 {
			fmt.Fprintf(w, "(no diff — file is identical to its previous snapshot)\n")
			return
		}
		w.Write(out)
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

// computeCurrentDrift through sha256File removed — drift detection deleted, simplification per user request.

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
