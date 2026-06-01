package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// Project is one entry in a multi-project config: the name surfaces in the URL
// (e.g. /p/<name>/) and in the dashboard's project switcher; the path is the
// absolute filesystem path containing .sync/.
type Project struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

// DefaultProjectsConfigPath returns ~/.vibelog/projects.yaml.
func DefaultProjectsConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".vibelog/projects.yaml"
	}
	return filepath.Join(home, ".vibelog", "projects.yaml")
}

// LoadProjectsConfig reads a YAML list of Project entries. Missing file → (nil,
// nil) so the caller can decide what to fall back to.
func LoadProjectsConfig(path string) ([]Project, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var projects []Project
	if err := yaml.Unmarshal(b, &projects); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := validateProjects(projects); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return projects, nil
}

// ParseProjectsFlag parses the `-projects name=dir,name2=dir2` CLI form.
func ParseProjectsFlag(s string) ([]Project, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []Project
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		i := strings.Index(entry, "=")
		if i <= 0 || i == len(entry)-1 {
			return nil, fmt.Errorf("invalid project entry %q (expected name=dir)", entry)
		}
		out = append(out, Project{
			Name: strings.TrimSpace(entry[:i]),
			Path: strings.TrimSpace(entry[i+1:]),
		})
	}
	return out, validateProjects(out)
}

func validateProjects(ps []Project) error {
	seen := make(map[string]bool, len(ps))
	for i, p := range ps {
		if p.Name == "" {
			return fmt.Errorf("entry %d: name is required", i)
		}
		if p.Path == "" {
			return fmt.Errorf("entry %d (%s): path is required", i, p.Name)
		}
		if !urlSafeName(p.Name) {
			return fmt.Errorf("entry %d: name %q must be URL-safe (letters, digits, '-', '_' only)", i, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("entry %d: duplicate name %q", i, p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func urlSafeName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// ----- MultiServer: a long-running, mutable multi-project HTTP handler -----

// MultiServer holds the current project list and rebuilds its internal mux
// each time a new project is registered. ServeHTTP swaps to the latest mux
// atomically so a register call mid-request doesn't tear.
//
// Endpoints exposed in addition to the per-project routes:
//
//	GET  /api/health         { server: "vibelog", projects: N }   (used by other
//	                           `vibelog serve` invocations to detect us before
//	                           registering — see ProbeAndRegister)
//	GET  /projects.json      sorted Project list for the UI tab strip
//	POST /api/projects       { name, path } registers a project, auto-saves
//
// Registrations are ephemeral: a project lives only as long as either the
// server is alive (for the seed project the server started with) or the
// lease's HTTP connection is open (for projects added via /api/projects/lease).
type MultiServer struct {
	mu       sync.RWMutex
	projects []Project
	mux      atomic.Pointer[http.ServeMux]
}

// NewMultiServer builds the server with an initial project list.
func NewMultiServer(initial []Project) (*MultiServer, error) {
	if err := validateProjects(initial); err != nil {
		return nil, err
	}
	s := &MultiServer{projects: append([]Project(nil), initial...)}
	if err := s.rebuildLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Projects returns a snapshot of the current list (sorted by name).
func (s *MultiServer) Projects() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Project(nil), s.projects...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Register adds (or updates) a project. Same-name overwrites.
func (s *MultiServer) Register(p Project) error {
	if err := validateProjects([]Project{p}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i := range s.projects {
		if s.projects[i].Name == p.Name {
			s.projects[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		s.projects = append(s.projects, p)
	}
	if err := s.rebuildLocked(); err != nil {
		return err
	}
	return nil
}

// Deregister removes a project by name. No-op if name isn't present.
func (s *MultiServer) Deregister(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.projects[:0]
	for _, p := range s.projects {
		if p.Name != name {
			out = append(out, p)
		}
	}
	s.projects = out
	_ = s.rebuildLocked()
}

// ServeHTTP dispatches to the current mux. The atomic pointer means a
// concurrent register can swap the mux without locking readers.
func (s *MultiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := s.mux.Load()
	if m == nil {
		http.Error(w, "server not ready", http.StatusServiceUnavailable)
		return
	}
	m.ServeHTTP(w, r)
}

// rebuildLocked constructs a fresh mux from the current projects list. Caller
// must hold s.mu (write or read — we only read s.projects).
func (s *MultiServer) rebuildLocked() error {
	sorted := append([]Project(nil), s.projects...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	mux := http.NewServeMux()

	for _, p := range sorted {
		sub, err := Handler(p.Path)
		if err != nil {
			return fmt.Errorf("project %q at %s: %w", p.Name, p.Path, err)
		}
		prefix := "/p/" + p.Name
		exact := prefix
		mux.HandleFunc(exact, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, exact+"/", http.StatusMovedPermanently)
		})
		mux.Handle(prefix+"/", http.StripPrefix(prefix, sub))
	}

	// GET /projects.json — current list for the UI switcher.
	mux.HandleFunc("/projects.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		s.mu.RLock()
		out := append([]Project(nil), s.projects...)
		s.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		_ = json.NewEncoder(w).Encode(out)
	})

	// GET /api/health — signature endpoint. Other `vibelog serve` invocations
	// hit this to confirm the process on :7100 is us before posting a project.
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		n := len(s.projects)
		s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"server": "vibelog", "projects": n})
	})

	// POST /api/projects — fire-and-forget register (no lease). Used by tests
	// and by callers that want to add a project without holding the connection.
	mux.HandleFunc("/api/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var p Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.Register(p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)
	})

	// GET /api/projects/lease?name=X&path=Y — long-lived lease. Registers the
	// project, then holds the connection open. The project lives only as long
	// as the connection is open: when the client closes (Ctrl+C, network drop)
	// the project is auto-deregistered. This is what the second `vibelog serve`
	// in another repo uses — it blocks on this connection so the user can
	// cancel the registration by killing the process.
	mux.HandleFunc("/api/projects/lease", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := Project{
			Name: r.URL.Query().Get("name"),
			Path: r.URL.Query().Get("path"),
		}
		if err := s.Register(p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Vibelog-Lease", p.Name)
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			fmt.Fprintf(w, ": leased %s\n\n", p.Name)
			flusher.Flush()
		}
		// Hold the connection open until the client disconnects. Server-side
		// shutdown also closes Context(), so we deregister cleanly on either.
		ctx := r.Context()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.Deregister(p.Name)
				return
			case <-ticker.C:
				if flusher != nil {
					if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
						s.Deregister(p.Name)
						return
					}
					flusher.Flush()
				}
			}
		}
	})

	// Root: redirect to the first project's tab, so http://host/ lands somewhere.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if len(sorted) == 0 {
			http.Error(w, "no projects registered yet", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/p/"+sorted[0].Name+"/", http.StatusFound)
	})

	s.mux.Store(mux)
	return nil
}

