// Package gitcmd implements `vibelog ingest-git` — walks `git log` and
// appends each new commit to iterations.jsonl as a kind=commit entry, so
// the rail shows real commits alongside agent iterations.
//
// Idempotent on SHA: re-running ingests only commits not already recorded.
// Commit ids start at 1 and increment within the commit-kind sequence
// (composite (kind, id) uniqueness in the model means commit#3 and iter#3
// can coexist).
package gitcmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"vibelog/internal/model"
	"vibelog/internal/store"
)

// Result reports what ingest-git did.
type Result struct {
	Added   int // commits newly appended
	Skipped int // commits already present
}

// Run walks `git log` in projectDir and appends new commits to
// .sync/iterations.jsonl. limit caps how many commits to consider (0 = all).
func Run(projectDir string, limit int) (Result, error) {
	state, err := store.Load(projectDir)
	if err != nil {
		return Result{}, fmt.Errorf("load current state: %w", err)
	}

	existing := make(map[string]bool)
	maxCommitID := 0
	for _, it := range state.Iterations {
		if it.Kind == model.KindCommit && it.SHA != "" {
			existing[it.SHA] = true
			if it.ID > maxCommitID {
				maxCommitID = it.ID
			}
		}
	}

	args := []string{"-C", projectDir, "log", "--pretty=format:%h|%ct|%s", "--no-merges"}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		// Distinguish "not a git repo" from real errors so callers can no-op.
		if ee, ok := err.(*exec.ExitError); ok {
			return Result{}, fmt.Errorf("git log failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return Result{}, fmt.Errorf("git log: %w", err)
	}

	// git log returns newest first. Walk reverse so commits are appended in
	// chronological order (matches iterations.jsonl convention).
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	iterPath := filepath.Join(projectDir, ".sync", "iterations.jsonl")
	f, err := os.OpenFile(iterPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("open iterations.jsonl: %w", err)
	}
	defer f.Close()

	res := Result{}
	nextID := maxCommitID + 1
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		sha := parts[0]
		if existing[sha] {
			res.Skipped++
			continue
		}
		ts, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		summary := parts[2]

		iter := model.Iteration{
			ID:      nextID,
			Ts:      time.Unix(ts, 0).UTC(),
			Kind:    model.KindCommit,
			Summary: summary,
			SHA:     sha,
		}
		if err := iter.Validate(); err != nil {
			continue
		}
		buf, err := json.Marshal(iter)
		if err != nil {
			continue
		}
		if _, err := f.Write(append(buf, '\n')); err != nil {
			return res, fmt.Errorf("append commit %s: %w", sha, err)
		}
		nextID++
		res.Added++
	}
	return res, nil
}
