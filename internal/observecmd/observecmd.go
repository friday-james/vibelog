// Package observecmd implements `cockpit observe` — the Stop-hook handler.
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

	"cockpit/internal/mcpserver"
)

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
	SummaryText     string
	Files           []string
}

// Run reads a Stop-hook payload from stdin and records an iteration in
// projectDir's .sync/. If projectDir is empty, uses payload.Cwd. Skips when
// stop_hook_active is true (loop guard) or when the turn touched no files.
func Run(projectDir string) error {
	var payload HookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode stdin: %w", err)
	}
	if payload.StopHookActive {
		return nil // loop guard
	}
	if payload.TranscriptPath == "" {
		return fmt.Errorf("payload missing transcript_path")
	}
	if projectDir == "" {
		projectDir = payload.Cwd
	}
	if projectDir == "" {
		return nil // can't determine project; silently skip
	}

	// Only observe projects that have been `cockpit init`'d. Lets the hook be
	// global (one entry in ~/.claude/settings.json works across every project)
	// without spamming errors when Claude Code runs in a non-cockpit repo.
	if _, err := os.Stat(filepath.Join(projectDir, ".sync", "anchor.yaml")); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat anchor: %w", err)
	}

	res, err := AnalyzeTranscript(payload.TranscriptPath)
	if err != nil {
		return fmt.Errorf("analyze transcript: %w", err)
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

	if len(rel) == 0 {
		return nil // pure conversation turn — nothing to track
	}

	summary := res.SummaryText
	if summary == "" {
		summary = fmt.Sprintf("end-of-turn (touched %d file%s)", len(rel), plural(len(rel)))
	}

	_, err = mcpserver.RecordIteration(projectDir, mcpserver.RecordIterationArgs{
		Summary:             summary,
		FilesChanged:        rel,
		TranscriptMessageID: res.LastMessageUUID,
	})
	return err
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

	// Step 1: locate the most recent user message — everything after it is "this turn".
	startIdx := 0
	for i := len(lines) - 1; i >= 0; i-- {
		var entry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		if entry.Type == "user" {
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
	return res, nil
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

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
