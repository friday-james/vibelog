package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
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
			mcp.WithDescription(
				"Primary vibelog end-of-turn logging tool. Call this BEFORE ending a turn whenever you changed files in this project, "+
					"and also for meaningful pure-conversation turns you want on the dashboard. Direct MCP clients such as Codex should use "+
					"this tool to append the row itself. Transcript-hook clients such as Claude Code may still call set_implementation "+
					"during the turn, but the actual row append still happens here (directly or via the hook bridge).",
			),
			mcp.WithString("summary",
				mcp.Required(),
				mcp.Description("One-line past-tense summary of what just happened. Example: 'wired the record_iteration MCP tool with atomic JSONL append'."),
			),
			mcp.WithArray("files_changed",
				mcp.Description("Paths (relative to project root) of files this iteration modified."),
			),
			mcp.WithString("transcript_message_id",
				mcp.Description("UUID of the assistant turn that produced this iteration (for future rollback reconciliation)."),
			),
			mcp.WithString("user_prompt",
				mcp.Description("The user message that triggered this turn. Supplying it gives the dashboard the same prompt-card head text that Claude's stop-hook path captures automatically."),
			),
			mcp.WithString("implementation",
				mcp.Description("The full teach-back or response text for this turn. Direct MCP clients such as Codex should pass it here so the dashboard can render the IMPLEMENTATION/RESPONSE layer without relying on transcript hooks."),
			),
			mcp.WithString("agent",
				mcp.Description("Optional agent label override, e.g. 'codex' or 'claude-code'. Omit unless your client already knows it; vibelog auto-detects when possible."),
			),
			mcp.WithString("session_id",
				mcp.Description("Optional stable session identifier. Omit unless your client already has one; vibelog auto-detects or falls back to a per-process session id."),
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
				TranscriptMessageID: req.GetString("transcript_message_id", ""),
				UserPrompt:          req.GetString("user_prompt", ""),
				Implementation:      req.GetString("implementation", ""),
				Agent:               req.GetString("agent", ""),
				SessionID:           req.GetString("session_id", ""),
			}
			iter, err := RecordIteration(projectDir, args)
			if err != nil {
				if errors.Is(err, ErrRecordingInactive) {
					return mcp.NewToolResultText("vibelog is inactive for this project; start `vibelog serve` here to enable logging"), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.Marshal(iter)
			return mcp.NewToolResultText(fmt.Sprintf("recorded iteration #%d in %s\n%s", iter.ID, projectDir, string(b))), nil
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
				"Structured teach-back helper. Claude Code uses this to bridge its Stop-hook flow into vibelog. "+
					"Direct MCP clients such as Codex can skip this and pass both summary and implementation directly to "+
					"record_iteration, or call this first and then record_iteration in the same turn. Plain markdown "+
					"(paragraphs, *emphasis*, `code`, lists) renders cleanly. Multiple calls in one turn → last call wins.\n\n"+
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
				if errors.Is(err, ErrRecordingInactive) {
					return mcp.NewToolResultText("vibelog is inactive for this project; start `vibelog serve` here to enable logging"), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("teach-back queued (summary=%d chars, text=%d chars) — direct MCP clients should still call record_iteration before ending the turn", len(summary), len(text))), nil
		},
	)

	return server.ServeStdio(s)
}
