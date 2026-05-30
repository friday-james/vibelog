package watchcmd_test

import (
	"strings"
	"testing"

	"vibelog/internal/watchcmd"
)

func TestFormat_IterationFull(t *testing.T) {
	line := `{"id":2,"ts":"2026-05-28T04:16:40Z","kind":"iteration","summary":"end-to-end MCP smoke test","files_changed":["a.go","b.go"],"claims_added":["alpha"],"claims_violated":["beta"],"agent":"claude-code"}`
	out := watchcmd.Format(line)
	if !strings.Contains(out, "#2") {
		t.Errorf("missing id: %q", out)
	}
	if !strings.Contains(out, "iteration") {
		t.Errorf("missing kind: %q", out)
	}
	if !strings.Contains(out, "end-to-end MCP smoke test") {
		t.Errorf("missing summary: %q", out)
	}
	if !strings.Contains(out, "a.go, b.go") {
		t.Errorf("files not rendered: %q", out)
	}
	if !strings.Contains(out, "+alpha") {
		t.Errorf("claims_added not rendered: %q", out)
	}
	if !strings.Contains(out, "✗beta") {
		t.Errorf("claims_violated not rendered: %q", out)
	}
}

func TestFormat_Commit(t *testing.T) {
	line := `{"id":3,"ts":"2026-05-27T15:00:00Z","kind":"commit","sha":"3f17ac","summary":"jwt + refresh"}`
	out := watchcmd.Format(line)
	if !strings.Contains(out, "commit") {
		t.Errorf("missing kind: %q", out)
	}
	if !strings.Contains(out, "3f17ac") {
		t.Errorf("sha not rendered: %q", out)
	}
}

func TestFormat_MalformedFallback(t *testing.T) {
	line := `{not json`
	out := watchcmd.Format(line)
	if out != line {
		t.Errorf("expected raw line fallback, got %q", out)
	}
}
