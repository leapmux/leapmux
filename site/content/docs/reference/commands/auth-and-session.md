---
title: "Auth & Session"
description: "Reference for leapmux auth, context, settings, whoami, doctor, skill, guide, version, and rpc commands."
type: docs
weight: 7
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Online commands on this page also take the [flags of every command](/docs/reference/commands/#flags-of-every-command); offline commands (`guide`, `context show`, `context clear`) do not contact a Hub and reject `--hub`. See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `--scope string` | local install scope: user (home directory) or project (current working directory) (one of: user, project; default: user) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

## `leapmux auth`

Sign this CLI in to a hub, and manage its credentials.

### `leapmux auth login`

Sign this CLI in to a hub through a browser.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux auth login [flags]
```

| Flag | Description |
|---|---|
| `--device-name string` | a label for this credential; the account's list of connected apps shows it (default: &lt;user&gt;@&lt;host&gt; of this machine) |
| `--device-code` | sign in with a code that you enter on any device with a browser (for SSH and containers) |
| `--scope string` | permissions to ask for, separated by spaces or commas, such as "file:read git:read" (default: everything except admin:*) |

```sh
leapmux auth login --hub https://hub.example.com

# Sign in from a machine without a browser.
leapmux auth login --hub https://hub.example.com --device-code
```

### `leapmux auth logout`

Revoke this CLI's credential for a hub and delete it.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux auth logout [flags]
```

```sh
leapmux auth logout --hub https://hub.example.com
```

### `leapmux auth list`

List the hubs that this machine holds a credential for.

_Aliases: `ls`._

```text
leapmux auth list [flags]
```

```sh
leapmux auth list
```

### `leapmux auth use`

Choose the hub that a command uses without --hub.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux auth use <hub> [flags]
```

```sh
leapmux auth use https://hub.example.com
```

### `leapmux auth status`

Show the user, the expiry and the permissions of the hub's credential.

_Does not support `-q` / `--quiet`._

```text
leapmux auth status [flags]
```

```sh
leapmux auth status
```

### `leapmux auth credentials`

List every credential that can reach your account on the hub.

```text
leapmux auth credentials [flags]
```

```sh
leapmux auth credentials
```

### `leapmux auth change-password`

Change your account password on the hub (prompts securely when --password is omitted).

_Mutates state._

```text
leapmux auth change-password [flags]
```

| Flag | Description |
|---|---|
| `--password string` | new password (prompted for when omitted, so it stays out of shell history) |

```sh
# Prompt for the new password interactively.
leapmux auth change-password
```

## `leapmux context`

Show, set, or clear persistent default hub, worker, workspace, and provider outside a tab.

Aliases: `ctx`.

### `leapmux context show`

Show the saved CLI defaults.

_Does not support `-q` / `--quiet`._

```text
leapmux context show [flags]
```

```sh
leapmux context show
```

### `leapmux context use`

Save default hub, worker, workspace, or provider for commands outside a tab.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux context use [flags]
```

| Flag | Description |
|---|---|
| `--hub string` | default hub URL (pass "" to clear) |
| `--worker string` | default worker id or name (pass "" to clear) |
| `--workspace string` | default workspace id or title (pass "" to clear) |
| `--provider string` | default agent provider (pass "" to clear) |

```sh
leapmux context use --worker build-box --workspace "Release 1.4"

# Clear a single saved default by passing an empty string.
leapmux context use --provider ""
```

### `leapmux context clear`

Remove saved CLI defaults (all by default, or specific fields with --field).

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux context clear [flags]
```

| Flag | Description |
|---|---|
| `--field string` | clear only this saved default; repeat for more than one (one of: hub, worker, workspace, provider; repeatable) |

```sh
leapmux context clear

# Clear only the saved default worker.
leapmux context clear --field worker
```

## `leapmux settings`

Read and change your account preferences on the hub.

Aliases: `pref`, `prefs`.

### `leapmux settings list`

List your account settings with their effective values.

_Aliases: `ls`._

```text
leapmux settings list [flags]
```

```sh
leapmux settings list
```

### `leapmux settings get`

Show one account setting, its value, and its schema.

```text
leapmux settings get <key> [flags]
```

```sh
leapmux settings get appearance
```

### `leapmux settings set`

Update an account setting from JSON or a bare value.

_Mutates state._

```text
leapmux settings set <key> <value> [flags]
```

```sh
leapmux settings set appearance '{"theme":"dark"}'
```

### `leapmux settings reset`

Reset an account setting back to its default value.

_Mutates state._

```text
leapmux settings reset <key> [flags]
```

```sh
leapmux settings reset appearance
```

## `leapmux whoami`

Show the identity and tab context that this CLI acts as.

_Does not support `-q` / `--quiet`._

```text
leapmux whoami [flags]
```

```sh
leapmux whoami

# Inside a tab, print the id of this tab.
leapmux whoami --json | jq -r .data.tab_id
```

## `leapmux doctor`

Check local daemons, Hub connectivity, auth, pins, and online Workers.

_Does not support `-q` / `--quiet`._

```text
leapmux doctor [flags]
```

```sh
leapmux doctor

# Inspect diagnostics as JSON.
leapmux doctor --json
```

## `leapmux skill`

Inspect and install LeapMux agent skills locally or on a Worker.

Aliases: `skills`.

### `leapmux skill list`

List bundled LeapMux agent skills and their install state.

_Aliases: `ls`._

```text
leapmux skill list [flags]
```

| Flag | Description |
|---|---|
| `--target string` | target directory: agents (~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt; (default: agents) |
| `--scope string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux skill list

# Check installed skill state on a remote Worker.
leapmux skill list --worker name:build-box

# Inspect project-scoped skills in the current repository.
leapmux skill list --scope project
```

### `leapmux skill show`

Print SKILL.md or companion reference documents for a bundled skill.

```text
leapmux skill show <skill> [flags]
```

| Argument | Description |
|---|---|
| `<skill>` | bundled skill name (see `leapmux skill list`) |

| Flag | Description |
|---|---|
| `--references` | list companion reference names bundled with this skill |
| `--reference string` | print one companion reference document instead of SKILL.md |

```sh
leapmux skill show leapmux
leapmux skill show leapmux-review

# List companion reference documents for a skill.
leapmux skill show leapmux-review --references

# Print one companion reference document.
leapmux skill show leapmux-review --reference review-schemas
```

### `leapmux skill status`

Show installed vs. bundled skill state and Worker auto-sync status.

```text
leapmux skill status [flags]
```

| Flag | Description |
|---|---|
| `--target string` | target directory: agents (~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt; (default: agents) |
| `--scope string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux skill status
leapmux skill status --worker name:build-box --json
```

### `leapmux skill install`

Install bundled LeapMux SKILL.md playbooks (plus companion references and Codex metadata) into standard coding-agent skill directories so agents (Claude Code, Codex, OpenCode, Gemini CLI, Antigravity, Cursor, and others) can invoke them directly.

By default, installs into ~/.agents/skills (universal user scope discovered by Codex, OpenCode, Gemini CLI, Antigravity, and Cursor). Pass --target claude|codex|cursor|gemini|dir:&lt;path&gt; or --scope project to target another location. Pass --worker &lt;worker&gt; to enable and sync skills on a remote Worker over E2EE (with optional --auto-sync to keep them updated on Worker startup).

Installing via the leapmux CLI always matches the installed binary offline; installing via "npx skills add leapmux/leapmux" tracks the main branch.

Locally edited or unmanaged skill files are never overwritten unless --force is passed.

_Aliases: `add` · Mutates state._

```text
leapmux skill install [skill]... [flags]
```

| Argument | Description |
|---|---|
| `[skill]...` | bundled skill names to install (or pass --all) |

| Flag | Description |
|---|---|
| `--target string` | install target: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt;; repeat for multiple (repeatable) |
| `--scope string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--all` | select every bundled skill |
| `--force` | overwrite locally modified or unmanaged skill files |
| `--auto-sync` | with --worker, keep enabled skills refreshed automatically on Worker startup |
| `--no-auto-sync` | with --worker, disable automatic skill refresh on Worker startup |

```sh
# Install all bundled skills into ~/.agents/skills.
leapmux skill install --all

# Install specific skills into the current repo.
leapmux skill install leapmux leapmux-review --scope project

# Enable and auto-sync all skills on a remote Worker.
leapmux skill install --all --worker name:build-box --auto-sync
```

### `leapmux skill uninstall`

Remove LeapMux-managed agent skills locally or from a Worker.

_Aliases: `rm`, `remove` · Mutates state._

```text
leapmux skill uninstall [skill]... [flags]
```

| Argument | Description |
|---|---|
| `[skill]...` | bundled skill names to remove (or pass --all) |

| Flag | Description |
|---|---|
| `--target string` | install target: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt;; repeat for multiple (repeatable) |
| `--scope string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--all` | select every bundled skill |
| `--force` | also remove locally modified or unmanaged skill files |

```sh
leapmux skill uninstall leapmux-council
leapmux skill uninstall --all --worker name:build-box
```

## `leapmux guide`

Print the guide for an agent that drives LeapMux.

```text
leapmux guide [topic] [flags]
```

| Argument | Description |
|---|---|
| `[topic]` | a topic ID from --topics |

| Flag | Description |
|---|---|
| `--topics` | list the available guide topics |

```sh
leapmux guide
leapmux guide --topics --json
leapmux guide targets
```

## `leapmux version`

Print the version of this CLI, and of the hub and worker when connected.

_Does not support `-q` / `--quiet`._

```text
leapmux version [flags]
```

```sh
leapmux version
leapmux version --hub https://hub.example.com
```

## `leapmux rpc`

Call one procedure of the hub or of a Worker, and print its response. Use it for a call that no other command makes.

The request is protobuf JSON: the second argument, "-" to read it from standard input, or {} when the command line omits it. The command refuses a field that the request type does not declare, before it sends anything. The response prints as protobuf JSON with the field names of the .proto files, indented. --json prints it as the data of one envelope instead. A field that holds its default value does not print, as protobuf JSON states.

The command passes the request directly to the Hub or Worker. The receiving service applies its own authorization and validation. Check what the procedure does before calling one that changes state.

The procedures are the methods of the services in proto/leapmux/v1. A procedure of a hub service runs on the hub. A procedure of a Worker service needs --worker, the Worker that serves the call. A procedure that streams does not run here. "leapmux events" prints the events of agents and terminals, and "leapmux agent messages --follow" prints the messages of an agent.

Inside a LeapMux tab, the socket of the tab relays these hub procedures alone:

- leapmux.v1.SystemService/GetSystemInfo
- leapmux.v1.UserCRDT/GetMaterialized
- leapmux.v1.UserCRDT/SubmitOps
- leapmux.v1.UserCRDT/UpdatePresence
- leapmux.v1.WorkerManagementService/GetWorker
- leapmux.v1.WorkerManagementService/GetWorkerEnrollmentRevision
- leapmux.v1.WorkerManagementService/ListWorkers
- leapmux.v1.WorkspaceService/CreateWorkspace
- leapmux.v1.WorkspaceService/DeleteWorkspace
- leapmux.v1.WorkspaceService/GetWorkspace
- leapmux.v1.WorkspaceService/ListTabs
- leapmux.v1.WorkspaceService/ListWorkspaces
- leapmux.v1.WorkspaceService/LocateTab
- leapmux.v1.WorkspaceService/LocateTile
- leapmux.v1.WorkspaceService/ReadLayoutEvents
- leapmux.v1.WorkspaceService/RenameWorkspace

Pass --hub &lt;url&gt; to call another hub procedure with your own login.

_Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux rpc <procedure> [request] [flags]
```

| Argument | Description |
|---|---|
| `<procedure>` | The procedure, as "&lt;service&gt;/&lt;method&gt;", such as leapmux.v1.WorkspaceService/ListTabs. The "leapmux.v1." prefix and a leading "/" are optional. |
| `[request]` | The request in protobuf JSON, or "-" to read it from standard input. Without it, the request is {}. |

| Flag | Description |
|---|---|
| `--worker string` | the Worker that serves a Worker procedure: an id, a unique id prefix, "name:&lt;name&gt;" or "self" |

```sh
# List the tabs of one workspace.
leapmux rpc leapmux.v1.WorkspaceService/ListTabs '{"workspace_ids": ["87a19d5f-4838-4e34-9f0f-56130f0a62b1"]}'

# Read one agent from the Worker of this tab.
leapmux rpc leapmux.v1.WorkerAgentService/ListAgents '{"tab_ids": ["4cbb3f34-3c1e-4f0e-9a51-2a6a3b1f0d7e"]}' --worker self

# Send the request that a file holds, and print the response as one JSON envelope.
leapmux rpc WorkerTerminalService/ListAvailableShells - --worker name:build-box --json < request.json
```
