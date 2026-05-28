package model

import (
	"fmt"
	"time"
)

// State is the in-memory union of everything under .sync/.
type State struct {
	Anchor     Anchor      `json:"anchor"`
	Claims     []Claim     `json:"claims"`
	Iterations []Iteration `json:"iterations"`
}

func (s *State) Validate() error {
	if err := s.Anchor.Validate(); err != nil {
		return fmt.Errorf("anchor: %w", err)
	}
	seenClaim := make(map[string]bool, len(s.Claims))
	for _, c := range s.Claims {
		if seenClaim[c.ID] {
			return fmt.Errorf("duplicate claim id %q", c.ID)
		}
		seenClaim[c.ID] = true
		if err := c.Validate(); err != nil {
			return err
		}
	}
	type entryKey struct {
		kind IterationKind
		id   int
	}
	seenEntry := make(map[entryKey]bool, len(s.Iterations))
	for _, it := range s.Iterations {
		k := entryKey{it.Kind, it.ID}
		if seenEntry[k] {
			return fmt.Errorf("duplicate %s id %d", it.Kind, it.ID)
		}
		seenEntry[k] = true
		if err := it.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ----- Anchor -----

type Anchor struct {
	Intent   Intent   `yaml:"intent" json:"intent"`
	Approach Approach `yaml:"approach" json:"approach"`
	Now      Now      `yaml:"now" json:"now"`
}

type Intent struct {
	Statement     string     `yaml:"statement" json:"statement"`
	Evidence      []Evidence `yaml:"evidence" json:"evidence"`
	Established   time.Time  `yaml:"established" json:"established"`
	EstablishedBy string     `yaml:"established_by" json:"established_by"`
}

type Approach struct {
	Statement    string     `yaml:"statement" json:"statement"`
	Evidence     []Evidence `yaml:"evidence" json:"evidence"`
	LastChanged  time.Time  `yaml:"last_changed" json:"last_changed"`
	ChangeReason string     `yaml:"change_reason" json:"change_reason"`
}

type Now struct {
	Statement   string    `yaml:"statement" json:"statement"`
	IterationID int       `yaml:"iteration_id" json:"iteration_id"`
	Started     time.Time `yaml:"started" json:"started"`
}

func (a *Anchor) Validate() error {
	if err := a.Intent.Validate(); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	if err := a.Approach.Validate(); err != nil {
		return fmt.Errorf("approach: %w", err)
	}
	if err := a.Now.Validate(); err != nil {
		return fmt.Errorf("now: %w", err)
	}
	return nil
}

func (i *Intent) Validate() error {
	if i.Statement == "" {
		return fmt.Errorf("statement is required")
	}
	if len(i.Evidence) == 0 {
		return fmt.Errorf("at least one evidence entry required (use type=missing if none)")
	}
	for k, e := range i.Evidence {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("evidence[%d]: %w", k, err)
		}
	}
	if i.Established.IsZero() {
		return fmt.Errorf("established is required")
	}
	return nil
}

func (a *Approach) Validate() error {
	if a.Statement == "" {
		return fmt.Errorf("statement is required")
	}
	if len(a.Evidence) == 0 {
		return fmt.Errorf("at least one evidence entry required")
	}
	for k, e := range a.Evidence {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("evidence[%d]: %w", k, err)
		}
	}
	return nil
}

func (n *Now) Validate() error {
	if n.Statement == "" {
		return fmt.Errorf("statement is required")
	}
	if n.IterationID <= 0 {
		return fmt.Errorf("iteration_id must be positive")
	}
	return nil
}

// ----- Claim -----

type ClaimCategory string

const (
	CategoryInvariant   ClaimCategory = "invariant"
	CategoryContract    ClaimCategory = "contract"
	CategoryFailureMode ClaimCategory = "failure-mode"
	CategoryAssumption  ClaimCategory = "assumption"
)

type ClaimStatus string

const (
	StatusUnknown   ClaimStatus = "unknown"
	StatusSuspected ClaimStatus = "suspected"
	StatusHolding   ClaimStatus = "holding"
	StatusViolated  ClaimStatus = "violated"
)

type Severity string

const (
	SeverityLow  Severity = "low"
	SeverityMed  Severity = "med"
	SeverityHigh Severity = "high"
)

