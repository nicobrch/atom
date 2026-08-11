# Project conventions

- Use Conventional Commits: `<type>(<scope>): <summary>`.
- Version releases with SemVer. On `0.y.z`, use a minor bump for breaking public changes, a minor bump for backward-compatible features, and a patch bump for fixes. Do not bump versions for documentation-only changes.
- Documentation is part of feature completion. For significant capabilities or integrations (for example MCPs, providers, tools, commands, or skills), update or add focused `docs/` content in same change. Cover setup, configuration, use, and material limits; keep instructions and skills documentation aligned with `internal/instructions`.
- Run `go test ./...` before committing code changes.
