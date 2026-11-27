# Driving LeapMux with `leapmux`

This guide is for an agent or operator driving LeapMux from the command line:
starting agents and terminals, sending work, answering prompts, reading output,
and inspecting workspaces. Every command prints its own help with `--help`, and
`leapmux commands [prefix...] --compact --json` prints filtered machine-readable
schemas without dumping the full tree.

`leapmux` (and its short alias `lmx`) on your `PATH` is the CLI of the Worker
that started you, so it matches the Worker's version. A shell profile can move
another `leapmux` ahead of it. That `leapmux` hands each control command to the
Worker's CLI, `$LEAPMUX_CONTROL_CLI`, so the command still matches the Worker.
`"$LEAPMUX_CONTROL_CLI"` always runs the Worker's own CLI directly.

## Rules that keep you safe <!-- topic: safety -->

- A command that changes or removes an object needs an explicit target. Use
  `self` to select your own tab.
- `tab close`, `agent close`, `terminal close`, `tile close`, `tile remove-grid`,
  `window close`, `workspace delete`, and `terminal restart --replace` refuse to
  remove or replace the tab that runs the command. `--allow-self` overrides that.
- `tab close` requires `--allow-busy` when work is in progress or the Worker
  cannot check the tab's activity. The same rule — plus dirty git worktree and
  unpushed commit checks — applies when a multi-tab `tab close`, `tile close`,
  `tile remove-grid`, `window close`, or `workspace delete` closes its tabs.
- Write a text that starts with `-` after `--`, so that a misspelled flag never
  reaches an agent as text: `leapmux agent send 87a19d5f -- "-v"`.
- Use full command names or documented canonical aliases in scripts. Stable
  aliases (`lmx`, `ag`, `term`, `ws`, `wt`, `wk`, `win`, `ev`, `notif`, `ctx`,
  `tun`, `pref`/`prefs`, `q`, `req`, `msg`, `skills`, `fwd`, `nc`, `grep`,
  `find`, `overview`, `ps`, `ls`, `rm`, `mv`, `cat`) are guaranteed; ad-hoc
  prefix abbreviations such as `leapmux ag s` can become ambiguous in a later
  release.
- `agent send`, `agent open`, `agent fork`, and `terminal run` automatically
  generate a `--request-id` ID when omitted. On transport or wait errors,
  inspect the reported `request_id` and disposition (`not_sent`, `in_progress`,
  or `outcome_unknown`), retry safely with `--request-id <key>`, and (for
  `agent send` or the post-open message of `agent open`/`agent fork`) query
  `leapmux agent request-status <agent> <request-id>`.
- Pass `--output-schema <file|json>` (with `--wait` and optional
  `--schema-retries <n>`) on `agent send`, `agent open`, or `agent fork` when
  you need a JSON Schema-validated reply with automatic corrective retries.
- Run `leapmux skill list` or `leapmux skill show <name>` (with optional
  `--references` / `--reference <ref>`) to read curated multi-agent playbooks
  and troubleshooting guides (`leapmux`, `leapmux-delegate`, `leapmux-review`,
  `leapmux-council`, `leapmux-handoff`, `leapmux-help`).

## How a command finds its object <!-- topic: targets -->

An argument such as `<agent>`, `<tab>`, `<workspace>`, or `<worker>` takes:

- A full ID, or a unique ID prefix of at least 4 characters (tables print the
  first 8 characters).
- `id:<id>` for an exact ID (any length, including a short ID or `self`) or a
  unique ID prefix of at least 4 characters, without matching titles or names.
- `title:<exact title>` for a tab or workspace, or `name:<name>` for a worker or
  an agent slug.
- A bare case-insensitive title, unique agent `--name` slug, or worker name.
  Ambiguous matches fail closed with `ambiguous_target` and list candidates.
  Pass `--workspace <ws>` on tab/agent/terminal commands to disambiguate.
- `self` for the tab, tile, workspace, or worker running the command.
- `parent` (in an agent tab, on `agent` or `tab` commands) for the parent agent;
  `children` (on multi-agent commands: `agent wait`, `agent requests`,
  `agent interrupt`, and `agent close`) for all direct child subagents.

A command that only reads defaults to `self` inside a tab: `leapmux agent get`
shows your own agent. Outside a tab, `leapmux context use --hub ... --worker ...
--workspace ... --provider ...` sets persistent defaults, and when no stored
credentials exist a running local `solo`, `dev`, or standalone Hub, sole online
Worker, and sole active Workspace are discovered automatically.

## Output <!-- topic: output -->

- Text is the default: aligned tables for lists, `key: value` lines for single
  objects, and one readable line per stream event. ANSI colors and stderr wait
  spinners appear only on interactive TTYs (`NO_COLOR` or `--color never`
  disables them).
- `--json` (or `--format json`) prints one `{"data": ...}` or `{"error": ...}`
  envelope per line.
- `--format ndjson` prints unwrapped compact JSON objects, one per line.
- `--format tsv` prints tab-separated columns without padding or ANSI escapes.
- `--fields col1,col2`, `--no-header`, and `-w`/`--wide` shape tables and TSV.
- `-q` / `--quiet` prints only IDs, one per line: `A=$(leapmux agent open -q)`.
- Do not combine `--json`/`--format` with `--quiet`.

