// Package observecmd implements `vibelog observe` — the Stop-hook handler.
//
// Claude Code's Stop hook fires when an assistant turn ends. It writes a JSON
// payload to stdin containing the transcript path; observecmd reads that,
// walks back through the transcript to the previous user message, extracts
// (a) the UUID of the latest assistant message, (b) file paths touched by
// Edit/Write/NotebookEdit/MultiEdit tool_use blocks in this turn, and (c)
// the first line of the assistant's final text response — then calls
// mcpserver.RecordIteration with those fields.
//
// Idempotency: RecordIteration is keyed on transcript_message_id, so repeated
// fires of the same hook (Claude Code retries, double-invocations) only land
// one entry.
//
// Recording is gated by vibelog serve's active marker so a global Stop hook is
// harmless in repos where vibelog is not currently enabled.
package observecmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/friday-james/vibelog/internal/mcpserver"
	"github.com/friday-james/vibelog/internal/serve"
)

// dbg appends a diagnostic line to /tmp/vibelog-observe.log so we can see
// when Stop hook actually fires (or doesn't) and what payload it receives.
// Failure to open the log file is silently ignored — the hook must never
// disrupt the assistant's turn even if /tmp is unwritable.
func dbg(format string, args ...any) {
	f, err := os.OpenFile("/tmp/vibelog-observe.log", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// HookPayload is the JSON shape Claude Code's Stop hook writes to stdin.
type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
	HookEventName  string `json:"hook_event_name"`
	Cwd            string `json:"cwd,omitempty"`
}

// Result is the analysis of the last turn (between the previous user message
// and the end of the transcript).
type Result struct {
	LastMessageUUID string
	SummaryText     string // assistant's final text (the "what I did" subtitle)
	Files           []string
	UserPrompt      string // the user's prompt that triggered the turn
	Implementation  string // every assistant text block joined — the L1 teach-back

	// SyntheticTurn is true when the back-walker landed on a harness-injected
	// auto-fired turn (e.g. <task-notification> from a background workflow).
	// There is no human behind such a turn, so Run() skips recording it.
	SyntheticTurn bool

	// WorkflowTaskID is set when THIS turn invoked the Workflow tool. The ID
	// is extracted from the Workflow tool_result text ("Task ID: <id>").
	// The recorded iter carries this as a marker so a later turn can attribute
	// its diff back via WorkflowMergeOf.
	WorkflowTaskID string

	// SyntheticWorkflowTaskIDs lists every <task-id> seen inside synthetic
	// <task-notification> blocks that fell BETWEEN the prior real user turn
	// and this turn's anchor. When non-empty, Run() looks for a pending
	// originating iter (one with WorkflowTaskID set and no later merge yet)
	// to merge this turn's files into.
	SyntheticWorkflowTaskIDs []string
}

// Run reads a Stop-hook payload from stdin and records an iteration in
// projectDir's .sync/. If projectDir is empty, uses payload.Cwd. Skips when
// stop_hook_active is true (loop guard), the repo is not initialized, or the
// repo has no active vibelog serve marker.
func Run(projectDir string) error {
	dbg("Run() invoked, projectDir-arg=%q, PATH=%q", projectDir, os.Getenv("PATH"))
	var payload HookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			dbg("stdin EOF — no payload; exiting nil")
			return nil
		}
		dbg("decode stdin failed: %v", err)
		return fmt.Errorf("decode stdin: %w", err)
	}
	dbg("payload received: session=%q transcript=%q cwd=%q stop_hook_active=%v event=%q",
		payload.SessionID, payload.TranscriptPath, payload.Cwd, payload.StopHookActive, payload.HookEventName)
	if payload.StopHookActive {
		dbg("stop_hook_active=true → loop guard, exiting")
		return nil
	}
	if payload.TranscriptPath == "" {
		dbg("payload missing transcript_path → ERROR")
		return fmt.Errorf("payload missing transcript_path")
	}
	if projectDir == "" {
		projectDir = payload.Cwd
		dbg("projectDir empty → using payload.cwd=%q", projectDir)
	}
	if projectDir == "" {
		dbg("no projectDir (neither arg nor payload.cwd) → silent skip")
		return nil
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".sync", "anchor.yaml")); errors.Is(err, fs.ErrNotExist) {
		dbg("project %q lacks .sync/anchor.yaml → silent skip (not a vibelog project)", projectDir)
		return nil
	} else if err != nil {
		dbg("stat anchor failed: %v", err)
		return fmt.Errorf("stat anchor: %w", err)
	}
	if !serve.IsActive(projectDir) {
		dbg("project %q has no active serve marker → silent skip", projectDir)
		return nil
	}

	// Retry the transcript read with backoff if Implementation comes back
	// empty — Claude Code's transcript flush can lag the Stop hook by 100ms-1s
	// for longer assistant responses, and we'd otherwise miss the text. We
	// only retry if Implementation AND Files are both empty, since a real
	// pure-conversation turn that the agent legitimately ended quickly
	// (no response requested) shouldn't be punished with extra latency.
	// Worst case: 200 + 400 + 800 = 1.4s extra; common case (immediate hit): 0ms.
	res, err := AnalyzeTranscript(payload.TranscriptPath)
	if err != nil {
		dbg("AnalyzeTranscript failed: %v", err)
		return fmt.Errorf("analyze transcript: %w", err)
	}
	// Synthetic turns (e.g. <task-notification> auto-fired by the harness when
	// a background Workflow completes) have no human behind them. Skip
	// recording entirely — the assistant's response to the notification is
	// just bookkeeping noise, not a real iteration of the user's work.
	if res.SyntheticTurn {
		dbg("synthetic turn (harness-injected user message) — skipping record")
		return nil
	}
	for attempt := 0; res.Implementation == "" && len(res.Files) == 0 && attempt < 3; attempt++ {
		delay := time.Duration(200*(attempt+1)) * time.Millisecond
		dbg("transcript Implementation empty (attempt %d/3) — backing off %v then re-reading", attempt+1, delay)
		time.Sleep(delay)
		res, err = AnalyzeTranscript(payload.TranscriptPath)
		if err != nil {
			dbg("AnalyzeTranscript retry failed: %v", err)
			return fmt.Errorf("analyze transcript (retry): %w", err)
		}
	}
	dbg("analyzed: msgID=%q, summary=%q (len=%d), %d files, impl=%d", res.LastMessageUUID, res.SummaryText, len(res.SummaryText), len(res.Files), len(res.Implementation))

	// Layer 1 (preferred): the agent called mcp__vibelog__set_implementation
	// during the turn. The MCP tool wrote a JSON envelope to .sync/pending_implementation.txt.
	// Validate session_id + age, then consume + delete. On validation failure
	// we just fall through and keep the heuristic Implementation (last text block).
	pendingSummary, pendingText := consumePendingImplementation(projectDir, payload.SessionID)
	if pendingText != "" {
		dbg("consumed pending teach-back (summary=%d, text=%d) from %s", len(pendingSummary), len(pendingText), filepath.Join(projectDir, ".sync", "pending_implementation.txt"))
		res.Implementation = pendingText
		if pendingSummary != "" {
			res.SummaryText = pendingSummary
		}
	} else {
		dbg("no valid pending teach-back; using last-text-block fallback (len=%d)", len(res.Implementation))
	}

	// Relativize file paths to the project root for portability.
	//
	// Primary path: filepath.Rel against projectDir. If the result doesn't
	// escape the project (no leading ".."), use it.
	//
	// Recovery path: a path that escapes (e.g. the agent's transcript carries
	// "/Users/.../old-project-name/foo.go" because the project was renamed
	// mid-session) is REMAPPED by walking its suffixes from the leaf up and
	// finding the longest suffix that exists under projectDir. This catches
	// the rename case without us tracking rename history: the project tree
	// itself is the source of truth.
	rel := make([]string, 0, len(res.Files))
	for _, p := range res.Files {
		if r, err := filepath.Rel(projectDir, p); err == nil && !strings.HasPrefix(r, "..") {
			rel = append(rel, r)
			continue
		}
		if remapped, ok := suffixUnderProject(projectDir, p); ok {
			dbg("relativize: remapped %q → %q (path didn't fit projectDir directly)", p, remapped)
			rel = append(rel, remapped)
			continue
		}
		dbg("relativize: keeping absolute %q (no matching suffix under %q)", p, projectDir)
		rel = append(rel, p)
	}

	dbg("relativized files: %v (count=%d)", rel, len(rel))

	// Record EVERY assistant turn — the user wants every prompt visible in
	// the dashboard, even pure-conversation responses with no file edits.
	// Subtitle source of truth, in order: pending envelope summary → last-text
	// heuristic → synthesized placeholder. The pending value (when present)
	// already overwrote res.SummaryText above.
	summary := res.SummaryText
	if summary == "" {
		if len(rel) > 0 {
			summary = "(no teach-back submitted)"
		} else {
			summary = "no action"
		}
	}

	// Workflow attribution: if this turn's transcript window contains bridge
	// task IDs from synthetic <task-notification> blocks, see if any of them
	// matches a pending originating iter (one that called Workflow but has
	// no later merge yet). If found, this iter records as a merge — the
	// dashboard renders the diff under the originating prompt's card.
	var mergeOf int
	if len(res.SyntheticWorkflowTaskIDs) > 0 {
		mergeOf = findPendingMergeTarget(projectDir, res.SyntheticWorkflowTaskIDs)
		if mergeOf > 0 {
			dbg("workflow merge: bridge task IDs %v → merging into iter #%d", res.SyntheticWorkflowTaskIDs, mergeOf)
		}
	}

	iter, err := mcpserver.RecordIteration(projectDir, mcpserver.RecordIterationArgs{
		Summary:             summary,
		FilesChanged:        rel,
		TranscriptMessageID: res.LastMessageUUID,
		UserPrompt:          res.UserPrompt,
		Implementation:      res.Implementation,
		WorkflowTaskID:      res.WorkflowTaskID,
		WorkflowMergeOf:     mergeOf,
	})
	if err != nil {
		dbg("RecordIteration failed: %v", err)
		return err
	}
	dbg("recorded iter #%d (workflow_task_id=%q merge_of=%d)", iter.ID, res.WorkflowTaskID, mergeOf)
	return nil
}

