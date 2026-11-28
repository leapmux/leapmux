---
title: "Automate Agents & Terminals Without Polling"
description: "Script coding agents, interactive shells, and workspace events reliably using --wait, --output-schema, --request-id, terminal wait --change, and event checkpoints."
type: docs
weight: 4
---

Polling `sleep 5` loops wastes tokens, races output, and misses state transitions. Every wait primitive in the `leapmux` command-line interface (CLI) is event-driven over the encrypted Worker stream — no polling and no fragile screen scraping.

## 1. Ask an agent and validate its reply

Use `leapmux agent send --wait` to send a prompt, wait for the turn to settle, and print the agent's reply. Add `--output-schema` to enforce a JSON contract and `--request-id` so retrying after a network drop never sends a duplicate prompt:

```sh
RESULT=$(leapmux agent send name:ci-triage \
  --file .leapmux/tmp/triage-prompt.md \
  --request-id "ci-run-${BUILD_ID}" \
  --wait \
  --timeout 10m \
  --output-schema .leapmux/tmp/triage-schema.json \
  --schema-retries 2)
```

Branch on the exit code in scripts instead of parsing stderr:

| Exit code | Meaning | Recommended action |
| --- | --- | --- |
| `0` | Command succeeded (reply settled and passed schema validation) | Consume stdout and continue |
| `2` | Usage error (invalid flag, missing argument, or rejected value) | Fix the command line; do not retry |
| `5` | Safety guard refusal or `agent_waiting` (already blocked on a permission prompt) | Inspect `leapmux agent requests` or pass the required override flag |
| `6` | Wait timed out, agent stopped at a new prompt, or `--schema-retries` exhausted | Inspect `leapmux agent requests` / `agent messages`, then retry with the same `--request-id` |
| `7` | Hub or Worker unavailable (`worker_offline` / `unavailable`) | Retry after backoff with the same `--request-id` |

## 2. Drive a terminal without `sleep`

Run a command in a live pseudo-terminal (PTY) and wait for a sentinel line in one call:

```sh
T=$(leapmux terminal open --dir ~/src/app --no-focus -q)
leapmux terminal run "$T" 'task test; echo __TESTS_DONE__' \
  --wait \
  --regex '^__TESTS_DONE__$' \
  --timeout 15m
```

When driving an interactive program (such as a REPL, debugger, or text UI), use `--change` to block until the visible screen updates:

```sh
leapmux terminal send "$T" "status" --key enter
leapmux terminal wait "$T" --change --timeout 30s
```

If the program may print output before `terminal wait` starts, capture `screen_end_offset` first and pass `--after-offset` so your script never misses fast output between commands:

```sh
OFFSET=$(leapmux terminal read "$T" --json | jq -r .data.screen_end_offset)
leapmux terminal send "$T" "status" --key enter
leapmux terminal wait "$T" --change --after-offset "$OFFSET" --timeout 30s
```

To run a one-shot command, capture its exact shell exit code, and clean up the exited tab so tabs do not pile up:

```sh
T=$(leapmux terminal open --dir ~/src/app --command 'make build; exit' --no-focus -q)
CODE=$(leapmux terminal wait "$T" --exit --timeout 15m)
leapmux terminal close "$T"
```

## 3. React to live workspace events with checkpoints

Stream structured events across a workspace or your entire account with `leapmux events --json`:

```sh
# Notify or log whenever any agent finishes a turn or requests permission
leapmux events \
  --kind agent.turn_end,agent.request \
  --checkpoint .leapmux/tmp/agent-events.cursor.json \
  --json
```

Passing `--checkpoint` saves the stream cursor to disk after each printed event (and adds `.leapmux/tmp/` to `.git/info/exclude` when writing inside a git repository). When your script restarts, the next run resumes from that position and replays retained Worker events without missing requests that arrived while the script was down.

## Prompt an agent to build a script for you

{{< copy-prompt label="Ask your coding agent to write a CLI automation script" >}}
Write a bash script using the leapmux CLI (verify flags with `leapmux commands <prefix> --json`, such as `leapmux commands agent --json` and `leapmux commands terminal --json`) that opens a terminal on Worker `name:build-box`, runs `task test`, waits for completion with `terminal run --wait`, closes the temporary terminal, and sends any failing output to agent `name:debugger` using `agent send --wait --request-id`.
{{< /copy-prompt >}}

## Next steps

- [CLI Guide](/docs/using/cli-guide/) — targets, output formats (`--json`, `--format ndjson`, `-q`), safety guards, and every exit code.
- [Control Command Reference](/docs/reference/commands/) — complete flag documentation for `agent`, `terminal`, and `events`.