## Exit codes <!-- topic: exit-codes -->

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | The hub, the worker, or the agent failed |
| 2 | Usage error; nothing was sent |
| 3 | Not logged in, or permission denied |
| 4 | Target does not exist, or matches more than one object |
| 5 | A guard refused (`self_target_refused`, `tab_busy_refused`, or `agent_waiting`); the hint gives the override flag or `agent requests` |
| 6 | A wait ended before its condition held |
| 7 | Unavailable now; a retry can succeed |
| 8 | Local daemon is stopped (`daemon <mode> status`) |
| 130 | Interrupted by Ctrl-C / `SIGINT` (`events` exits `0` on Ctrl-C) |

## Common tasks <!-- topic: tasks -->

Orient yourself and inspect the fleet:

```sh
leapmux whoami
leapmux tree
leapmux doctor
```

Start an agent in a new worktree, send work, and wait for its exact reply:

```sh
A=$(leapmux agent open --provider claude --name reviewer --worktree fix-cli --base main -q)
leapmux agent send "$A" "Run the tests and summarize failures" --wait --timeout 15m
```

Or open and wait in one shot:

```sh
leapmux agent open --provider claude --message "Run the tests" --wait --timeout 15m
```

Handle pending permission prompts, plans, and questions:

```sh
leapmux agent requests
leapmux agent requests children
leapmux agent approve "$A"
leapmux agent deny "$A" --message "Run unit tests only"
leapmux agent answer "$A" --option 2
```

Inspect messages, transcripts, goals, usage, and manage the input queue:

```sh
leapmux agent messages "$A"
leapmux agent messages "$A" --follow --markdown
leapmux agent search "race condition" --since 24h
leapmux agent goal set "$A" "Ship CLI v2 with zero lint errors"
leapmux agent todos "$A"
leapmux agent tasks "$A"
leapmux agent usage "$A"
leapmux agent queue list "$A"
leapmux agent queue steer "$A"
leapmux agent queue preempt "$A"
leapmux agent queue retry "$A"
leapmux agent send "$A" "Use the new helper" --steer
leapmux agent send "$A" "Stop and pivot" --preempt
leapmux agent fork "$A" --worktree alt-fix --base main
leapmux agent request-status "$A" <request-id>
```

Wait for multiple agents or subagents:

```sh
leapmux agent wait "$A" "$B" --timeout 20m
leapmux agent wait children
leapmux agent tree "$A"
```

Run a command in a terminal, follow output, inspect processes, use a quake terminal, restart, or attach interactively:

```sh
T=$(leapmux terminal open -q)
leapmux terminal run "$T" 'make test; echo DONE' --wait --regex '^DONE$' --timeout 15m
leapmux terminal wait "$T" --exit --timeout 10m
leapmux terminal wait "$T" --change --timeout 30s
leapmux terminal read "$T" --lines 40
leapmux terminal read "$T" --follow
leapmux terminal ps "$T"
leapmux terminal quake run 'git status' --dir ~/src/app
leapmux terminal restart "$T" --replace
leapmux terminal attach "$T"
```

Search remote files, forward TCP ports or tunnel stdio over E2EE, archive/unarchive, and export/apply workspace templates:

```sh
leapmux file search "NewLeaf" /path/to/repo/backend/internal/cli --glob "**/*.go"
leapmux file glob "**/*.proto" /path/to/repo/proto/leapmux/v1
leapmux tunnel forward 5432:127.0.0.1:5432 --worker name:build-box
leapmux tunnel connect 127.0.0.1:22 --worker name:build-box
leapmux workspace archive self
leapmux workspace unarchive 9f3kQ2aB
leapmux workspace export self --output team-dev.yaml
leapmux workspace apply team-dev.yaml --title "Feature Sprint"
```

Watch workspace or account events with checkpoints, snapshots, and capped timeouts:

```sh
# Emit a one-shot snapshot of current workspace state and exit immediately:
leapmux events --workspace self --kind agent --snapshot --json

# Stream live agent events with a persistent account checkpoint file and capped timeout:
leapmux events --kind agent --checkpoint /tmp/agent-events.json --timeout 5m --json
```

Inspect git state, diffs, history, worktrees, floating windows, and raw RPCs:

```sh
leapmux git status
leapmux git diff --staged
leapmux git log -n 10
leapmux git commit -a -m "Checkpoint" --dir ~/src/app
leapmux worktree create feat/cli --dir ~/src/app
leapmux worktree inspect ~/wt/feat-cli
leapmux worktree remove ~/wt/feat-cli
leapmux window list
leapmux section list
leapmux rpc leapmux.v1.SystemService/GetSystemInfo '{}'
```

Show status in the tab header, notify the user, and close finished tabs:

```sh
leapmux tab status set self "Running tests" --progress 40
leapmux tab status clear self
leapmux notify "Tests passed" --body "All checks pass"
leapmux agent close "$A"
```
