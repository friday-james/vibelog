package mcpserver

import (
	"strings"
	"testing"
)

func TestFallbackSessionID_StableAcrossCallsInSameProcess(t *testing.T) {
	// fallbackSessionID is the in-process constant assigned at package init
	// from the parent PID. The same MCP-process invocation must always see
	// the same value (otherwise concurrency detection on the dashboard reads
	// every Stop-hook fire as a fresh session and false-flags every file).
	if fallbackSessionID == "" {
		t.Fatal("fallbackSessionID should not be empty")
	}
	if !strings.HasPrefix(fallbackSessionID, "ppid-") {
		t.Errorf("fallbackSessionID should be parent-PID-keyed (ppid-N), got %q", fallbackSessionID)
	}
	first := fallbackSessionID
	for i := 0; i < 5; i++ {
		if fallbackSessionID != first {
			t.Fatalf("fallbackSessionID mutated mid-process: was %q, now %q", first, fallbackSessionID)
		}
	}
}

func TestResolveAgentMetadata_RespectsClaudeEnv(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "4ea73732-650f-4011-9c9c-989989b9069d")
	m := resolveAgentMetadata("", "")
	if m.Agent != "claude-code" {
		t.Errorf("expected agent=claude-code from env, got %q", m.Agent)
	}
	if m.SessionID != "4ea73732-650f-4011-9c9c-989989b9069d" {
		t.Errorf("expected env session_id passthrough, got %q", m.SessionID)
	}
}