// findPendingMergeTarget scans .sync/iterations.jsonl for the most recent
// iteration whose WorkflowTaskID is in taskIDs AND has no later iter pointing
// back to it via WorkflowMergeOf. Returns 0 if no eligible target exists.
// Uses a raw read so it stays decoupled from the store package's strict
// Validate path.
func findPendingMergeTarget(projectDir string, taskIDs []string) int {
	if len(taskIDs) == 0 {
		return 0
	}
	wanted := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		wanted[id] = true
	}
	path := filepath.Join(projectDir, ".sync", "iterations.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	type row struct {
		ID              int    `json:"id"`
		WorkflowTaskID  string `json:"workflow_task_id,omitempty"`
		WorkflowMergeOf int    `json:"workflow_merge_of,omitempty"`
	}
	var rows []row
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		rows = append(rows, r)
	}
	mergedTargets := make(map[int]bool)
	for _, r := range rows {
		if r.WorkflowMergeOf > 0 {
			mergedTargets[r.WorkflowMergeOf] = true
		}
	}
	// Walk newest → oldest, pick the latest pending iter whose task ID matches.
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.WorkflowTaskID == "" || !wanted[r.WorkflowTaskID] {
			continue
		}
		if mergedTargets[r.ID] {
			continue
		}
		return r.ID
	}
	return 0
}

