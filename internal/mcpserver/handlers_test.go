package mcpserver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/mcpserver"
	"github.com/friday-james/vibelog/internal/serve"
	"github.com/friday-james/vibelog/internal/store"
)

func activate(t *testing.T, dir string) func() {
	t.Helper()
	release, err := serve.AcquireActiveMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !serve.IsActive(dir) {
		t.Fatal("expected active serve marker")
	}
	return release
}

func TestRecordIteration_AppendsAndAssignsID(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	// After init, iter #1 exists. Record another.
	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{
		Summary:      "first MCP-driven iteration",
		FilesChanged: []string{"foo.go"},
	})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if iter.ID != 2 {
		t.Errorf("expected id=2 (after init's #1), got %d", iter.ID)
	}
	if iter.Kind != "iteration" {
		t.Errorf("expected kind=iteration, got %q", iter.Kind)
	}
	if strings.TrimSpace(iter.Agent) == "" {
		t.Errorf("expected non-empty agent label")
	}
	if strings.TrimSpace(iter.SessionID) == "" {
		t.Errorf("expected non-empty session_id")
	}
	state, err := store.Load(tmp)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if len(state.Iterations) != 2 {
		t.Errorf("expected 2 iterations after one append, got %d", len(state.Iterations))
	}
	if got := state.Iterations[1].Summary; !strings.Contains(got, "first MCP-driven") {
		t.Errorf("summary not propagated: %q", got)
	}
}

func TestRecordIteration_UninitializedDir(t *testing.T) {
	tmp := t.TempDir()
	_, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{Summary: "x"})
	if err == nil {
		t.Fatal("expected error on uninitialized dir")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("expected 'not initialized' hint, got %v", err)
	}
}

func TestRecordIteration_CommitsDoNotShareSequence(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	// Hand-append a commit with id=99 — must NOT bump the iteration sequence.
	iterPath := filepath.Join(tmp, ".sync", "iterations.jsonl")
	f, err := os.OpenFile(iterPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":99,"ts":"2026-05-27T00:00:00Z","kind":"commit","sha":"deadbeef","summary":"old"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{Summary: "next"})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if iter.ID != 2 {
		t.Errorf("expected id=2 (commit#99 must not bump iteration sequence), got %d", iter.ID)
	}
}

func TestRecordIteration_IdempotentOnTranscriptMessageID(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	args := mcpserver.RecordIterationArgs{
		Summary:             "first",
		TranscriptMessageID: "msg-uuid-xyz",
	}
	first, err := mcpserver.RecordIteration(tmp, args)
	if err != nil {
		t.Fatal(err)
	}
	// Second call with same message_id but different summary — must return the original.
	args.Summary = "second (should be ignored)"
	second, err := mcpserver.RecordIteration(tmp, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("expected idempotent return of id %d, got %d", first.ID, second.ID)
	}
	if second.Summary != "first" {
		t.Errorf("expected first summary preserved, got %q", second.Summary)
	}
	state, _ := store.Load(tmp)
	if len(state.Iterations) != 2 { // init's iter#1 + our one record
		t.Errorf("expected 2 entries (init + one record), got %d", len(state.Iterations))
	}
}

func TestRecordIteration_PropagatesOptionalFields(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{
		Summary:             "exercises every optional field",
		FilesChanged:        []string{"a.go", "b.go"},
		TranscriptMessageID: "msg-uuid-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(iter.FilesChanged) != 2 {
		t.Errorf("files_changed not propagated")
	}
	if iter.TranscriptMessageID != "msg-uuid-123" {
		t.Errorf("transcript_message_id not propagated")
	}
}

func TestRecordIteration_UsesClaudeSessionEnv(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	t.Setenv("CLAUDE_SESSION_ID", "claude-session-123")

	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{
		Summary: "captured through claude env",
	})
	if err != nil {
		t.Fatal(err)
	}
	if iter.Agent != "claude-code" {
		t.Errorf("expected agent=claude-code, got %q", iter.Agent)
	}
	if iter.SessionID != "claude-session-123" {
		t.Errorf("expected session_id from env, got %q", iter.SessionID)
	}
}

func TestRecordIteration_ConsumesPendingImplementation(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	defer activate(t, tmp)()
	if err := mcpserver.SetImplementation(tmp, mcpserver.SetImplementationArgs{
		Summary: "condensed summary",
		Text:    "full implementation block",
	}); err != nil {
		t.Fatal(err)
	}

	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{
		FilesChanged: []string{"foo.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if iter.Summary != "condensed summary" {
		t.Errorf("expected summary from pending envelope, got %q", iter.Summary)
	}
	if iter.Implementation != "full implementation block" {
		t.Errorf("expected implementation from pending envelope, got %q", iter.Implementation)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".sync", "pending_implementation.txt")); !os.IsNotExist(err) {
		t.Errorf("expected pending envelope to be consumed, stat err=%v", err)
	}
}

func TestRecordIteration_InactiveWithoutServe(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	_, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{Summary: "x"})
	if err == nil || !strings.Contains(err.Error(), "inactive") {
		t.Fatalf("expected inactive error, got %v", err)
	}
}
