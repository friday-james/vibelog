<h1 align="center">
  📒 vibelog
</h1>

<p align="center">
  <b>The discipline layer for AI-assisted coding.</b><br/>
  A reviewable per-turn log of what the agent did and why.
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

vibelog is a local audit trail for AI-assisted coding. It records each agent turn as a row with a short teach-back, the files touched, and a per-file diff against the previous time that file was touched. It runs in the background next to your repo, reads git, and stores plain files on disk. You keep coding the way you already do. When something breaks or a reviewer asks what changed, you have a sequence to point at instead of a chat scrollback to excavate.

No daemon, no database, no telemetry. Plain JSONL into `.sync/`, a single page on `localhost:7100`.

---

## Why

Vibe coding ships real software. Most of the time it is fine. The trouble starts when you need to look backwards, and the tools you have were built for a slower kind of editing.

A few of the failure modes vibelog is built around:

- Two Claude sessions on the same checkout race each other. The second write wins. The first session's edits vanish without a commit ever naming them.
- An agent rewrites a function you hand-tuned an hour ago. The diff looks plausible, you accept, the tuning is gone.
- A regression lands. You cannot tell whether you introduced it during a refactor, the agent introduced it while fixing a test, or the formatter shifted a line that masked it.
- By the time something breaks, the working tree has ten unrelated edits stacked on top of the one that mattered. `git diff` shows the current mess, not the sequence that produced it. `git log` only sees what you remembered to commit, and vibe sessions rarely commit per turn.
- A teammate asks what you changed yesterday. The honest answer is "the agent did a lot" and you cannot point at the moment it happened.
- Future-you is a stranger. Two weeks from now you will not remember which prompt produced which line.

vibelog puts the engineering workflow back: an ordered log you scroll, a per-turn diff you can attribute, a teach-back written when the change was made.

---

## Philosophy

- Every agent turn leaves a receipt. If a file was touched, there is a row you can point at, with a diff and a one-line reason.
- Diffs are per-file and per-turn, not per-commit. You see what this specific edit did to this specific file, without the noise of unrelated changes.
- The teach-back is written when the change happens, by the agent that made it. Not reconstructed later from chat scrollback.
- Working tree state is not a substitute for history. A later `git stash` or rebase does not erase what the agent actually did.
- If you cannot tell who touched a line and why, the tooling failed you, not you.
- Optimize for the question you will ask in six hours, not the one you are asking now.
- Local first, plain files on disk. Git is the source of truth. vibelog reads it, it does not replace it.

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

### MCP server for teach-backs (required)

```bash
claude mcp add vibelog vibelog mcp
```

This registers a `set_implementation` tool the agent calls at the end of every turn. The curated summary + response text are saved *during* the turn, so they're guaranteed to be there when the Stop hook reads. Without it you'll see empty cards on most Q&A turns and on longer file-touching turns. Claude Code flushes the assistant's reply to the transcript after the Stop hook fires, so the post-hoc heuristic loses the race.

---

## How it works

```
                  ┌─────────────────┐
   you ──prompt──▶│   claude code   │──response──▶ you
                  └────────┬────────┘
                           │ Stop hook
                           ▼
                  ┌─────────────────┐
                  │  vibelog observe│ ── writes one row ─▶ .sync/iterations.jsonl
                  └─────────────────┘
                                              │
                                              ▼
                                     ┌─────────────────┐
                                     │  vibelog serve  │
                                     └────────┬────────┘
                                              │ HTTP
                                              ▼
                                    http://localhost:7100
```

Every assistant turn becomes one row in `.sync/iterations.jsonl` and one card on the dashboard. The card expands progressively:

| Tap | Reveals |
| --- | --- |
| **L0** | The user prompt + a one-line subtitle of what the agent did |
| **L1** | `show response` (Q&A) or `show implementation` (file-touching). The curated teach-back. |
| **L2** | `show files touched`. Paths the agent edited this turn. |
| **L3** | `show diffs`. Per-file unified diff vs the snapshot from the previous touch. |

The data is plain JSONL. You can `cat .sync/iterations.jsonl | jq` it any time.

---

## Project layout

```
cmd/vibelog/                CLI: init, mcp, observe, serve, watch, ingest-git
internal/
├── model/                  Iteration, Anchor (typed schema)
├── store/                  reads .sync/, tolerant of unknown row kinds
├── observecmd/             Stop-hook handler (transcript → row)
├── mcpserver/              MCP tools (set_implementation, …)
├── serve/                  HTTP server + embedded UI
├── gitcmd/                 walks git log (optional ingest-git subcommand)
├── initcmd/                scaffolds .sync/
└── watchcmd/               tails .sync/iterations.jsonl in the terminal
.sync/                      per-project state (add to .gitignore)
├── anchor.yaml             project intent (optional, mostly informational)
├── iterations.jsonl        one row per assistant turn
└── snapshots/iter-N/...    file contents at iter N (used by the diff endpoint)
```

---

## Status

Early. Daily-driven by the author against `claude code` on macOS (Linux probably works, untested). Codex support is in the design pile. Issues and PRs welcome.

---

## License

MIT.
