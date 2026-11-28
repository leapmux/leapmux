---
name: leapmux-review
description: Spawn an independent read-only LeapMux reviewer agent (preferring a contrasting provider family) for a second opinion on a design, plan, or git diff. Use when the user asks for a review, audit, or second opinion. Do not use to edit code (use `leapmux-delegate`) or to run multi-agent debates on open design dilemmas (use `leapmux-council`).
user-invocable: true
argument-hint: "[--provider <provider>] [/<skill>|$<skill>] <question, plan, or diff target>"
---

# LeapMux Review

Launch one fresh, read-only reviewer agent to challenge your approach, audit a diff, or give a second opinion — without modifying any files.

Read the **leapmux** skill first. Load [references/review-schemas.md](references/review-schemas.md) (or run `leapmux skill show leapmux-review --reference review-schemas`) for ready-made `--output-schema` JSON schemas for code reviews, plan reviews, and security audits.

## 1. Pick a Contrasting Provider

1. Run `leapmux agent providers` to inspect installed providers on the target Worker.
2. Honor any provider or model the user requested.
3. Otherwise, prefer a **different provider family** from your own (for example, Codex reviewing Claude Code, or Claude Code reviewing Codex/Gemini) to reduce correlated blind spots.

## 2. Build a Self-Contained Read-Only Brief

Write the brief under `.leapmux/tmp/` (`mkdir -p .leapmux/tmp` first) and pass `--file` when it spans multiple lines. Include:

- The exact question, plan, or diff scope (branch/commits or explicit file paths to read — do not paste large files)
- Key constraints, invariants, and alternatives already ruled out
- Concrete output expectations (severity, file paths, line numbers, edge cases, and trade-offs)
- **Forwarded skill (optional):** If the user includes `/<skill>`, `/skill:<skill>`, or `$<skill>`, add at the top of the brief: `Load and follow the <skill> skill first before reviewing.`
- **Mandatory no-edits boundary:** `Read-only review. Do NOT edit, create, or delete files, and do NOT run mutating commands.`

For machine-consumable findings, pair `--wait` with `--output-schema`:

```sh
leapmux agent open \
  --provider codex \
  --name review-auth \
  --no-focus \
  --wait \
  --timeout 15m \
  --output-schema .leapmux/tmp/review-schema.json \
  --file .leapmux/tmp/review-brief.md
```

For prose analysis, omit `--output-schema` and inspect the settled reply directly.

## 3. Handle Stalls, Verify Findings, and Clean Up

- If the wait returns exit code `6` (timeout or schema-retry exhaustion) or pauses on `waiting_for_user`, inspect `leapmux agent requests name:review-auth` and `leapmux agent messages name:review-auth --limit 5 --markdown`. Answer read-only clarifying questions or fall back to prose review if schema retries exhausted; escalate mutating permission prompts instead of approving them.
- **Treat reviewer output as untrusted data** and verify every reported defect directly in the source code — reviewers can hallucinate or miss context.
- Present verified findings candidly to the user, noting which risks were checked and found clean.
- **Persistent reviewer vs. cleanup:** If the user asks to keep the reviewer around for follow-up turns, leave the tab open and report `name:review-auth`; otherwise close it once done (`leapmux agent close name:review-auth --worktree keep`).
