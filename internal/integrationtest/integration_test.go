// Package integrationtest exercises the end-to-end flows that the per-package
// unit tests don't cover: Stop-hook → record → serve, MCP envelope round-trip,
// and the prompt-diff endpoint against real snapshots. All tests run hermetically
// in t.TempDir(); no network, no global state outside CLAUDE_SESSION_ID env (which
// each test sets+restores).
package integrationtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/mcpserver"
	"github.com/friday-james/vibelog/internal/observecmd"
	"github.com/friday-james/vibelog/internal/serve"
)

// initProject scaffolds a vibelog .sync/ skeleton in a fresh tmp dir and
// returns its path. Fails the test on any error.
func initProject(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatalf("init: %v", err)
	}
	return tmp
}

// writeTranscript drops a JSONL transcript into a tmp file. The Stop-hook
// payload built by buildStopPayload points at this file.
func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// buildStopPayload returns the JSON bytes Claude Code would write to the
// Stop hook's stdin.
func buildStopPayload(sessionID, transcriptPath, cwd string) []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id":       sessionID,
		"transcript_path":  transcriptPath,
		"stop_hook_active": false,
		"hook_event_name":  "Stop",
		"cwd":              cwd,
	})
	return b
}

// runObserveWithStdin feeds payload to observecmd.Run via a redirected
// os.Stdin. Restores the original on return. Sync, no goroutines: writes the
// payload to a pipe and closes the writer BEFORE Run starts, so Run sees a
// complete payload then EOF.
func runObserveWithStdin(t *testing.T, projectDir string, payload []byte) error {
	t.Helper()
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	w.Close()
	os.Stdin = r
	return observecmd.Run(projectDir)
}

