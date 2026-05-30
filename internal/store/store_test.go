package store_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vibelog/internal/store"
)

func TestLoad_Happy(t *testing.T) {
	state, err := store.Load("../../examples/sample_repo")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := len(state.Claims); got != 9 {
		t.Errorf("expected 9 claims, got %d", got)
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

func TestLoad_ValidationFails(t *testing.T) {
	tmp := t.TempDir()
	syncDir := filepath.Join(tmp, ".sync")
	if err := os.MkdirAll(syncDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(syncDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal valid anchor + empty iterations + claim with NO evidence (should fail Claim.Validate).
	write("anchor.yaml", `intent:
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
`)
	write("claims.yaml", `- id: empty
  statement: "no evidence here"
  category: invariant
  status: holding
  severity: med
  evidence: []
  established: 2026-01-01
`)
	write("iterations.jsonl", "")

	_, err := store.Load(tmp)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "at least one evidence") {
		t.Errorf("expected at-least-one-evidence error, got %v", err)
	}
}

func TestLoad_MalformedJSONL(t *testing.T) {
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
	os.WriteFile(filepath.Join(syncDir, "claims.yaml"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(syncDir, "iterations.jsonl"), []byte("{not valid json\n"), 0o644)

	_, err := store.Load(tmp)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("expected line-numbered error, got %v", err)
	}
}
