---
title: "CLI Guide"
description: "Drive LeapMux from scripts, CI jobs, or coding agents: authenticate, select targets, wait for agents and terminals without polling, stream events, and open Worker tunnels."
type: docs
weight: 8
aliases:
  - /docs/using/control-cli/
  - /docs/using/cli-automation/
---

The `leapmux` CLI (also available as `lmx`) drives any running LeapMux instance from a shell, a CI pipeline, or another coding agent. It manages workspaces, tabs, layouts, coding agents, terminals, files, git worktrees, live events, and Worker TCP tunnels over the same end-to-end encrypted (E2EE) channels used by the web and desktop apps.

For every command's complete argument and flag table, see the **[Control Command Reference](/docs/reference/commands/)**. For Hub administration (`leapmux admin`), see [Admin CLI](/docs/admin/admin-cli/); for offline recovery (`leapmux recover`), see [Recovery](/docs/admin/recover/).

## Which automation tool should I use?

| Goal | Recommended tool | Why |
| --- | --- | --- |
| Run a quick action or inspect state | Standard `leapmux <group> <cmd>` | Short aliases, `--json`/`--quiet`, and automatic Hub or tab discovery. |
| Send a prompt and get a structured, retry-safe answer | `leapmux agent send --wait --output-schema --request-id` | Event-driven wait with automatic schema validation retries and idempotent deduplication. |
| Run shell commands and wait for output or exit | `leapmux terminal open/run/wait --regex\|--change\|--exit` | Zero-polling waits over live output streams with offset tracking (`--after-offset`). |
| React to activity across workspaces without missing events | `leapmux events --checkpoint <file>` | Unified Hub + Worker event stream with durable replay across disconnects. |
| Let coding agents orchestrate other agents automatically | [Agent Skills](/docs/using/agent-skills/) (`leapmux skill install --all`) | Curated playbooks for parallel delegation, cross-provider review, councils, and handoffs. |
| Call an advanced unary protobuf procedure directly | `leapmux rpc <service/method> [json]` | Direct JSON access to any Hub or Worker unary RPC (bypasses CLI safety guards). |

## Connecting and authenticating

The same CLI commands run both **outside LeapMux** (on your laptop or in CI) and **inside any Worker-spawned agent or terminal tab**:

1. **Hub resolution order:** `--hub <url|socket>` → tab local socket (`LEAPMUX_CONTROL_SOCK`) → `LEAPMUX_HUB` → saved context (`leapmux context use`) or default logged-in Hub (`leapmux auth use`) → auto-discovered local `solo`, `dev`, or standalone Hub socket.
2. **Sign in once outside a tab:**

```bash
# Browser PKCE login (opens a loopback callback on 127.0.0.1)
leapmux auth login --hub https://leapmux.example.com

# Headless, SSH, or Unix/named-pipe socket login
leapmux auth login --hub https://leapmux.example.com --device-code

# Narrow or administrative scopes (admin scopes are never requested by default)
leapmux auth login --hub https://leapmux.example.com --scope "file:read git:read"
```