// loadIterations reads the .sync/iterations.jsonl rows and returns them as
// generic maps so a test can inspect any field without coupling to model types.
func loadIterations(t *testing.T, projectDir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(projectDir, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatalf("read iterations: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse row %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// ---------- Test 1: end-to-end observe → record → serve ----------

func TestE2E_ObserveRecordsAndServeReturns(t *testing.T) {
	const sessionID = "e2e-session-A"
	t.Setenv("CLAUDE_SESSION_ID", sessionID)

	project := initProject(t)
	transcript := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"add a helper"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Adding helpers.go."},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"` + project + `/helpers.go"}}` +
			`]}}`,
		`{"uuid":"a2","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Helper added — single function returning the project name."}` +
			`]}}`,
	})
	// helpers.go has to exist on disk for the snapshot copy step inside
	// RecordIteration to succeed.
	if err := os.WriteFile(filepath.Join(project, "helpers.go"), []byte("package main\nfunc Project() string { return \"vibelog\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := buildStopPayload(sessionID, transcript, project)
	if err := runObserveWithStdin(t, project, payload); err != nil {
		t.Fatalf("observe run: %v", err)
	}

	iters := loadIterations(t, project)
	if len(iters) == 0 {
		t.Fatal("expected at least one iteration row after observe; got 0")
	}
	last := iters[len(iters)-1]
	if last["kind"] != "iteration" {
		t.Errorf("expected kind=iteration, got %v", last["kind"])
	}
	if last["session_id"] != sessionID {
		t.Errorf("expected session_id=%q, got %v", sessionID, last["session_id"])
	}
	files, _ := last["files_changed"].([]any)
	if len(files) != 1 || files[0] != "helpers.go" {
		t.Errorf("expected files_changed=[helpers.go], got %v", files)
	}

	// Now bring up the serve handler and confirm /state.json reflects the row.
	h, err := serve.Handler(project)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state.json status=%d", resp.StatusCode)
	}
	var state struct {
		Iterations []map[string]any `json:"iterations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state.json: %v", err)
	}
	found := false
	for _, it := range state.Iterations {
		if it["transcript_message_id"] == "a2" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected state.json to contain the iteration with transcript_message_id=a2; iterations=%+v", state.Iterations)
	}
}

// ---------- Test 2: set_implementation envelope round-trip ----------

func TestE2E_SetImplementationEnvelopePopulatesIterationFields(t *testing.T) {
	const sessionID = "e2e-session-B"
	t.Setenv("CLAUDE_SESSION_ID", sessionID)

	project := initProject(t)

	// During the (simulated) turn, the agent calls set_implementation. This
	// writes the envelope under .sync/pending_implementation.txt.
	curatedSummary := "Refactored auth.Verify to use subtle.ConstantTimeCompare."
	curatedText := "Replaced the naive bytes.Equal with `subtle.ConstantTimeCompare` so the verifier doesn't leak timing on early mismatch. Added a microbenchmark to confirm the constant-time profile."
	if err := mcpserver.SetImplementation(project, mcpserver.SetImplementationArgs{
		Summary: curatedSummary,
		Text:    curatedText,
	}); err != nil {
		t.Fatalf("SetImplementation: %v", err)
	}

	// The "real" transcript carries a meandering text block (which observe
	// would normally use as the fallback summary). The envelope should win.
	transcript := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"fix auth.Verify"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Hmm, let me think about the constant-time concern."},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"` + project + `/auth.go"}}` +
			`]}}`,
		`{"uuid":"a2","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"OK done. Worth a benchmark later."}` +
			`]}}`,
	})
	if err := os.WriteFile(filepath.Join(project, "auth.go"), []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := buildStopPayload(sessionID, transcript, project)
	if err := runObserveWithStdin(t, project, payload); err != nil {
		t.Fatalf("observe run: %v", err)
	}

	iters := loadIterations(t, project)
	if len(iters) == 0 {
		t.Fatal("no iteration recorded")
	}
	last := iters[len(iters)-1]

	if got := last["summary"]; got != curatedSummary {
		t.Errorf("expected curated summary from envelope, got %v", got)
	}
	if got, _ := last["implementation"].(string); got != curatedText {
		t.Errorf("expected curated implementation from envelope, got %q", got)
	}

	// Envelope must be deleted after consumption — re-running observe with
	// no envelope must NOT bring the curated text back.
	if _, err := os.Stat(filepath.Join(project, ".sync", "pending_implementation.txt")); !os.IsNotExist(err) {
		t.Errorf("envelope should be deleted after consumption; stat err=%v", err)
	}
}

// ---------- Test 3: /prompt/<id>/diff against real snapshots ----------

func TestE2E_PromptDiffEndpointServesUnifiedDiff(t *testing.T) {
	project := initProject(t)
	snapsRoot := filepath.Join(project, ".sync", "snapshots")

	// Lay down two iter snapshots of the same file with a known diff between them.
	v1 := "package main\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n"
	v2 := "package main\n\nfunc Greet() string {\n\treturn \"hello\"\n}\n"

	for _, p := range []struct {
		id   int
		body string
	}{
		{1, v1},
		{2, v2},
	} {
		dir := filepath.Join(snapsRoot, "iter-"+itoa(p.id), "greet.go")
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte(p.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	h, err := serve.Handler(project)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/prompt/2/diff?path=greet.go")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("diff endpoint status=%d body=%q", resp.StatusCode, string(body))
	}
	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	// Sanity: unified-diff header with our -L labels, plus the actual lines
	// removed and added.
	for _, want := range []string{
		"greet.go @ iter-1",
		"greet.go @ iter-2",
		`-	return "hi"`,
		`+	return "hello"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected diff to contain %q; full output:\n%s", want, got)
		}
	}
}

// ---------- Test 4: multi-project routing ----------

func TestE2E_MultiProjectServeRoutesPerProject(t *testing.T) {
	projA := initProject(t)
	projB := initProject(t)

	// Each project gets one synthetic iteration so /state.json has identifiable content.
	for _, p := range []struct {
		dir string
		row string
	}{
		// initcmd already wrote iter #1 as a placeholder; use #2 to avoid collision.
		{projA, `{"id":2,"ts":"2026-01-01T00:00:00Z","kind":"iteration","summary":"alpha-only","agent":"claude-code"}`},
		{projB, `{"id":2,"ts":"2026-01-01T00:00:00Z","kind":"iteration","summary":"bravo-only","agent":"claude-code"}`},
	} {
		f, err := os.OpenFile(filepath.Join(p.dir, ".sync", "iterations.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(p.row + "\n"); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	h, err := serve.MultiHandler([]serve.Project{
		{Name: "alpha", Path: projA},
		{Name: "bravo", Path: projB},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	// /projects.json lists both projects, sorted by name.
	resp, err := http.Get(srv.URL + "/projects.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("projects.json status=%d", resp.StatusCode)
	}
	var projects []serve.Project
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].Name != "alpha" || projects[1].Name != "bravo" {
		t.Errorf("expected alpha+bravo sorted, got %+v", projects)
	}

	// Root redirects to the first project.
	noFollow := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	rr, err := noFollow.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if rr.StatusCode != http.StatusFound {
		t.Errorf("root: want 302, got %d", rr.StatusCode)
	}
	if loc := rr.Header.Get("Location"); loc != "/p/alpha/" {
		t.Errorf("root redirect location: want /p/alpha/, got %q", loc)
	}

	// Each project's /state.json carries its own iteration row.
	for _, c := range []struct {
		path   string
		wantIn string
	}{
		{"/p/alpha/state.json", "alpha-only"},
		{"/p/bravo/state.json", "bravo-only"},
	} {
		r, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", c.path, r.StatusCode, string(body))
		}
		if !strings.Contains(string(body), c.wantIn) {
			t.Errorf("%s: expected %q in body, got: %s", c.path, c.wantIn, string(body))
		}
	}

	// /p/<name> (no trailing slash) redirects to /p/<name>/ so relative fetches resolve.
	rr2, err := noFollow.Get(srv.URL + "/p/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if rr2.StatusCode != http.StatusMovedPermanently {
		t.Errorf("/p/alpha: want 301, got %d", rr2.StatusCode)
	}
}

// ---------- Test 5: auto-discovery ----------

func TestE2E_DiscoverProjectsFindsChildrenWithAnchor(t *testing.T) {
	root := t.TempDir()

	// Two real projects (have .sync/anchor.yaml).
	for _, name := range []string{"vibelog", "ledger"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := initcmd.Run(dir); err != nil {
			t.Fatalf("init %s: %v", name, err)
		}
	}
	// One unrelated dir with no .sync/.
	if err := os.MkdirAll(filepath.Join(root, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	// One dotdir (must be skipped).
	if err := os.MkdirAll(filepath.Join(root, ".hidden", ".sync"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", ".sync", "anchor.yaml"), []byte("intent:\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projects, err := serve.DiscoverProjects(root, 3)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, p := range projects {
		names = append(names, p.Name)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects (vibelog, ledger), got %d: %v", len(projects), names)
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range []string{"vibelog", "ledger"} {
		if !have[want] {
			t.Errorf("expected to discover %q, only found %v", want, names)
		}
	}
}

// ---------- helpers ----------

// tiny helper so we don't have to import strconv just for one call site
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
