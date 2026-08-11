# Atom

Atom is a tiny, dependency-free Go harness for coding agents. It starts as a
single binary, keeps sessions in readable JSONL, and deliberately has a small
surface area:

- OpenAI and GitHub Copilot adapters behind one streaming provider interface
- An agent loop with `read`, `write`, `edit`, `bash`, and `grep`
- `AGENTS.md` discovery and small, file-based skills
- A quick ANSI terminal UI plus print mode for scripts
- Explicit and automatic context compaction

## Install and run

```bash
go install github.com/nicobrch/atom/cmd/atom@latest
export OPENAI_API_KEY=...
atom
```

When developing from a checkout, use `go install ./cmd/atom` instead.

### Installing from the private repository

If the repository remains private, configure Go to skip the public module proxy
for your GitHub namespace and make Git use your authenticated SSH key. This is
a one-time setup on each development machine:

```bash
go env -w GOPRIVATE=github.com/nicobrch/*
git config --global url."git@github.com:".insteadOf https://github.com/
go install github.com/nicobrch/atom/cmd/atom@latest
```

Alternatively, make the repository public and only the final `go install`
command is needed.

OpenAI defaults to `gpt-5.4`; override it with `--model`.

```bash
atom --model gpt-5.4
atom -p "explain this repository"
```

### ChatGPT subscription / Codex sign-in

Atom also reuses the credential created by the supported Codex **Sign in with
ChatGPT** flow. It reads `~/.codex/auth.json` only in memory; it never copies
the credential into the repository or an Atom session. Sign in once, then run
Atom normally without `OPENAI_API_KEY`:

```bash
atom auth openai
atom --model gpt-5.4
```

This delegates browser authentication to the installed Codex CLI. If you do
not have it installed, install/login with Codex first, then use an API key.

For GitHub Copilot, provide a Copilot bearer token (not a general GitHub PAT)
and, when necessary, its API endpoint:

```bash
export COPILOT_TOKEN=...
# optional; this is the normal individual endpoint
export COPILOT_BASE_URL=https://api.individual.githubcopilot.com
atom --provider copilot --model gpt-5.4
```

`COPILOT_GITHUB_TOKEN` is accepted as an alias for `COPILOT_TOKEN`. OAuth/device
login is intentionally outside the 0.1 core; refresh or obtain a Copilot token
with the GitHub tooling you already use.

## Interaction

Run `atom` without `-p` to enter the terminal UI. Commands are:

- `/help` — show commands
- `/compact` — replace conversation history with a model-generated handoff
- `/clear` — begin a fresh conversation in the current session file
- `/session` — show the JSONL session path
- `/logs` — show today's diagnostic-log path
- `/skills` — list discovered skills
- `/exit` — quit

Every user, assistant, tool-call, tool-result, and compaction event is appended
to `.atom/sessions/<timestamp>.jsonl`. Resume one with `--session PATH`.

## Diagnostics

Every normal model request writes metadata-only lifecycle events to
`.atom/logs/<YYYY-MM-DD>.jsonl`: `request_started`, `request_succeeded`, or
`request_failed`; a transient pre-output failure also emits `request_retrying`.
Atom retries overloads, rate limits, 5xx failures, and transport failures up
to three total attempts, waiting 2 seconds then 4 seconds. It never retries a
stream after it has produced text or a tool call, avoiding duplicated output or
side effects. Compaction uses the equivalent `compaction_*` events. A
request failure includes Atom's correlation ID, provider,
model, latency, token counts, HTTP status, provider request/response IDs, and
the provider error code/type/message when supplied. The same failure is also
appended to its session JSONL as an `error` record, without changing
conversation replay.

Diagnostic logs deliberately exclude prompts, system instructions, tool
arguments/results, authorization headers, and raw HTTP bodies. Log directories
and files are owner-only (`0700` and `0600`). Existing session files are made
owner-only the next time Atom opens them.

## Project customization

Atom loads every `AGENTS.md` from the workspace root down to the working
directory. Put a skill in either `.atom/skills/<name>/SKILL.md` or
`~/.atom/skills/<name>/SKILL.md`; the agent sees each skill's name and
description. A user can explicitly load one with `/skill <name>`.

The optional `.atom/config.json` can provide defaults:

```json
{
  "provider": "openai",
  "model": "gpt-5.4",
  "context_tokens": 128000,
  "auto_compact_at": 0.80,
  "bash_timeout_seconds": 120
}
```

## Architecture

```
CLI/TUI → Agent loop → Provider.Stream (SSE) → OpenAI-compatible endpoint
             │                     ↑
             ├── Tool registry ────┘
             ├── JSONL session
             └── AGENTS.md / skills
```

The provider and tool interfaces live in `internal/agent/types.go`; adding a
provider does not require changing the loop. The 0.1 implementation has no
third-party Go modules, which keeps startup and distribution simple.

See [PLAN.md](PLAN.md) for the delivered 0.1 scope and the deliberately staged
path to OAuth, a richer optional Bubble Tea UI, MCP, and plugins.

## Safety boundary

Atom's built-in file tools are constrained to its starting workspace. `bash`
runs in that workspace, has a time limit, but otherwise has the permissions of
the Atom process. Use a container, a restricted user, or a future approval
policy when working in a sensitive repository.
