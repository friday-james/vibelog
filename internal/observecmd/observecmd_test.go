package observecmd_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/observecmd"
	"github.com/friday-james/vibelog/internal/serve"
)

// withStdin temporarily replaces os.Stdin with the given bytes for the
// duration of fn(), then restores.
func withStdin(t *testing.T, payload string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.Copy(w, bytes.NewReader([]byte(payload)))
		w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	fn()
}

// writeTranscript creates a minimal Claude-Code-style JSONL transcript.
func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnalyzeTranscript_ExtractsFilesAndSummary(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"On it. Editing two files."},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/bar.go"}}` +
			`]}}`,
		`{"uuid":"a2","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"text","text":"Final wrap-up message after the edits."}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.LastMessageUUID != "a2" {
		t.Errorf("expected latest UUID a2, got %q", res.LastMessageUUID)
	}
	if !strings.Contains(res.SummaryText, "Final wrap-up") {
		t.Errorf("summary should come from latest assistant text, got %q", res.SummaryText)
	}
	if len(res.Files) != 2 || res.Files[0] != "/abs/foo.go" || res.Files[1] != "/abs/bar.go" {
		t.Errorf("expected [foo.go, bar.go] in chronological order, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_StopsAtUserBoundary(t *testing.T) {
	path := writeTranscript(t, []string{
		// PREVIOUS turn — must NOT be included
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"old"}]}}`,
		`{"uuid":"old-asst","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/should_not_appear.go"}}` +
			`]}}`,
		// CURRENT turn
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"new request"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/current.go"}},` +
			`{"type":"text","text":"done"}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0] != "/abs/current.go" {
		t.Errorf("expected only current-turn file, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_PureConversation(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected zero files on pure conversation, got %v", res.Files)
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected UUID a1, got %q", res.LastMessageUUID)
	}
}

func TestRun_SilentSkipOnUninitializedProject(t *testing.T) {
	// Project dir exists but has no .sync/ — observe must return nil cleanly,
	// not error, so the global Stop hook is harmless in non-vibelog repos.
	tmp := t.TempDir()
	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"foo.go"}}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`

	var err error
	withStdin(t, payload, func() {
		err = observecmd.Run("") // empty arg → falls back to payload.cwd
	})
	if err != nil {
		t.Fatalf("uninitialized project must not error, got %v", err)
	}
}

func TestRun_NoTranscriptPath_Errors(t *testing.T) {
	var err error
	withStdin(t, `{"session_id":"s","stop_hook_active":false,"hook_event_name":"Stop","cwd":"/tmp"}`, func() {
		err = observecmd.Run("")
	})
	if err == nil || !strings.Contains(err.Error(), "transcript_path") {
		t.Errorf("expected missing-transcript_path error, got %v", err)
	}
}

func TestRun_StopHookActive_NoOp(t *testing.T) {
	var err error
	withStdin(t, `{"stop_hook_active":true,"hook_event_name":"Stop"}`, func() {
		err = observecmd.Run("/nonexistent")
	})
	if err != nil {
		t.Errorf("expected nil on stop_hook_active=true, got %v", err)
	}
}

func TestRun_SilentSkipWhenServeInactive(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"foo.go"}}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("inactive project should skip, got %v", err)
		}
	})
	b, err := os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 1 {
		t.Fatalf("expected only init row while inactive, got %d rows", got)
	}

	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("active project should record, got %v", err)
		}
	})
	b, err = os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 2 {
		t.Fatalf("expected init row + observed row while active, got %d rows", got)
	}
}

