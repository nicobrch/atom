# Rivet plan

## 0.1 — usable tiny harness (implemented)

1. Define small provider and tool interfaces, independent of any provider SDK.
2. Implement streamed OpenAI-compatible Chat Completions with OpenAI and
   Copilot environment-backed constructors.
3. Build a sequential, observable agent/tool loop and persist all durable
   conversation state as JSONL.
4. Provide constrained workspace tools, instruction discovery, skills, a
   terminal UI, print mode, resume, and context compaction.
5. Keep distribution to one Go binary and zero non-standard-library modules.

## 0.2 — strengthen the everyday experience

1. Add Copilot OAuth/device login with encrypted credential storage and token
   refresh; retain the direct-token path for headless use.
2. Add a richer optional Bubble Tea UI (scrollback, queued input, keymap,
   status/footer) behind a build or configuration choice.
3. Add approval policies, diffs before mutation, shell allow/deny lists, and
   per-project trust.
4. Add model discovery and OpenAI Responses support while keeping the current
   interface stable.

## 0.3 — extension ecosystem

1. Load external Go tools/providers through a versioned RPC plugin protocol
   instead of Go's platform-sensitive `plugin` package.
2. Add MCP client support and portable extension manifests.
3. Add structured tracing, replay fixtures, and provider conformance tests.

## Design constraints

- No hidden database: JSONL must remain easy to inspect, copy, and replay.
- No SDK dependency in the core: HTTP/SSE is enough for the first two
  providers and means the binary has near-zero cold-start overhead.
- A tool owns its safety boundary; the agent loop does not receive raw file or
  shell access.