// ProbeRunning checks whether a vibelog is running at addr by hitting
// /api/health. Returns true on a clean vibelog signature, false on
// connection-refused or anything else.
func ProbeRunning(addr string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	r, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		var nerr net.Error
		if errors.As(err, &nerr) || strings.Contains(err.Error(), "connection refused") {
			return false
		}
		return false
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return bytes.Contains(body, []byte(`"server":"vibelog"`))
}

// LeaseProject opens a long-lived lease against a running vibelog. The project
// stays registered as long as this call blocks. Returns when the connection
// closes (server shutdown, network drop) or ctx is cancelled (Ctrl+C). The
// caller is expected to block on this to keep the registration alive.
func LeaseProject(ctx context.Context, addr string, p Project) error {
	q := url.Values{}
	q.Set("name", p.Name)
	q.Set("path", p.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+addr+"/api/projects/lease?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	// Long-lived: no timeout on the client, and no transport idle timeout that
	// would kill our connection.
	client := &http.Client{
		Transport: &http.Transport{DisableKeepAlives: false, IdleConnTimeout: 0},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("open lease: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("lease rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	// Block draining the stream until the server closes it or ctx cancels.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// RunMulti is the multi-project counterpart of Run. Same port-fallback
// behavior: if the preferred port is taken, walks up to the next 20.
func RunMulti(projects []Project, preferredAddr string) error {
	return RunMultiContext(context.Background(), projects, preferredAddr)
}

// RunMultiContext is RunMulti with graceful shutdown when ctx is cancelled.
func RunMultiContext(ctx context.Context, projects []Project, preferredAddr string) error {
	srv, err := NewMultiServer(projects)
	if err != nil {
		return err
	}
	ln, actual, err := listenWithFallback(preferredAddr, 20)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Printf("vibelog serving on http://%s\n", actual)
	for _, p := range srv.Projects() {
		fmt.Printf("  /p/%s/  →  %s\n", p.Name, p.Path)
	}
	httpSrv := &http.Server{Handler: srv}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err = httpSrv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

// MultiHandler kept for backward compatibility with tests / direct callers.
// Returns a *MultiServer (which implements http.Handler).
func MultiHandler(projects []Project) (http.Handler, error) {
	return NewMultiServer(projects)
}