func TestAnalyzeTranscript_DedupesFiles(t *testing.T) {
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"x"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Edit","input":{"file_path":"/abs/foo.go"}},` +
			`{"type":"tool_use","name":"Write","input":{"file_path":"/abs/foo.go"}}` +
			`]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 {
		t.Errorf("expected deduped to 1, got %v", res.Files)
	}
}

func TestAnalyzeTranscript_FlagsTaskNotificationAsSyntheticTurn(t *testing.T) {
	// When a background Workflow/Agent completes, the harness injects a
	// <task-notification> block as type:"user". observe must flag the result
	// as SyntheticTurn so Run() skips recording entirely — there's no human
	// behind it and the assistant's response is bookkeeping noise.
	// Both content shapes appear in real transcripts: array-of-blocks AND
	// bare string. Test both.
	cases := []struct {
		name   string
		userMsg string
	}{
		{
			name:   "blocks_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"<task-notification>\n<task-id>wf123</task-id>\n</task-notification>"}]}}`,
		},
		{
			name:   "string_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification>\n<task-id>wf456</task-id>\n</task-notification>"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTranscript(t, []string{
				`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"first prompt"}]}}`,
				`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first reply"}]}}`,
				tc.userMsg,
				`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"response to synthetic"}]}}`,
			})
			res, err := observecmd.AnalyzeTranscript(path)
			if err != nil {
				t.Fatal(err)
			}
			if !res.SyntheticTurn {
				t.Errorf("expected SyntheticTurn=true for harness-injected task-notification (%s)", tc.name)
			}
			if res.LastMessageUUID != "a1" {
				t.Errorf("expected anchor at synthetic turn (uuid a1), got %q", res.LastMessageUUID)
			}
		})
	}
}

func TestAnalyzeTranscript_KeepsUserPastedTaskNotificationWithFraming(t *testing.T) {
	// A user who PASTES a notification frames it with prose ("look at this:").
	// That's a real user turn and must not be skipped — only messages that
	// START with the wrapper tag (i.e. injected by the harness) get scrubbed.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"look at this: <task-notification>...</task-notification> what gives?"}]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"that's a workflow result"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.UserPrompt, "look at this:") {
		t.Errorf("expected to preserve user prose framing, got %q", res.UserPrompt)
	}
}

// ---------- Hardening pass (Workflow-generated) ----------

func TestAnalyzeTranscript_KeepsUserPastedTaskNotificationWithFraming_StringForm(t *testing.T) {
	// Symmetric to TestAnalyzeTranscript_KeepsUserPastedTaskNotificationWithFraming
	// but exercises the BARE STRING content branch of extractUserPromptText,
	// which has its own call into isHarnessInjectedUserText. Without this, the
	// negative case for the string branch is uncovered.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"look at this: <task-notification>...</task-notification> what gives?"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"that's a workflow result"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=false when prose precedes the tag (string form)")
	}
	if !strings.Contains(res.UserPrompt, "look at this:") {
		t.Errorf("expected to preserve user prose framing, got %q", res.UserPrompt)
	}
}

func TestAnalyzeTranscript_LeadingWhitespaceBeforeNotificationTag(t *testing.T) {
	// Locks in the strings.TrimSpace() guard inside isHarnessInjectedUserText.
	// Claude Code's blob writer has been observed to emit leading newlines on
	// harness-injected messages; a refactor swapping TrimSpace for HasPrefix-on-raw
	// would silently start logging them as user prompts.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"   \n\t<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=true when only leading whitespace precedes the tag")
	}
}

func TestAnalyzeTranscript_TaskNotificationWithAttributes(t *testing.T) {
	// Exercises the second prefix in the allow-list ("<task-notification " with
	// trailing space). A refactor that consolidates the prefix list to only
	// "<task-notification>" would regress on the attributed form — and we'd
	// silently start logging 3KB XML blobs as user prompts again.
	cases := []struct {
		name    string
		userMsg string
	}{
		{
			name:    "blocks_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"<task-notification id=\"wf-99\" status=\"complete\">\n<task-id>x</task-id>\n</task-notification>"}]}}`,
		},
		{
			name:    "string_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification id=\"wf-99\" status=\"complete\">\n<task-id>x</task-id>\n</task-notification>"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTranscript(t, []string{
				tc.userMsg,
				`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
			})
			res, err := observecmd.AnalyzeTranscript(path)
			if err != nil {
				t.Fatal(err)
			}
			if !res.SyntheticTurn {
				t.Errorf("expected SyntheticTurn=true for attributed task-notification (%s)", tc.name)
			}
		})
	}
}

