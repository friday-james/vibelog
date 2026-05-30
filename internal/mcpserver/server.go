package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Serve runs the MCP server on stdio. projectDir is the directory whose .sync/
// the tools mutate; pass the workspace root from `vibelog mcp <dir>`. Returns
// when stdin closes or an unrecoverable error occurs.
func Serve(projectDir string) error {
	s := server.NewMCPServer("vibelog", "0.1.0-dev")

	s.AddTool(
		mcp.NewTool("record_iteration",
			mcp.WithDescription("Append an iteration to .sync/iterations.jsonl. Call after finishing a meaningful unit of work (typically at end-of-turn) — the iteration records what you just did so the human can stay coupled with the agent's progress."),
			mcp.WithString("summary",
				mcp.Required(),
				mcp.Description("One-line past-tense summary of what just happened. Example: 'wired the record_iteration MCP tool with atomic JSONL append'."),
			),
			mcp.WithArray("files_changed",
				mcp.Description("Paths (relative to project root) of files this iteration modified."),
			),
			mcp.WithArray("claims_added",
				mcp.Description("IDs of claims newly authored or asserted by this iteration."),
			),
			mcp.WithArray("claims_violated",
				mcp.Description("IDs of claims now violated due to this iteration's changes."),
			),
			mcp.WithString("transcript_message_id",
				mcp.Description("UUID of the assistant turn that produced this iteration (for future rollback reconciliation)."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			summary, err := req.RequireString("summary")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			args := RecordIterationArgs{
				Summary:             summary,
				FilesChanged:        req.GetStringSlice("files_changed", nil),
				ClaimsAdded:         req.GetStringSlice("claims_added", nil),
				ClaimsViolated:      req.GetStringSlice("claims_violated", nil),
				TranscriptMessageID: req.GetString("transcript_message_id", ""),
			}
			iter, err := RecordIteration(projectDir, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(iter)
			return mcp.NewToolResultText(fmt.Sprintf("recorded iteration #%d in %s\n%s", iter.ID, projectDir, string(b))), nil
		},
	)

	s.AddTool(
		mcp.NewTool("assert_claim",
			mcp.WithDescription("Create or update a claim in .sync/claims.yaml. Use this to author invariants, contracts, failure-modes, or assumptions about the system. If a claim with the same id exists, it's overwritten in place (Established date preserved, LastVerified bumped to now)."),
			mcp.WithString("id",
				mcp.Required(),
				mcp.Description("Kebab-case slug, stable across edits. Example: 'evidence-required-per-claim'."),
			),
			mcp.WithString("statement",
				mcp.Required(),
				mcp.Description("One-sentence proposition the claim makes about the system."),
			),
			mcp.WithString("category",
				mcp.Required(),
				mcp.Description("One of: invariant | contract | failure-mode | assumption"),
			),
			mcp.WithString("status",
				mcp.Required(),
				mcp.Description("One of: unknown | suspected | holding | violated"),
			),
			mcp.WithString("severity",
				mcp.Required(),
				mcp.Description("One of: low | med | high"),
			),
			mcp.WithString("evidence_json",
				mcp.Required(),
				mcp.Description(`JSON array of evidence entries. Each entry: {"type": "code"|"test"|"doc"|"decision"|"benchmark"|"metric"|"commit"|"missing", "path": "...", "polarity": "positive"|"negative", "note": "...", "ref": "...", "sha": "...", "kind": "test"|"comms"|"decision"|"verification"}. Per-type required: code/test need path+polarity; missing needs kind; metric needs ref; commit needs sha; doc/decision/benchmark need path. Use a missing entry if you can't ground the claim — never an empty array.`),
			),
			mcp.WithString("established_by",
				mcp.Description("Who authored this claim (handle, 'agent', 'design', etc.)."),
			),
			mcp.WithString("related_claims",
				mcp.Description("Comma-separated ids of related claims, if any."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := AssertClaimArgs{
				ID:            firstNonErr(req.RequireString("id")),
				Statement:     firstNonErr(req.RequireString("statement")),
				Category:      firstNonErr(req.RequireString("category")),
				Status:        firstNonErr(req.RequireString("status")),
				Severity:      firstNonErr(req.RequireString("severity")),
				EvidenceJSON:  firstNonErr(req.RequireString("evidence_json")),
				EstablishedBy: req.GetString("established_by", ""),
				RelatedClaims: req.GetString("related_claims", ""),
			}
			claim, err := AssertClaim(projectDir, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(claim)
			return mcp.NewToolResultText(fmt.Sprintf("asserted claim %q (status=%s) in %s\n%s", claim.ID, claim.Status, projectDir, string(b))), nil
		},
	)

	s.AddTool(
		mcp.NewTool("update_anchor",
			mcp.WithDescription("Replace one or more sections of .sync/anchor.yaml. Pass JSON for any section you want to update; omit to leave unchanged. Use this to set the project's intent/approach/now — not direct file edits."),
			mcp.WithString("intent_json",
				mcp.Description(`JSON of the Intent struct: {"statement": "...", "evidence": [...], "established": "YYYY-MM-DD", "established_by": "..."}. Evidence is the same shape as in assert_claim.`),
			),
			mcp.WithString("approach_json",
				mcp.Description(`JSON of the Approach struct: {"statement": "...", "evidence": [...], "last_changed": "RFC3339", "change_reason": "..."}.`),
			),
			mcp.WithString("now_json",
				mcp.Description(`JSON of the Now struct: {"statement": "...", "iteration_id": <int>, "started": "RFC3339"}. Use this to update focus mid-session.`),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := UpdateAnchorArgs{
				IntentJSON:   req.GetString("intent_json", ""),
				ApproachJSON: req.GetString("approach_json", ""),
				NowJSON:      req.GetString("now_json", ""),
			}
			anchor, err := UpdateAnchor(projectDir, args)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(anchor)
			return mcp.NewToolResultText(fmt.Sprintf("updated anchor in %s\n%s", projectDir, string(b))), nil
		},
	)

	s.AddTool(
		mcp.NewTool("set_implementation",
			mcp.WithDescription(
				"REQUIRED for every assistant turn that edits files in this project. "+
					"Submit a structured teach-back describing what you did this turn — "+
					"shown on the vibelog dashboard's prompt card. Call this BEFORE ending "+
					"your turn, after all your edits are done. Plain markdown (paragraphs, "+
					"*emphasis*, `code`, lists) renders cleanly. Multiple calls in one turn "+
					"→ last call wins. If you forget, the L0 subtitle stays empty and the L1 "+
					"falls back to your last text block (noisy).\n\n"+
					"Two fields:\n"+
					"  - summary: 1-2 line condensed teach-back. Shown as the L0 card "+
					"subtitle, directly under the user prompt. ~140 chars is the sweet spot.\n"+
					"  - text: the full teach-back, ~50-300 words at one level of abstraction "+
					"above the diff. What was built, the load-bearing reason for the shape, "+
					"any decision the user should know about. NOT a code dump.",
			),
			mcp.WithString("summary",
				mcp.Required(),
				mcp.Description("1-2 line condensed teach-back. Becomes the L0 subtitle on the prompt card. Keep it crisp — what you did, in plain prose. Avoid markdown lists; one or two sentences."),
			),
			mcp.WithString("text",
				mcp.Required(),
				mcp.Description("The full teach-back. Plain markdown. ~50-300 words. This is what the user reads when they click into the IMPLEMENTATION block (L1)."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			summary, err := req.RequireString("summary")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := SetImplementation(projectDir, SetImplementationArgs{Summary: summary, Text: text}); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("teach-back queued (summary=%d chars, text=%d chars) — observe will consume at end of turn", len(summary), len(text))), nil
		},
	)

	return server.ServeStdio(s)
}

// firstNonErr returns the value, ignoring the error — fine because the MCP
// framework already enforces required-arg validation before our handler runs.
func firstNonErr[T any](v T, _ error) T { return v }