// AnalyzeTranscript walks the JSONL transcript file backward from the end to
// the most recent user message, returning the latest assistant UUID, the
// first-line text of the latest assistant message, and the files touched by
// Edit/Write/MultiEdit/NotebookEdit tool_use blocks in the turn (chronological).
func AnalyzeTranscript(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	var lines [][]byte
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<24) // up to 16 MiB per line
	for scanner.Scan() {
		buf := make([]byte, len(scanner.Bytes()))
		copy(buf, scanner.Bytes())
		lines = append(lines, buf)
	}
	if err := scanner.Err(); err != nil {
		return Result{}, fmt.Errorf("scan: %w", err)
	}

	// Step 1: locate the LATEST user-type entry. If it's a real prompt we
	// anchor there; if it's a synthetic <task-notification> the whole turn
	// is harness-fired and Run() will skip recording.
	startIdx := 0
	var userPromptText string
	var syntheticTurn bool
	anchorIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		if entry.Type != "user" {
			continue
		}
		text, ok, synth := extractUserPromptText(entry.Message)
		if !ok {
			continue
		}
		userPromptText = text
		startIdx = i + 1
		syntheticTurn = synth
		anchorIdx = i
		break
	}

	// Step 1b: if the anchor is a REAL user prompt, scan backward from it to
	// the PRIOR real prompt and collect any synthetic <task-notification>
	// task IDs along the way. These are bridge IDs that Run() can use to
	// attribute this turn's diff back to an originating workflow-invocation
	// iter (so the dashboard shows the diff on the prompt card that asked
	// for the workflow, not on the application-turn card).
	var bridgeTaskIDs []string
	if !syntheticTurn && anchorIdx > 0 {
		for j := anchorIdx - 1; j >= 0; j-- {
			var e struct {
				Type    string          `json:"type"`
				Message json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(lines[j], &e); err != nil {
				continue
			}
			if e.Type != "user" {
				continue
			}
			text, ok, synth := extractUserPromptText(e.Message)
			if !ok {
				continue
			}
			if !synth {
				break // hit prior real prompt, stop scanning
			}
			bridgeTaskIDs = append(bridgeTaskIDs, extractTaskIDsFromNotification(text)...)
		}
	}

	// Step 2: walk forward through the turn, collecting files in order and
	// keeping the LATEST assistant uuid + LATEST text block as the summary.
	type contentBlock struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	}
	type msgPayload struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	var res Result
	var lastFullText string
	allText := []string{}
	seen := make(map[string]bool)
	for i := startIdx; i < len(lines); i++ {
		var entry struct {
			UUID    string          `json:"uuid"`
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" {
			continue
		}
		res.LastMessageUUID = entry.UUID

		var msg msgPayload
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		var blocks []contentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		var latestText string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					latestText = firstLine(b.Text)
					lastFullText = b.Text
					allText = append(allText, b.Text)
				}
			case "tool_use":
				if isFileMutator(b.Name) {
					if p := extractPath(b.Input); p != "" && !seen[p] {
						seen[p] = true
						res.Files = append(res.Files, p)
					}
				}
				if b.Name == "Workflow" {
					// Mark the iter as workflow-originating. The task ID lives
					// in the matching tool_result (next user-wrapped entry
					// with type=tool_result and tool_use_id = b's id). We'll
					// pick it up below in the per-tool_result scan.
					res.WorkflowTaskID = "" // placeholder; resolved next pass
				}
			}
		}
		if latestText != "" {
			res.SummaryText = latestText
		}
	}
	// Second pass: scan this turn's tool_results for a Workflow result.
	// The Workflow tool's text payload begins with "Workflow launched in
	// background. Task ID: <id>". Extract the first match within the turn.
	for i := startIdx; i < len(lines) && res.WorkflowTaskID == ""; i++ {
		var entry struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		if entry.Type != "user" {
			continue
		}
		var msg struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		var blocks []struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_result" || b.Content == "" {
				continue
			}
			if id := extractWorkflowTaskID(b.Content); id != "" {
				res.WorkflowTaskID = id
				break
			}
		}
	}
	res.SyntheticWorkflowTaskIDs = bridgeTaskIDs

	if len(res.SummaryText) > 200 {
		res.SummaryText = strings.TrimRight(res.SummaryText[:200], " ") + "…"
	}
	res.UserPrompt = sanitizeUserPrompt(userPromptText)
	res.SyntheticTurn = syntheticTurn
	// Implementation defaults to the LAST text block (heuristic fallback).
	// Run() may override this with the contents of .sync/pending_implementation.txt
	// if the agent called set_implementation during the turn.
	res.Implementation = strings.TrimSpace(lastFullText)
	_ = allText // reserved for future use; tests check joined behavior
	return res, nil
}