func TestAnalyzeTranscript_WorkflowNotificationFlagged(t *testing.T) {
	// The third prefix (<workflow-notification>) is in the production allow-list
	// but had zero test coverage. A reviewer doing dead-code elimination could
	// remove it without any test going red.
	cases := []struct {
		name    string
		userMsg string
	}{
		{
			name:    "blocks_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"<workflow-notification>\n<id>w1</id>\n</workflow-notification>"}]}}`,
		},
		{
			name:    "string_form",
			userMsg: `{"uuid":"u1","type":"user","message":{"role":"user","content":"<workflow-notification>\n<id>w1</id>\n</workflow-notification>"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTranscript(t, []string{
				tc.userMsg,
				`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
			})
			res, err := observecmd.AnalyzeTranscript(path)
			if err != nil {
				t.Fatal(err)
			}
			if !res.SyntheticTurn {
				t.Errorf("expected SyntheticTurn=true for workflow-notification (%s)", tc.name)
			}
		})
	}
}

func TestAnalyzeTranscript_MixedBlocks_NotificationFirst_FlaggedSynthetic(t *testing.T) {
	// Documents current semantics: a prepended notification block, even when
	// followed by real prose, hijacks the whole turn because the prefix check
	// runs on the joined string. Forces the team to make this trade-off explicit.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[` +
			`{"type":"text","text":"<task-notification>\n<id>x</id>\n</task-notification>"},` +
			`{"type":"text","text":"please continue"}` +
			`]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"k"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=true when first block is the notification tag")
	}
}

func TestAnalyzeTranscript_MixedBlocks_ProseFirst_KeepsTurn(t *testing.T) {
	// Confirms the prefix check operates on the JOINED string, not per-block.
	// A future change that loops per-block and OR's the synthetic flag would
	// silently start dropping turns where the user pastes a notification AFTER
	// their own prose.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[` +
			`{"type":"text","text":"here's what happened"},` +
			`{"type":"text","text":"<task-notification>\n<id>x</id>\n</task-notification>"}` +
			`]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"k"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=false when prose precedes the notification block")
	}
	if !strings.Contains(res.UserPrompt, "here's what happened") {
		t.Errorf("expected user prose preserved, got %q", res.UserPrompt)
	}
	if !strings.Contains(res.UserPrompt, "task-notification") {
		t.Errorf("expected both segments joined in UserPrompt, got %q", res.UserPrompt)
	}
}

func TestAnalyzeTranscript_EmptyContentArray_WalksBack(t *testing.T) {
	// An empty content array returns ok=false from extractUserPromptText, so
	// the walker continues to the previous real user prompt. A refactor that
	// returned ("", true, false) for empty would anchor on a ghost turn with
	// no prompt.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"the real thing"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"follow-up"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.UserPrompt != "the real thing" {
		t.Errorf("expected walker to skip empty-content entry and anchor on real prompt, got %q", res.UserPrompt)
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected forward walk to land on latest assistant (a1), got %q", res.LastMessageUUID)
	}
}

func TestAnalyzeTranscript_AllWhitespaceUserText_WalksBack(t *testing.T) {
	// Today's behaviour: extractUserPromptText returns ok=true for a non-empty
	// whitespace string, so the walker anchors on u1; sanitizeUserPrompt then
	// TrimSpace's it to "" and downstream the dashboard renders a blank prompt.
	// This test documents the current (arguably wrong) behaviour so the team
	// has to make a decision: either keep it pinned, or update the test when
	// extractUserPromptText is taught to skip whitespace-only strings.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"the real thing"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"   \n\t  "}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"follow-up"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	// CURRENT behaviour: anchors on u1, UserPrompt sanitized to "" (TrimSpace).
	// If you change the extractor to skip whitespace-only strings, flip this
	// test to assert UserPrompt=="the real thing" and LastMessageUUID=="a1".
	if res.UserPrompt != "" {
		t.Errorf("CURRENT contract: whitespace-only user msg sanitizes to empty UserPrompt, got %q. If this changes, update the test and the contract together.", res.UserPrompt)
	}
}

func TestAnalyzeTranscript_StackedSyntheticNotifications_AnchorsAtLatest(t *testing.T) {
	// Two background workflows completing in a row produce stacked synthetic
	// anchors. Current code anchors at the LATEST synthetic (u2) and flags
	// synthetic; Run() then skips. A refactor to "walk back through synthetics
	// to the last real prompt" could silently start recording a2 against u0's
	// prompt and double-count the work.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"first real"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"r0"}]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification>\n<id>1</id>\n</task-notification>"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"r1"}]}}`,
		`{"uuid":"u2","type":"user","message":{"role":"user","content":"<task-notification>\n<id>2</id>\n</task-notification>"}}`,
		`{"uuid":"a2","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"r2"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=true at the latest synthetic anchor")
	}
	if res.LastMessageUUID != "a2" {
		t.Errorf("expected to anchor at latest assistant (a2), got %q", res.LastMessageUUID)
	}
}

func TestAnalyzeTranscript_ToolResultLooksLikeNotification_StillSkipped(t *testing.T) {
	// Adversarial overlap of the tool_result skip and the synthetic scrub:
	// an assistant tool whose result payload happens to look like a notification
	// tag. Because extractUserPromptText only collects type=="text" blocks, the
	// tool_result yields zero parts → ok=false → walker continues to the real
	// prompt. Locks in that the tool-result skip takes precedence.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"real prompt"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}` +
			`]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"<task-notification>fake</task-notification>"}` +
			`]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.UserPrompt != "real prompt" {
		t.Errorf("expected walker to step over tool_result entry and land on real prompt, got %q", res.UserPrompt)
	}
	if res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=false — tool_result payload mimicking a tag must not trigger the scrub")
	}
}

func TestAnalyzeTranscript_BareStringEmpty_WalksBack(t *testing.T) {
	// Symmetric to the empty-content-array case but on the string branch.
	// Today's behaviour: the extractor handles empty bare string "" — json
	// unmarshals to "", isHarnessInjectedUserText returns false, and it
	// returns ("", true, false), anchoring on u1 with an empty prompt.
	// This test surfaces the asymmetry between the two shapes so we either
	// fix it (skip empty bare strings too) or document it.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"the real thing"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":""}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"follow-up"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	// CURRENT (asymmetric) behaviour: anchors on u1 with empty UserPrompt.
	// If the extractor is taught to skip empty bare strings, flip this to
	// UserPrompt=="the real thing" and LastMessageUUID=="a1".
	if res.UserPrompt != "" {
		t.Errorf("CURRENT contract: empty bare-string user msg anchors with empty UserPrompt (asymmetric with empty array). Got %q. If you symmetrize the two branches, update this test and the documented contract together.", res.UserPrompt)
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected anchor at u1's follow-up (a1) under current behaviour, got %q", res.LastMessageUUID)
	}
}

func TestAnalyzeTranscript_NonTextBlocksOnly_WalksBack(t *testing.T) {
	// An entry whose content has only non-text blocks (image, tool_result)
	// must be treated as not-a-user-prompt and the walker must continue.
	// Otherwise a screenshot-only paste lands as an anchor with empty prompt.
	path := writeTranscript(t, []string{
		`{"uuid":"u0","type":"user","message":{"role":"user","content":[{"type":"text","text":"real prompt"}]}}`,
		`{"uuid":"a0","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`,
		`{"uuid":"u1","type":"user","message":{"role":"user","content":[` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}` +
			`]}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"follow-up"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.UserPrompt != "real prompt" {
		t.Errorf("expected walker to skip image-only entry and land on real prompt, got %q", res.UserPrompt)
	}
}

