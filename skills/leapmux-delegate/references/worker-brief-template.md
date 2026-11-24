# Delegated Worker Brief Template & Recovery Checklist

Write one file per delegated agent and pass it with `leapmux agent open --file <path>`.

## Brief Template

```markdown
## Owned Scope
- Edit only: `<exact directories or files>`
- Do NOT modify: `<shared or other-agent files>`

## Goal
<Concrete user outcome and invariants to preserve>

## Context
- Base branch: `<branch>`
- Relevant entry points to read first: `<paths>`
- Constraints / decisions already made: `<constraints>`

## Verification Required Before Reporting Done
Run these exact commands and include their results:
1. `<targeted test command>`
2. `<lint or typecheck command>`

## Output Format
Report:
1. Files changed and why
2. Verification commands run and exact pass/fail results
3. Any blockers or assumptions needing coordinator review
```

## Stall and Failure Triage Checklist

When `leapmux agent wait` returns exit code `6` (timeout or closed) or an agent stops making progress:

1. Check live status: `leapmux agent tree`
2. Check pending prompts: `leapmux agent requests <agent>`
3. Read recent output: `leapmux agent messages <agent> --limit 5`
4. Decide the next step:
   - Pending question you can answer → `leapmux agent answer <agent> ...`
   - Pending destructive/out-of-scope permission → escalate to the user
   - Agent wandered off scope → `leapmux agent send <agent> "..." --steer`
   - Provider failed to start → close the failed tab (`leapmux agent close <agent>`) and relaunch on an available provider from `leapmux agent providers`
