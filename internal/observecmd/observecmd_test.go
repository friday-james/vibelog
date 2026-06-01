package observecmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/observecmd"
	"github.com/friday-james/vibelog/internal/serve"
)

// withStdin temporarily replaces os.Stdin with the given bytes for the
// duration of fn(), then restores.
func withStdin(t *testing.T, payload string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.Copy(w, bytes.NewReader([]byte(payload)))
		w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	fn()
}

// writeTranscript creates a minimal Claude-Code-style JSONL transcript.
func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnalyzeTranscript_ExtractsFilesAndSummary(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"On it. Editing two files."},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/bar.go"}}` +
			`]}}`,
		`{"uuid":"a2","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Final wrap-up message after the edits."}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.LastMessageUUID != "a2" {
		t.Errorf("expected latest UUID a2, got %q", res.LastMessageUUID)
	}
	if !strings.Contains(res.SummaryText, "Final wrap-up") {
		t.Errorf("summary should come from latest assistant text, got %q", res.SummaryText)
	}
	if len(res.Files) != 2 || res.Files[0] != "/abs/foo.go" || res.Files[1] != "/abs/bar.go" {
		t.Errorf("expected [foo.go, bar.go] in chronological order, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_StopsAtUserBoundary(t *testing.T) {
	path := writeTranscript(t, []string{
		// PREVIOUS turn — must NOT be included
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"old"}]}}`,
		`{"uuid":"old-asst","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/should_not_appear.go"}}` +
			`]}}`,
		// CURRENT turn
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"new request"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/current.go"}},` +
			`{"type":"text","text":"done"}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0] != "/abs/current.go" {
		t.Errorf("expected only current-turn file, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_PureConversation(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected zero files on pure conversation, got %v", res.Files)
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected UUID a1, got %q", res.LastMessageUUID)
	}
}

func TestRun_SilentSkipOnUninitializedProject(t *testing.T) {
	// Project dir exists but has no .sync/ — observe must return nil cleanly,
	// not error, so the global Stop hook is harmless in non-vibelog repos.
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"foo.go"}}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`

	var err error
	withStdin(t, payload, func() {
		err = observecmd.Run("") // empty arg → falls back to payload.cwd
	})
	if err != nil {
		t.Fatalf("uninitialized project must not error, got %v", err)
	}
}

func TestRun_NoTranscriptPath_Errors(t *testing.T) {
	var err error
	withStdin(t, `{"session_id":"s","stop_hook_active":false,"hook_event_name":"Stop","cwd":"/tmp"}`, func() {
		err = observecmd.Run("")
	})
	if err == nil || !strings.Contains(err.Error(), "transcript_path") {
		t.Errorf("expected missing-transcript_path error, got %v", err)
	}
}

func TestRun_StopHookActive_NoOp(t *testing.T) {
	var err error
	withStdin(t, `{"stop_hook_active":true,"hook_event_name":"Stop"}`, func() {
		err = observecmd.Run("/nonexistent")
	})
	if err != nil {
		t.Errorf("expected nil on stop_hook_active=true, got %v", err)
	}
}

func TestRun_SilentSkipWhenServeInactive(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"foo.go"}}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("inactive project should skip, got %v", err)
		}
	})
	b, err := os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 1 {
		t.Fatalf("expected only init row while inactive, got %d rows", got)
	}

	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("active project should record, got %v", err)
		}
	})
	b, err = os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 2 {
		t.Fatalf("expected init row + observed row while active, got %d rows", got)
	}
}

func TestAnalyzeTranscript_DedupesFiles(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/foo.go"}}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Errorf("expected deduped to 1, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_SkipsTaskNotificationAsUserPrompt(t *testing.T) {
	// When a background Workflow/Agent completes, the harness injects a
	// <task-notification> block as type:"user". observe must anchor the turn
	// at that synthetic message (so the assistant's response still gets
	// recorded) but return an empty UserPrompt so the dashboard falls back
	// to the summary instead of leaking the meta block.
	path := writeTranscript(t, []string{
		// previous real turn
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"first prompt"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first reply"}]}}`,
		// synthetic user message from harness
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"<task-notification>\n<task-id>wf123</task-id>\n<status>completed</status>\n</task-notification>"}]}}`,
		// assistant response to the auto-fired turn
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Here's the final article."}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.UserPrompt != "" {
		t.Errorf("expected empty UserPrompt for harness-injected task-notification, got %q", res.UserPrompt)
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected to anchor at the auto-fired turn (uuid a1), got %q", res.LastMessageUUID)
	}
	if !strings.Contains(res.Implementation, "Here's the final article.") {
		t.Errorf("expected Implementation to contain the new turn's assistant text, got %q", res.Implementation)
	}
	if strings.Contains(res.Implementation, "first reply") {
		t.Errorf("Implementation should not include the PREVIOUS turn's assistant text; got %q", res.Implementation)
	}
}

func TestAnalyzeTranscript_KeepsUserPastedTaskNotificationWithFraming(t *testing.T) {
	// A user who PASTES a notification frames it with prose ("look at this:").
	// That's a real user turn and must not be skipped — only messages that
	// START with the wrapper tag (i.e. injected by the harness) get scrubbed.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"look at this: <task-notification>...</task-notification> what gives?"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"that's a workflow result"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.UserPrompt, "look at this:") {
		t.Errorf("expected to preserve user prose framing, got %q", res.UserPrompt)
	}
}
