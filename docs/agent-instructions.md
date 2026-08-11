# Agent instructions and skills

Atom loads agent guidance and skills from local filesystem conventions. It
includes discovered guidance in every agent system prompt and exposes skills
for progressive loading with `/skill` and the `load_skill` tool.

## `AGENTS.md`

Atom loads one global file from `$ATOM_HOME` (normally `~/.atom`), preferring
`AGENTS.override.md` over `AGENTS.md`. It then walks from repository root to
current working directory and loads at most one non-empty instruction file per
directory, with same override precedence. Later, more-specific files take
precedence.

Total project instruction content is capped by `project_doc_max_bytes` in
`.atom/config.json` (default: 32 KiB). Use `AGENTS.md` for project-wide rules;
use `AGENTS.override.md` for local rules that should replace it.

## Skills

Place each skill in a named directory with a `SKILL.md` file:

```text
.agents/skills/<skill-name>/SKILL.md
```

Atom searches nearest repository directories first, then user skills in
`~/.agents/skills`, then admin skills in `/etc/atom/skills`. Duplicate names
remain visible; select an exact path when a name is ambiguous.

`SKILL.md` needs YAML front matter with `name` and `description`:

```md
---
name: review
description: Review a change for regressions.
---

# Instructions

Read relevant files, then report findings.
```

Use `/skills` to list discovered skills and `/skill` to load one in the UI.
Agents should load a relevant skill before applying its instructions.
