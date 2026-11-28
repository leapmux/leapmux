# Structured Handoff Briefing Template

Write this template under `.leapmux/tmp/` (for example `.leapmux/tmp/handoff-brief.md`) and pass it to `leapmux agent open --file .leapmux/tmp/handoff-brief.md` or `leapmux agent fork self --fresh --file .leapmux/tmp/handoff-brief.md`.

```markdown
## 1. Goal & Scope
- Objective: <exact user goal>
- Mode: <implement | investigate-only | plan-only | review-only>
- Acceptance criteria:
  - <criterion 1>
  - <criterion 2>

## 2. Current State
- Repository / worktree path: <path>
- Branch & HEAD commit: <branch> (<short sha>)
- Uncommitted changes: <clean | list of modified/untracked files>
- Checks actually run and passed in this worktree: <exact commands + results, or "None yet">

## 3. Key Files & Symbols
- `<path/to/file.go>` — <why it matters>
- `<path/to/test.go>` — <what it covers>

## 4. Decisions & Constraints
- <user preference or settled design decision>
- <invariant that must not break>

## 5. What Was Tried / Ruled Out
- <approach tried and exact reason it failed or was rejected>

## 6. Remaining Work
First safe command to run:
`<read-only status/diff/test command>`

Ordered next steps:
1. <step 1>
2. <step 2>

## 7. Verification Commands
Run before reporting completion:
- `<targeted tests>`
- `<lint / typecheck>`
```