type Claim struct {
	ID                     string        `yaml:"id" json:"id"`
	Statement              string        `yaml:"statement" json:"statement"`
	Category               ClaimCategory `yaml:"category" json:"category"`
	Status                 ClaimStatus   `yaml:"status" json:"status"`
	Severity               Severity      `yaml:"severity" json:"severity"`
	Evidence               []Evidence    `yaml:"evidence" json:"evidence"`
	Established            time.Time     `yaml:"established" json:"established"`
	EstablishedBy          string        `yaml:"established_by,omitempty" json:"established_by,omitempty"`
	EstablishedInIteration int           `yaml:"established_in_iteration,omitempty" json:"established_in_iteration,omitempty"`
	LastVerified           *time.Time    `yaml:"last_verified,omitempty" json:"last_verified,omitempty"`
	RelatedClaims          []string      `yaml:"related_claims,omitempty" json:"related_claims,omitempty"`
}

func (c *Claim) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("claim: id is required")
	}
	if c.Statement == "" {
		return fmt.Errorf("claim %s: statement is required", c.ID)
	}
	switch c.Category {
	case CategoryInvariant, CategoryContract, CategoryFailureMode, CategoryAssumption:
	default:
		return fmt.Errorf("claim %s: invalid category %q (want invariant|contract|failure-mode|assumption)", c.ID, c.Category)
	}
	switch c.Status {
	case StatusUnknown, StatusSuspected, StatusHolding, StatusViolated:
	default:
		return fmt.Errorf("claim %s: invalid status %q (want unknown|suspected|holding|violated)", c.ID, c.Status)
	}
	switch c.Severity {
	case SeverityLow, SeverityMed, SeverityHigh:
	default:
		return fmt.Errorf("claim %s: invalid severity %q (want low|med|high)", c.ID, c.Severity)
	}
	if len(c.Evidence) == 0 {
		return fmt.Errorf("claim %s: must have at least one evidence entry (use type=missing if no positive evidence)", c.ID)
	}
	for i, ev := range c.Evidence {
		if err := ev.Validate(); err != nil {
			return fmt.Errorf("claim %s: evidence[%d]: %w", c.ID, i, err)
		}
	}
	return nil
}

// ----- Evidence (discriminated union on Type) -----

type EvidenceType string

const (
	EvidenceCode      EvidenceType = "code"
	EvidenceTest      EvidenceType = "test"
	EvidenceDoc       EvidenceType = "doc"
	EvidenceDecision  EvidenceType = "decision"
	EvidenceBenchmark EvidenceType = "benchmark"
	EvidenceMetric    EvidenceType = "metric"
	EvidenceCommit    EvidenceType = "commit"
	EvidenceMissing   EvidenceType = "missing"
)

type Polarity string

const (
	PolarityPositive Polarity = "positive"
	PolarityNegative Polarity = "negative"
)

type MissingKind string

const (
	MissingTest         MissingKind = "test"
	MissingComms        MissingKind = "comms"
	MissingDecision     MissingKind = "decision"
	MissingVerification MissingKind = "verification"
)

// Evidence is a flat struct; field validity is gated by Type via Validate().
// Discriminated-union shape was rejected for Phase 0 (extra UnmarshalYAML cost,
// no semantic gain — Validate() gives the same parse-time safety).
type Evidence struct {
	Type EvidenceType `yaml:"type" json:"type"`

	Path     string      `yaml:"path,omitempty" json:"path,omitempty"`           // code, test, doc, decision, benchmark
	Line     int         `yaml:"line,omitempty" json:"line,omitempty"`           // code
	Polarity Polarity    `yaml:"polarity,omitempty" json:"polarity,omitempty"`   // code, test
	Note     string      `yaml:"note,omitempty" json:"note,omitempty"`           // any
	Ref      string      `yaml:"ref,omitempty" json:"ref,omitempty"`             // metric
	SHA      string      `yaml:"sha,omitempty" json:"sha,omitempty"`             // commit
	Kind     MissingKind `yaml:"kind,omitempty" json:"kind,omitempty"`           // missing
}

