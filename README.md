<h1 align="center">
  🎚️ vibelog
</h1>

<p align="center">
  <b>A log for vibe coding.</b><br/>
  Every prompt, every edit, every drift — recorded.
</p>

<p align="center">
  <a href="#install"><img alt="go" src="https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go"></a>
  <a href="LICENSE"><img alt="license" src="https://img.shields.io/badge/license-MIT-blue"></a>
  <a href="https://github.com/friday-james/vibelog"><img alt="status" src="https://img.shields.io/badge/status-early-orange"></a>
</p>

<p align="center">
  <img src="docs/screenshot.png" width="820" alt="vibelog dashboard"/>
</p>

---

## What it is

A small dashboard that watches your AI coding sessions.

It records what the agent did (prompts, file edits, replies), shows what you've changed manually that's not yet committed, and surfaces the moments two coding sessions or you-and-the-agent stepped on each other.

There's no daemon, no DB, no telemetry. It writes plain JSONL into `.sync/` next to your repo, reads `git status`, and serves a single page on `localhost:7100`.

---

## Why

If you let an agent move fast in your repo, you stop remembering what it touched. You lose the receipts. vibelog gives them back.

Common things it catches:

- The agent rewrote a file you'd been editing in your IDE and didn't tell you.
- Another `claude` session on the same project touched the same file you're working on.
- You hand-edited `.env` and forgot — vibelog still shows it as drift.
- You want to look back at "what did I ask 3 hours ago?" — every prompt is there.

---

## Install

Requires Go 1.25+, [Claude Code](https://claude.ai/code), git.

```bash
git clone https://github.com/friday-james/vibelog
cd vibelog
go install ./cmd/vibelog
```

That puts `vibelog` on your `$PATH` (assuming `~/go/bin` is on it).

### Set up a project

```bash
cd /path/to/your/repo
vibelog init        # creates .sync/ skeleton
vibelog serve &     # dashboard on http://localhost:7100
```

### Wire the Stop hook (records every assistant turn)

Add this to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "vibelog observe" }]
      }
    ]
  }
}
```

If `vibelog` isn't on the PATH that Claude Code spawns hooks with, use the absolute path: `/Users/you/go/bin/vibelog observe`.

### (Optional) MCP server for deterministic teach-backs

```bash
claude mcp add vibelog vibelog mcp
```

This gives the agent a `set_implementation` tool. When it calls it, the curated summary + response text are saved during the turn, which sidesteps the Claude-Code-flushes-the-transcript-after-the-Stop-hook race. Without it, vibelog falls back to reading the last assistant text block from the transcript.

---

## How it works

```
                  ┌─────────────────┐
   you ──prompt──▶│   claude code   │──response──▶ you
                  └────────┬────────┘
                           │ Stop hook
                           ▼
                  ┌─────────────────┐
                  │  vibelog observe│
                  └────────┬────────┘
                           │ writes one row
                           ▼
              .sync/iterations.jsonl  (append-only)
                           │
                           ▼
                  ┌─────────────────┐    git status
                  │  vibelog serve  │◀────────────── working tree
                  └────────┬────────┘
                           │ HTTP
                           ▼
                http://localhost:7100
```

Three data sources, one feed:

| Source | What it gives |
| --- | --- |
| Stop hook | One row per assistant turn — prompt, edits, response |
| `git status` | The leading drift card: files you changed that the agent didn't |
| `git log` (one-shot) | Commits, shown as nodes in the timeline |

---

## What you see

| Element | When it shows |
| --- | --- |
| **Leading drift card** | Whenever uncommitted files exist that the agent isn't responsible for |
| **Prompt card** | One per assistant turn. Expands to: response → files touched → per-file diff |
| `⚠ overwrites external edit` | An agent turn wrote over a file you'd manually changed |
| `⇄ interleaved with another session` | Another `claude` session in this repo touched the same file |
| **Commit nodes** | Every git commit in the timeline, clickable for `git show` |

---

## Project layout

```
cmd/vibelog/                CLI: init, mcp, observe, serve, watch, ingest-git
internal/
├── model/                  Iteration, Anchor — typed schema
├── store/                  reads .sync/ — tolerant of unknown row kinds
├── observecmd/             Stop-hook handler
├── mcpserver/              MCP tools (set_implementation, record_iteration, …)
├── serve/                  HTTP server + embedded UI
├── gitstatus/              git-status-based drift detector
├── gitcmd/                 ingest-git for commit rows
└── untrackedstore/         history of untracked-but-modified files (.env etc.)
.sync/                      per-project state (gitignore this)
├── anchor.yaml
├── iterations.jsonl
└── snapshots/iter-N/...
```

---

## Status

Early. Daily-driven by the author against `claude code` on a Mac. Codex adapter and git-tree snapshots are on the roadmap but not built. Issues and PRs welcome.

---

## License

MIT.
