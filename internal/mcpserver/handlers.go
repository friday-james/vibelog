// Package mcpserver wires vibelog's MCP tools and runs the stdio server.
//
// Each tool's logic lives in a pure function (RecordIteration, etc.) so it
// can be unit-tested without the JSON-RPC plumbing. server.go is a thin
// adapter that decodes the MCP CallToolRequest and forwards to the pure
// function.
package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"vibelog/internal/model"
	"vibelog/internal/store"
)

// RecordIterationArgs is the typed input for the record_iteration tool.
type RecordIterationArgs struct {
	Summary             string   `json:"summary"`
	FilesChanged        []string `json:"files_changed,omitempty"`
	ClaimsAdded         []string `json:"claims_added,omitempty"`
	ClaimsViolated      []string `json:"claims_violated,omitempty"`
	TranscriptMessageID string   `json:"transcript_message_id,omitempty"`
	UserPrompt          string   `json:"user_prompt,omitempty"`
	Implementation      string   `json:"implementation,omitempty"`
}

// AssertClaimArgs is the typed input for the assert_claim tool. evidence_json
// is a JSON-encoded array of evidence objects so the tool can pass arbitrary
// nested shapes through the MCP wire (mark3labs/mcp-go is awkward with deeply
// nested schemas).
type AssertClaimArgs struct {
	ID            string `json:"id"`
	Statement     string `json:"statement"`
	Category      string `json:"category"`
	Status        string `json:"status"`
	Severity      string `json:"severity"`
	EvidenceJSON  string `json:"evidence_json"`
	EstablishedBy string `json:"established_by,omitempty"`
	RelatedClaims string `json:"related_claims,omitempty"` // comma-separated ids
}

// UpdateAnchorArgs is the typed input for the update_anchor tool. Each section
// is optional; passing JSON for a section replaces it in anchor.yaml. Omit to
// leave that section unchanged.
type UpdateAnchorArgs struct {
	IntentJSON   string `json:"intent_json,omitempty"`
	ApproachJSON string `json:"approach_json,omitempty"`
	NowJSON      string `json:"now_json,omitempty"`
}

// SetImplementationArgs is the typed input for the set_implementation tool.
// Summary is the L0 subtitle (1-2 lines); Text is the L1 long teach-back.
type SetImplementationArgs struct {
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// pendingEnvelope is the JSON shape written to .sync/pending_implementation.txt.
// observe validates session_id + ts before consuming.
type pendingEnvelope struct {
	Summary   string `json:"summary"`
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
	Ts        string `json:"ts"`
}

// RecordIteration appends a new iteration to projectDir/.sync/iterations.jsonl,
// auto-assigning id (max iteration-kind id + 1), ts (now), kind=iteration,
// agent="claude-code", and session_id from $CLAUDE_SESSION_ID. The new
// iteration is validated before write; returns the recorded value.
//
// Atomicity: append is via O_APPEND on POSIX, which is atomic for writes up to
// PIPE_BUF (>=4096B on Linux/macOS). A single JSONL line is always smaller, so
// concurrent appends from multiple writers will not interleave.
func RecordIteration(projectDir string, args RecordIterationArgs) (*model.Iteration, error) {
	state, err := store.Load(projectDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(".sync/ not initialized at %s — run `vibelog init` first", projectDir)
		}
		return nil, fmt.Errorf("load current state: %w", err)
	}

	// Idempotency: if the same transcript_message_id is already recorded,
	// return that entry rather than appending a duplicate. Protects against
	// Stop-hook double-fires.
	if args.TranscriptMessageID != "" {
		for i := range state.Iterations {
			if state.Iterations[i].TranscriptMessageID == args.TranscriptMessageID {
				return &state.Iterations[i], nil
			}
		}
	}

	// Next id walks ALL iteration-like kinds (iteration + legacy external_edit
	// rows in older histories) so they share a single linear timeline. Commit
	// ids stay in their own sequence (composite (kind,id) uniqueness) and
	// don't perturb this.
	nextID := 1
	for _, it := range state.Iterations {
		if (it.Kind == model.KindIteration || it.Kind == model.KindExternalEdit) && it.ID >= nextID {
			nextID = it.ID + 1
		}
	}

	iter := model.Iteration{
		ID:                  nextID,
		Ts:                  time.Now().UTC().Truncate(time.Second),
		Kind:                model.KindIteration,
		Summary:             args.Summary,
		FilesChanged:        args.FilesChanged,
		ClaimsAdded:         args.ClaimsAdded,
		ClaimsViolated:      args.ClaimsViolated,
		Agent:               "claude-code",
		SessionID:           os.Getenv("CLAUDE_SESSION_ID"),
		TranscriptMessageID: args.TranscriptMessageID,
		UserPrompt:          args.UserPrompt,
		Implementation:      args.Implementation,
	}
	if err := iter.Validate(); err != nil {
		return nil, fmt.Errorf("invalid iteration: %w", err)
	}

	line, err := json.Marshal(iter)
	if err != nil {
		return nil, fmt.Errorf("marshal iteration: %w", err)
	}
	line = append(line, '\n')

	// H1: snapshot every touched file BEFORE appending the iter row. The
	// drift detector reads "the most recent prior snapshot for this file"
	// the moment it sees a new row; if the row landed before its snapshots,
	// a concurrent poll could briefly hash-mismatch and falsely flag the
	// agent's own write as drift. Ordering closes that race.
	snapshotsDir := filepath.Join(projectDir, ".sync", "snapshots", fmt.Sprintf("iter-%d", iter.ID))
	for _, rel := range iter.FilesChanged {
		src := filepath.Join(projectDir, rel)
		dst := filepath.Join(snapshotsDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			continue
		}
		// H2: atomic snapshot write (tmp + rename) so a half-written snapshot
		// can't be observed by a concurrent reader.
		_ = atomicCopyFile(src, dst)
	}

	iterPath := filepath.Join(projectDir, ".sync", "iterations.jsonl")
	f, err := os.OpenFile(iterPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open iterations.jsonl: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return nil, fmt.Errorf("append: %w", err)
	}

	return &iter, nil
}

