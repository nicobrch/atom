# Rivet

Rivet is a tiny, dependency-free Go harness for coding agents. It starts as a
single binary, keeps sessions in readable JSONL, and deliberately has a small
surface area:

- OpenAI and GitHub Copilot adapters behind one streaming provider interface
- An agent loop with `read`, `write`, `edit`, `bash`, and `grep`
- `AGENTS.md` discovery and small, file-based skills
- A quick ANSI terminal UI plus print mode for scripts
- Explicit and automatic context compaction

## Install and run

```bash
go install github.com/nicobrch/rivet/cmd/rivet@latest
export OPENAI_API_KEY=...
rivet
```

When developing from a checkout, use `go install ./cmd/rivet` instead.

OpenAI defaults to `gpt-5.4`; override it with `--model`.

```bash
rivet --model gpt-5.4
rivet -p "explain this repository"
```

### ChatGPT subscription / Codex sign-in

Rivet also reuses the credential created by the supported Codex **Sign in with
ChatGPT** flow. It reads `~/.codex/auth.json` only in memory; it never copies
the credential into the repository or a Rivet session. Sign in once, then run
Rivet normally without `OPENAI_API_KEY`:

```bash
rivet auth openai
rivet --model gpt-5.4
```

This delegates browser authentication to the installed Codex CLI. If you do
not have it installed, install/login with Codex first, then use an API key.

For GitHub Copilot, provide a Copilot bearer token (not a general GitHub PAT)
and, when necessary, its API endpoint:

```bash
export COPILOT_TOKEN=...
# optional; this is the normal individual endpoint
export COPILOT_BASE_URL=https://api.individual.githubcopilot.com
rivet --provider copilot --model gpt-5.4
```

`COPILOT_GITHUB_TOKEN` is accepted as an alias for `COPILOT_TOKEN`. OAuth/device
login is intentionally outside the 0.1 core; refresh or obtain a Copilot token
with the GitHub tooling you already use.

## Interaction

Run `rivet` without `-p` to enter the terminal UI. Commands are:

- `/help` — show commands
- `/compact` — replace conversation history with a model-generated handoff
- `/clear` — begin a fresh conversation in the current session file
- `/session` — show the JSONL session path
- `/skills` — list discovered skills
- `/exit` — quit

Every user, assistant, tool-call, tool-result, and compaction event is appended
to `.rivet/sessions/<timestamp>.jsonl`. Resume one with `--session PATH`.

## Project customization

Rivet loads every `AGENTS.md` from the workspace root down to the working
directory. Put a skill in either `.rivet/skills/<name>/SKILL.md` or
`~/.rivet/skills/<name>/SKILL.md`; the agent sees each skill's name and
description. A user can explicitly load one with `/skill <name>`.

The optional `.rivet/config.json` can provide defaults:

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

Rivet's built-in file tools are constrained to its starting workspace. `bash`
runs in that workspace, has a time limit, but otherwise has the permissions of
the Rivet process. Use a container, a restricted user, or a future approval
policy when working in a sensitive repository.