func TestAnalyzeTranscript_SyntheticIsFirstEntryInTranscript(t *testing.T) {
	// Boundary case: the back-walker's FIRST hit is synthetic, and there is
	// no prior turn to fall back to. Must not panic, must still flag synthetic,
	// must anchor on the assistant reply.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification>\n<id>only</id>\n</task-notification>"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reply to synthetic"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=true when first entry is harness-injected")
	}
	if res.LastMessageUUID != "a1" {
		t.Errorf("expected anchor at a1, got %q", res.LastMessageUUID)
	}
	if !strings.Contains(res.UserPrompt, "task-notification") {
		t.Errorf("expected UserPrompt to contain the raw tag text, got %q", res.UserPrompt)
	}
}

func TestAnalyzeTranscript_UserAskingAboutTagIsFalsePositive_LocksBehavior(t *testing.T) {
	// KNOWN false-positive trade-off, documented here so a future contributor
	// reading the test sees the design decision instead of "fixing" it:
	//
	// The prefix heuristic cannot distinguish a user who STARTS their prompt
	// with the literal tag from a harness injection. A tighter check (e.g.
	// "entire message must be just the tag") was rejected because real harness
	// blocks contain prose/IDs between the tags.
	//
	// If you later tighten the heuristic, this test will fail — at which point
	// please update both this test and the documented contract together.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification> — how do I parse these blocks in Go?"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"here's how"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SyntheticTurn {
		t.Errorf("CURRENT contract: any message starting with the tag is flagged synthetic, even if the user typed a real question after it. Got SyntheticTurn=false — if you intentionally tightened the heuristic, update this test and the contract.")
	}
}