// sanitizeUserPrompt keeps the full prompt text intact for ordinary messages,
// but condenses skill invocations to "/skill-name <args>" so a multi-screen
// skill template doesn't fill the head.
//
// Claude Code writes a skill prompt to the transcript like this (NO tags):
//
//	Base directory for this skill: /Users/jai/.claude/skills/guide-me
//
//	# Guide Me
//	...full skill template body, often thousands of lines...
//
//	ARGUMENTS: <the args the user actually typed>
//
// Earlier versions tried to match <command-name> tags — but those tags only
// appear in the live conversation, not in the persisted transcript. The
// correct detection is the literal "Base directory for this skill:" prefix
// + the trailing "ARGUMENTS:" section.
func sanitizeUserPrompt(s string) string {
	s = strings.TrimSpace(s)

	// Skill invocation — recognize by the "Base directory for this skill:" prefix.
	if strings.HasPrefix(s, "Base directory for this skill: ") {
		first, rest, _ := strings.Cut(s, "\n")
		path := strings.TrimSpace(strings.TrimPrefix(first, "Base directory for this skill: "))
		skillName := path[strings.LastIndex(path, "/")+1:]
		args := ""
		if i := strings.LastIndex(rest, "\nARGUMENTS:"); i >= 0 {
			args = strings.TrimSpace(rest[i+len("\nARGUMENTS:"):])
		} else if strings.HasPrefix(rest, "ARGUMENTS:") {
			args = strings.TrimSpace(strings.TrimPrefix(rest, "ARGUMENTS:"))
		}
		if len(args) > 240 {
			args = strings.TrimRight(args[:240], " ") + "…"
		}
		out := "/" + skillName
		if args != "" {
			out += " " + args
		}
		return out
	}

	// Legacy tag-based form (live conversation copy, not transcript).
	head := s
	if len(head) > 500 {
		head = head[:500]
	}
	if i := strings.Index(head, "<command-name>"); i >= 0 {
		if end := strings.Index(s[i:], "</command-name>"); end > 0 {
			name := strings.TrimSpace(s[i+len("<command-name>") : i+end])
			if !strings.HasPrefix(name, "/") {
				name = "/" + name
			}
			args := ""
			if a := strings.Index(s, "<command-args>"); a >= 0 {
				if b := strings.Index(s[a:], "</command-args>"); b > 0 {
					raw := strings.TrimSpace(s[a+len("<command-args>") : a+b])
					if line, _, _ := strings.Cut(raw, "\n"); line != "" {
						if len(line) > 240 {
							line = strings.TrimRight(line[:240], " ") + "…"
						}
						args = " " + line
					}
				}
			}
			return name + args
		}
	}

	// Bare slash command at start
	if strings.HasPrefix(s, "/") {
		if line, _, _ := strings.Cut(s, "\n"); line != "" {
			return line
		}
	}
	return s
}

