---
title: "Agent Skills"
description: "Install and auto-sync curated LeapMux Agent Skills so your coding agents can delegate parallel worktrees, run cross-provider reviews, hold councils, and hand off tasks."
type: docs
weight: 7
---

**Agent Skills** teach your coding agents — Claude Code, Codex, OpenCode, Gemini CLI, Antigravity, Cursor, and others — how to use LeapMux effectively on their own. Instead of pasting orchestration instructions into every prompt, install LeapMux's six curated `SKILL.md` playbooks so agents automatically know how to split tasks across git worktrees, request structured code reviews from a different model family, run multi-agent design councils, hand off context cleanly, and diagnose setup issues.

## Included skills

| Skill | What it lets an agent do | Raw `SKILL.md` | Companion reference |
| --- | --- | --- | --- |
| `leapmux` | Core control playbook: discover Workers and providers, target tabs safely (`self`, `parent`, `children`), wait without polling, pass multiline payloads, and guard against untrusted peer output. | [`skills/leapmux/SKILL.md`](/skills/leapmux/SKILL.md) | — |
| `leapmux-delegate` | Split an engineering task across parallel agents in isolated git worktrees, wait for completion, triage permission prompts or stalls, review diffs, and clean up. | [`skills/leapmux-delegate/SKILL.md`](/skills/leapmux-delegate/SKILL.md) | [`worker-brief-template.md`](/skills/leapmux-delegate/references/worker-brief-template.md) |
| `leapmux-review` | Launch a read-only reviewer from a contrasting provider family with structured JSON Schema findings and source verification. | [`skills/leapmux-review/SKILL.md`](/skills/leapmux-review/SKILL.md) | [`review-schemas.md`](/skills/leapmux-review/references/review-schemas.md) |
| `leapmux-council` | Run two or more high-reasoning agents in parallel for root-cause analysis or architecture planning, with optional anonymous cross-examination. | [`skills/leapmux-council/SKILL.md`](/skills/leapmux-council/SKILL.md) | [`cross-examination.md`](/skills/leapmux-council/references/cross-examination.md) |
| `leapmux-handoff` | Transfer an active task via `agent fork` (cloning provider, model, options, and working directory into a fresh session) or a fresh `agent open` (switching provider, resetting context, or moving Workers) with a structured briefing. | [`skills/leapmux-handoff/SKILL.md`](/skills/leapmux-handoff/SKILL.md) | [`handoff-template.md`](/skills/leapmux-handoff/references/handoff-template.md) |
| `leapmux-help` | Answer LeapMux setup and troubleshooting questions using live `/llms.txt` documentation and read-only diagnostics with secret redaction. | [`skills/leapmux-help/SKILL.md`](/skills/leapmux-help/SKILL.md) | [`diagnostics-checklist.md`](/skills/leapmux-help/references/diagnostics-checklist.md) |

## Installing skills

Choose whichever method fits your workflow — all three install the same bundled playbooks and companion references:

{{< tabs >}}

{{< tab name="LeapMux CLI" selected=true >}}

The `leapmux` binary bundles all six skills offline so installed playbooks always match your CLI version:

```sh
# Install all skills into the universal user directory (~/.agents/skills)
leapmux skill install --all

# Install for Claude Code (~/.claude/skills)
leapmux skill install --all --target claude

# Other built-in targets: codex, cursor, gemini, or dir:<path>
leapmux skill install --all --target gemini

# Install specific skills into the current repository (.agents/skills)
leapmux skill install leapmux leapmux-delegate --scope project

# Install and enable startup auto-sync on a remote Worker
leapmux skill install --all --worker name:build-box --auto-sync

# Inspect installed status, view a skill, or remove one
leapmux skill status
leapmux skill show leapmux-review
leapmux skill uninstall leapmux-council
```

{{< /tab >}}

{{< tab name="Preferences (Auto-Sync)" >}}

{{< screenshot src="preferences-agent-skills" alt="LeapMux Preferences Chat and Composer Agent skills panel showing Worker selector, auto-sync switch, target directories, and curated LeapMux skills" caption="Manage Agent Skills per connected Worker and target directory under Preferences → Chat & Composer → Agent skills." width="1280" height="856" >}}

Open **Preferences → Chat & Composer → Agent skills**:

1. Choose a connected Worker.
2. Select one or more target directories:
   - `~/.agents/skills` — universal default (Codex, OpenCode, Gemini CLI, Antigravity, Cursor)
   - `~/.claude/skills` — Claude Code
   - `~/.codex/skills` — legacy Codex directory
   - `~/.cursor/skills` — Cursor-specific directory
   - `~/.gemini/skills` — Gemini CLI-specific directory
3. Enable the skills you want installed on that Worker.
4. Optionally turn on **Refresh enabled skills automatically on Worker startup** so LeapMux upgrades keep unmodified skills current automatically.

{{< /tab >}}

{{< tab name="npx skills" >}}

Install directly from the LeapMux GitHub repository (`main` branch):

```sh
npx skills add leapmux/leapmux
```

{{< callout type="info" >}}
Use `leapmux skill install --all` or Preferences auto-sync when you want skills guaranteed to match your installed LeapMux binary version.
{{< /callout >}}

{{< /tab >}}

{{< /tabs >}}

## Safe upgrades and local edits

LeapMux tracks installed skill versions and file checksums so updates stay safe:

- **Unmodified managed skills** refresh cleanly on `leapmux skill install` or Worker startup auto-sync while keeping any extra files you added.
- **Locally edited or pre-existing unmanaged skills** are never overwritten or deleted unless you pass `--force`.
- **Symlinked skill paths** are refused for writes and removals to prevent modifying files outside the target directory.

## Machine-readable documentation

LeapMux also publishes [`/llms.txt`](/llms.txt), indexing every documentation page's plain Markdown companion (`/docs/.../index.md`) alongside all raw `/skills/<name>/SKILL.md` files.

## Related chapters

- [CLI Guide](/docs/using/cli-guide/) — the `leapmux` automation commands that Agent Skills orchestrate.
- [Settings & Preferences](/docs/using/settings/#chat--composer) — managing Agent Skills per Worker in Preferences.