// atomicCopyFile copies src → dst by writing to dst.tmp first then renaming,
// so a partial write is never observable by readers. Safe across crashes:
// the dst either has the old content (if rename never happened) or the new
// content (if rename completed). Same-filesystem rename is atomic on macOS.
func atomicCopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".snap-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// AssertClaim creates or updates a claim by id in projectDir's claims.yaml.
// If a claim with the same id exists, its fields are overwritten in place
// (preserving the original Established date). Otherwise a new claim is
// appended. The whole file is rewritten atomically (tmp + rename).
func AssertClaim(projectDir string, args AssertClaimArgs) (*model.Claim, error) {
	state, err := store.Load(projectDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(".sync/ not initialized at %s — run `vibelog init` first", projectDir)
		}
		return nil, fmt.Errorf("load current state: %w", err)
	}

	var evidence []model.Evidence
	if strings.TrimSpace(args.EvidenceJSON) == "" {
		return nil, fmt.Errorf("evidence_json is required (use a 'missing' entry if no positive evidence)")
	}
	if err := json.Unmarshal([]byte(args.EvidenceJSON), &evidence); err != nil {
		return nil, fmt.Errorf("parse evidence_json: %w", err)
	}

	var related []string
	if strings.TrimSpace(args.RelatedClaims) != "" {
		for _, r := range strings.Split(args.RelatedClaims, ",") {
			if s := strings.TrimSpace(r); s != "" {
				related = append(related, s)
			}
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	claim := model.Claim{
		ID:            args.ID,
		Statement:     args.Statement,
		Category:      model.ClaimCategory(args.Category),
		Status:        model.ClaimStatus(args.Status),
		Severity:      model.Severity(args.Severity),
		Evidence:      evidence,
		Established:   now,
		EstablishedBy: args.EstablishedBy,
		RelatedClaims: related,
	}

	updated := false
	for i := range state.Claims {
		if state.Claims[i].ID == claim.ID {
			// Preserve original Established date; the rest is replaced.
			claim.Established = state.Claims[i].Established
			claim.LastVerified = &now
			state.Claims[i] = claim
			updated = true
			break
		}
	}
	if !updated {
		state.Claims = append(state.Claims, claim)
	}

	if err := claim.Validate(); err != nil {
		return nil, fmt.Errorf("claim validation: %w", err)
	}

	data, err := yaml.Marshal(state.Claims)
	if err != nil {
		return nil, fmt.Errorf("marshal claims: %w", err)
	}
	if err := atomicWrite(filepath.Join(projectDir, ".sync", "claims.yaml"), data); err != nil {
		return nil, err
	}
	return &claim, nil
}

// UpdateAnchor applies optional JSON-encoded patches to one or more sections
// of anchor.yaml (intent, approach, now). Each section is replaced wholesale
// if provided. Empty section args leave that section unchanged. The whole
// file is rewritten atomically.
func UpdateAnchor(projectDir string, args UpdateAnchorArgs) (*model.Anchor, error) {
	state, err := store.Load(projectDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(".sync/ not initialized at %s — run `vibelog init` first", projectDir)
		}
		return nil, fmt.Errorf("load current state: %w", err)
	}
	anchor := state.Anchor

	if s := strings.TrimSpace(args.IntentJSON); s != "" {
		var v model.Intent
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("parse intent_json: %w", err)
		}
		anchor.Intent = v
	}
	if s := strings.TrimSpace(args.ApproachJSON); s != "" {
		var v model.Approach
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("parse approach_json: %w", err)
		}
		anchor.Approach = v
	}
	if s := strings.TrimSpace(args.NowJSON); s != "" {
		var v model.Now
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("parse now_json: %w", err)
		}
		anchor.Now = v
	}

	if err := anchor.Validate(); err != nil {
		return nil, fmt.Errorf("anchor validation: %w", err)
	}
	data, err := yaml.Marshal(&anchor)
	if err != nil {
		return nil, fmt.Errorf("marshal anchor: %w", err)
	}
	if err := atomicWrite(filepath.Join(projectDir, ".sync", "anchor.yaml"), data); err != nil {
		return nil, err
	}
	return &anchor, nil
}

// atomicWrite writes data to path via tmp + rename so readers never see a
// half-written file (POSIX-atomic rename within the same directory).
func atomicWrite(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}

// SetImplementation writes a teach-back envelope to
// .sync/pending_implementation.txt. observe (running in the Stop hook)
// consumes the envelope and applies the text to that turn's iteration
// record, then deletes the file. Multi-call-per-turn → last call wins.
//
// Envelope carries session_id + timestamp so observe can reject stale
// pending files (left over from a crashed observe in a previous turn).
func SetImplementation(projectDir string, args SetImplementationArgs) error {
	if strings.TrimSpace(args.Text) == "" {
		return fmt.Errorf("text is required")
	}
	if strings.TrimSpace(args.Summary) == "" {
		return fmt.Errorf("summary is required (1-2 line condensed teach-back for the L0 subtitle)")
	}
	env := pendingEnvelope{
		Summary:   args.Summary,
		Text:      args.Text,
		SessionID: os.Getenv("CLAUDE_SESSION_ID"),
		Ts:        time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".sync")); err != nil {
		return fmt.Errorf(".sync/ not initialized at %s — run `vibelog init`", projectDir)
	}
	return atomicWrite(filepath.Join(projectDir, ".sync", "pending_implementation.txt"), data)
}
