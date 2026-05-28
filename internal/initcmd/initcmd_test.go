package initcmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cockpit/internal/initcmd"
	"cockpit/internal/store"
)

func TestRun_FreshDir(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	syncDir := filepath.Join(tmp, ".sync")
	for _, f := range []string{"anchor.yaml", "claims.yaml", "iterations.jsonl"} {
		if _, err := os.Stat(filepath.Join(syncDir, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	// The whole point: freshly-init'd dir must load through store.Load without errors.
	state, err := store.Load(tmp)
	if err != nil {
		t.Fatalf("store.Load on init'd dir failed: %v", err)
	}
	if len(state.Iterations) != 1 || state.Iterations[0].ID != 1 {
		t.Errorf("expected 1 iter with ID 1, got %d", len(state.Iterations))
	}
	if state.Anchor.Now.IterationID != 1 {
		t.Errorf("expected anchor.now.iteration_id=1, got %d", state.Anchor.Now.IterationID)
	}
	if len(state.Claims) != 0 {
		t.Errorf("expected 0 claims, got %d", len(state.Claims))
	}
}

func TestRun_AlreadyInitialized(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	err := initcmd.Run(tmp)
	if err == nil {
		t.Fatal("expected error on re-init")
	}
	if !errors.Is(err, initcmd.ErrAlreadyInitialized) {
		t.Errorf("expected ErrAlreadyInitialized, got %v", err)
	}
}
