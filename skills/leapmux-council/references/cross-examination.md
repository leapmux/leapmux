# Anonymous Cross-Examination Protocol (Round 2)

Use this protocol only when Round 1 council members disagree on root cause or architecture.

## Rules

1. **Keep provider and model names hidden** from council members. Label Round 1 outputs `Member A`, `Member B`, etc. (Reveal provider names only in your final summary to the user.)
2. **Prevent bandwagoning.** Instruct each member that changing position requires citing concrete code evidence or a flaw they missed — never agree merely because another member sounded confident.
3. **Cap at one cross-examination round** unless the user asks for another round.

## Round 2 Prompt Template

Write `.tmp/council-r2-a.md` (for `C1`, showing `Member B`'s summary) and `.tmp/council-r2-b.md` (for `C2`, showing `Member A`'s summary):

```markdown
Below is an independent analysis from another council member (Member B):

<concise summary of Member B's thesis, key code citations, and proposed direction>

Respond concisely:
1. Which specific claims from Member B hold up against the code?
2. Which claims are wrong, incomplete, or depend on an unverified assumption? Cite exact file paths and symbols.
3. Does your recommendation change? If yes, state the exact new evidence that changed your mind. If no, state the decisive trade-off.

Analysis only. Do NOT edit, create, or delete files, and do NOT run mutating commands.
```

Send both in parallel and wait once:

```sh
leapmux agent send "$C1" --file .tmp/council-r2-a.md
leapmux agent send "$C2" --file .tmp/council-r2-b.md
leapmux agent wait "$C1" "$C2" --timeout 15m
leapmux agent messages "$C1" --limit 2
leapmux agent messages "$C2" --limit 2
```

## Synthesis Checklist

- Highlight verified consensus vs. unresolved trade-offs that require a user call.
- Flag any position flip that lacked new code evidence so the user sees whether convergence was earned.
- Close council tabs (`leapmux agent close "$C1" "$C2"`) once the user confirms no follow-up checkpoint is needed.
