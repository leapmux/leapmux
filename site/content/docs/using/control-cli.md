---
title: "Control CLI"
description: "Drive LeapMux from a script, CI job, or another agent with leapmux control: open tabs, message agents, type into terminals, reshape layouts, and stream events."
type: docs
weight: 8
---

`leapmux control` is a JSON-emitting command-line surface for driving LeapMux from outside the browser. Use it from a script, a CI job, or another agent. It opens and closes tabs, sends messages to agents, types into terminals, reshapes the tile layout, inspects files and git state on a Worker, and streams live workspace events.

This chapter covers authentication, the universal entity-ID flags, the output envelope, and every command group with its subcommands and key flags. The online administration groups (`leapmux control admin ...`) are documented in the companion [Admin CLI](/docs/admin/admin-cli/) chapter. For the offline break-glass tree (`bootstrap`, `password`, `encryption-key`, `db`), see [Recovery](/docs/admin/recover/). For the agent and terminal features these commands drive, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## Two callers, one CLI

The same `leapmux control ...` invocation works in two contexts:

1. **An external user on their own machine.** You authorize the CLI against a Hub with `leapmux control auth login --hub ...`, which persists a bearer token on disk. Every subsequent command attaches that token and talks to the Hub over HTTPS.
2. **An agent or terminal spawned inside a Worker.** When a Worker spawns an agent process or a shell, it hands the process a private local-IPC socket and a per-process token through `LEAPMUX_CONTROL_*` environment variables. A script running inside that agent or shell can call `leapmux control` with no login and no flags — the env vars supply the credential and pre-fill the entity IDs of the spawning tab.

