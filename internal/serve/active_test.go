package serve_test

import (
	"os"
	"testing"

	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/serve"
)

func TestAcquireActiveMarker_WritesAndRemoves(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !serve.IsActive(tmp) {
		t.Fatal("expected active marker after acquire")
	}
	if _, err := os.Stat(serve.ActiveMarkerPath(tmp)); err != nil {
		t.Fatalf("active marker missing: %v", err)
	}
	release()
	if serve.IsActive(tmp) {
		t.Fatal("expected marker removed after release")
	}
}

func TestAcquireActiveMarker_UninitializedProjectNoOp(t *testing.T) {
	tmp := t.TempDir()
	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if serve.IsActive(tmp) {
		t.Fatal("expected inactive project without .sync/anchor.yaml")
	}
}

func TestIsActive_RejectsStaleMarker(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serve.ActiveMarkerPath(tmp), []byte(`{"pid":-1,"started":"2026-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if serve.IsActive(tmp) {
		t.Fatal("expected stale marker with invalid pid to be inactive")
	}
}
