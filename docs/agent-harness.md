# Coding-agent behavior and limits

Atom runs every provider through one provider-neutral loop. Assistant tool
calls and results are streamed to the UI, written to session JSONL, and replayed
on resume. Providers cannot execute hidden tools.

Copilot transport is selected per authenticated model capability. This matters
for current GPT reasoning models, which require Responses when combining
reasoning effort with function tools; Claude, Gemini, and legacy models can
continue through Chat Completions.

## Everyday behavior

- Escape or Ctrl-C cancels an active provider request or shell command. Ctrl-C
  exits when idle.
- Shift-Enter inserts a newline. While idle, Alt-Enter also inserts a newline;
  during a turn it queues a follow-up delivered only after all agent work.
- Up/Down recall submitted prompts; Page Up/Down scroll transcript.
- Prompts submitted during a turn queue as steering and enter at next safe
  assistant/tool boundary. Provider errors or cancellation preserve them as
  follow-up prompts.
- Sessions compact automatically at `auto_compact_at` context usage (default
  `0.8`) and can be compacted manually with `/compact`.
- A model response cut off while producing tool arguments never executes that
  tool call.
- Failed shell commands return both captured output and exit error to the
  model.
- `read` accepts one-based `offset` and `limit` line ranges for large files.
- Pi-compatible user agent profiles can handle bounded tasks through isolated,
  non-recursive `delegate` calls.
- File writes and edits are atomic and preserve existing permission bits.
- A crash-truncated final JSONL record is ignored on resume; malformed records
  in the middle still fail closed.
- `/new` starts a clean session without erasing the current one; `/clone`
  duplicates the active history into a new session before experimentation.
- `--json -p 'prompt'` exposes status, streamed text, complete tool events,
  failures, token totals, and session path as JSONL for scripts; it uses the
  same durable loop as the terminal UI.

JSON event types are `status`, `text_delta`, `tool_start`, `tool_end`, `error`,
and final `result`. Tool events include the call ID, name, arguments, output,
and optional error. Consumers should ignore unknown fields and event types so
Atom can add metadata without breaking them.

## Workspace boundary

`read`, `write`, and `edit` reject lexical and symlink escapes outside startup
working directory. `bash` starts in that directory but is intentionally a real
user shell: commands may access anything the Atom process can access and may
use network credentials available to that process. Start Atom only in trusted
projects and review project `AGENTS.md` instructions.

Atom does not yet provide OS sandboxing or interactive per-command approval.
Those require platform-specific enforcement; pretending path checks constrain
a shell would provide false security.

## Remaining parity work

Like pi, Atom intentionally uses CLI tools plus Agent Skills instead of a
built-in MCP client. Wider surfaces still lacking include image inputs, LSP
diagnostics, point-in-time session tree/revert, extension hooks, and
configurable tool approval policies. They should enter Atom only with focused
workflows and without bypassing the single durable tool loop.