The CLI decides which transport to use automatically (see [Authentication](#authentication)). Because both transports expose the same RPCs, a script you write to run inside an agent also works verbatim from your laptop once you point it at a Hub.

**The two transports converging on the Worker(s):**

```text
  ┌────────────────┐                  ┌────────────────────┐
  │  External CLI  │                  │  Worker-spawned    │
  │  (your laptop) │                  │  agent / terminal  │
  └───────┬────────┘                  └─────────┬──────────┘
          │ LEAPMUX_HUB +                       │ LEAPMUX_CONTROL_SOCK
          │ Bearer token                        │ + X-LeapMux-Token
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

{{< callout >}}
Inside an agent or terminal, run `printenv | grep LEAPMUX_CONTROL_` to see exactly what context you were spawned with. Every entity-ID variable uses the `_ID` suffix.
{{< /callout >}}

## Output envelope and exit codes

Every command prints a single JSON object to **stdout**:

- **Success:** `{"data": <value>}` — pretty-printed with 2-space indentation.
- **Failure:** `{"error": {"code": "<code>", "message": "<message>"}}`.

Both envelopes go to stdout, so `leapmux control ... | jq` works uniformly whether the command succeeded or failed. Failure is signaled by a **non-zero process exit code**, not by writing to a separate stream. Stderr is reserved for diagnostics and warnings outside the JSON contract.

A few commands deliberately break the envelope to emit raw bytes:

- `terminal get --screen` prints the terminal's retained PTY window directly to stdout (ANSI escapes intact, no JSON).
- `agent messages` (without `--follow`) prints a JSON array; with `--follow` it prints JSON-lines.
- `events watch` prints JSON-lines (one event object per line).

```bash
# Pull just the tab id out of a successful tab open
leapmux control tab open --type agent --worker-id "$W" --workspace-id "$WS" \
  | jq -r '.data.tab_id'

# Detect failure and read the error code
if ! out=$(leapmux control agent get --tab-id "$T"); then
  echo "$out" | jq -r '.error.code'
fi
```

## Authentication

### How the transport is chosen

For each invocation, the CLI selects a transport in this order:

| Condition | Transport |
| --- | --- |
| `LEAPMUX_CONTROL_SOCK` is set (Worker-spawned) | Local IPC over that socket, presenting `LEAPMUX_CONTROL_TOKEN` as the `X-LeapMux-Token` header |
| `--hub <url>` flag or `LEAPMUX_HUB` env var is set | Hub client over HTTPS, presenting the stored bearer as `Authorization: Bearer <token>` |
| Neither | Error `not_logged_in`: "no `--hub` flag or `LEAPMUX_HUB` / `LEAPMUX_CONTROL_SOCK` env var; run `leapmux control auth login --hub <url>` or invoke from inside an agent" |

The default per-request HTTP timeout is 60 seconds.

### `leapmux control auth login`

Authorizes the CLI against a Hub and writes a credential file to disk. The `--hub` flag is required (or set `LEAPMUX_HUB`).

| Flag | Default | Purpose |
| --- | --- | --- |
| `--hub <url>` | `$LEAPMUX_HUB` | Hub base URL (required) |
| `--device-name <name>` | `$USER@$hostname` | Label for this copy of the CLI, shown in the account's connected-apps list |
| `--device-code` | `false` | Force the RFC 8628 device-code flow (headless / SSH / container) |
| `--scope <permissions>` | everything except `admin:*` | The permissions to ask for, space- or comma-separated |

The CLI is a registered app like any other, so a login is an ordinary OAuth 2.1 authorization. See [App Authorization](/docs/admin/app-authorization/) for how apps are registered and what they may ask for, and [OAuth API](/docs/reference/oauth-api/) for the wire contract.

**You verify your identity in the browser, not in the terminal.** Authorizing a credential needs an elevated session, so the consent page sends you through a verification prompt (password, passkey, or your identity provider) before it hands anything back. That elevation lasts {{< duration elevation-window >}}, so a second login in the same sitting does not ask again. See [Session elevation](/docs/admin/security/#session-elevation).

**Default flow (PKCE local redirect).** The CLI opens a loopback listener on `127.0.0.1`, prints the authorization URL with an instruction to open it in your browser, and tries to launch your browser automatically (`open` on macOS, `xdg-open` on Linux, the shell handler on Windows). You sign in on the Hub's web page; the Hub redirects back to the loopback listener to complete the exchange. The CLI waits up to **10 minutes** for the callback before failing with `{"error":{"code":"timeout",...}}`.

```bash
leapmux control auth login --hub https://leapmux.example.com
```

**Device-code flow (`--device-code`).** Use this on a headless box, over SSH, or inside a container where no browser is available. The CLI prints a verification URL and a short user code:

```bash
leapmux control auth login --hub https://leapmux.example.com --device-code
```

The output holds three things: the verification URL (`https://<hub>/oauth/device`), the user code, and a second link that carries the code in its query string.

The user code is six characters from an ambiguity-free alphabet (no `0`/`1`/`I`/`O`/`L`), displayed as `XXX-XXX`. You open the verification URL on any device, enter the code, and the CLI (which polls in the background) completes once you approve. The second link pre-fills the code so you can skip the typing. On success, both flows persist a credential file and emit:

```json
{
  "data": {
    "hub_url": "https://leapmux.example.com",
    "username": "alice",
    "user_id": "usr_...",
    "scope": "account:read account:write workspace:read workspace:write worker:read worker:admin agent:read agent:write terminal:read terminal:write file:read git:read git:write tunnel:open"
  }
}
```

### `--scope`: what the credential may do

An omitted `--scope` asks for everything you can do **except** administer the hub. So a routine login leaves no hub-administration credential on disk, and `leapmux control admin ...` refuses it even when your account is an administrator.

Name the permissions to ask for less, or to ask for administration:

```bash
# A credential that reads files and git state, and nothing else.
leapmux control auth login --hub https://leapmux.example.com --scope "file:read git:read"

# A credential that administers the hub. Only an administrator may grant it.
leapmux control auth login --hub https://leapmux.example.com --scope "admin:read admin:users admin:settings admin:workers admin:apps"
```

Both separators work. The wire format is space-delimited, so a shell needs quotes around it; a comma-separated list also works.

The **browser** decides. The consent page states in sentences what the credential will be able to do, and you approve or refuse there. The permission list in the response is what you actually granted, which is what the CLI records — and it may be wider than you asked for, because some permissions imply others. `file:read` implies `worker:read`, since reading a file means reaching the machine that holds it.

The whole vocabulary is in [App Authorization](/docs/admin/app-authorization/#permissions).

### Renewal and lifetime

The access token lives for {{< duration access-token >}}. The CLI renews it silently: before a call whose stored expiry is close, and once more if the hub refuses a call it expected to succeed. You do not run anything to renew.

Each renewal moves the {{< duration refresh-token >}} refresh window forward, but never past **{{< duration absolute-cap >}}** from the day you authorized the credential. After that the device signs in again. `auth status` reports both deadlines.

Logging in again on the same machine **revokes the credential it replaces**, so a re-login leaves no live secret behind.

### `leapmux control auth status` / `list` / `credentials` / `logout`

| Command | Flags | Output |
| --- | --- | --- |
| `auth status` | `--hub` | `{hub_url, username, user_id, expires, expired, refresh_expires, scope, token_id, hub_checked, is_admin}` for the specified Hub. Error `not_logged_in` if there is no credential. `expires` is the hour-long access token, which renews itself; `refresh_expires` is when the device must sign in again. `hub_checked` says whether the Hub confirmed the credential (`is_admin` from the Hub when it did, a `warning` when it could not be reached). |
| `auth list` | none | An array of `{hub_url, username, user_id, expires, scope}` for every Hub you have credentials for. |
| `auth credentials` | `--hub` | An array of `{id, client_id, client_name, installation_name, created_at, last_used_at, refresh_expires, expires, granted_scopes, client_verified, current}` for every credential the account holds. `client_name` is the app and `installation_name` is which copy of it. `current` marks the one this command uses. Each credential carries exactly one deadline: a renewing credential reports `refresh_expires`, and one minted with `--ttl` (an [Admin CLI](/docs/admin/admin-cli/#api-tokens) issuance) reports `expires`. A row with neither never expires. |
| `auth logout` | `--hub` | Best-effort revokes the token on the Hub, then deletes the local credential file. Emits `{hub_url}`. |

**`list` and `credentials` answer different questions.** `list` reads this machine's credential files — which Hubs this box can reach. `credentials` asks the Hub what the whole account holds — what else can reach your account, from anywhere. It is the same list the browser shows under **Preferences → Apps → Connected apps**, where you can also disconnect. See [Connected Apps](/docs/using/connected-apps/).

```bash
leapmux control auth list
leapmux control auth credentials --hub https://leapmux.example.com
leapmux control auth status --hub https://leapmux.example.com
leapmux control auth logout --hub https://leapmux.example.com
```

### Credential file location

Credentials are written one file per Hub:

```
<ConfigDir>/<hub-host>.json
```

`<ConfigDir>` resolves in this order:

1. `LEAPMUX_CONTROL_CONFIG_DIR` (used verbatim if set)
2. `$XDG_CONFIG_HOME/leapmux/control`
3. `~/.config/leapmux/control`

`<hub-host>` is the Hub's hostname, with `_<port>` appended when the URL carries a port (for example `leapmux.example.com_8443`). The file is written atomically with mode `0600`, in a directory created with mode `0700`. It contains the access token, the refresh token, both expiries, your user identity, the token id, and the permissions the credential holds.

{{< callout >}}
Point `LEAPMUX_CONTROL_CONFIG_DIR` at a per-job directory to keep CI credentials isolated and easy to discard.
{{< /callout >}}

## Worker-spawned environment variables

When a Worker spawns an agent or terminal (and remote control is enabled on that Worker), it injects this set of environment variables into the child process:

| Variable | When present | Meaning |
| --- | --- | --- |
| `LEAPMUX_CONTROL_SOCK` | always | Local-IPC socket URL the CLI talks to |
| `LEAPMUX_CONTROL_TOKEN` | always | Per-process bearer token |
| `LEAPMUX_CONTROL_USER_ID` | always | Authenticated user (informational; no flag reads it) |
| `LEAPMUX_CONTROL_WORKER_ID` | always | The host Worker |
| `LEAPMUX_CONTROL_TAB_ID` | when non-empty | The spawned tab's id |
| `LEAPMUX_CONTROL_TAB_TYPE` | when non-empty | `agent`, `terminal`, or `file` |
| `LEAPMUX_CONTROL_WORKING_DIR` | when non-empty | Working directory at spawn time |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | agents only | The agent's provider |

These variables become the **defaults** for the matching entity flags, so a script running inside an agent can call `leapmux control agent send --message "hi"` with no IDs at all — the current tab is inferred from `LEAPMUX_CONTROL_TAB_ID`.

Two IDs are deliberately **not** injected: the workspace id and the tile id. They are derived from the tab id at call time via the Hub's tab-locator RPC, so a script never targets a stale tile after somebody moves the tab.

{{< callout type="info" >}}
There is no "remote-enabled" flag or checkbox. Terminals and agents receive `LEAPMUX_CONTROL_*` automatically whenever the Worker has remote control enabled. Inherited `LEAPMUX_CONTROL_*` values are stripped before re-injection, so a Worker spawned from inside another agent gets a fresh context rather than its parent's. See [Terminals](/docs/using/terminals/) for the terminal side of this.
{{< /callout >}}

## Universal entity-ID flags

Almost every command needs to know which entity to act on. Rather than hand-roll flags per command, LeapMux exposes one uniform set, and a resolver derives the rest from whatever subset you provide.

| Flag | Env default | Notes |
| --- | --- | --- |
| `--tab-id` | `$LEAPMUX_CONTROL_TAB_ID` | The agent/terminal/file tab |
| `--tab-type` | (none) | `agent` or `terminal`; auto-detected when omitted. Not on `agent`/`terminal` commands, which pin the type; hidden on `tab list`, which reuses the flag as an output filter. |
| `--tile-id` | (none) | Derivable from `--tab-id` |
| `--workspace-id` | (none) | Derivable from `--tab-id` / `--tile-id` |
| `--worker-id` | `$LEAPMUX_CONTROL_WORKER_ID` | The host Worker |

There is no `--user-id` flag: the Hub takes the tenant from the authenticated session, so no command needs one.

### Which IDs you can omit

Supply the most specific ID you have and the Hub fills in the rest:

- `--tab-id` gives the tab's matched type, workspace, tile, and Worker;
- `--tile-id` gives its workspace.

So `--tab-id` alone is usually enough. `--workspace-id` and `--worker-id` sit at the end of these chains — nothing is derived from them, so pass one only when you have nothing more specific.

### Pinned tab type for agent and terminal commands

Commands under `agent ...` and `terminal ...` pin the tab type for you. As a safety measure, the `--tab-id` env default only fires when `$LEAPMUX_CONTROL_TAB_TYPE` matches the command's pinned type. That means `agent send` run from inside a *terminal* won't silently auto-target the terminal you're sitting in — you'd have to pass `--tab-id` explicitly. The generic `tab` group has no such restriction.

### Conflicts and missing IDs

- An **explicit** flag you typed always wins over an env-derived value, silently shadowing a disagreeing env default.
- Two **explicit** inputs that disagree on the same derived field are a hard error with the code `invalid_request`.
- If the resolver still can't satisfy a required field, you get `invalid_request`, and the message identifies each missing ID and the flags that can derive it.

Resolver-rejected input always uses code `invalid_request`; a transport/derivation failure (the RPC itself errored) surfaces as `resolve_failed`.

## `whoami` and `version`

```bash
leapmux control whoami          # who am I, where am I?
leapmux control version --hub https://leapmux.example.com
```

- `whoami` from inside an agent/terminal returns `{user_id, username, worker_id, tab_id, tab_type}`. From your laptop (Hub mode) it returns `{hub_url, user_id, username, is_admin}`.
- `version` always emits the CLI's `{cli:{version, commit, branch, build_time, formatted}}`; when `--hub` is set it also probes the Hub's unauthenticated version endpoint and adds `hub:{...}` (or a non-fatal `hub_error`).

## Workspace commands

| Command | Key flags | Output |
| --- | --- | --- |
| `workspace list` | (any entity flag, or none when authenticated) | Your workspaces |
| `workspace get` | `--workspace-id` (or `--tab-id`/`--tile-id`) | One workspace |
| `workspace create` | `--title` (required) | `{workspace_id}` |
| `workspace rename` | `--workspace-id`, `--title` (required) | `{workspace_id}` |
| `workspace delete` | `--workspace-id`, `--force` | Deletion + per-worker cleanup status |

```bash
leapmux control workspace create --title "Release 2.0"
```

`workspace list` returns the workspaces you own; workspace access is owner-only.

`workspace delete` cascades a Hub delete and then fans out worktree cleanup to every Worker that hosted tabs in the workspace, emitting `{workspace_id, worker_ids, status, cleanup:[...]}` where `status` is `ok` or `partial`.

If the *calling* tab lives in the workspace you're deleting, the [self-target guard](#self-target-guard) refuses unless you pass `--force`, which deletes the workspace even when the calling tab lives in it and therefore kills the caller's own PTY.

## Tab commands

The `tab` group is the generic open/close/list/rename surface across all three tab types (agent, terminal, file). Use it for lifecycle operations; use the `agent` and `terminal` groups for type-specific actions.

| Command | Key flags |
| --- | --- |
| `tab list` | `--workspace-id`, `--tab-type agent\|terminal\|file` (output filter) |
| `tab get` | `--tab-id` (type auto-detected) |
| `tab open` | `--type agent\|terminal\|file` (required) + type-specific flags + [placement flags](#placement-flags) |
| `tab close` | `--tab-id`, `--force`, `--worktree keep\|push\|discard` |
| `tab rename` | `--tab-id`, `--title` (required) |
| `tab move` | `--tab-id`, `--target-tile-id`, `--target-workspace-id` + [placement flags](#placement-flags) |

{{< callout type="info" >}}
On `tab list`, `--tab-type` is an **output filter**, not a resolver constraint. On `tab get`/`tab move`, omitting the type lets the resolver auto-detect it.
{{< /callout >}}

### Opening a tab

`tab open` requires `--type`. The remaining flags depend on the type.

**Agent (`--type agent`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--worker-id` | `$LEAPMUX_CONTROL_WORKER_ID` (required) | Host Worker |
| `--provider` | `$LEAPMUX_CONTROL_AGENT_PROVIDER` | Agent provider; if unset and the Worker has exactly one installed provider it is auto-picked. Zero → `no_providers_installed`; more than one → `ambiguous_provider`. |
| `--model` | provider default | Initial model |
| `--effort` | provider default | `low`/`medium`/`high`/`max` |
| `--permission-mode` | provider default | Initial permission mode |
| `--working-dir` | `$LEAPMUX_CONTROL_WORKING_DIR` | Where the agent runs |
| `--title` | auto | Tab title |
| `--initial-message` | (none) | First message to send |

**Terminal (`--type terminal`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--worker-id` | `$LEAPMUX_CONTROL_WORKER_ID` (required) | Host Worker |
| `--shell` | Worker default | Shell to launch |
| `--shell-start-dir` | working dir | Starting directory |

**File (`--type file`):**

| Flag | Default | Purpose |
| --- | --- | --- |
| `--path` | (required) | Absolute file path; registered Worker-side over the encrypted channel so the Hub never sees it |
| `--display-mode` | `0` | File-tab display mode |
| `--file-view-mode` | `0` | File view mode |

`tab open` emits `{tab_id, tab_type, workspace_id, worker_id, tile_id, position}` plus per-type extras such as `initial_message_warning` or `path`.

```bash
# Spin up a Claude Code agent in a worker's repo and send it a task
leapmux control tab open --type agent \
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

`--before`/`--after` take a **tab id** (not a rank). For those two, the destination tile is taken from the referenced tab's tile; if you also pass `--tile-id`/`--target-tile-id`, the two must agree. The flags `--first`, `--last`, `--before`, and `--after` are mutually exclusive, so the command refuses more than one of them. It also refuses a `--before`/`--after` tab id that no tab uses.

### Closing a tab

```bash
leapmux control tab close --tab-id "$T" --worktree push
```

| Flag | Purpose |
| --- | --- |
| `--force` | Self-target override: close even if the target is the calling tab |
| `--worktree keep\|push\|discard` | Worktree disposition (`remove` is a synonym for `discard`) |

`--worktree` is **required** when the close would remove the last tab for a worktree, or close the last tab on a non-worktree branch that has uncommitted or unpushed changes — omitting it then returns an `invalid_request` with the details. `--worktree push` runs `git push` and fails with `invalid_request` if the branch isn't pushable. `--worktree discard` fails with `invalid_request` when git refuses to remove the worktree — it is locked, or git no longer lists the path. The CLI runs this check before it tombstones the tab; the removal itself starts after the tab is gone.

The command emits `{tab_id, tab_type, tombstoned, worktree?, worker_close_error?}`, plus `inspect_hint`, `closed_subagent_tab_ids`, or `subagent_close_error` when they apply. The CLI inspects every tab type, file tabs included: a file tab holds a worktree open and sits on a branch exactly as an agent or terminal tab does. See [Worktrees and Branches](/docs/using/worktrees-and-branches/) for the disposition rules.

### Renaming and moving

```bash
leapmux control tab rename --tab-id "$T" --title "Reviewer"
leapmux control tab move --tab-id "$T" --target-tile-id "$DEST"
leapmux control tab move --tab-id "$T" --target-workspace-id "$OTHER_WS"
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
leapmux control tile split --tile-id "$TILE" --direction horizontal
leapmux control tile set-ratios --tile-id "$SPLIT" --ratios 1,2
```

### `layout`

```bash
# layout set takes only the tree node, so extract `.data.tree` from the get envelope.
leapmux control layout get --workspace-id "$WS" | jq '.data.tree' > layout.json
# edit layout.json ...
leapmux control layout set --workspace-id "$WS" --file layout.json
```

- `layout get` emits `{workspace_id, root_node_id, tree, tabs_by_tile}`.
- `layout set` requires exactly one of `--file PATH` or `--stdin`, and it accepts **only the tree node** — the value of the `tree` field, *not* the full `layout get` envelope. Feeding back the whole `{workspace_id, root_node_id, tree, tabs_by_tile}` object fails validation with `root: unrecognized kind`, because the top-level keys it expects are `kind`/`direction`/`ratios`/`rows`/`cols`/`row_ratios`/`col_ratios`/`children`. Extract the `tree` field first (e.g. `jq '.data.tree'`).
- `layout set` rewrites the entire tree in one batch and repoints every live tab onto the new tree's first leaf; the root node id never changes.

The input tree's `kind` accepts `leaf`/`split`/`grid` (uppercase and `NODE_KIND_*` forms too). A `split` needs at least 2 children and a `direction`; a `grid` needs `rows`/`cols` in `1..20` and exactly `rows*cols` children. Validation errors are path-anchored, e.g. "root.children[1].children[0]: SPLIT requires at least 2 children (got 1)".

If a tab races in during the rewrite, `layout set` retries once (it makes at most two attempts); persistent contention yields `{"error":{"code":"concurrent_modification",...}}`. The success envelope includes `attempts` (`1` normally, `2` after a retry).

## Self-target guard

Several destructive commands refuse to destroy the very tab you're calling from. The guard is anchored on `LEAPMUX_CONTROL_TAB_ID`, so it only matters when you call from inside an agent or terminal. It fires for:

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
leapmux control worker list --hub https://leapmux.example.com
```

### Worker TOFU pins

LeapMux pins each Worker's key on first connection (trust-on-first-use). The `worker pins` subgroup manages those pins from the CLI. All pins commands require `--hub` (or `$LEAPMUX_HUB`).

| Command | Key flags | Output |
| --- | --- | --- |
| `worker pins list` | `--hub` | Every pinned Worker (sorted by id) |
| `worker pins show` | `--worker-id` (defaults to `$LEAPMUX_CONTROL_WORKER_ID`) | One recorded pin; `not_found` if none |
| `worker pins remove` | `--worker-id` | Drops the pin so the next connect re-prompts; emits `{removed_worker_id}` |

Pins are stored at `<ConfigDir>/<hub-host>/pins.json` with mode `0644` (they are not secrets). For the Worker registration and approval lifecycle, see [Managing Workers](/docs/admin/managing-workers/).

## Agent commands

The `agent` group is the type-specific surface for agent tabs — use `tab open`/`close`/`list`/`rename` for lifecycle. Every agent command pins the agent tab type and needs at least one entity input.

| Command | Key flags | Output |
| --- | --- | --- |
| `agent send` | `--tab-id`, `--message "..."` or `--stdin` | `{agent_id}` |
| `agent interrupt` | `--tab-id`, `--reason "..."` | `{agent_id}` |
| `agent get` | `--tab-id` | Full agent state (model, status, provider, option groups, git status, ...) |
| `agent providers` | `--tab-id` / `--worker-id` | `[{name, aliases}]` for the Worker |
| `agent messages` | `--tab-id`, `--anchor`, `--cursor-seq`, `--limit`, `--follow` | A message page, or a stream with `--follow` |
| `agent set` | `--tab-id`, `--model`, `--effort`, `--permission-mode`, `--option key=value` | `{agent_id, applied:{...}}` |
| `agent send-control-response` | `--tab-id`, `--content "..."` | `{agent_id}` |

```bash
# Send a message and then tail the agent's reply stream
leapmux control agent send --tab-id "$T" --message "Refactor the auth module."
leapmux control agent messages --tab-id "$T" --follow
```

Notes:

- `agent send` requires one of `--message` or `--stdin`; passing neither is an `invalid_request`. If you pass both, `--message` wins and `--stdin` is ignored.
- `agent messages` returns the most recent page by default (`--anchor latest`). Pick a different page with `--anchor oldest` (the first messages in history), `--anchor before --cursor-seq N` (the page older than seq N), or `--anchor after --cursor-seq N` (the page newer than seq N). `--cursor-seq` is required for `before`/`after` and rejected for `latest`/`oldest`. Messages always come back ascending by seq.
- `agent messages --limit` defaults to 50, which is also the Hub's cap. Without `--follow` you get one page as a JSON array; with `--follow` you get the first page followed by new messages as JSON-lines, reconnecting automatically on transient drops. `--follow` exists **only** on `agent messages`, not on `events watch`. `--follow` cannot be combined with `--anchor oldest` or `--anchor before` (paging backward through history while tailing the live stream forward is contradictory); use `--anchor latest` (the default) or `--anchor after --cursor-seq N` with `--follow`.
- `agent set` applies model/effort/permission-mode and repeatable `--option key=value` provider options. Most settings (model, effort, permission-mode) apply live on providers that support it (e.g. Claude Code, Codex); changes a provider can't apply to the running process trigger a restart (e.g. switching effort back to auto). See [Coding Agents](/docs/using/coding-agents/) for the per-provider settings.
- `agent get`/`agent list` report every provider setting as one unified `option_groups` array (each entry `{id, label, current_value, options:[...], ...}`); `model`/`effort`/`permission_mode` stay as top-level convenience keys. There is no separate `extra_settings`/`available_models`/`available_option_groups` field -- read a provider option from `option_groups`, e.g. `leapmux control agent get --tab-id "$T" | jq '.data.option_groups[] | select(.id=="sandbox_policy") | .current_value'`.
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
printf 'ls -la\n' | leapmux control terminal send --tab-id "$T" --stdin
leapmux control terminal get --tab-id "$T" --screen
```

`terminal send` rejects an empty payload: pass `--data`, or `--stdin` with non-empty input. Use `--stdin` for binary, escape sequences, or pasted content. `terminal get` returns a metadata map by default (geometry, shell, working dir, git info, status); `--screen` prints the retained PTY window directly to stdout with ANSI intact. Terminals receive remote-control env vars automatically — see [Terminals](/docs/using/terminals/).

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
| `git status` | `--path <dir>` (defaults to `$LEAPMUX_CONTROL_WORKING_DIR`) | `{info, files}` |
| `git branches` | `--path <dir>` | Branch list |
| `git worktrees` | `--path <dir>` | Worktree list |
| `git read` | `--path <file>` (required), `--ref head\|staged` | `{ref, path, content}` |

`git status`/`branches`/`worktrees` default `--path` to the spawn's working dir; `git read` keeps `--path` required (it is a file path) and defaults `--ref` to `head`.

```bash
leapmux control git status            # uses $LEAPMUX_CONTROL_WORKING_DIR inside an agent
leapmux control git read --path src/main.go --ref staged
```

## Streaming events

`events watch` subscribes to a workspace's live event stream and prints one JSON object per line. The resolver fills missing entity IDs from any entity flag you provide.

```bash
leapmux control events watch --workspace-id "$WS"
```

The first line is always the bootstrap snapshot (`{"kind":"materialized",...}`). Subsequent lines carry one of these `kind` values:

| `kind` | Meaning |
| --- | --- |
| `materialized` | Bootstrap snapshot (always first) |
| `batch` | A batch of layout/tab operations |
| `batch_end` | Boundary after a committed batch |
| `resume_delta` | Resumed stream frames (reserved; not sent today) |
| `entity_materialized` | An entity (tab, node, floating window) became visible |
| `entity_removed` | An entity was removed |
| `presence` | Active-client presence changed for a workspace |
| `workspace_renamed` | A workspace title changed |
| `workspace_created` | A workspace was created |
| `workspace_deleted` | A workspace was deleted |
| `unknown` | An event the CLI doesn't project |

The command runs until you interrupt it (SIGINT/SIGTERM) or the stream closes. Errors surface as `rpc_failed` or `stream_error`.

{{< callout type="info" >}}
`events watch` streams **workspace/layout** events only (the CRDT user event stream). It has no `--include` source filter, no `--follow`, and no per-line `source` key. To tail an agent's chat, use `agent messages --follow` instead.
{{< /callout >}}

```bash
# React to tab removals in a workspace
leapmux control events watch --workspace-id "$WS" \
  | jq -c 'select(.kind == "entity_removed")'
```

## End-to-end examples

### Drive an agent from your laptop

```bash
export LEAPMUX_HUB=https://leapmux.example.com
leapmux control auth login --hub "$LEAPMUX_HUB"

WS=$(leapmux control workspace create --title "Bugfix" | jq -r '.data.workspace_id')
W=$(leapmux control worker list | jq -r '.data[0].id')

T=$(leapmux control tab open --type agent \
      --worker-id "$W" --workspace-id "$WS" \
      --provider "Claude Code" --working-dir /home/dev/project \
      --initial-message "Find and fix the failing test in ./pkg/auth." \
    | jq -r '.data.tab_id')

leapmux control agent messages --tab-id "$T" --follow
```

### A script running inside an agent

No login, no IDs — the spawn context supplies everything:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Inspect the repo we were spawned in
leapmux control git status | jq '.data.info'

# Open a sibling terminal on the same worker and run the build
TERM_TAB=$(leapmux control tab open --type terminal | jq -r '.data.tab_id')
printf 'make build\n' | leapmux control terminal send --tab-id "$TERM_TAB" --stdin
```

### Snapshot and restore a layout

```bash
# layout set takes only the tree node, so extract `.data.tree` from the get envelope.
leapmux control layout get --workspace-id "$WS" | jq '.data.tree' > before.json
leapmux control tile split --tile-id "$TILE" --direction vertical
# ... experiment ...
leapmux control layout set --workspace-id "$WS" --file before.json
```

## Sockets, `--hub unix:`/`npipe:`, and login

A `--hub` value may be a hub IPC listener (`unix:$HOME/.config/leapmux/hub/hub.sock` on Unix, `npipe:...` on Windows) as well as an http(s) URL. A socket hub URL is still the HUB peer: the CLI presents the same `Authorization: Bearer` credential as over http(s) — only the worker-IPC transport uses the internal `X-LeapMux-Token` header.

Login rules:

- **Solo needs no login over its local socket.** That socket is reachable only from the machine that runs the Hub, so ordinary commands need no credential there — which is the default `--hub` for solo. A TCP address asks for one once the `solo` account holds a password; see [Solo mode: a reduced threat model](/docs/admin/security/#solo-mode-a-reduced-threat-model). Solo still **authorizes apps**: a login there mints a scoped credential, and the Hub binds that credential's permissions rather than the solo account's. Both flows complete, because the consent pages accept the solo account. See [App Authorization](/docs/admin/app-authorization/#solo-mode).
- A **non-solo hub over a socket** uses `--device-code`: `leapmux control auth login --hub unix:...hub.sock --device-code` dials the socket for the token exchange while you approve in a browser against the hub's **public** origin (which the hub derives itself from its settings — not from `--hub`). The PKCE local-redirect flow is refused for socket URLs with a message pointing at `--device-code`, because a browser cannot reach a socket hub origin.

```bash
leapmux hub &
leapmux control auth login --hub unix:$HOME/.config/leapmux/hub/hub.sock --device-code
leapmux control admin settings list --hub unix:$HOME/.config/leapmux/hub/hub.sock
```

## Error code reference

| Code | Typical cause |
| --- | --- |
| `not_logged_in` | No usable credential (no `--hub`/`LEAPMUX_HUB`/`LEAPMUX_CONTROL_SOCK`, or no stored token) |
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
- **Spawned agent or terminal.** The Worker hands the process a private local-IPC socket (mode `0600`) and a per-process token scoped to the spawning user and tab. When the agent or terminal closes, the socket is torn down and the token is invalidated.
- **Cross-worker calls from a spawned agent.** Reaching a *sibling* Worker (an `agent`/`terminal`/`file`/`git` command with a different `--worker-id`) uses a Worker-minted delegation token: it carries your identity and is pinned to the machines it may reach. It is minted lazily on the first cross-worker call and revoked when the agent closes — so an agent that never reaches across Workers never holds one.

For the full trust model, what the Hub can and cannot see, and the encryption primitives, see [Security & Threat Model](/docs/admin/security/).

## See also

- [Coding Agents](/docs/using/coding-agents/) — providers, models, effort, control prompts, and resume that `agent` commands drive.
- [Terminals](/docs/using/terminals/) — PTY sessions, shells, and the automatic `LEAPMUX_CONTROL_*` injection.
- [Managing Workers](/docs/admin/managing-workers/) — Worker registration, approval, and TOFU pinning.
- [Admin CLI](/docs/admin/admin-cli/) — the `control admin` surface for hub administration.
- [Recovery](/docs/admin/recover/) — the offline break-glass tree.
- [Tabs and Layout](/docs/using/tabs-and-layout/) — the tile/split/grid model that `tile` and `layout` manipulate.
- [Worktrees and Branches](/docs/using/worktrees-and-branches/) — the worktree dispositions used by `tab close --worktree`.