func (e *Evidence) Validate() error {
	switch e.Type {
	case EvidenceCode:
		if e.Path == "" {
			return fmt.Errorf("code evidence requires path")
		}
		if e.Polarity != PolarityPositive && e.Polarity != PolarityNegative {
			return fmt.Errorf("code evidence requires polarity (positive|negative), got %q", e.Polarity)
		}
	case EvidenceTest:
		if e.Path == "" {
			return fmt.Errorf("test evidence requires path")
		}
		if e.Polarity != PolarityPositive && e.Polarity != PolarityNegative {
			return fmt.Errorf("test evidence requires polarity (positive|negative), got %q", e.Polarity)
		}
	case EvidenceDoc:
		if e.Path == "" {
			return fmt.Errorf("doc evidence requires path")
		}
	case EvidenceDecision:
		if e.Path == "" {
			return fmt.Errorf("decision evidence requires path")
		}
	case EvidenceBenchmark:
		if e.Path == "" {
			return fmt.Errorf("benchmark evidence requires path")
		}
	case EvidenceMetric:
		if e.Ref == "" {
			return fmt.Errorf("metric evidence requires ref")
		}
	case EvidenceCommit:
		if e.SHA == "" {
			return fmt.Errorf("commit evidence requires sha")
		}
	case EvidenceMissing:
		switch e.Kind {
		case MissingTest, MissingComms, MissingDecision, MissingVerification:
		default:
			return fmt.Errorf("missing evidence requires kind in {test, comms, decision, verification}, got %q", e.Kind)
		}
	case "":
		return fmt.Errorf("evidence type is required")
	default:
		return fmt.Errorf("unknown evidence type %q", e.Type)
	}
	return nil
}

// ----- Iteration -----

type IterationKind string

const (
	KindIteration IterationKind = "iteration"
	KindCommit    IterationKind = "commit"
)

// Iteration is one assistant turn that touched code (kind=iteration) OR one
// git commit (kind=commit). Each kind has its own id sequence; uniqueness is
// (kind, id), not id alone.
//
// The supersede fields (TranscriptMessageID, FileHashes, SupersededAt,
// SupersededReason) support post-hoc reconciliation when Claude Code's
// double-Esc rollback or a file-level rollback (git checkout, editor undo,
// etc.) leaves .sync/ entries that no longer reflect reality. Writers
// populate the anchors; a watcher marks entries superseded when anchors
// diverge.
type Iteration struct {
	ID             int           `json:"id"`
	Ts             time.Time     `json:"ts"`
	Kind           IterationKind `json:"kind"`
	Summary        string        `json:"summary,omitempty"`
	FilesChanged   []string      `json:"files_changed,omitempty"`
	ClaimsAdded    []string      `json:"claims_added,omitempty"`
	ClaimsViolated []string      `json:"claims_violated,omitempty"`
	Agent          string        `json:"agent,omitempty"`
	SessionID      string        `json:"session_id,omitempty"` // $CLAUDE_SESSION_ID for kind=iteration
	SHA            string        `json:"sha,omitempty"`        // required for kind=commit

	// Supersede anchors + state (all optional)
	TranscriptMessageID string            `json:"transcript_message_id,omitempty"` // assistant turn UUID — for conversation-rollback detection
	FileHashes          map[string]string `json:"file_hashes,omitempty"`           // path -> sha256 of post-state content, for file-divergence detection
	SupersededAt        *time.Time        `json:"superseded_at,omitempty"`
	SupersededReason    string            `json:"superseded_reason,omitempty"` // "rollback" | "file_diverged" | "manual"
}

func (i *Iteration) Validate() error {
	if i.ID <= 0 {
		return fmt.Errorf("iteration: id must be positive")
	}
	if i.Ts.IsZero() {
		return fmt.Errorf("iteration %d: ts is required", i.ID)
	}
	switch i.Kind {
	case KindIteration:
		// summary optional but recommended; no hard requirement
	case KindCommit:
		if i.SHA == "" {
			return fmt.Errorf("iteration %d: kind=commit requires sha", i.ID)
		}
	case "":
		return fmt.Errorf("iteration %d: kind is required", i.ID)
	default:
		return fmt.Errorf("iteration %d: unknown kind %q", i.ID, i.Kind)
	}
	hasAt := i.SupersededAt != nil
	hasReason := i.SupersededReason != ""
	if hasAt && !hasReason {
		return fmt.Errorf("iteration %d: superseded_at set but superseded_reason empty", i.ID)
	}
	if hasReason && !hasAt {
		return fmt.Errorf("iteration %d: superseded_reason set but superseded_at empty", i.ID)
	}
	switch i.SupersededReason {
	case "", "rollback", "file_diverged", "manual":
	default:
		return fmt.Errorf("iteration %d: invalid superseded_reason %q (want rollback|file_diverged|manual)", i.ID, i.SupersededReason)
	}
	return nil
}
