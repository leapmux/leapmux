---
name: leapmux-handoff
description: Transfer an in-progress task cleanly to another LeapMux agent — via a fresh cloned-config fork (`--fresh`), a fresh worktree agent, another Worker, or a native history fork of an idle agent — with complete decisions, file paths, and acceptance criteria. Use for one-way handoffs or switching providers/machines. Do not use for supervised parallel fan-out (use `leapmux-delegate`) or read-only second opinions (use `leapmux-review`).
user-invocable: true
argument-hint: "[--provider <provider>] [--worker <worker>] [--worktree <branch>] [instructions]"
---

# LeapMux Handoff

Transfer an active task to another agent cleanly so the receiving agent can continue immediately without repeating exploration or losing user constraints.

Read the **leapmux** skill first. Load [references/handoff-template.md](references/handoff-template.md) (or run `leapmux skill show leapmux-handoff --reference handoff-template`) for the copy-ready 7-section briefing template.

## 1. Choose the Right Handoff Mode

- **Self-handoff on the same provider, model, and options (clean session)**: use `leapmux agent fork self --fresh` with a structured briefing file. It clones provider, model, options, and working directory into a clean empty session. An active agent running `/leapmux-handoff` is mid-turn (`WORKING`), so native history forking of `self` is rejected while the turn is running.
- **Switch provider or move to another Worker** (handing off from Claude Code to Codex, or moving heavy builds to a remote Worker): use `leapmux agent open` with a structured briefing file.
- **Fork an idle agent with native history** (branching an approach or moving onto a new git worktree while keeping prior context and prompt cache): once the source agent's turn finishes, run `leapmux agent fork <agent-id>` from another tab, terminal, or external CLI.

Inspect `leapmux worker list`, `leapmux agent providers`, and `leapmux git status` before launching.

## 2. Prepare an Accurate Briefing

For clean-session handoffs (`agent fork self --fresh` or `agent open`), write the briefing under `.leapmux/tmp/` (for example `.leapmux/tmp/handoff-brief.md`) using the 7-section template in [references/handoff-template.md](references/handoff-template.md):

- **Preserve exact task scope.** Never turn an *investigate-only* or *plan-only* task into an *implement* task (or vice versa).
- **Never fabricate verification.** Do not claim a build, lint, or test passed unless that command ran and succeeded in the current worktree; list unverified checks explicitly under Remaining Work.
- **Give the first safe command** the receiving agent should run to orient or verify state immediately.

## 3. Launch the Receiving Agent

**Option A — Self-handoff with cloned provider, model, and options (clean empty session):**

```sh
leapmux agent fork self \
  --fresh \
  --worktree feat/next-phase \
  --name handoff-impl \
  --file .leapmux/tmp/handoff-brief.md
```

**Option B — Clean-context handoff (optionally switching provider or Worker):**

Pass `--dir` with the repository path on the target Worker so the CLI does not forward the local tab's working directory to another machine:

```sh
leapmux agent open \
  --worker name:build-box \
  --dir /path/to/repo \
  --provider codex \
  --worktree feat/next-phase \
  --name handoff-build \
  --file .leapmux/tmp/handoff-brief.md
```

**Option C — Native history fork of an idle agent (from another tab, terminal, or external CLI after the source turn finishes):**

```sh
leapmux agent fork <idle-agent-id> \
  --worktree feat/next-phase \
  --name handoff-impl \
  --message "Continue with phase 2 using the settled design."
```

## 4. Confirm and Stop (Fire-and-Forget)

- Do **not** block waiting or polling on the receiving agent unless the user explicitly asked you to monitor it.
- Report the receiving agent's ID, `--name` slug, Worker, and branch/worktree path to the user.
- Reveal the new tab (`leapmux agent reveal <id>`) only if the user asked to switch straight to it. Never close `self` unless explicitly instructed.