func TestAnalyzeTranscript_CaseSensitiveTagMatch_LocksBehavior(t *testing.T) {
	// Locks the current case-sensitive HasPrefix contract. The harness emits
	// lowercase tags; if someone "helpfully" makes the check case-insensitive,
	// real user prose like `<Task-Notification logic>` at the start of a
	// question would be wrongly scrubbed.
	path := writeTranscript(t, []string{
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<Task-Notification>not the harness</Task-Notification>"}}`,
		`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"k"}]}}`,
	})
	res, err := observecmd.AnalyzeTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.SyntheticTurn {
		t.Errorf("expected SyntheticTurn=false for capitalized tag (case-sensitive contract)")
	}
}

func TestRun_SkipsSyntheticTurnUnderActiveProject(t *testing.T) {
	// Bug #4: Result.SyntheticTurn → Run() must early-return without recording.
	// Existing tests prove AnalyzeTranscript sets the flag and prove an active
	// project DOES record real turns; nothing fails if someone deletes the
	// `if res.SyntheticTurn { return nil }` early-return in Run().
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification>\n<id>wf1</id>\n</task-notification>"}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack to synthetic"}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`

	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("synthetic turn must not error, got %v", err)
		}
	})
	b, err := os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 1 {
		t.Fatalf("expected only init row (synthetic turn skipped), got %d rows", got)
	}
}

func TestRun_SyntheticTurnIsIdempotent(t *testing.T) {
	// Locks in that repeated Stop-hook fires on a synthetic transcript stay
	// a clean no-op. Without this, a refactor could move state mutation
	// (file writes, marker reads) ahead of the SyntheticTurn check and pass
	// single-shot tests while breaking double-fire safety.
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	release, err := serve.AcquireActiveMarker(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	transcript := filepath.Join(tmp, "tr.jsonl")
	if err := os.WriteFile(transcript, []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"<task-notification>\n<id>wf1</id>\n</task-notification>"}}`+"\n"+
			`{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ack"}]}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := `{"session_id":"s","transcript_path":"` + transcript + `","stop_hook_active":false,"hook_event_name":"Stop","cwd":"` + tmp + `"}`

	// First fire.
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("first synthetic fire must not error, got %v", err)
		}
	})
	// Second fire (same payload, simulating Claude Code retry / double-invocation).
	withStdin(t, payload, func() {
		if err := observecmd.Run(""); err != nil {
			t.Fatalf("second synthetic fire must not error, got %v", err)
		}
	})

	b, err := os.ReadFile(filepath.Join(tmp, ".sync", "iterations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; got != 1 {
		t.Fatalf("expected only init row after two synthetic fires, got %d rows", got)
	}
	// Side-effect guard: pending_implementation.txt must not be created or
	// consumed as part of skipping a synthetic turn.
	if _, err := os.Stat(filepath.Join(tmp, ".sync", "pending_implementation.txt")); !os.IsNotExist(err) {
		t.Errorf("expected no pending_implementation.txt side effect on synthetic skip, stat err=%v", err)
	}
}