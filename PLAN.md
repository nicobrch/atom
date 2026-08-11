# Atom plan

## 0.1 — usable tiny harness (implemented)

1. Define small provider and tool interfaces, independent of any provider SDK.
2. Implement streamed OpenAI-compatible Chat Completions with OpenAI and
   Copilot environment-backed constructors.
3. Build a sequential, observable agent/tool loop and persist all durable
   conversation state as JSONL.
4. Provide constrained workspace tools, instruction discovery, skills, a
   terminal UI, print mode, resume, and context compaction.
5. Keep distribution to one Go binary with only terminal UI dependencies.

## 0.2 — strengthen the everyday experience (implemented)

1. Atom-owned OpenAI and Copilot OAuth with token refresh and private atomic
   credential storage.
2. Bubble Tea UI with scrollback, queued input, cancellation, model/effort
   selection, status/footer, and context usage.
3. OpenAI Responses, Copilot model discovery, automatic compaction, durable
   diagnostics, and crash-tolerant session replay.
4. Atomic workspace edits, symlink-safe file boundaries, ranged reads, and
   truncated-tool-call protection.

## 0.3 — reliable harness (implemented)

1. Keep one provider-neutral, observable tool loop for OpenAI and Copilot.
2. Add safe-boundary steering, prompt history, multiline input, cancellation,
   and JSONL automation output.
3. Keep CLI tools plus Agent Skills as the extension path; do not add MCP or a
   plugin protocol without a concrete workflow.

## Later, when required

1. Add optional approval policy and OS sandbox adapters.
2. Add image input, LSP diagnostics, and session branching/revert when focused
   workflows require them.

## Design constraints

- No hidden database: JSONL must remain easy to inspect, copy, and replay.
- No SDK dependency in the core: HTTP/SSE is enough for the first two
  providers and means the binary has near-zero cold-start overhead.
- A tool owns its safety boundary; the agent loop does not receive raw file or
  shell access.
