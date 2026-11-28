---
name: leapmux-handoff
description: Transfer an in-progress task cleanly to another LeapMux agent — via a native conversation-history fork, a fresh cloned-config fork (`--fresh`), a fresh worktree agent, or another Worker — with complete decisions, file paths, and acceptance criteria. Use for one-way handoffs or switching providers/machines. Do not use for supervised parallel fan-out (use `leapmux-delegate`) or read-only second opinions (use `leapmux-review`).
user-invocable: true
argument-hint: "[--provider <provider>] [--worker <worker>] [--worktree <branch>] [instructions]"
---

# LeapMux Handoff

Transfer an active task to another agent cleanly so the receiving agent can continue immediately without repeating exploration or losing user constraints.

Read the **leapmux** skill first. Load [references/handoff-template.md](references/handoff-template.md) (or run `leapmux skill show leapmux-handoff --reference handoff-template`) for the copy-ready 7-section briefing template.

## 1. Choose the Right Handoff Mode

- **Same provider with conversation history** (branching an approach or moving onto a new git worktree while keeping context): use `leapmux agent fork self`. When the provider supports native session forking, it clones the provider session history and the Worker transcript so the forked agent keeps prior context and prompt cache.
- **Same provider, model, and options in a clean empty session** (resetting noisy context without changing provider or settings): use `leapmux agent fork self --fresh` with a structured briefing file. It clones provider, model, options, and working directory without copying conversation history.
- **Switch provider or move to another Worker** (handing off from Claude Code to Codex, or moving heavy builds to a remote Worker): use `leapmux agent open` with a structured briefing file.

Inspect `leapmux worker list`, `leapmux agent providers`, and `leapmux git status` before launching.

## 2. Prepare an Accurate Briefing

For clean-session handoffs (`agent fork --fresh` or `agent open`), write the briefing under `.leapmux/tmp/` (for example `.leapmux/tmp/handoff-brief.md`) using the 7-section template in [references/handoff-template.md](references/handoff-template.md):

- **Preserve exact task scope.** Never turn an *investigate-only* or *plan-only* task into an *implement* task (or vice versa).
- **Never fabricate verification.** Do not claim a build, lint, or test passed unless that command ran and succeeded in the current worktree; list unverified checks explicitly under Remaining Work.
- **Give the first safe command** the receiving agent should run to orient or verify state immediately.

## 3. Launch the Receiving Agent

**Option A — Fork the current agent (keeps conversation history and prompt cache by default; add `--fresh` for a clean empty session):**

```sh
# Native conversation-history fork onto a new worktree:
leapmux agent fork self \
  --worktree feat/next-phase \
  --name handoff-impl \
  --message "Continue with phase 2 using the settled design."

# Clean empty session with cloned provider, model, and options:
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

## 4. Confirm and Stop (Fire-and-Forget)

- Do **not** block waiting or polling on the receiving agent unless the user explicitly asked you to monitor it.
- Report the receiving agent's ID, `--name` slug, Worker, and branch/worktree path to the user.
- Reveal the new tab (`leapmux agent reveal <id>`) only if the user asked to switch straight to it. Never close `self` unless explicitly instructed.
