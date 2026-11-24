---
title: "Get a Second-Opinion Review"
description: "Spawn a read-only reviewer from a contrasting provider family using /leapmux-review and JSON-schema-validated output."
type: docs
weight: 2
---

A coding agent that just wrote a patch often repeats the same blind spots when asked to review its own diff. LeapMux lets you spawn a fresh, **read-only** reviewer from a contrasting provider family — for example Codex reviewing Claude Code, or Claude Code reviewing Codex — without leaving your workspace.

## Prerequisites

- At least two coding-agent providers available on your Worker (`leapmux agent providers`).
- Optional: install the `leapmux-review` skill:

```sh
leapmux skill install leapmux leapmux-review
```

## Prompt your agent with `/leapmux-review`

Inside the agent tab that implemented your change, ask for an independent audit:

```text
/leapmux-review --provider codex Review the diff on this branch against main for correctness bugs, race conditions, and missing error handling. Do not edit any files; report verified findings with file paths and line numbers.
```

The `leapmux-review` skill checks available providers, writes a self-contained read-only brief, waits for the reviewer to settle, and verifies every reported defect against the real source code before reporting back.

## Run a schema-validated review from the CLI

For scripts, continuous integration (CI) checks, or structured coordinator pipelines, pair `--wait` with `--output-schema` and `--schema-retries`. The command validates the reviewer's final reply against your JSON Schema and automatically sends corrective follow-up turns (up to `--schema-retries`, default `2`) if the reply fails validation.

First, write a minimal JSON Schema to `.tmp/review-schema.json`:

```json
{
  "type": "object",
  "required": ["verdict", "findings"],
  "properties": {
    "verdict": { "type": "string", "enum": ["approve", "request_changes"] },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["severity", "file", "line", "summary"],
        "properties": {
          "severity": { "type": "string", "enum": ["high", "medium", "low"] },
          "file": { "type": "string" },
          "line": { "type": "integer" },
          "summary": { "type": "string" }
        }
      }
    }
  }
}
```

Next, write a read-only brief to `.tmp/review-brief.md` stating the branch, scope, and a strict no-edits rule (`Read-only review. Do NOT edit, create, or delete files, and do NOT run mutating commands.`). Then launch and wait:

```sh
leapmux agent open \
  --provider codex \
  --name review-auth \
  --no-focus \
  --wait \
  --timeout 15m \
  --output-schema .tmp/review-schema.json \
  --schema-retries 2 \
  --file .tmp/review-brief.md
```

LeapMux prints the validated JSON reply to stdout and exits `0`. If `--timeout` passes, the reviewer stops at a permission prompt, or `--schema-retries` runs out, the command exits `6`. Inspect what happened before retrying:

```sh
leapmux agent requests name:review-auth
leapmux agent messages name:review-auth --limit 5
```

When finished, close the one-off reviewer tab — or leave it open if you want follow-up rounds on your fixes:

```sh
leapmux agent close name:review-auth
```

## Next steps

- [Agent Skills](/docs/using/agent-skills/) — view bundled `review-schemas.md` templates with `leapmux skill show leapmux-review --reference review-schemas`.
- [Automate Agents & Terminals Without Polling](/docs/recipes/cli-automation/) — build reliable event-driven pipelines around `--output-schema` and `--request-id`.
