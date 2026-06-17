---
title: "Remote Control CLI"
description: "Drive LeapMux from a script, CI job, or another agent with leapmux remote: open tabs, message agents, type into terminals, reshape layouts, and stream events."
type: docs
weight: 8
---

`leapmux remote` is a JSON-emitting command-line surface for driving LeapMux from outside the browser. It lets you open and close tabs, send messages to agents, type into terminals, reshape the tile layout, inspect files and git state on a Worker, and stream live workspace events — all from a script, a CI job, or another agent.

This chapter covers authentication, the universal entity-ID flags, the output envelope, and every command group with its subcommands and key flags. For the operator-facing database/keystore CLI (`leapmux admin`), see [Admin CLI](/docs/operating/admin-cli/). For the agent and terminal features these commands drive, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## Two callers, one CLI

The same `leapmux remote ...` invocation works in two very different contexts:

1. **An external user on their own machine.** You authorize the CLI against a Hub with `leapmux remote auth login --hub ...`, which persists a bearer token on disk. Every subsequent command attaches that token and talks to the Hub over HTTPS.
2. **An agent or terminal spawned inside a Worker.** When a Worker spawns an agent process or a shell, it hands the process a private local-IPC socket and a per-process token through `LEAPMUX_REMOTE_*` environment variables. A script running inside that agent or shell can call `leapmux remote` with no login and no flags — the env vars supply the credential and pre-fill the entity IDs of the spawning tab.

