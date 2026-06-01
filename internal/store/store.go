// Package store reads the three .sync/ files into a validated *model.State.
//
// Load is read-only and synchronous. Writers such as the MCP server and
// transcript observer live outside this package.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/friday-james/vibelog/internal/model"
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

	iterations, err := readJSONL(filepath.Join(syncDir, "iterations.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("iterations.jsonl: %w", err)
	}

	state := &model.State{
		Anchor:     anchor,
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

// readJSONL is forgiving on a per-row basis: an iteration written by a NEWER
// vibelog binary (e.g. a kind this binary doesn't yet recognise) is dropped
// with a stderr warning rather than failing the whole load. Without this, a
// single-row schema mismatch turns every future agent hook into an error
// — including hooks from older sessions whose binary is out of date relative
// to the project's .sync/.
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
			fmt.Fprintf(os.Stderr, "vibelog: skipping iterations.jsonl line %d (unparseable): %v\n", lineNum+1, err)
			continue
		}
		if err := it.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "vibelog: skipping iterations.jsonl line %d (validation failed — likely a newer binary wrote this row): %v\n", lineNum+1, err)
			continue
		}
		out = append(out, it)
	}
	return out, nil
}
