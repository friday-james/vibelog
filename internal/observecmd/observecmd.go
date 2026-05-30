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
// Pure-conversation turns (no Edit/Write/MultiEdit/NotebookEdit calls) are
// skipped: nothing to track when the assistant only chatted.
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
	"strings"
	"time"

	"vibelog/internal/mcpserver"
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
	SummaryText     string   // assistant's final text (the "what I did" subtitle)
	Files           []string
	UserPrompt      string   // the user's prompt that triggered the turn
	Implementation  string   // every assistant text block joined — the L1 teach-back
}

// Run reads a Stop-hook payload from stdin and records an iteration in
// projectDir's .sync/. If projectDir is empty, uses payload.Cwd. Skips when
// stop_hook_active is true (loop guard) or when the turn touched no files.
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
		dbg("project %q lacks .sync/anchor.yaml → silent skip (not a cockpit project)", projectDir)
		return nil
	} else if err != nil {
		dbg("stat anchor failed: %v", err)
		return fmt.Errorf("stat anchor: %w", err)
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
	rel := make([]string, 0, len(res.Files))
	for _, p := range res.Files {
		if r, err := filepath.Rel(projectDir, p); err == nil && !strings.HasPrefix(r, "..") {
			rel = append(rel, r)
		} else {
			rel = append(rel, p)
		}
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

	iter, err := mcpserver.RecordIteration(projectDir, mcpserver.RecordIterationArgs{
		Summary:             summary,
		FilesChanged:        rel,
		TranscriptMessageID: res.LastMessageUUID,
		UserPrompt:          res.UserPrompt,
		Implementation:      res.Implementation,
	})
	if err != nil {
		dbg("RecordIteration failed: %v", err)
		return err
	}
	dbg("recorded iter #%d", iter.ID)
	return nil
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

	// Step 1: locate the most recent REAL user prompt — not a tool_result.
	// Claude Code transcripts wrap tool_results as type:"user" messages, so a
	// naive "first type=user walking backward" picks the latest tool_result
	// and the resulting "current turn" window is just the trailing chatter
	// after the last tool, missing all the Edit/Write calls.
	startIdx := 0
	var userPromptText string
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
		if text, ok := extractUserPromptText(entry.Message); ok {
			userPromptText = text
			startIdx = i + 1
			break
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
			}
		}
		if latestText != "" {
			res.SummaryText = latestText
		}
	}

	if len(res.SummaryText) > 200 {
		res.SummaryText = strings.TrimRight(res.SummaryText[:200], " ") + "…"
	}
	res.UserPrompt = sanitizeUserPrompt(userPromptText)
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
//   Base directory for this skill: /Users/jai/.claude/skills/guide-me
//
//   # Guide Me
//   ...full skill template body, often thousands of lines...
//
//   ARGUMENTS: <the args the user actually typed>
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
func extractUserPromptText(message json.RawMessage) (string, bool) {
	var msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &msg); err != nil {
		return "", false
	}
	// content may be a bare string OR an array of blocks; both forms appear.
	if len(msg.Content) > 0 && msg.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(msg.Content, &s); err == nil {
			return s, true
		}
		return "", false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}