The CLI decides which transport to use automatically (see [Authentication](#authentication)). Because both transports expose the same RPCs, a script you write to run inside an agent also works verbatim from your laptop once you point it at a Hub.

**The two transports converging on the Worker(s):**

```text
  ┌────────────────┐                  ┌────────────────────┐
  │  External CLI  │                  │  Worker-spawned    │
  │  (your laptop) │                  │  agent / terminal  │
  └───────┬────────┘                  └─────────┬──────────┘
          │ LEAPMUX_HUB +                       │ LEAPMUX_REMOTE_SOCK
          │ Bearer token                        │ + X-Leapmux-Token
          ▼                                     ▼ (local IPC)
  ┌────────────────┐                  ┌────────────────────┐
  │      Hub       │                  │   Host Worker      │
  │  (relays the   │                  │  (delegates the    │
  │   channel)     │                  │   inner RPC)       │
  └───────┬────────┘                  └─────────┬──────────┘
          │ relayed E2EE                        │ Noise channel
          │ (Noise) channel                     │ (cross-worker)
          ▼                                     ▼
  ┌────────────────┐                  ┌────────────────────┐
  │  Target Worker │                  │  Sibling Worker    │
  └────────────────┘                  └────────────────────┘
```

> **Tip:** Inside an agent or terminal, run `printenv | grep LEAPMUX_REMOTE_` to see exactly what context you were spawned with. Every entity-ID variable uses the `_ID` suffix.

## Output envelope and exit codes

Every command prints a single JSON object to **stdout**:

- **Success:** `{"data": <value>}` — pretty-printed with 2-space indentation.
- **Failure:** `{"error": {"code": "<code>", "message": "<message>"}}`.

Both envelopes go to stdout, so `leapmux remote ... | jq` works uniformly whether the command succeeded or failed. Failure is signaled by a **non-zero process exit code**, not by writing to a separate stream. Stderr is reserved for diagnostics and warnings outside the JSON contract.

A few commands deliberately break the envelope to emit raw bytes:

- `terminal get --screen` prints the terminal's retained PTY window directly to stdout (ANSI escapes intact, no JSON).
- `agent messages` (without `--follow`) prints a JSON array; with `--follow` it prints JSON-lines.
- `events watch` prints JSON-lines (one event object per line).

```bash
# Pull just the tab id out of a successful tab open
leapmux remote tab open --type agent --worker-id "$W" --workspace-id "$WS" \
  | jq -r '.data.tab_id'

# Detect failure and read the error code
if ! out=$(leapmux remote agent get --tab-id "$T"); then
  echo "$out" | jq -r '.error.code'
fi
```

## Authentication

### How the transport is chosen

For each invocation, the CLI selects a transport in this order:

| Condition | Transport |
| --- | --- |
| `LEAPMUX_REMOTE_SOCK` is set (Worker-spawned) | Local IPC over that socket, presenting `LEAPMUX_REMOTE_TOKEN` as the `X-Leapmux-Token` header |
| `--hub <url>` flag or `LEAPMUX_HUB` env var is set | Hub client over HTTPS, presenting the stored bearer as `Authorization: Bearer <token>` |
| Neither | Error `not_logged_in`: "no `--hub` flag or `LEAPMUX_HUB` / `LEAPMUX_REMOTE_SOCK` env var; run `leapmux remote auth login --hub <url>` or invoke from inside an agent" |

The default per-request HTTP timeout is 60 seconds.

### `leapmux remote auth login`

Authorizes the CLI against a Hub and writes a credential file to disk. The `--hub` flag is required (or set `LEAPMUX_HUB`).

| Flag | Default | Purpose |
| --- | --- | --- |
| `--hub <url>` | `$LEAPMUX_HUB` | Hub base URL (required) |
| `--device-name <name>` | `$USER@$hostname` | Human-visible name recorded with the credential |
| `--device-code` | `false` | Force the RFC 8628 device-code flow (headless / SSH / container) |

**Default flow (PKCE local redirect).** The CLI opens a loopback listener on `127.0.0.1`, prints "Open this URL in your browser to authorize the CLI:" followed by the authorization URL, and tries to launch your browser automatically (`open` on macOS, `xdg-open` on Linux, the shell handler on Windows). You sign in on the Hub's web page; the Hub redirects back to the loopback listener to complete the exchange. The CLI waits up to **10 minutes** for the callback before failing with `{"error":{"code":"timeout",...}}`.

```bash
leapmux remote auth login --hub https://leapmux.example.com
```

**Device-code flow (`--device-code`).** Use this on a headless box, over SSH, or inside a container where no browser is available. The CLI prints a verification URL and a short user code:

```bash
leapmux remote auth login --hub https://leapmux.example.com --device-code
```

```
To authorize this CLI, on any device with a browser:
  1. Visit https://leapmux.example.com/auth/cli/activate
  2. Enter the code: 7XC-8DZ
Or open: https://leapmux.example.com/auth/cli/activate?user_code=7XC-8DZ
```

The user code is six characters from an ambiguity-free alphabet (no `0`/`1`/`I`/`O`/`L`), displayed as `XXX-XXX`. You open the verification URL on any device, enter the code, and the CLI (which polls in the background) completes once you approve. The "Or open" link pre-fills the code so you can skip the typing. On success, both flows persist a credential file and emit:

```json
{
  "data": {
    "hub_url": "https://leapmux.example.com",
    "username": "alice",
    "user_id": "usr_..."
  }
}
```

### `leapmux remote auth status` / `list` / `logout`

| Command | Flags | Output |
| --- | --- | --- |
| `auth status` | `--hub` | `{hub_url, username, user_id, expires, expired}` for the named Hub. Error `not_logged_in` if there is no credential. |
| `auth list` | none | An array of `{hub_url, username, user_id, expires}` for every Hub you have credentials for. |
| `auth logout` | `--hub` | Best-effort revokes the token on the Hub, then deletes the local credential file. Emits `{hub_url}`. |

```bash
leapmux remote auth list
leapmux remote auth status --hub https://leapmux.example.com
leapmux remote auth logout --hub https://leapmux.example.com
```

### Credential file location

Credentials are written one file per Hub:

```
<ConfigDir>/<hub-host>.json
```

`<ConfigDir>` resolves in this order:

1. `LEAPMUX_REMOTE_CONFIG_DIR` (used verbatim if set)
2. `$XDG_CONFIG_HOME/leapmux/remote`
3. `~/.config/leapmux/remote`

`<hub-host>` is the Hub's hostname, with `_<port>` appended when the URL carries a port (for example `leapmux.example.com_8443`). The file is written atomically with mode `0600`, in a directory created with mode `0700`. It contains the access token, refresh token, expiry, and your user identity.

> **Tip:** Point `LEAPMUX_REMOTE_CONFIG_DIR` at a per-job directory to keep CI credentials isolated and easy to discard.

### Headless service accounts

The interactive login flows above are for humans. For unattended scripts and integrations, mint a durable bearer token with the admin CLI instead:

```bash
leapmux admin api-token issue --user usr_... --client-name "ci-bot"
```

This prints an `access_token` of the form `lmx_a<id>_<secret>` exactly once. Supply it to the CLI by setting it as the bearer for the Hub transport. Issuing, listing, and revoking these tokens is covered in [Admin CLI](/docs/operating/admin-cli/).

## Worker-spawned environment variables

When a Worker spawns an agent or terminal (and remote control is enabled on that Worker), it injects this set of environment variables into the child process:

| Variable | When present | Meaning |
| --- | --- | --- |
| `LEAPMUX_REMOTE_SOCK` | always | Local-IPC socket URL the CLI talks to |
| `LEAPMUX_REMOTE_TOKEN` | always | Per-process bearer token |
| `LEAPMUX_REMOTE_USER_ID` | always | Authenticated user |
| `LEAPMUX_REMOTE_WORKER_ID` | always | The host Worker |
| `LEAPMUX_REMOTE_ORG_ID` | when non-empty | Organization |
| `LEAPMUX_REMOTE_TAB_ID` | when non-empty | The spawned tab's id |
| `LEAPMUX_REMOTE_TAB_TYPE` | when non-empty | `agent`, `terminal`, or `file` |
| `LEAPMUX_REMOTE_WORKING_DIR` | when non-empty | Working directory at spawn time |
| `LEAPMUX_REMOTE_AGENT_PROVIDER` | agents only | The agent's provider |

These variables become the **defaults** for the matching entity flags, so a script running inside an agent can call `leapmux remote agent send --message "hi"` with no IDs at all — the current tab is inferred from `LEAPMUX_REMOTE_TAB_ID`.

Two IDs are deliberately **not** injected: the workspace id and the tile id. They are derived from the tab id at call time via the Hub's tab-locator RPC, so a script never targets a stale tile after the tab has been moved.

> **Note:** There is no "remote-enabled" flag or checkbox. Terminals and agents receive `LEAPMUX_REMOTE_*` automatically whenever the Worker has remote control enabled. Inherited `LEAPMUX_REMOTE_*` values are stripped before re-injection, so a Worker spawned from inside another agent gets a fresh context rather than its parent's. See [Terminals](/docs/using/terminals/) for the terminal side of this.

## Universal entity-ID flags

Almost every command needs to know which entity to act on. Rather than hand-roll flags per command, LeapMux exposes one uniform set, and a resolver derives the rest from whatever subset you provide.

| Flag | Env default | Notes |
| --- | --- | --- |
| `--tab-id` | `$LEAPMUX_REMOTE_TAB_ID` | The agent/terminal/file tab |
| `--tab-type` | (none) | `agent` or `terminal`; auto-detected when omitted. Only on the generic `tab` group. |
| `--tile-id` | (none) | Derivable from `--tab-id` |
| `--workspace-id` | (none) | Derivable from `--tab-id` / `--tile-id` |
| `--worker-id` | `$LEAPMUX_REMOTE_WORKER_ID` | The host Worker |
| `--org-id` | `$LEAPMUX_REMOTE_ORG_ID` | Derivable from any other entity flag |
| `--user-id` | `$LEAPMUX_REMOTE_USER_ID` | Derivable from `--tab-id` / `--workspace-id` / `--worker-id` |

### How the resolver derives missing IDs

The Hub resolver follows the obvious chains so you only ever supply the most specific ID you have:

- a **tab** locates its matched type, workspace, tile, and Worker;
- a **tile** locates its workspace and org;
- a **workspace** locates its org and owner;
- a **Worker** locates its org and the user who registered it;
- a **user** locates its org.

So `--tab-id` alone is usually enough; the resolver fills in workspace, tile, Worker, and org behind the scenes.

### Pinned tab type for agent and terminal commands

Commands under `agent ...` and `terminal ...` pin the tab type for you. As a safety measure, the `--tab-id` env default only fires when `$LEAPMUX_REMOTE_TAB_TYPE` matches the command's pinned type. That means `agent send` run from inside a *terminal* won't silently auto-target the terminal you're sitting in — you'd have to pass `--tab-id` explicitly. The generic `tab` group has no such restriction.

### Conflicts and missing IDs

- An **explicit** flag you typed always wins over an env-derived value, silently shadowing a disagreeing env default.
- Two **explicit** inputs that disagree on the same derived field are a hard error: `{"error":{"code":"invalid_request","message":"conflicting inputs: ..."}}`.
- If the resolver still can't satisfy a required field, you get `invalid_request` naming the missing ID(s), e.g. "missing required ID(s): `--workspace-id` (or pass `--tab-id` / `--tile-id` to derive it)".

Resolver-rejected input always uses code `invalid_request`; a transport/derivation failure (the RPC itself errored) surfaces as `resolve_failed`.

## `whoami` and `version`

```bash
leapmux remote whoami          # who am I, where am I?
leapmux remote version --hub https://leapmux.example.com
```

- `whoami` from inside an agent/terminal returns `{user_id, username, org_id, workspace_id, worker_id, tab_id, tab_type, scope}`. From your laptop (Hub mode) it returns `{hub_url, user_id, username}`.
- `version` always emits the CLI's `{cli:{version, commit, branch, build_time, formatted}}`; when `--hub` is set it also probes the Hub's unauthenticated version endpoint and adds `hub:{...}` (or a non-fatal `hub_error`).

## Workspace commands

| Command | Key flags | Output |
| --- | --- | --- |
| `workspace list` | `--org-id` (or any entity flag) | The org's workspaces |
| `workspace get` | `--workspace-id` (or `--tab-id`/`--tile-id`) | One workspace |
| `workspace create` | `--org-id`, `--title` (required) | `{workspace_id}` |
| `workspace rename` | `--workspace-id`, `--title` (required) | `{workspace_id}` |
| `workspace delete` | `--workspace-id`, `--force` | Deletion + per-worker cleanup status |

```bash
leapmux remote workspace create --org-id "$ORG" --title "Release 2.0"
```

`workspace delete` cascades a Hub delete and then fans out worktree cleanup to every Worker that hosted tabs in the workspace, emitting `{workspace_id, worker_ids, status, cleanup:[...]}` where `status` is `ok` or `partial`. If the *calling* tab lives in the workspace you're deleting, the [self-target guard](#self-target-guard) refuses unless you pass `--force` ("delete even if the calling tab lives in the target workspace (would kill the caller's own PTY)").

## Tab commands

The `tab` group is the generic open/close/list/rename surface across all three tab types (agent, terminal, file). Use it for lifecycle operations; use the `agent` and `terminal` groups for type-specific actions.

| Command | Key flags |
| --- | --- |
| `tab list` | `--workspace-id`, `--org-id`, `--tab-type agent\|terminal\|file` (output filter) |
| `tab get` | `--tab-id` (type auto-detected) |
| `tab open` | `--type agent\|terminal\|file` (required) + type-specific flags + [placement flags](#placement-flags) |
| `tab close` | `--tab-id`, `--force`, `--worktree keep\|push\|discard` |
| `tab rename` | `--tab-id`, `--title` (required) |
| `tab move` | `--tab-id`, `--target-tile-id`, `--target-workspace-id` + [placement flags](#placement-flags) |

> **Note:** On `tab list`, `--tab-type` is an **output filter**, not a resolver constraint. On `tab get`/`tab move`, omitting the type lets the resolver auto-detect it.

### Opening a tab

`tab open` requires `--type`. The remaining flags depend on the type.

**Agent (`--type agent`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--worker-id` | `$LEAPMUX_REMOTE_WORKER_ID` (required) | Host Worker |
| `--provider` | `$LEAPMUX_REMOTE_AGENT_PROVIDER` | Agent provider; if unset and the Worker has exactly one installed provider it is auto-picked. Zero → `no_providers_installed`; more than one → `ambiguous_provider`. |
| `--model` | provider default | Initial model |
| `--effort` | provider default | `low`/`medium`/`high`/`max` |
| `--permission-mode` | provider default | Initial permission mode |
| `--working-dir` | `$LEAPMUX_REMOTE_WORKING_DIR` | Where the agent runs |
| `--title` | auto | Tab title |
| `--initial-message` | (none) | First message to send |

**Terminal (`--type terminal`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--worker-id` | `$LEAPMUX_REMOTE_WORKER_ID` (required) | Host Worker |
| `--shell` | Worker default | Shell to launch |
| `--shell-start-dir` | working dir | Starting directory |

**File (`--type file`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--path` | (required) | Absolute file path; registered Worker-side over the encrypted channel so the Hub never sees it |
| `--display-mode` | `0` | File-tab display mode |
| `--file-view-mode` | `0` | File view mode |

`tab open` emits `{tab_id, tab_type, workspace_id, worker_id, tile_id, position}` plus per-type extras such as `initial_message_warning` or `path`. (The permission mode now rides in the open request and is applied at launch, so there is no longer a `permission_mode_warning`.)

```bash
# Spin up a Claude Code agent in a worker's repo and send it a task
leapmux remote tab open --type agent \
  --worker-id "$W" --workspace-id "$WS" \
  --provider "Claude Code" --working-dir /home/dev/project \
  --initial-message "Run the test suite and summarize failures."
```

### Placement flags

`tab open` and `tab move` accept the same four mutually-exclusive placement flags. The default is `--last`.

| Flag | Effect |
| --- | --- |
| `--first` | Place as the first tab on the destination tile |
| `--last` | Place as the last tab (default) |
| `--before <tab-id>` | Place immediately before the referenced tab |
| `--after <tab-id>` | Place immediately after the referenced tab |

`--before`/`--after` take a **tab id** (not a rank). For those two, the destination tile is taken from the referenced tab's tile; if you also pass `--tile-id`/`--target-tile-id`, the two must agree. Misuse errors include "--first, --last, --before, and --after are mutually exclusive" and "no such tab".

### Closing a tab

```bash
leapmux remote tab close --tab-id "$T" --worktree push
```

| Flag | Purpose |
| --- | --- |
| `--force` | Self-target override: close even if the target is the calling tab |
| `--worktree keep\|push\|discard` | Worktree disposition (`remove` is a synonym for `discard`) |

`--worktree` is **required** when the close would remove the last tab for a worktree, or close the last tab on a non-worktree branch that has uncommitted or unpushed changes — omitting it then returns an `invalid_request` with the details. `--worktree push` runs `git push` and fails with `invalid_request` if the branch isn't pushable. The command emits `{tab_id, tab_type, tombstoned, worktree?, worker_close_error?}`. File tabs skip worktree inspection entirely. See [Worktrees and Branches](/docs/using/worktrees-and-branches/) for the disposition rules.

### Renaming and moving

```bash
leapmux remote tab rename --tab-id "$T" --title "Reviewer"
leapmux remote tab move --tab-id "$T" --target-tile-id "$DEST"
leapmux remote tab move --tab-id "$T" --target-workspace-id "$OTHER_WS"
```

`tab move` needs one of `--target-tile-id`, `--target-workspace-id`, or a `--before`/`--after` placement. `--target-workspace-id` alone drops the tab onto that workspace's first live leaf. Cross-workspace moves happen as a single operation. There is no `tab focus` command — the active tab and focused tile are client-local UI state, not shared.

## Tile and layout commands

The `tile` group mutates the tile tree one operation at a time; the `layout` group reads or replaces the whole tree at once. See [Tabs and Layout](/docs/using/tabs-and-layout/) for the conceptual model of splits and grids.

### `tile`

| Command | Key flags | Notes |
| --- | --- | --- |
| `tile list` | `--workspace-id` | Projected tile tree (no tabs) |
| `tile split` | `--tile-id`, `--direction vertical\|horizontal` | Default `vertical`; accepts `v`/`h`. Leaf → split with two children (50/50). |
| `tile make-grid` | `--tile-id`, `--rows N`, `--cols M` | Both required, each `1..20`. Migrates tabs to cell `[0,0]`. No `--with-tabs`. |
| `tile close` | `--tile-id`, `--with-tabs close\|move`, `--recursive`, `--force` | See policy below |
| `tile remove-grid` | `--tile-id`, `--with-tabs close\|move`, `--force` | Target must be a grid |
| `tile set-ratios` | `--tile-id`, `--ratios r1,r2[,...]` | Target must be a split |
| `tile set-grid-ratios` | `--tile-id`, `--row-ratios ...`, `--col-ratios ...` | Target must be a grid; at least one required |

**`tile close` policy.** The `--with-tabs` flag controls what happens to tabs living on the tile, and the structure of the tile decides what's allowed:

- A **leaf with no tabs** closes plainly.
- A **leaf with tabs** requires `--with-tabs close` (close the tabs) or `--with-tabs move` (migrate them to the nearest adjacent leaf).
- A **split** requires `--recursive` (cascade the whole subtree).
- A **grid** is rejected — use `tile remove-grid` instead, even with `--recursive`.
- A **grid cell** is rejected (closing it would leave an unusable hole; close its tabs or remove the whole grid).

`tile close` emits `{tile_id, tabs_closed, tabs_moved, heir_tile_id?}`.

**Ratios.** `--ratios`, `--row-ratios`, and `--col-ratios` take comma-separated non-negative floats that are rescaled to sum to 1.0, so `1,3` is equivalent to `0.25,0.75`. The length must match the live child count (or rows/cols). Empty lists, malformed numbers, negatives, NaN/Inf, and all-zero lists are rejected.

```bash
# Split the current tile and give the right pane two-thirds of the width
leapmux remote tile split --tile-id "$TILE" --direction horizontal
leapmux remote tile set-ratios --tile-id "$SPLIT" --ratios 1,2
```

### `layout`

```bash
# layout set takes only the tree node, so extract `.data.tree` from the get envelope.
leapmux remote layout get --workspace-id "$WS" | jq '.data.tree' > layout.json
# edit layout.json ...
leapmux remote layout set --workspace-id "$WS" --file layout.json
```

- `layout get` emits `{workspace_id, root_node_id, tree, tabs_by_tile}`.
- `layout set` requires exactly one of `--file PATH` or `--stdin`, and it accepts **only the tree node** — the value of the `tree` field, *not* the full `layout get` envelope. Feeding back the whole `{workspace_id, root_node_id, tree, tabs_by_tile}` object fails validation with `root: unrecognized kind`, because the top-level keys it expects are `kind`/`direction`/`ratios`/`rows`/`cols`/`children`. Extract the `tree` field first (e.g. `jq '.data.tree'`). It rewrites the entire tree in one batch and repoints every live tab onto the new tree's first leaf; the root node id never changes.

The input tree's `kind` accepts `leaf`/`split`/`grid` (uppercase and `NODE_KIND_*` forms too). A `split` needs at least 2 children and a `direction`; a `grid` needs `rows`/`cols` in `1..20` and exactly `rows*cols` children. Validation errors are path-anchored, e.g. "root.children[1].children[0]: SPLIT requires at least 2 children (got 1)".

If a tab races in during the rewrite, `layout set` retries once (it makes at most two attempts); persistent contention yields `{"error":{"code":"concurrent_modification",...}}`. The success envelope includes `attempts` (`1` normally, `2` after a retry).

## Self-target guard

Several destructive commands refuse to destroy the very tab you're calling from. The guard is anchored on `LEAPMUX_REMOTE_TAB_ID`, so it only matters when you call from inside an agent or terminal. It fires for:

- `workspace delete` when the calling tab lives in the target workspace;
- `tab close` when the target *is* the calling tab;
- `tile close` / `tile remove-grid` with `--with-tabs=close` (or a no-tab close) when the calling tab is inside the doomed subtree.

When triggered, the command returns code `self_target_refused` with a message ending "; pass `--force` to override". The guard is **skipped** for `--with-tabs=move` variants, because the tab and its PTY survive the migration. Pass `--force` on the relevant command to bypass it deliberately.

## Worker commands

| Command | Key flags | Output |
| --- | --- | --- |
| `worker list` | `--hub` | Accessible Workers |
| `worker get` | `--worker-id` (or `--tab-id`) | Worker metadata |

```bash
leapmux remote worker list --hub https://leapmux.example.com
```

### Worker TOFU pins

LeapMux pins each Worker's key on first connection (trust-on-first-use). The `worker pins` subgroup manages those pins from the CLI. All pins commands require `--hub` (or `$LEAPMUX_HUB`).

| Command | Key flags | Output |
| --- | --- | --- |
| `worker pins list` | `--hub` | Every pinned Worker (sorted by id) |
| `worker pins show` | `--worker-id` (defaults to `$LEAPMUX_REMOTE_WORKER_ID`) | One recorded pin; `not_found` if none |
| `worker pins remove` | `--worker-id` | Drops the pin so the next connect re-prompts; emits `{removed_worker_id}` |

Pins are stored at `<ConfigDir>/<hub-host>/pins.json` with mode `0644` (they are not secrets). For the Worker registration and approval lifecycle, see [Managing Workers](/docs/operating/managing-workers/).

## Agent commands

The `agent` group is the type-specific surface for agent tabs — use `tab open`/`close`/`list`/`rename` for lifecycle. Every agent command pins the agent tab type and needs at least one entity input.

| Command | Key flags | Output |
| --- | --- | --- |
| `agent send` | `--tab-id`, `--message "..."` or `--stdin` | `{agent_id}` |
| `agent interrupt` | `--tab-id`, `--reason "..."` | `{agent_id}` |
| `agent get` | `--tab-id` | Full agent state (model, status, provider, option groups, git status, ...) |
| `agent providers` | `--tab-id` / `--worker-id` | `[{name, aliases}]` for the Worker |
| `agent messages` | `--tab-id`, `--after-seq`, `--before-seq`, `--limit`, `--follow` | A message page, or a stream with `--follow` |
| `agent set` | `--tab-id`, `--model`, `--effort`, `--permission-mode`, `--option key=value` | `{agent_id, applied:{...}}` |
| `agent send-control-response` | `--tab-id`, `--content "..."` | `{agent_id}` |

```bash
# Send a message and then tail the agent's reply stream
leapmux remote agent send --tab-id "$T" --message "Refactor the auth module."
leapmux remote agent messages --tab-id "$T" --follow
```

Notes:

- `agent send` requires one of `--message` or `--stdin`; passing neither is an `invalid_request` ("--message or --stdin is required"). If you pass both, `--message` wins and `--stdin` is ignored.
- `agent messages --limit` defaults to 5 (the Hub caps it at 50). Without `--follow` you get one page as a JSON array; with `--follow` you get the first page followed by new messages as JSON-lines, reconnecting automatically on transient drops. `--follow` exists **only** on `agent messages`, not on `events watch`.
- `agent set` applies model/effort/permission-mode and repeatable `--option key=value` provider options. Most settings (model, effort, permission-mode) apply live on providers that support it (e.g. Claude Code, Codex); changes a provider can't apply to the running process trigger a restart (e.g. switching effort back to auto). See [Coding Agents](/docs/using/coding-agents/) for the per-provider settings.
- `agent get`/`agent list` report every provider setting as one unified `option_groups` array (each entry `{id, label, current_value, options:[...], ...}`); `model`/`effort`/`permission_mode` stay as top-level convenience keys. There is no separate `extra_settings`/`available_models`/`available_option_groups` field -- read a provider option from `option_groups`, e.g. `leapmux remote agent get --tab-id "$T" | jq '.data.option_groups[] | select(.id=="sandbox_policy") | .current_value'`.
- `agent send-control-response` forwards a raw `control_response` JSON payload for Claude-Code-style agents — the scripting equivalent of clicking an approval button in the UI.

## Terminal commands

The `terminal` group is the type-specific surface for terminal tabs; use `tab open`/`close`/`rename` for lifecycle.

| Command | Key flags | Output |
| --- | --- | --- |
| `terminal send` | `--tab-id`, `--data "..."` or `--stdin` | `{tab_id, bytes_sent}` |
| `terminal get` | `--tab-id`, `--screen` | Terminal metadata, or raw PTY bytes with `--screen` |
| `terminal shells` | `--worker-id` | `{shells, default_shell}` |

```bash
# Type a command into a terminal (newline runs it), then grab the screen
printf 'ls -la\n' | leapmux remote terminal send --tab-id "$T" --stdin
leapmux remote terminal get --tab-id "$T" --screen
```

`terminal send` rejects an empty payload ("--data or --stdin (with non-empty input) is required"). Use `--stdin` for binary, escape sequences, or pasted content. `terminal get` returns a metadata map by default (geometry, shell, working dir, git info, status); `--screen` prints the retained PTY window directly to stdout with ANSI intact. Terminals receive remote-control env vars automatically — see [Terminals](/docs/using/terminals/).

## File and git inspection

These groups inspect a Worker's filesystem and git state read-only. The Worker is resolved through the universal resolver, so `--tab-id <agent>` is enough to target the Worker hosting that agent.

### `file`

| Command | Key flags | Output |
| --- | --- | --- |
| `file list` | `--path <dir>` (required), `--max-depth N`, `--dirs-only` | `{path, truncated, entries}` |
| `file read` | `--path <file>` (required), `--offset N`, `--limit N` | `{path, total_size, content}` |
| `file stat` | `--path <path>` (required) | Stat info |

`file read --limit 0` means the default 64 KB cap.

### `git`

| Command | Key flags | Output |
| --- | --- | --- |
| `git status` | `--path <dir>` (defaults to `$LEAPMUX_REMOTE_WORKING_DIR`) | `{info, files}` |
| `git branches` | `--path <dir>` | Branch list |
| `git worktrees` | `--path <dir>` | Worktree list |
| `git read` | `--path <file>` (required), `--ref head\|staged` | `{ref, path, content}` |

`git status`/`branches`/`worktrees` default `--path` to the spawn's working dir; `git read` keeps `--path` required (it is a file path) and defaults `--ref` to `head`.

```bash
leapmux remote git status            # uses $LEAPMUX_REMOTE_WORKING_DIR inside an agent
leapmux remote git read --path src/main.go --ref staged
```

## Streaming events

`events watch` subscribes to a workspace's live event stream and prints one JSON object per line. The resolver fills `org_id` from any entity flag.

```bash
leapmux remote events watch --workspace-id "$WS"
```

The first line is always the bootstrap snapshot (`{"kind":"materialized",...}`). Subsequent lines carry one of these `kind` values:

| `kind` | Meaning |
| --- | --- |
| `materialized` | Bootstrap snapshot (always first) |
| `batch` | A batch of layout/tab operations |
| `entity_materialized` | An entity (tab, node, floating window) became visible |
| `entity_removed` | An entity was removed |
| `presence` | Active-client presence changed for a workspace |
| `workspace_renamed` | A workspace title changed |
| `workspace_created` | A workspace was created |
| `workspace_deleted` | A workspace was deleted |
| `unknown` | An event the CLI doesn't project |

The command runs until you interrupt it (SIGINT/SIGTERM) or the stream closes. Errors surface as `rpc_failed` or `stream_error`.

> **Note:** `events watch` streams **workspace/layout** events only (the CRDT org stream). It has no `--include` source filter, no `--follow`, and no per-line `source` key. To tail an agent's chat, use `agent messages --follow` instead.

```bash
# React to tab removals in a workspace
leapmux remote events watch --workspace-id "$WS" \
  | jq -c 'select(.kind == "entity_removed")'
```

## End-to-end examples

### Drive an agent from your laptop

```bash
export LEAPMUX_HUB=https://leapmux.example.com
leapmux remote auth login --hub "$LEAPMUX_HUB"

WS=$(leapmux remote workspace create --org-id "$ORG" --title "Bugfix" | jq -r '.data.workspace_id')
W=$(leapmux remote worker list | jq -r '.data[0].worker_id')

T=$(leapmux remote tab open --type agent \
      --worker-id "$W" --workspace-id "$WS" \
      --provider "Claude Code" --working-dir /home/dev/project \
      --initial-message "Find and fix the failing test in ./pkg/auth." \
    | jq -r '.data.tab_id')

leapmux remote agent messages --tab-id "$T" --follow
```

### A script running inside an agent

No login, no IDs — the spawn context supplies everything:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Inspect the repo we were spawned in
leapmux remote git status | jq '.data.info'

# Open a sibling terminal on the same worker and run the build
TERM_TAB=$(leapmux remote tab open --type terminal | jq -r '.data.tab_id')
printf 'make build\n' | leapmux remote terminal send --tab-id "$TERM_TAB" --stdin
```

### Snapshot and restore a layout

```bash
# layout set takes only the tree node, so extract `.data.tree` from the get envelope.
leapmux remote layout get --workspace-id "$WS" | jq '.data.tree' > before.json
leapmux remote tile split --tile-id "$TILE" --direction vertical
# ... experiment ...
leapmux remote layout set --workspace-id "$WS" --file before.json
```

## Error code reference

| Code | Typical cause |
| --- | --- |
| `not_logged_in` | No usable credential (no `--hub`/`LEAPMUX_HUB`/`LEAPMUX_REMOTE_SOCK`, or no stored token) |
| `invalid_request` | Bad/missing/conflicting flags; resolver could not satisfy a required ID |
| `resolve_failed` | A derivation RPC failed while resolving entity IDs |
| `not_found` | The referenced tab/Worker/pin/agent does not exist |
| `self_target_refused` | The operation would destroy the calling tab (pass `--force`) |
| `no_providers_installed` / `ambiguous_provider` | `tab open --type agent` with zero / more than one installed provider and no `--provider` |
| `concurrent_modification` | `layout set` lost the retry race against a concurrent change |
| `rpc_failed` / `stream_error` | `events watch` failed to open or the stream errored |
| `timeout` | `auth login` PKCE callback didn't arrive within 10 minutes |

## Security model

The two transports carry different credentials and trust boundaries:

- **External CLI.** Each Hub credential is a single bearer token (an `api_tokens` row, stored only as a peppered HMAC-SHA256 hash). End-to-end channels to Workers use the same Noise_NK handshake the browser uses, with each Worker's static key pinned per-hub on first use (see [Worker TOFU pins](#worker-tofu-pins)).
- **Spawned agent or terminal.** The Worker hands the process a private local-IPC socket (mode `0600`) and a per-process token scoped to the spawning user and workspace. When the agent or terminal closes, the socket is torn down and the token is invalidated.
- **Cross-worker calls from a spawned agent.** Reaching a *sibling* Worker (an `agent`/`terminal`/`file`/`git` command with a different `--worker-id`) uses a Worker-minted delegation token scoped to your `(user, workspace)`. It is minted lazily on the first cross-worker call and revoked when the agent closes — so an agent that never reaches across Workers never holds one.

For the full trust model, what the Hub can and cannot see, and the encryption primitives, see [Security & Threat Model](/docs/operating/security/).

## See also

- [Coding Agents](/docs/using/coding-agents/) — providers, models, effort, control prompts, and resume that `agent` commands drive.
- [Terminals](/docs/using/terminals/) — PTY sessions, shells, and the automatic `LEAPMUX_REMOTE_*` injection.
- [Managing Workers](/docs/operating/managing-workers/) — Worker registration, approval, and TOFU pinning.
- [Admin CLI](/docs/operating/admin-cli/) — `leapmux admin api-token` for headless service-account tokens.
- [Tabs and Layout](/docs/using/tabs-and-layout/) — the tile/split/grid model that `tile` and `layout` manipulate.
- [Worktrees and Branches](/docs/using/worktrees-and-branches/) — the worktree dispositions used by `tab close --worktree`.
