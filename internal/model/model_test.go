package model

import (
	"strings"
	"testing"
	"time"
)

func validState() *State {
	return &State{
		Anchor: Anchor{
			Intent: Intent{
				Statement: "x",
				Evidence:  []Evidence{{Type: EvidenceDoc, Path: "docs/intent.md"}},
				Established: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			Approach: Approach{
				Statement: "x",
				Evidence:  []Evidence{{Type: EvidenceDoc, Path: "docs/approach.md"}},
				LastChanged: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
			Now: Now{
				Statement:   "x",
				IterationID: 1,
				Started:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		Iterations: []Iteration{
			{ID: 12, Ts: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindIteration, Summary: "scaffold"},
		},
	}
}

func TestState_Validate_Happy(t *testing.T) {
	s := validState()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid state failed: %v", err)
	}
}

func TestIteration_Commit_RequiresSHA(t *testing.T) {
	it := Iteration{ID: 3, Ts: time.Now(), Kind: KindCommit, Summary: "scaffold"}
	if err := it.Validate(); err == nil || !strings.Contains(err.Error(), "sha") {
		t.Fatalf("expected sha-required error, got %v", err)
	}
}

func TestState_DuplicateIterationID(t *testing.T) {
	s := validState()
	s.Iterations = append(s.Iterations, s.Iterations[0])
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate iteration id") {
		t.Fatalf("expected duplicate-iteration-id error, got %v", err)
	}
}

func TestState_SameIDDifferentKind_OK(t *testing.T) {
	s := validState()
	s.Iterations = append(s.Iterations, Iteration{
		ID: 12, Ts: s.Iterations[0].Ts, Kind: KindCommit, SHA: "abc123", Summary: "scaffold",
	})
	if err := s.Validate(); err != nil {
		t.Fatalf("iter#12 + commit#12 should coexist, got %v", err)
	}
}

func TestIteration_SupersededFields_OK(t *testing.T) {
	now := time.Now()
	it := Iteration{
		ID: 5, Ts: now, Kind: KindIteration, Summary: "rolled-back work",
		TranscriptMessageID: "msg-abc-123",
		FileHashes:          map[string]string{"foo.go": "deadbeef"},
		SupersededAt:        &now,
		SupersededReason:    "rollback",
	}
	if err := it.Validate(); err != nil {
		t.Fatalf("supersede metadata should validate, got %v", err)
	}
}

func TestIteration_SupersededAt_RequiresReason(t *testing.T) {
	at := time.Now()
	it := Iteration{ID: 5, Ts: time.Now(), Kind: KindIteration, SupersededAt: &at}
	err := it.Validate()
	if err == nil || !strings.Contains(err.Error(), "superseded_reason empty") {
		t.Fatalf("expected reason-required error, got %v", err)
	}
}

func TestIteration_SupersededReason_RequiresAt(t *testing.T) {
	it := Iteration{ID: 5, Ts: time.Now(), Kind: KindIteration, SupersededReason: "rollback"}
	err := it.Validate()
	if err == nil || !strings.Contains(err.Error(), "superseded_at empty") {
		t.Fatalf("expected at-required error, got %v", err)
	}
}

func TestIteration_BadSupersededReason(t *testing.T) {
	at := time.Now()
	it := Iteration{ID: 5, Ts: time.Now(), Kind: KindIteration, SupersededAt: &at, SupersededReason: "vibes"}
	err := it.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid superseded_reason") {
		t.Fatalf("expected invalid-reason error, got %v", err)
	}
}
