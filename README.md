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

Requires [Claude Code](https://claude.ai/code) or [Codex CLI](https://help.openai.com/en/articles/11096431), plus git.

### From a release binary (recommended)

Grab the latest tarball for your platform from [Releases](https://github.com/friday-james/vibelog/releases) and drop the binary anywhere on your `$PATH`. One-liner for macOS / Linux:

```bash
# pick your OS and arch: darwin_arm64, darwin_amd64, linux_amd64, linux_arm64
VERSION=v0.1.0
OS_ARCH=darwin_arm64

curl -sSL "https://github.com/friday-james/vibelog/releases/download/${VERSION}/vibelog_${VERSION#v}_${OS_ARCH}.tar.gz" \
  | tar -xz -C /usr/local/bin vibelog
```

`vibelog --version` should print the tagged version.

### From source

If you'd rather build from `main`, or you want to hack on it:

```bash
# Go 1.25+ required
go install github.com/friday-james/vibelog/cmd/vibelog@latest
```

That puts `vibelog` in `~/go/bin/` (make sure it's on your `$PATH`).

### Set up a project

```bash
cd /path/to/your/repo
vibelog init        # creates .sync/ skeleton
vibelog serve &     # dashboard on http://localhost:7100; logging stays on while this process lives
```

`vibelog serve` is the activation lease. While it runs, the project has a `.sync/serve-active.json` marker and agent turns are recorded. `Ctrl+C` the process and logging stops for that repo.

### Multiple projects on one dashboard (leases)

Just type `vibelog serve` in any repo. The first one starts the dashboard at `localhost:7100` with the current repo as the only tab. Every subsequent `vibelog serve` in another repo detects the running dashboard and **opens a lease** for the new repo, which appears as another tab. The lease lives only as long as that process does: `Ctrl+C` it and the tab disappears.

```bash
# terminal 1
cd ~/code/vibelog && vibelog serve
# vibelog serving on http://localhost:7100
#   /p/vibelog/  →  /Users/you/code/vibelog

# terminal 2
cd ~/code/ledger && vibelog serve
# vibelog: leased ledger with running serve at http://localhost:7100
#   → http://localhost:7100/p/ledger/   (Ctrl+C to deregister)

# now http://localhost:7100 has two tabs: vibelog, ledger
# Ctrl+C terminal 2 → ledger tab disappears
# Ctrl+C terminal 1 → the whole dashboard goes away
```

No config file, no flag flips. Each `vibelog serve` is a long-running process you can cancel cleanly. Claude's Stop hook and direct MCP clients like Codex record only while that repo's `vibelog serve` lease is alive, so the tabs always show *current* leases and `Ctrl+C` cleanly stops both the tab and the logging lease.

**Static modes (for CI or fixed setups)** — bypass the lease dance with either flag:

```bash
vibelog serve -projects "vibelog=/path/a,ledger=/path/b"
vibelog serve -config /path/to/projects.yaml
```

These bind a fixed project list at startup. They still create per-project logging leases while the process is alive, but they do not auto-register or auto-deregister tabs against another already-running dashboard.

### Agent setup: Claude Code (recommended)

Claude Code is the recommended path today. It has a Stop hook, so vibelog can record a turn after Claude finishes writing to its transcript. The MCP server adds the structured teach-back that appears on each card.

Add the Stop hook to `~/.claude/settings.json`:


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

Register the MCP server:

```bash
claude mcp add -s user vibelog vibelog mcp
```

Confirm it took:

```bash
claude mcp list | grep vibelog
# expect: vibelog: vibelog mcp - ✓ Connected
```

`-s user` registers vibelog at the user scope so every project picks it up. (Without it, the default is project-local and you'd have to re-run the command in every repo.)

This gives the agent a `set_implementation` tool, which it calls at the end of every turn. The curated summary + response text are saved *during* the turn, so they're guaranteed to be there when the Stop hook reads. Without the MCP server you'll see empty cards on most Q&A turns and on longer file-touching turns. Claude Code flushes the assistant's reply to the transcript after the Stop hook fires, so the post-hoc heuristic loses the race.

### Agent setup: Codex (beta)

Codex support is beta. Codex has no Claude-style Stop hook, so it relies on global instructions telling the agent to call the MCP tool directly before ending a turn. This works, but it depends on Codex following that instruction in the active session.

The setup is two pieces:

- Register `vibelog mcp` once so Codex can launch the MCP server.
- Add a global Codex instruction that uses vibelog only while the current repo has `.sync/serve-active.json`.

Register the MCP server:

```bash
codex mcp add vibelog -- vibelog mcp
```

Add the global instruction file:

```bash
mkdir -p ~/.codex

cat > ~/.codex/vibelog-instructions.md <<'EOF'
Use the `vibelog` MCP server only when the current project has an active vibelog serve lease.

Activation rule:
- First check whether `./.sync/serve-active.json` exists in the current project root.
- If it does not exist, ignore `vibelog` entirely and do not call any `mcp__vibelog__*` tools.
- If it does exist, then before ending any turn that changed files in this project, call `mcp__vibelog__record_iteration`.
- You may also call `mcp__vibelog__record_iteration` for meaningful pure-conversation turns that should appear on the dashboard.

When calling `mcp__vibelog__record_iteration`:
- Pass `summary` as a short past-tense description of what changed.
- Pass `files_changed` as project-relative paths for files edited this turn.
- Pass `user_prompt` when available.
- Pass `implementation` when available so the dashboard can render the detailed teach-back.

Do not call `mcp__vibelog__record_iteration` for projects that do not contain `./.sync/serve-active.json`.
EOF
```

Point Codex at that file from `~/.codex/config.toml`:

```toml
model_instructions_file = "/Users/you/.codex/vibelog-instructions.md"
```

Use your real home directory path. Codex reads this when a new session starts, so restart Codex after changing it.

Now the runtime flow is:

```text
cd repo
vibelog init          # one-time: creates .sync/
vibelog serve         # per session: creates .sync/serve-active.json

codex session starts
  -> Codex reads ~/.codex/config.toml
  -> Codex can launch vibelog mcp
  -> global instruction checks .sync/serve-active.json
  -> if present, Codex calls record_iteration before ending turns
  -> vibelog mcp appends .sync/iterations.jsonl
  -> vibelog serve renders the row in the dashboard
```

The important part: the MCP server can be registered globally, but logging is still per-project and only active while `vibelog serve` is alive for that repo. The MCP server also validates that the marker belongs to a live serve process, so stale marker files do not keep logging enabled after a crash.

---

## How it works

```
 Claude Code: prompt → tool call (`set_implementation`) → Stop hook (`vibelog observe`) → `.sync/iterations.jsonl`
 Codex:       prompt → tool call (`record_iteration`)                                → `.sync/iterations.jsonl`
 Dashboard:   `vibelog serve` reads the shared `.sync/` history and renders both.
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
├── observecmd/             Claude Stop-hook handler (transcript → row)
├── mcpserver/              MCP tools (record_iteration, set_implementation, …)
├── serve/                  HTTP server, embedded UI, active logging lease
├── gitcmd/                 walks git log (optional ingest-git subcommand)
├── initcmd/                scaffolds .sync/
└── watchcmd/               tails .sync/iterations.jsonl in the terminal
.sync/                      per-project state (add to .gitignore)
├── anchor.yaml             project intent (optional, mostly informational)
├── serve-active.json       lease marker written while vibelog serve is alive
├── iterations.jsonl        one row per assistant turn
└── snapshots/iter-N/...    file contents at iter N (used by the diff endpoint)
```

---

## Status

Early. Daily-driven by the author against `claude code` and Codex on Unix. Issues and PRs welcome.

---

## License

MIT.

---

## Releases

Tagged releases publish binaries for linux/macOS × amd64/arm64 to https://github.com/friday-james/vibelog/releases. Built by goreleaser via `.github/workflows/release.yml` on every `v*` tag.
