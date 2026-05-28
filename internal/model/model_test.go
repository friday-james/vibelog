package model

import (
	"strings"
	"testing"
	"time"
)

func validClaim() Claim {
	return Claim{
		ID:        "rate-limit-survives-restart",
		Statement: "Rate-limit state survives process restart.",
		Category:  CategoryInvariant,
		Status:    StatusViolated,
		Severity:  SeverityHigh,
		Evidence: []Evidence{
			{Type: EvidenceCode, Path: "gateway/middleware/rate_limit.py", Line: 14, Polarity: PolarityNegative, Note: "in-memory dict"},
		},
		Established: time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC),
	}
}

func validState() State {
	now := time.Date(2026, 5, 27, 14, 32, 0, 0, time.UTC)
	return State{
		Anchor: Anchor{
			Intent: Intent{
				Statement: "Passwordless auth via magic links.",
				Evidence: []Evidence{
					{Type: EvidenceDoc, Path: "docs/specs/auth-v2.md"},
				},
				Established:   time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
				EstablishedBy: "charlene",
			},
			Approach: Approach{
				Statement: "JWT + rotating refresh tokens in Redis.",
				Evidence: []Evidence{
					{Type: EvidenceDecision, Path: "docs/adr/015-jwt-refresh-rotation.md"},
				},
				LastChanged:  now.Add(-90 * time.Minute),
				ChangeReason: "security review",
			},
			Now: Now{
				Statement:   "Adding per-IP rate limit.",
				IterationID: 12,
				Started:     now,
			},
		},
		Claims: []Claim{validClaim()},
		Iterations: []Iteration{
			{ID: 12, Ts: now, Kind: KindIteration, Summary: "per-IP rate limit", Agent: "claude-code", SessionID: "sess-abc"},
		},
	}
}

func TestState_Validate_Happy(t *testing.T) {
	s := validState()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid state failed: %v", err)
	}
}

func TestClaim_RequiresEvidence(t *testing.T) {
	c := validClaim()
	c.Evidence = nil
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one evidence") {
		t.Fatalf("expected at-least-one-evidence error, got %v", err)
	}
}

func TestClaim_BadCategory(t *testing.T) {
	c := validClaim()
	c.Category = "vibes"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid category") {
		t.Fatalf("expected invalid-category error, got %v", err)
	}
}

func TestClaim_BadStatus(t *testing.T) {
	c := validClaim()
	c.Status = "maybe"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("expected invalid-status error, got %v", err)
	}
}

func TestEvidence_Code_RequiresPath(t *testing.T) {
	e := Evidence{Type: EvidenceCode, Polarity: PolarityNegative}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected path-required error, got %v", err)
	}
}

func TestEvidence_Code_RequiresPolarity(t *testing.T) {
	e := Evidence{Type: EvidenceCode, Path: "x.go"}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "polarity") {
		t.Fatalf("expected polarity-required error, got %v", err)
	}
}

func TestEvidence_Missing_RequiresKind(t *testing.T) {
	e := Evidence{Type: EvidenceMissing}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected kind-required error, got %v", err)
	}
}

func TestEvidence_Missing_BadKind(t *testing.T) {
	e := Evidence{Type: EvidenceMissing, Kind: "bogus"}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected bad-kind error, got %v", err)
	}
}

func TestEvidence_UnknownType(t *testing.T) {
	e := Evidence{Type: "rumor"}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "unknown evidence type") {
		t.Fatalf("expected unknown-type error, got %v", err)
	}
}

func TestEvidence_Commit_RequiresSHA(t *testing.T) {
	e := Evidence{Type: EvidenceCommit}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "sha") {
		t.Fatalf("expected sha-required error, got %v", err)
	}
}

func TestEvidence_Metric_RequiresRef(t *testing.T) {
	e := Evidence{Type: EvidenceMetric}
	if err := e.Validate(); err == nil || !strings.Contains(err.Error(), "ref") {
		t.Fatalf("expected ref-required error, got %v", err)
	}
}

func TestIteration_Commit_RequiresSHA(t *testing.T) {
	it := Iteration{ID: 3, Ts: time.Now(), Kind: KindCommit, Summary: "scaffold"}
	if err := it.Validate(); err == nil || !strings.Contains(err.Error(), "sha") {
		t.Fatalf("expected sha-required error, got %v", err)
	}
}

func TestState_DuplicateClaimID(t *testing.T) {
	s := validState()
	s.Claims = append(s.Claims, validClaim()) // same id again
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate claim id") {
		t.Fatalf("expected duplicate-claim-id error, got %v", err)
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
		t.Fatalf("expected iter#12 + commit#12 to coexist, got %v", err)
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
