package store_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/friday-james/vibelog/internal/store"
)

func TestLoad_Happy(t *testing.T) {
	state, err := store.Load("../../examples/sample_repo")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := len(state.Iterations); got != 4 {
		t.Errorf("expected 4 iterations, got %d", got)
	}
	if got := state.Anchor.Now.IterationID; got != 4 {
		t.Errorf("expected anchor.now.iteration_id=4, got %d", got)
	}
	if got := state.Anchor.Intent.Statement; !strings.Contains(got, "cognitive-coupling") {
		t.Errorf("anchor.intent.statement looks wrong: %q", got)
	}
}

func TestLoad_MissingDir(t *testing.T) {
	_, err := store.Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error on missing dir")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected error wrapping fs.ErrNotExist, got %v", err)
	}
}


func TestLoad_MalformedJSONL(t *testing.T) {
	// readJSONL is intentionally tolerant: a row that fails to parse OR fails
	// per-row Validate is logged to stderr and skipped, not surfaced as a
	// load error. This keeps older binaries from crashing on rows written by
	// a newer schema-extending binary. The test confirms a malformed row is
	// dropped and the rest of the load succeeds.
	tmp := t.TempDir()
	syncDir := filepath.Join(tmp, ".sync")
	os.MkdirAll(syncDir, 0o755)
	os.WriteFile(filepath.Join(syncDir, "anchor.yaml"), []byte(`intent:
  statement: "x"
  evidence:
    - type: doc
      path: x
  established: 2026-01-01
approach:
  statement: "x"
  evidence:
    - type: doc
      path: x
now:
  statement: "x"
  iteration_id: 1
`), 0o644)
	os.WriteFile(filepath.Join(syncDir, "iterations.jsonl"), []byte("{not valid json\n"), 0o644)

	state, err := store.Load(tmp)
	if err != nil {
		t.Fatalf("expected tolerant load to succeed (bad row should be skipped), got: %v", err)
	}
	if len(state.Iterations) != 0 {
		t.Errorf("expected 0 iterations after skipping the malformed row, got %d", len(state.Iterations))
	}
}
