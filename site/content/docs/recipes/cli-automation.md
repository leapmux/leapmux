---
title: "Automate Agents & Terminals Without Polling"
description: "Script coding agents, interactive shells, and workspace events reliably using --wait, --output-schema, --request-id, terminal wait --change, and event checkpoints."
type: docs
weight: 4
---

Polling `sleep 5` loops burn tokens, race output, and miss state transitions. Every wait primitive in the `leapmux` command-line interface (CLI) is event-driven over the encrypted Worker stream — no polling and no fragile screen scraping.

## 1. Ask an agent and validate its reply

Use `leapmux agent send --wait` to send a prompt, wait for the turn to settle, and print the agent's reply. Add `--output-schema` to enforce a JSON contract and `--request-id` so retrying after a network drop never sends a duplicate prompt:

```sh
RESULT=$(leapmux agent send name:ci-triage \
  --file .tmp/triage-prompt.md \
  --request-id "ci-run-${BUILD_ID}" \
  --wait \
  --timeout 10m \
  --output-schema .tmp/triage-schema.json \
  --schema-retries 2)
```

- Exit code `0`: the agent settled and its reply passed schema validation.
- Exit code `5`: the agent is already blocked on a permission prompt (`agent_waiting`).
- Exit code `6`: `--timeout` passed, the agent stopped at a new prompt, or schema retries exhausted. Inspect `leapmux agent requests name:ci-triage` before retrying with the same `--request-id`.

## 2. Drive a terminal without `sleep`

Run a command in a live pseudo-terminal (PTY) and wait for a sentinel line in one call:

```sh
T=$(leapmux terminal open --dir ~/src/app --no-focus -q)
leapmux terminal run "$T" 'task test; echo __TESTS_DONE__' \
  --wait \
  --regex '^__TESTS_DONE__$' \
  --timeout 15m
```

When you drive an interactive program (such as a REPL, debugger, or text UI) and need to wait for the next screen update, chain `screen_end_offset` with `--change --after-offset` so you never miss output produced between commands:

```sh
# Capture the current screen offset before sending input
OFFSET=$(leapmux terminal read "$T" --json | jq -r .data.screen_end_offset)

# Send keystrokes and block until the visible screen actually changes
leapmux terminal send "$T" "status" --key enter
leapmux terminal wait "$T" --change --after-offset "$OFFSET" --timeout 30s
```

To run a one-shot command and capture its exact exit code instead, pass `--wait --exit`:

```sh
CODE=$(leapmux terminal open --command 'make build; exit' --wait --exit)
```

## 3. React to live workspace events with checkpoints

Stream structured events across a workspace or your entire account with `leapmux events --json`:

```sh
# Notify or log whenever any agent finishes a turn or requests permission
leapmux events \
  --kind agent.turn_end,agent.request \
  --checkpoint .tmp/agent-events.cursor.json \
  --json
```

Passing `--checkpoint` stores the stream cursor on disk after each printed event. If your script restarts, the next run resumes from the saved position and replays retained Worker events without missing requests that arrived while the script was down.

## Prompt an agent to build a script for you

{{< copy-prompt label="Ask your coding agent to write a CLI automation script" >}}
Write a bash script using the leapmux CLI (verify flags with `leapmux commands --json`) that opens a terminal on worker name:build-box, runs `task test`, waits for completion with `terminal run --wait`, and sends any failing output to agent `name:debugger` using `agent send --wait --request-id`.
{{< /copy-prompt >}}

## Next steps

- [Control CLI](/docs/using/control-cli/) — targets, output formats (`--json`, `--format ndjson`, `-q`), safety guards, and exit codes.
- [Control Command Reference](/docs/reference/commands/) — complete flag documentation for `agent`, `terminal`, and `events`.
