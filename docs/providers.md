# Providers and authentication

Atom supports OpenAI API keys, ChatGPT subscriptions, and GitHub Copilot
subscriptions without another coding-agent executable.

## Login

Use the interactive `/login` picker or run:

```bash
atom login openai subscription
atom login openai api
atom login copilot subscription
atom --provider copilot --list-models
```

Subscription login prints a device URL and one-time code. API keys are read
without terminal echo. Credentials live in `$ATOM_HOME/auth.json` (normally
`~/.atom/auth.json`) with mode `0600`; writes replace the file atomically.

Environment alternatives:

- `OPENAI_API_KEY` and optional `OPENAI_BASE_URL` for OpenAI-compatible APIs.
- `COPILOT_GITHUB_TOKEN` for a GitHub token authorized for Copilot.

Stored credentials take precedence over environment variables. Logging in
with an OpenAI API key clears stored ChatGPT OAuth, and ChatGPT login clears a
stored API key, so the selected method takes effect immediately.

## Refresh and model discovery

ChatGPT OAuth access tokens refresh before expiry. GitHub tokens are exchanged
for short-lived, account-specific Copilot API tokens and refreshed before
expiry. Copilot models and their supported transports come from the account
`/models` endpoint. Atom uses Responses for models that advertise it and Chat
Completions for the rest, matching pi's per-model transport behavior. ChatGPT
uses a release-bundled model catalog, following pi's provider pattern; update
Atom to receive newly supported ChatGPT models.

Credentials created by Atom 0.2.0's old GitHub OAuth client cannot be exchanged
directly for Copilot API access. Run `atom login copilot` once after upgrading.

## Material limits

- GitHub Enterprise device login is not yet exposed; current login targets
  `github.com`.
- ChatGPT login uses device-code flow. Browser callback login is unnecessary
  for local and headless terminals and is not implemented.
- Provider HTTP errors are recorded as metadata-only diagnostics. Tokens,
  prompts, tool arguments, and tool output are excluded from diagnostic logs.