Pass `--device-name` to label the machine in [Connected Apps](/docs/using/accounts-and-apps/#connected-apps). Access tokens renew automatically for up to {{< duration absolute-cap >}} (with a sliding {{< duration refresh-token >}} refresh window); check status or sign out with `leapmux auth status`, `auth list`, `auth use`, `auth credentials`, and `auth logout`.

- **Solo mode over local IPC:** Commands talking to a local `solo` socket need no login. TCP connections require a normal login once the `solo` password is set.
- **Persistent defaults:** Save default `--hub`, `--worker`, `--workspace`, and `--provider` outside a tab with `leapmux context use` (inspect with `context show`, reset with `context clear`). Run `leapmux doctor` to verify daemons, connectivity, credentials, and online Workers.
- **Credential storage:** Stored one file per Hub (`<scheme>_<host>.json`, mode `0600`) in the first matching location: `LEAPMUX_CONTROL_CONFIG_DIR`, `$XDG_CONFIG_HOME/leapmux/control`, `~/.config/leapmux/control` (Unix/macOS), or `%APPDATA%\leapmux\control` (Windows). Set `LEAPMUX_CONTROL_CONFIG_DIR` in CI to isolate job credentials.

## Command syntax, aliases, and completions

Every command uses `leapmux <group> <command> [args] [flags]`. Place values starting with `-` after `--`.

- **Canonical aliases:** `lmx`, `ag`, `term`, `ws`, `wt`, `wk`, `win`, `ev`, `notif`, `ctx`, `tun`, `pref`/`prefs`, `q`, `req`, `msg`, `skills`, `overview`/`ps`, `grep`/`find`, `fwd`/`nc`, `ls`, `rm`, `mv`, `cat`. Use full names or canonical aliases in scripts rather than ad-hoc prefixes.
- **Self-documentation:** Every command supports `--help`. Agents can inspect bundled guidance with `leapmux guide` and the machine-readable schema with `leapmux commands --json`.
- **Shell completion:** Run `leapmux completion <bash|zsh|fish|powershell|elvish|nushell>` for command, flag, and live target ID completion.

## Selecting targets

Commands accept targets (`<agent>`, `<terminal>`, `<tab>`, `<workspace>`, `<worker>`) in several forms:

| Selector | Example | Matches |
| --- | --- | --- |
| Full ID or unique prefix (4+ chars) | `87a1` | Exact ID match (checked first) or single matching prefix. |
| `id:<id>` | `id:87a1`, `id:self` | Exact ID (any length) or unique ID prefix (4+ chars), never matching titles or names. |
| `title:<title>` / `name:<name>` | `"title:Release"`, `name:build-box` | Exact tab/workspace title, Worker name, or agent `--name` slug. |
| Bare title, slug, or Worker name | `reviewer` | Case-insensitive match across titles, slugs, and Worker names. |
| `self` | `self` | The calling tab, tile, workspace, or Worker inside a managed tab. |
| `parent` / `children` | `parent`, `children` | Inside an agent tab: the spawning parent agent (`parent`) or all direct child agents (`children`) — both provider subagents and agents opened or forked by this tab (`spawned_by_agent_id`) — on `agent wait`, `agent requests`, `agent interrupt`, and `agent close`. |

Read-only commands default to `self` inside a tab; mutating commands require an explicit target. Tab-opening commands (`agent open`, `terminal open`, `file open`) default `--workspace`, `--tile`, `--worker`, and `--dir` to the calling tab's context. Pass `--workspace <ws>` to narrow ambiguous tab lookups.

## Output, exit codes, and safety guards

- **Output formats:** Default text prints aligned tables (8-character IDs; `-w`/`--wide` shows full IDs, `--no-header` omits headers, `--fields` selects columns). Use `--json` (or `--format json`) for `{"data": ...}` / `{"error": {...}}` envelopes, `--format ndjson` for unwrapped JSON lines, `--format tsv` for tab-separated output, or `-q`/`--quiet` for bare IDs (do not combine `-q` with `--json`/`--format`).
- **Exit codes:**

| Exit | Error codes | Meaning |
| --- | --- | --- |
| `0` | — | Success (`events` also exits `0` on Ctrl-C). |
| `1` | `invalid_argument`, `conflict`, `internal`, `version_mismatch` | Request rejected by Hub/Worker or runtime error. |
| `2` | `usage` | Bad CLI syntax, missing/invalid argument before dialing. |
| `3` | `not_logged_in`, `permission_denied` | Missing credential or insufficient OAuth scope. |
| `4` | `not_found`, `ambiguous_target` | Target missing or matched multiple objects. |
| `5` | `self_target_refused`, `tab_busy_refused`, `agent_waiting` | Safety guard refused the action, or a pending prompt blocked `agent send --wait`. |
| `6` | `timeout` (or unmet wait) | Wait timed out, closed, or settled with an interrupted/failed outcome. |
| `7` | `worker_offline`, `unavailable` | Transient connectivity outage; safe to retry. |
| `8` | — | Local daemon is stopped (`leapmux daemon <mode> status`). |
| `130` | — | Interrupted by `SIGINT`. |

- **Safety guards:** Three guards prevent accidental data loss and print their override flag on refusal:
  - **Self-target** (`--allow-self`) — blocks closing or deleting the tab, tile, window, or workspace running the command.
  - **Busy tab** (`--allow-busy`) — blocks closing an active agent turn, running terminal process, or workspace with busy tabs or dirty worktrees.
  - **Last worktree tab** (`--worktree keep|push|discard`) — requires an explicit choice when closing the last tab on a git worktree or dirty branch.

## Event-driven waits, schemas, and safe retries

### Agent waits and structured outputs

`agent wait` and `agent send --wait` subscribe directly to Worker state over the encrypted stream without polling:

```bash
# Run two agents in parallel, then wait until both settle
A=$(leapmux agent open --provider claude --message "Fix auth tests" -q)
B=$(leapmux agent open --provider codex --message "Update release notes" -q)
leapmux agent wait "$A" "$B" --timeout 30m

# Request a JSON Schema-validated reply with retry safety
leapmux agent send "$A" "Review the diff and report findings" \
  --wait \
  --output-schema review-schema.json \
  --schema-retries 3 \
  --request-id "review-$(date +%F)" \
  --timeout 15m
```

- **`agent wait` and `agent tree`:** `agent wait` accepts `--until idle|working|waiting_for_user|settled` (repeatable; default `settled`) and `--any` to return when the first listed agent matches. Calling `agent wait` on `self` fails with `usage`. Run `leapmux agent tree` to inspect parent-child agent and subagent hierarchies and find subagent IDs.
- **Created agent IDs with `--wait`:** `agent open --wait -q` and `agent fork --wait -q` print the created agent ID on stdout (in text mode, they print a short ID notice on stderr before waiting).
- **Turn correlation:** Providers that emit native turn IDs correlate `agent send --wait` replies directly to the submitted input; other providers correlate by turn boundaries. Unmet waits print current state and exit `6`.
- **JSON Schema (`--output-schema`):** Accepts a file path, inline JSON, or `-` (stdin) alongside `--wait`. If a reply fails validation, the CLI automatically asks the agent to fix the schema errors up to `--schema-retries` times (default `2`, max `10`).
- **Idempotent retries (`--request-id`):** Supported by `agent send`, `agent open`, `agent fork`, and `terminal run`. Repeating the same request ID with identical arguments returns the original result instead of duplicating work (and fails with `conflict` if arguments change). Inspect status with `leapmux agent request-status <agent> <request-id>`. With scheduled `--at`, use an absolute RFC 3339 timestamp with `--request-id` rather than a relative offset like `+10m`.
- **Forking agents (`agent fork`):** By default, `leapmux agent fork <agent>` performs a native conversation-history fork when the provider supports it, cloning both the provider session history and the Worker transcript so the forked tab preserves context and prompt cache. Pass `--fresh` to clone only provider, model, options, and working directory into a clean empty session without copying history.
- **Repo-local scratch files (`.leapmux/tmp`):** Store repository-local briefs, JSON schemas, and event checkpoints under `<git toplevel>/.leapmux/tmp/` (part of the `.leapmux/<category>` layout, with `.leapmux/conf` reserved for future repository settings). LeapMux adds `.leapmux/tmp/` to `.git/info/exclude` automatically on worktree creation, `--file` and `--output-schema` reads, checkpoints, and exports.
- **Answering prompts:** Inspect pending permissions, plans, questions, and forms with `leapmux agent requests` and respond with `agent approve`, `agent deny --message`, or `agent answer --option / --field`. An agent cannot answer its own prompts (`usage`).

### Terminal waits

`terminal wait` (also available as `--wait` on `terminal open` and `terminal run`) watches rendered terminal output and exit status without polling:

```bash
T=$(leapmux terminal open -q)
OFFSET=$(leapmux terminal read "$T" --json | jq -r .data.screen_end_offset)
leapmux terminal run "$T" 'make test; echo DONE'
leapmux terminal wait "$T" --regex '^DONE$' --timeout 15m
leapmux terminal wait "$T" --change --after-offset "$OFFSET" --timeout 30s
code=$(leapmux terminal wait "$T" --exit)
```

Use `--text` or `--regex` (RE2, anchored with `^`/`$` to avoid matching echoed commands), `--change` with `--after-offset` to eliminate read-then-wait races, or `--exit` to capture the exit code.

### Resumable event streams and UI notifications

- **`leapmux events`** merges Hub workspace/tab lifecycle events with live Worker agent and terminal events. Filter with `--kind` (`agent`, `terminal`, `workspace`, `tab`, or specific kinds like `agent.request`), scope with `--workspace` or `--tab`, take a one-shot view with `--snapshot`, or pass **`--checkpoint <file>`** to replay missed events after disconnects (the stream emits `stream.gap` if retained history expired and `stream.retired` when a closed tab finishes draining).
- **Notifying the user:** Push toasts with `leapmux notify "<title>" [--body <text|->] [--tab <tab>]` (an empty title fails with `usage` / exit `2`; titles over 256 bytes or bodies over 4,096 bytes fail with `invalid_argument`), jump windows to a tab with `leapmux tab reveal <tab>`, or display a live tab progress badge with `leapmux tab status set self "Running tests" --progress 40 --tone neutral|success|warning|error`.

## Inside a managed tab

Whenever remote control is enabled on a Worker, every spawned agent and terminal automatically receives these `LEAPMUX_CONTROL_*` environment variables (parent variables are stripped first so child tabs always target themselves):

| Variable | When present | Meaning |
| --- | --- | --- |
| `LEAPMUX_CONTROL_SOCK` | Always | Local IPC socket the CLI connects to. |
| `LEAPMUX_CONTROL_TOKEN` | Always | Per-process bearer token scoped to this user and tab. |
| `LEAPMUX_CONTROL_USER_ID` | Always | Authenticated user ID (informational). |
| `LEAPMUX_CONTROL_WORKER_ID` | Always | Host Worker (`self` for Workers; default for `--worker`). |
| `LEAPMUX_CONTROL_TAB_ID` | Non-Quake tabs | Tab ID (`self` for tabs, agents, and terminals). |
| `LEAPMUX_CONTROL_TAB_TYPE` | Non-Quake tabs | `agent` or `terminal`. |
| `LEAPMUX_CONTROL_TERMINAL_ID` | Terminals only | Terminal ID (sole ID inside a Quake panel). |
| `LEAPMUX_CONTROL_WORKING_DIR` | When known | Working directory at spawn (default for `--dir`). |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | Agents only | Agent provider (default for `--provider`). |
| `LEAPMUX_CONTROL_CLI` | When Worker bundles/links a CLI | Path to the Worker's matching `leapmux` executable. |

{{< callout type="info" >}}
Workers with remote control disabled omit these variables, and Linux `.AppImage` builds do not bundle a linked CLI binary for `LEAPMUX_CONTROL_CLI`. Inside any managed tab, `leapmux admin ...` is refused to prevent agents from performing Hub administration.
{{< /callout >}}

The Worker prepends its linked `leapmux`/`lmx` directory onto `PATH`, and newer `leapmux` binaries automatically hand off control commands to `$LEAPMUX_CONTROL_CLI` so commands match the Worker's protocol version. Local-only commands skip hand-off (`context`, `guide`, `skill` without `--worker`, and `auth list|use`); set `LEAPMUX_NO_CONTROL_HANDOFF=1` to disable CLI hand-off explicitly. Workspace and tile IDs resolve dynamically from `LEAPMUX_CONTROL_TAB_ID`, so moving a tab never breaks scripts.

## Worker connections, key pins, and tunnels

Each Worker dials out to your Hub over WebSocket and establishes end-to-end encrypted channels; the CLI and browser verify each Worker's identity using trust-on-first-use (TOFU) key pinning.

- **Managing CLI key pins:** Use `leapmux worker pins list`, `get <worker>`, and `remove <worker>` (stored alongside your Hub credentials). A key mismatch aborts the connection immediately; verify the presented fingerprint against the Worker's startup log over an independent channel before removing a pin.
- **Desktop app tunnels:** In **LeapMux Desktop**, right-click any online Worker you own in the left sidebar and choose **Add tunnel...** (or **Delete all tunnels...**) to open a loopback **port-forward** (maps a local loopback port to a target host/port reachable from the Worker) or a local **SOCKS5** proxy (default `127.0.0.1:1080`, dialing destinations through the Worker). Desktop tunnels share the browser's trusted Worker key pin and stop immediately if the key changes.
- **CLI tunnels:** Forward TCP ports or pipe stdio directly over a Worker's encrypted channel from outside LeapMux:

```bash
# Forward local port 5432 to Postgres on a remote Worker
leapmux tunnel forward 5432:127.0.0.1:5432 --worker name:db-box

# Reach SSH on a Worker behind NAT via ProxyCommand
ssh -o ProxyCommand="leapmux tunnel connect %h:%p --worker name:build-box" user@127.0.0.1
```

{{< callout type="warning" >}}
`leapmux tunnel` commands require a direct Hub connection and **must run outside a LeapMux tab** — running them inside an agent or terminal tab's local IPC socket fails with `usage`.
{{< /callout >}}

## Direct unary RPC (`leapmux rpc`)

For low-level scripting, `leapmux rpc <procedure> [request-json|-]` invokes any unary Hub or Worker protobuf procedure directly using JSON input and output:

```bash
leapmux rpc leapmux.v1.WorkspaceService/ListTabs '{"workspace_ids":["'"$WS"'"]}'
leapmux rpc leapmux.v1.WorkerAgentService/ListAgents '{"tab_ids":["'"$A"'"]}' --worker self --json
```

Worker procedures require an explicit `--worker` flag, streaming procedures are unsupported (use `events`, `layout watch`, or `agent messages --follow`), and inside a managed tab only an allowlisted subset of Hub procedures is relayed (`leapmux rpc --help` lists them). `leapmux rpc` bypasses CLI safety guards while still enforcing Hub and Worker authorization.

## Related chapters

- [Control Command Reference](/docs/reference/commands/) — complete flag and subcommand reference by topic.
- [Agent Skills](/docs/using/agent-skills/) — curated multi-agent `SKILL.md` playbooks.
- [Managing Workers](/docs/admin/managing-workers/) — registering Workers and verifying fingerprints.
- [Security & Authentication](/docs/admin/security-and-auth/) — encryption, OAuth scopes, and TOFU trust model.
