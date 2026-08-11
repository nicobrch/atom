# Atom

Atom is a tiny, dependency-free Go harness for coding agents. It starts as a
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

## Documentation

- [Documentation](docs/README.md)

GitHub Copilot uses GitHub's supported Copilot CLI transport, bundled into Atom
release builds. Run `atom login copilot`; Atom opens Copilot's login flow and
the CLI owns GitHub authentication and model access.

Inside Atom, run `/update` to pull the latest version and rebuild it. Restart
Atom after the update completes.
