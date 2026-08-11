# Practical harness parity

Atom targets pi's durable coding loop, not identical feature count. This matrix
records current evidence and remaining breadth so parity claims stay testable.

| Capability | Atom evidence | State |
| --- | --- | --- |
| Core coding tools | `read`, `write`, `edit`, and `bash`, plus `grep` and `load_skill`; tool tests cover boundaries, atomic writes, bounded output, and cancellation | Complete |
| Observable agent loop | One provider-neutral loop records assistant calls and tool results before continuing; HTTP integration tests cover Chat Completions and Responses | Complete |
| GitHub Copilot auth | Copilot Chat device client, short-lived token exchange/refresh, authenticated catalog, per-model transport selection | Complete |
| OpenAI auth | ChatGPT device OAuth and refresh, account extraction, Responses headers and encrypted reasoning replay; API-key mode remains available | Complete; authenticated model listing, tool loop, and resumed replay verified |
| Interactive work | Streaming UI, multiline editor, prompt history, steering and follow-up queues, cancellation, model/effort picker, context status | Complete |
| Durable sessions | Private JSONL, resume, new, clone, clear events, automatic/manual fail-closed compaction, truncated-tail recovery | Complete |
| Automation | Plain one-shot output, JSONL event mode, explicit session reuse, authenticated model listing | Complete |
| Instructions and customization | Hierarchical `AGENTS.md`, Agent Skills discovery and progressive loading, arbitrary CLI workflows through `bash` | Complete for pi's CLI-plus-skills path |
| Built-in MCP | pi intentionally omits it; Atom does too | Not a parity gap |
| Permission popups | pi intentionally omits them; Atom documents real process authority and file-tool boundaries | Not a parity gap |
| Session tree/revert | Atom supports resume/new/clone, not point-in-time navigation inside one session | Optional breadth |
| Images and clipboard images | Text-only provider-neutral messages | Optional breadth |
| Extension runtime, prompt templates, themes | Skills and CLI tools only | Optional breadth |
| RPC/embedded SDK | JSON one-shot mode plus resumable sessions, no long-lived RPC server or Go SDK | Optional breadth |
| Provider breadth | OpenAI API/ChatGPT and GitHub Copilot, as explicitly targeted | Deliberately narrower than pi/opencode |

## Completion gates

Before release, run:

```bash
git diff --check
go test ./...
go test -race ./...
go vet ./...
make build
./atom -version
```

Provider changes additionally require an authenticated model listing, a live
tool-call turn, and a resumed follow-up turn. Tests alone do not replace those
smokes.
