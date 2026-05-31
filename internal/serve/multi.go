package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is one entry in a multi-project config: the name surfaces in the URL
// (e.g. /p/<name>/) and in the dashboard's project switcher; the dir is the
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
// nil) so the caller can fall back to single-project mode.
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

// validateProjects enforces unique names, non-empty paths, and that the names
// are URL-safe (since they appear in /p/<name>/).
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

// MultiHandler mounts a Handler per project at /p/<name>/, plus a /projects.json
// endpoint for the UI switcher, plus a / redirect to the first project so a bare
// http://host/ visit lands somewhere useful. Single-project mode (1 project) is
// supported but Handler(projectDir) is friendlier when callers know they only
// have one project.
func MultiHandler(projects []Project) (http.Handler, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("at least one project required")
	}
	if err := validateProjects(projects); err != nil {
		return nil, err
	}
	// Stable order, name-sorted, so the switcher renders predictably.
	sorted := append([]Project(nil), projects...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	mux := http.NewServeMux()
	for _, p := range sorted {
		sub, err := Handler(p.Path)
		if err != nil {
			return nil, fmt.Errorf("project %q at %s: %w", p.Name, p.Path, err)
		}
		prefix := "/p/" + p.Name
		// Trailing-slash discipline: redirect /p/<name> → /p/<name>/ so relative
		// fetches in the UI resolve correctly under the project's base URL.
		exact := prefix
		mux.HandleFunc(exact, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, exact+"/", http.StatusMovedPermanently)
		})
		mux.Handle(prefix+"/", http.StripPrefix(prefix, sub))
	}
	// Project list for the UI switcher.
	mux.HandleFunc("/projects.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(sorted)
	})
	// Root: redirect to first project so http://host/ doesn't 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/p/"+sorted[0].Name+"/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	return mux, nil
}

// RunMulti is the multi-project counterpart of Run. Blocks.
func RunMulti(projects []Project, addr string) error {
	h, err := MultiHandler(projects)
	if err != nil {
		return err
	}
	return http.ListenAndServe(addr, h)
}
