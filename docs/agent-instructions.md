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

### Automatic skills

Set `auto_load_skills` in `~/.atom/config.json` when a skill must apply to every
turn. Map each skill name to its activation value:

```json
{
  "auto_load_skills": {
    "caveman": "full",
    "ponytail": "full"
  }
}
```

Atom loads matching `SKILL.md` files into its startup system prompt and shows
active values in the footer. Missing or ambiguous names stop startup instead
of silently dropping required behavior. Leave a skill out of the map to keep
progressive, on-demand loading.

## Specialized agents

Atom accepts Pi-compatible user agent definitions in `~/.atom/agents/*.md`:

```md
---
name: repository-auditor
description: Read-only repository audit
model: github-copilot/gpt-5.6-luna
tools: read, grep, bash, load_skill
---

Audit only. Never modify repository state.
```

Use `/agents` to inspect discovered profiles. Atom advertises them to the main
agent through the `delegate` tool. Each delegation gets isolated conversation
history, profile model and tool restrictions, current workspace instructions,
and current skill catalog. Delegated agents cannot delegate recursively.

Only user profiles under `$ATOM_HOME/agents` load. Project `.atom/agents` stay
disabled until Atom has a project trust prompt. Profile tool names must match
Atom tools (`read`, `write`, `edit`, `bash`, `grep`, or `load_skill`); unknown
tools fail that delegation.
