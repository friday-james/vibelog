// Package initcmd implements `cockpit init` — creates a fresh .sync/ skeleton
// in a project directory so the writer (MCP server) has somewhere to record.
//
// The skeleton is intentionally minimum-valid: TODO statements with missing-
// decision evidence (validates because missing counts), a self-recording
// iter#1, an empty claims list. The user fills in real content after init.
package initcmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"cockpit/internal/model"
)

// ErrAlreadyInitialized is returned when .sync/anchor.yaml exists at the target.
var ErrAlreadyInitialized = errors.New("already initialized")

// Run creates dir/.sync/{anchor.yaml, claims.yaml, iterations.jsonl} with
// validate-passing placeholder content. Idempotency: returns
// ErrAlreadyInitialized if anchor.yaml already exists.
func Run(dir string) error {
	syncDir := filepath.Join(dir, ".sync")
	anchorPath := filepath.Join(syncDir, "anchor.yaml")

	if _, err := os.Stat(anchorPath); err == nil {
		return fmt.Errorf("%w at %s", ErrAlreadyInitialized, syncDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat anchor: %w", err)
	}

	if err := os.MkdirAll(syncDir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	user := os.Getenv("USER")
	if user == "" {
		user = "init"
	}

	anchor := &model.Anchor{
		Intent: model.Intent{
			Statement: "TODO: state what you are building, in one sentence.",
			Evidence: []model.Evidence{{
				Type: model.EvidenceMissing, Kind: model.MissingDecision,
				Note: "Author this when you have a written intent (DESIGN.md / README / ADR).",
			}},
			Established:   now,
			EstablishedBy: user,
		},
		Approach: model.Approach{
			Statement: "TODO: describe how you're going to build it.",
			Evidence: []model.Evidence{{
				Type: model.EvidenceMissing, Kind: model.MissingDecision,
				Note: "Author this once a clear approach is in place.",
			}},
			LastChanged:  now,
			ChangeReason: "initialized by cockpit init",
		},
		Now: model.Now{
			Statement:   "TODO: what are you doing right now?",
			IterationID: 1,
			Started:     now,
		},
	}
	if err := anchor.Validate(); err != nil {
		return fmt.Errorf("internal: init produced invalid anchor: %w", err)
	}

	anchorBytes, err := yaml.Marshal(anchor)
	if err != nil {
		return fmt.Errorf("marshal anchor: %w", err)
	}
	if err := os.WriteFile(anchorPath, anchorBytes, 0o644); err != nil {
		return fmt.Errorf("write anchor: %w", err)
	}

	if err := os.WriteFile(filepath.Join(syncDir, "claims.yaml"), []byte("[]\n"), 0o644); err != nil {
		return fmt.Errorf("write claims: %w", err)
	}

	iter1 := model.Iteration{
		ID: 1, Ts: now, Kind: model.KindIteration,
		Summary: "cockpit init", Agent: "cockpit-cli",
	}
	iter1Bytes, err := json.Marshal(iter1)
	if err != nil {
		return fmt.Errorf("marshal iteration: %w", err)
	}
	if err := os.WriteFile(filepath.Join(syncDir, "iterations.jsonl"), append(iter1Bytes, '\n'), 0o644); err != nil {
		return fmt.Errorf("write iterations: %w", err)
	}

	return nil
}
