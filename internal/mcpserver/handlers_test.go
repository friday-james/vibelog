package mcpserver_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibelog/internal/initcmd"
	"vibelog/internal/mcpserver"
	"vibelog/internal/store"
)

func TestRecordIteration_AppendsAndAssignsID(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
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
	if iter.Agent != "claude-code" {
		t.Errorf("expected agent=claude-code, got %q", iter.Agent)
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
	iter, err := mcpserver.RecordIteration(tmp, mcpserver.RecordIterationArgs{
		Summary:             "exercises every optional field",
		FilesChanged:        []string{"a.go", "b.go"},
		ClaimsAdded:         []string{"alpha"},
		ClaimsViolated:      []string{"beta"},
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
	if len(iter.ClaimsViolated) != 1 || iter.ClaimsViolated[0] != "beta" {
		t.Errorf("claims_violated not propagated: %v", iter.ClaimsViolated)
	}
}
