package serve

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const activeMarkerFile = "serve-active.json"

type activeMarker struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
}

// ActiveMarkerPath returns the per-project file that exists only while a
// `vibelog serve` process for this project is alive.
func ActiveMarkerPath(projectDir string) string {
	return filepath.Join(projectDir, ".sync", activeMarkerFile)
}

// AcquireActiveMarker writes the per-project activation marker if the project
// looks initialized (.sync/anchor.yaml exists). The returned release func is
// idempotent and removes only the marker written by this process.
func AcquireActiveMarker(projectDir string) (func(), error) {
	anchorPath := filepath.Join(projectDir, ".sync", "anchor.yaml")
	if _, err := os.Stat(anchorPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return func() {}, nil
		}
		return nil, err
	}

	marker := activeMarker{
		PID:     os.Getpid(),
		Started: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(ActiveMarkerPath(projectDir), data); err != nil {
		return nil, err
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		path := ActiveMarkerPath(projectDir)
		current, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var got activeMarker
		if err := json.Unmarshal(current, &got); err != nil {
			return
		}
		if got.PID == marker.PID && got.Started == marker.Started {
			_ = os.Remove(path)
		}
	}, nil
}

// IsActive returns whether the project currently has a live `vibelog serve`
// activation marker.
func IsActive(projectDir string) bool {
	data, err := os.ReadFile(ActiveMarkerPath(projectDir))
	if err != nil {
		return false
	}
	var marker activeMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	return processAlive(marker.PID)
}

func atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
