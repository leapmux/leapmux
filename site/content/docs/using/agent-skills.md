---
title: "Agent Skills"
description: "Install and auto-sync curated LeapMux Agent Skills (SKILL.md) with companion references for parallel worktree delegation, cross-provider reviews, multi-agent councils, clean task handoffs, and live troubleshooting."
type: docs
weight: 9
---

LeapMux ships six curated **Agent Skills** (`SKILL.md`), each with Codex `agents/openai.yaml` metadata and optional progressive-disclosure `references/*.md` templates, teaching coding agents — Claude Code, Codex, OpenCode, Gemini CLI, Antigravity, Cursor, and others — how to orchestrate multi-agent workflows and troubleshoot LeapMux natively.

While `leapmux guide` documents command mechanics and flags, Agent Skills provide reusable, opinionated playbooks with explicit trigger boundaries, untrusted-peer-output safeguards, multiline `--file` briefing patterns, and stall recovery checklists.

## Included skills

| Skill | Purpose | Raw `SKILL.md` | Companion Reference |
|---|---|---|---|
| `leapmux` | Foundational control reference: preflight checks, Worker/provider discovery, targeting (`self`, `parent`, `children`, `name:<slug>`), event-driven waits, multiline `--file` payloads, schema-validated outputs, safe retries, and untrusted-peer safety rules. | [`skills/leapmux/SKILL.md`](/skills/leapmux/SKILL.md) | — |
| `leapmux-delegate` | Split an engineering task across parallel agents in isolated git worktrees (`--worktree`, `--name`, `--no-focus`, `--file`), wait without polling (`agent wait --any`), triage permission prompts and stalls, inspect diffs, and clean up. | [`skills/leapmux-delegate/SKILL.md`](/skills/leapmux-delegate/SKILL.md) | [`worker-brief-template.md`](/skills/leapmux-delegate/references/worker-brief-template.md) |
| `leapmux-review` | Spawn a read-only reviewer from a contrasting provider family with optional forwarded skills (`/<skill>`, `$<skill>`), `--output-schema` structured JSON findings, source-code verification, and persistent-reviewer support. | [`skills/leapmux-review/SKILL.md`](/skills/leapmux-review/SKILL.md) | [`review-schemas.md`](/skills/leapmux-review/references/review-schemas.md) |
| `leapmux-council` | Run two or more contrasting high-reasoning agents in parallel for root-cause analysis or architecture planning, run an optional anonymous Round 2 cross-examination with anti-bandwagon rules, and verify disputed claims against the code. | [`skills/leapmux-council/SKILL.md`](/skills/leapmux-council/SKILL.md) | [`cross-examination.md`](/skills/leapmux-council/references/cross-examination.md) |
| `leapmux-handoff` | Transfer an active task via `agent fork` (keep conversation history) or a clean-context `agent open` (switch provider, reset context, or move to another Worker) using a structured 7-section briefing template. | [`skills/leapmux-handoff/SKILL.md`](/skills/leapmux-handoff/SKILL.md) | [`handoff-template.md`](/skills/leapmux-handoff/references/handoff-template.md) |
| `leapmux-help` | Answer LeapMux setup, configuration, and troubleshooting questions by consulting live `/llms.txt` Markdown docs and running topology-aware read-only diagnostics with secret redaction. | [`skills/leapmux-help/SKILL.md`](/skills/leapmux-help/SKILL.md) | [`diagnostics-checklist.md`](/skills/leapmux-help/references/diagnostics-checklist.md) |

## Installing skills

You can install LeapMux skills in three ways — all three manage the same `SKILL.md`, `agents/openai.yaml`, and `references/*.md` files:

### 1. From Preferences (per Worker, with startup auto-sync)

Open **Preferences → Chat & Composer → Agent skills**:

1. Pick a connected Worker (auto-selected when one Worker is online).
2. Choose one or more install targets:
   - `~/.agents/skills` (universal default — discovered automatically by Codex, OpenCode, Gemini CLI, Antigravity, and Cursor)
   - `~/.claude/skills` (Claude Code)
   - `~/.codex/skills` (legacy Codex path)
   - `~/.cursor/skills` (Cursor-specific path)
   - `~/.gemini/skills` (Gemini CLI-specific path)
3. Toggle the skills you want installed on that Worker.
4. Optionally enable **Refresh enabled skills automatically on Worker startup** so upgrading LeapMux refreshes unmodified skills automatically on the next Worker start.

All reads and writes travel over the end-to-end-encrypted Worker channel. Locally edited, unmanaged, or symlinked skill directories are never overwritten automatically.

### 2. From the `leapmux` CLI (offline, version-matched, or remote Worker)

The `leapmux` binary embeds all six skills and their companion references offline, so installed playbooks always match your CLI version with no Node.js runtime or network access required:

```sh
# List bundled skills and local install status
leapmux skill list

# Print any skill directly without installing
leapmux skill show leapmux-review

# List or print a skill's companion references on demand
leapmux skill show leapmux-review --references
leapmux skill show leapmux-review --reference review-schemas

# Install all skills into ~/.agents/skills (universal user scope)
leapmux skill install --all

# Install specific skills into the current repository (.agents/skills)
leapmux skill install leapmux leapmux-delegate --scope project

# Install into another agent directory (claude, codex, cursor, gemini, or dir:<path>)
leapmux skill install --all --target claude

# Enable and auto-sync all skills on a remote Worker over E2EE
leapmux skill install --all --worker name:build-box --auto-sync

# Check installed vs. bundled status and local drift
leapmux skill status

# Remove LeapMux-managed skills
leapmux skill uninstall leapmux-council
```

### 3. With `npx skills` (cross-agent Skills CLI)

The repository root exposes the standard `skills/<name>/SKILL.md` layout, so you can also install directly from GitHub (`main` branch):

```sh
npx skills add leapmux/leapmux
```

> **Note:** `npx skills add leapmux/leapmux` installs from the repository's `main` branch. Use `leapmux skill install --all` or Preferences auto-sync when you want skills guaranteed to match your installed LeapMux binary version.

## Safe upgrades, symlink protection, and local edit preservation

When `leapmux skill install` or Worker auto-sync writes a skill, it stores a `.leapmux-skill.json` marker containing the installed version and per-file SHA-256 hashes for every bundled file (`SKILL.md`, `agents/openai.yaml`, and `references/*.md`):

- **Unmodified managed skills** (`outdated`) refresh cleanly on `leapmux skill install` or Worker startup auto-sync, pruning obsolete unmodified managed files while keeping any extra files you added.
- **Locally edited skills** (`modified`) and pre-existing directories without a LeapMux marker (`unmanaged`) are never overwritten or removed unless you explicitly pass `--force`.
- **Symlinked skill directories or files** are rejected for both writes and removals to prevent path traversal outside the target skills root.

## Machine-readable documentation for agents

In addition to Agent Skills, LeapMux publishes [`/llms.txt`](/llms.txt) linking every documentation chapter to its plain Markdown companion (`/docs/.../index.md`) and every raw `/skills/<name>/SKILL.md` and companion reference file.
