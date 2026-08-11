# Atom

Atom is a tiny, single-binary Go harness for coding agents. It starts as a
single binary, keeps sessions in readable JSONL, and deliberately has a small
surface area.

## Install

Install Atom in `~/.atom`, alongside global tool directories such as
`~/.codex` and `~/.config/opencode`, then add that directory to your `PATH`:

```bash
mkdir -p ~/.atom
git clone https://github.com/nicobrch/atom.git ~/.atom/source
cd ~/.atom/source
go build -o ~/.atom/atom ./cmd/atom

echo 'export PATH="$HOME/.atom:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

Use `~/.bashrc` instead of `~/.zshrc` when you use Bash. You can now run:

```bash
atom
```

Atom starts without a selected provider or model. Use `/login` in the app (or
`atom login openai` / `atom login copilot`) and then select one of the models
available to that account before starting a conversation.
Use `atom --provider copilot --list-models` (or `openai`) to inspect the same
authenticated catalog without opening the UI.

Provider and model choices are saved in `~/.atom/config.json` and apply in
every folder. A project's `.atom/config.json` can override those defaults.

## Documentation

- [Documentation](docs/README.md)

Atom owns provider authentication and does not require Codex or Copilot CLI.
Run `atom login openai` for ChatGPT subscription OAuth or
`atom login copilot` for GitHub Copilot device OAuth. API-key login remains
available for OpenAI-compatible API access.

Active turns are cancellable with Escape or Ctrl-C. Ctrl-C exits when idle.
Long sessions compact automatically at configured context usage.

For scripts, `atom --print -p 'prompt'` emits assistant text and
`atom --json -p 'prompt'` emits JSONL status, text-delta, and tool events.

Inside Atom, run `/update` to pull the latest version and rebuild it. Restart
Atom after the update completes.