func isFileMutator(name string) bool {
	switch name {
	case "Edit", "Write", "NotebookEdit", "MultiEdit":
		return true
	}
	return false
}

func extractPath(input json.RawMessage) string {
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return ""
	}
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.Path
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimRight(s[:idx], " \t")
	}
	return s
}

// consumePendingImplementation reads .sync/pending_implementation.txt if it
// exists. Validates the envelope (session_id matches AND timestamp is within
// 60s) before returning (summary, text), then deletes the file regardless.
// Returns ("", "") on any validation failure — caller falls back to the
// heuristic. The unconditional delete is intentional: a stale or
// foreign-session envelope is garbage and shouldn't survive to pollute a
// later turn.
func consumePendingImplementation(projectDir, currentSessionID string) (string, string) {
	path := filepath.Join(projectDir, ".sync", "pending_implementation.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	defer os.Remove(path)
	var env struct {
		Summary   string `json:"summary"`
		Text      string `json:"text"`
		SessionID string `json:"session_id"`
		Ts        string `json:"ts"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		dbg("pending envelope unparseable: %v", err)
		return "", ""
	}
	if strings.TrimSpace(env.Text) == "" {
		return "", ""
	}
	if currentSessionID != "" && env.SessionID != "" && env.SessionID != currentSessionID {
		dbg("pending envelope session_id mismatch (%q != %q) — rejecting", env.SessionID, currentSessionID)
		return "", ""
	}
	if t, err := time.Parse(time.RFC3339, env.Ts); err == nil {
		if time.Since(t) > 60*time.Second {
			dbg("pending envelope is %.0fs old (>60s) — rejecting", time.Since(t).Seconds())
			return "", ""
		}
	}
	return strings.TrimSpace(env.Summary), strings.TrimSpace(env.Text)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// extractUserPromptText returns the user's prompt text (and ok=true) when a
// transcript line of type:"user" carries an actual user-typed message, NOT a
// tool_result wrapped as a user-role message. Claude Code's transcript makes
// both look like type:"user" at the top level; only content distinguishes them.
// extractUserPromptText returns (text, ok, synthetic):
//   - ok=false: this entry is not a real user message (e.g. tool_result wrapped
//     as type:"user", or unparseable). Caller keeps walking back.
//   - synthetic=true: this is the anchor of a HARNESS-INJECTED auto-fired turn
//     (e.g. <task-notification> emitted when a background Workflow/Agent
//     completes). text is the raw meta block. Caller should skip recording
//     this turn entirely — there is no human behind it.
//   - synthetic=false, ok=true: real user prompt.
func extractUserPromptText(message json.RawMessage) (text string, ok bool, synthetic bool) {
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return "", false, false
	}
	// content may be a bare string OR an array of blocks; both forms appear.
	if len(msg.Content) > 0 && msg.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(msg.Content, &s); err != nil {
			return "", false, false
		}
		if isHarnessInjectedUserText(s) {
			return s, true, true
		}
		return s, true, false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return "", false, false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return "", false, false
	}
	joined := strings.Join(parts, "\n")
	if isHarnessInjectedUserText(joined) {
		return joined, true, true
	}
	return joined, true, false
}

// isHarnessInjectedUserText reports whether the message content is purely a
// harness meta-block (task-notification, agent-completion, etc.) rather than
// a user-typed prompt. We only flag messages that BEGIN with a known wrapper
// tag — a user who pastes a notification will normally frame it with prose,
// which we want to preserve.
func isHarnessInjectedUserText(s string) bool {
	trimmed := strings.TrimSpace(s)
	for _, prefix := range []string{"<task-notification>", "<task-notification ", "<workflow-notification>"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// taskIDInTag matches the inner content of a <task-id>…</task-id> tag — the
// harness's task-notification format ALWAYS wraps the id in this element.
// Restrictive character class on purpose: workflow IDs are lowercase
// alphanum-and-hyphen, never longer than ~40 chars.
var taskIDInTagRE = regexp.MustCompile(`<task-id>([a-z0-9-]{1,40})</task-id>`)

// taskIDAfterPrefix matches the "Task ID: <id>" prefix the Workflow tool
// emits in its tool_result text. Used to attribute the originating turn.
var taskIDAfterPrefixRE = regexp.MustCompile(`Task ID:\s+([a-z0-9-]{1,40})`)

// extractTaskIDsFromNotification pulls every <task-id> value out of a
// <task-notification> block. Order is preserved.
func extractTaskIDsFromNotification(s string) []string {
	matches := taskIDInTagRE.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// extractWorkflowTaskID pulls the first "Task ID: <id>" value out of a
// tool_result text payload from the Workflow tool.
func extractWorkflowTaskID(s string) string {
	m := taskIDAfterPrefixRE.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// suffixUnderProject walks the path's components from the leaf back toward the
// root and returns the LONGEST suffix that exists as a file under projectDir.
// Used when filepath.Rel says the input escapes projectDir (e.g. the agent's
// transcript carries an absolute path from before the project was renamed).
// Example: projectDir="/foo/new-name" and p="/foo/old-name/internal/x.go" →
// returns ("internal/x.go", true) because /foo/vibelog/internal/x.go exists.
func suffixUnderProject(projectDir, p string) (string, bool) {
	// Normalize to forward slashes, then split.
	parts := strings.Split(filepath.ToSlash(p), "/")
	// Try longest suffix first so we don't grab a shorter accidental match.
	for start := 0; start < len(parts); start++ {
		suffix := strings.Join(parts[start:], "/")
		if suffix == "" || suffix == "/" {
			continue
		}
		// Trim a possible leading slash (when start==0 on absolute paths).
		clean := strings.TrimPrefix(suffix, "/")
		if clean == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, clean)); err == nil {
			return clean, true
		}
	}
	return "", false
}
