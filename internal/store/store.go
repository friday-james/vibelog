// Package store reads the three .sync/ files into a validated *model.State.
//
// Load is read-only and synchronous. It is the only path Phase 0 exercises;
// writers (MCP, Stop hook + sub-agent) land in Phase 5 and live elsewhere.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"cockpit/internal/model"
)

// Load reads dir/.sync/{anchor.yaml, claims.yaml, iterations.jsonl} and
// returns a validated state. Errors are wrapped (%w), so callers can use
// errors.Is(err, fs.ErrNotExist) to distinguish missing files from
// malformed content or validation failures.
func Load(dir string) (*model.State, error) {
	syncDir := filepath.Join(dir, ".sync")

	var anchor model.Anchor
	if err := readYAML(filepath.Join(syncDir, "anchor.yaml"), &anchor); err != nil {
		return nil, fmt.Errorf("anchor.yaml: %w", err)
	}

	var claims []model.Claim
	if err := readYAML(filepath.Join(syncDir, "claims.yaml"), &claims); err != nil {
		return nil, fmt.Errorf("claims.yaml: %w", err)
	}

	iterations, err := readJSONL(filepath.Join(syncDir, "iterations.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("iterations.jsonl: %w", err)
	}

	state := &model.State{
		Anchor:     anchor,
		Claims:     claims,
		Iterations: iterations,
	}
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return state, nil
}

func readYAML(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

func readJSONL(path string) ([]model.Iteration, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []model.Iteration
	for lineNum, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var it model.Iteration
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum+1, err)
		}
		out = append(out, it)
	}
	return out, nil
}
