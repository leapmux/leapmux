---
title: "Control Command Reference"
description: "Every leapmux command with its arguments, flags and examples, generated from the command tree of the binary."
type: docs
weight: 2
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

This page lists every control command of `leapmux` with its arguments, its flags and its examples. The binary prints the same text with `--help`, and `leapmux commands --json` prints it for a program. [Control CLI](/docs/using/control-cli/) explains the concepts: how a command selects its target, what it prints, how it fails, and how it signs in.

## Flags of every command

Every control command takes these output flags, and every online command takes `--hub` (offline commands such as `guide`, `context show`, and `context clear` do not contact a Hub). Commands marked below as not supporting `-q` / `--quiet` reject `--quiet` with exit code 2.

| Flag | Description |
|---|---|
| `--color string` | when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto) |
| `--fields string` | comma-separated columns or fields to print |
| `--format string` | output format (one of: text, json, ndjson, tsv; default: text) |
| `--hub string` | hub URL (default: tab socket, else $LEAPMUX_HUB, else context/default credential, else local solo/dev Hub) |
| `--json` | print the result as one JSON envelope per line |
| `--no-header` | omit the header row from table and TSV output |
| `-q, --quiet` | print only the ids of the result, one per line |
| `-w, --wide` | do not truncate table columns |

## `leapmux tree`

Show a hierarchical overview of sections, workspaces, tiles, agents, terminals and files.

Aliases: `overview`, `ps`.

```text
leapmux tree [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | show only this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | show only tabs hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--archived` | include archived workspaces |
| `--flat` | print a flat table of tabs instead of a hierarchy |

```sh
leapmux tree

# Show only one workspace.
leapmux tree --workspace self

# Include archived workspaces.
leapmux tree --archived

# Print a flat table of every tab.
leapmux tree --flat
```

## `leapmux workspace`

List, create, organize, export, apply, and delete workspaces.

Aliases: `ws`.

### `leapmux workspace list`

List your workspaces.

Aliases: `ls`.

```text
leapmux workspace list [flags]
```

| Flag | Description |
|---|---|
| `--archived` | include archived workspaces |

```sh
leapmux workspace list

# Include archived workspaces.
leapmux workspace list --archived

# Print only the ids.
leapmux workspace list -q
```

### `leapmux workspace get`

Show one workspace (default: the workspace of this tab).

```text
leapmux workspace get [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux workspace get
leapmux workspace get "title:Release 1.4"
```

### `leapmux workspace create`

Create a workspace.

This command changes state.

```text
leapmux workspace create <title> [flags]
```

```sh
# Create a workspace and keep its id.
W=$(leapmux workspace create "Release 1.4" -q)
```

### `leapmux workspace rename`

Rename a workspace.

This command changes state.

```text
leapmux workspace rename <workspace> <title> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux workspace rename self "Release 1.5"
```

### `leapmux workspace archive`

Archive a workspace (moves it to the Archived section and pauses its tabs).

This command changes state.

```text
leapmux workspace archive <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux workspace archive 9f3kQ2aB
```

### `leapmux workspace unarchive`

Restore an archived workspace back to an active section.

This command changes state.

```text
leapmux workspace unarchive <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--section string` | destination active section (default: "In progress") |

```sh
leapmux workspace unarchive 9f3kQ2aB
leapmux workspace unarchive 9f3kQ2aB --section "In progress"
```

### `leapmux workspace move`

Move a workspace to another sidebar section or reorder it within a section.

Aliases: `mv`.

This command changes state.

```text
leapmux workspace move <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--section string` | destination sidebar section (id, prefix, or name) (required) |
| `--first` | put the workspace first in the section |
| `--last` | put the workspace last in the section |
| `--before string` | put the workspace before this workspace in the section |
| `--after string` | put the workspace after this workspace in the section |

```sh
leapmux workspace move 9f3kQ2aB --section "Client Work" --last
leapmux workspace move 9f3kQ2aB --section "In progress" --first
```

### `leapmux workspace delete`

Delete a workspace and close every tab in it.

Aliases: `rm`.

This command changes state.

```text
leapmux workspace delete <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | delete the workspace even when this tab lives in it, which ends this tab's process too |

```sh
leapmux workspace delete 9f3kQ2aB
```

### `leapmux workspace export`

Export a workspace's main tile tree, floating windows, and live tab definitions
(agents with provider/model/options/working_dir, terminals, and file viewers)
into a declarative YAML or JSON template that "workspace apply" can reproduce.

Does not support `-q` / `--quiet`.

```text
leapmux workspace export [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `-o, --output string` | write the exported template to this file instead of stdout |
| `--style string` | text serialization format when --json is not set (one of: yaml, json; default: yaml) |

```sh
leapmux workspace export self > workspace.yaml
leapmux workspace export 9f3kQ2aB --output team-dev.yaml
leapmux workspace export 9f3kQ2aB --style json
```

### `leapmux workspace apply`

Apply a declarative workspace template produced by "workspace export" (or
written by hand). Reads YAML or JSON from &lt;file&gt; ("-" reads stdin).

Creates a new workspace unless --workspace targets an existing one, applies the
main tile layout and any floating windows, and opens each declared agent,
terminal, and file tab in its mapped tile.

This command changes state.

```text
leapmux workspace apply <file> [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | apply the template onto this existing workspace instead of creating a new one |
| `--title string` | override the workspace title from the template |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--dry-run` | validate the template and print the planned layout and tabs without mutating anything |

```sh
leapmux workspace apply workspace.yaml
leapmux workspace apply workspace.yaml --title "Feature Sprint" --worker name:build-box
leapmux workspace apply workspace.yaml --dry-run
```

## `leapmux section`

List, create, rename, move, and delete sidebar sections.

### `leapmux section list`

List your sidebar sections and how many workspaces each holds.

Aliases: `ls`.

```text
leapmux section list [flags]
```

```sh
leapmux section list
leapmux section list --json
```

### `leapmux section create`

Create a custom workspace section.

This command changes state.

```text
leapmux section create <name> [flags]
```

| Flag | Description |
|---|---|
| `--sidebar string` | sidebar to place the section in (one of: left, right; default: left) |

```sh
leapmux section create "Client Work"
leapmux section create "Monitoring" --sidebar right
```

### `leapmux section rename`

Rename a custom section.

This command changes state.

```text
leapmux section rename <section> <name> [flags]
```

```sh
leapmux section rename "Client Work" "Active Clients"
```

### `leapmux section move`

Move a section to another sidebar or reorder it.

Aliases: `mv`.

This command changes state.

```text
leapmux section move <section> [flags]
```

| Flag | Description |
|---|---|
| `--sidebar string` | destination sidebar (default: keep current sidebar) (one of: left, right) |
| `--first` | put the section first in the sidebar |
| `--last` | put the section last in the sidebar |
| `--before string` | put the section before this section (id, prefix, or name) |
| `--after string` | put the section after this section (id, prefix, or name) |

```sh
leapmux section move "Client Work" --first
leapmux section move "Client Work" --sidebar right --last
```

### `leapmux section delete`

Delete a custom section (moves its workspaces to In progress).

Aliases: `rm`.

This command changes state.

```text
leapmux section delete <section> [flags]
```

```sh
leapmux section delete "Client Work"
```

## `leapmux tab`

List, close, rename, move and reveal tabs of every type, and set their status.

### `leapmux tab list`

List your tabs with their titles and states.

Aliases: `ls`.

```text
leapmux tab list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the tabs of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | list only the tabs hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--type string` | list only the tabs of this type (one of: agent, terminal, file, image) |
| `--state string` | list only the tabs in this state (such as idle, working, waiting_for_user, ready, exited) |

```sh
leapmux tab list

# List the agent tabs of the workspace that this tab lives in.
leapmux tab list --workspace self --type agent
```

### `leapmux tab get`

Show one tab (default: this tab).

```text
leapmux tab get [tab] [flags]
```

| Argument | Description |
|---|---|
| `[tab]` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab get
leapmux tab get 87a19d5f
```

### `leapmux tab close`

Close one or more tabs and stop what runs in them.

Aliases: `rm`.

This command changes state.

```text
leapmux tab close <tab>... [flags]
```

| Argument | Description |
|---|---|
| `<tab>...` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux tab close 87a19d5f --worktree keep
```

### `leapmux tab rename`

Rename an agent or terminal tab.

This command changes state.

```text
leapmux tab rename <tab> <title> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab rename self "Release notes"
```

### `leapmux tab move`

Move a tab to another tile or workspace, or reorder it.

Aliases: `mv`.

This command changes state.

```text
leapmux tab move <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--tile string` | destination tile |
| `--workspace string` | destination workspace; without --tile, the tab lands on its first tile |
| `--first` | put the tab first on the destination tile |
| `--last` | put the tab last on the destination tile (default) |
| `--before string` | put the tab right before this tab |
| `--after string` | put the tab right after this tab |

```sh
# Move a tab to the first tile of another workspace.
leapmux tab move 87a19d5f --workspace "title:Release 1.4"

# Put a tab before another tab of the same tile.
leapmux tab move 87a19d5f --before 4cbb3f34
```

### `leapmux tab reveal`

Show a tab in every LeapMux window that you have open: each window switches
to the tab's workspace, and the tab becomes the active tab of its tile, as a
click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own
state, so a window that you open later shows what it showed before, and a
window that does not know the tab yet ignores the command.

This command changes state.

```text
leapmux tab reveal <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux tab reveal self
```

### `leapmux tab open-image`

Open an image from an agent message in its own tab.

This command changes state.

```text
leapmux tab open-image <agent> <seq> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<seq>` | Positive message sequence number in the agent's transcript. |

| Flag | Description |
|---|---|
| `--index int` | 0-based image index within the message |
| `--title string` | tab title (default: Image #&lt;seq&gt;) |
| `--dir string` | working directory context of the tab (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
leapmux tab open-image 87a19d5f 42
leapmux tab open-image 87a19d5f 42 --index 1 --title "Homepage screenshot"
```

### `leapmux tab status`

Report and inspect status entries on a tab.

#### `leapmux tab status set`

Show a short status beside a tab's title, in the tab bar and in the sidebar of
every LeapMux window that you have open. A window that you open later shows it
too. The most recently set status is visible.

Give each reporter a --key to keep its status separate. Setting a key again
replaces its previous status. Without --key, the command uses "default".

--progress adds a progress ring, from 0 to 100. Without it, the status shows
no progress. --tone colors the status: neutral (the default), success,
warning or error.

The status stays until "tab status clear" removes its key, the tab closes, or
the tab's Worker stops. Use "tab status list" to see all keys. The Worker
reports its limits on the status text and keys when it refuses a request.

This command changes state.

```text
leapmux tab status set <tab> <text> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |
| `<text>` | A short text, such as "Running the tests", or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--key string` | which status entry to change (default: default) |
| `--progress int` | how far the work is, from 0 to 100 (default: no progress) |
| `--tone string` | the color of the status (one of: neutral, success, warning, error; default: neutral) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab status set self "Running the tests"

# Report progress for a separate task.
leapmux tab status set 87a19d5f "Migrating rows" --key migration --progress 40

# Mark the tab when the work fails.
leapmux tab status set self "Tests failed" --tone error
```

#### `leapmux tab status clear`

Remove one or more status entries from a tab.

This command changes state.

```text
leapmux tab status clear <tab> [key]... [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |
| `[key]...` | One or more status keys to clear (default: the default key, or --all for every key). |

| Flag | Description |
|---|---|
| `--key string` | which status entry to change (default: default) |
| `--all` | remove every status entry on the tab |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab status clear self
leapmux tab status clear 87a19d5f --key migration

# Remove every status entry on the tab.
leapmux tab status clear self --all
```

#### `leapmux tab status list`

List a tab's status entries, with the visible one first (default: this tab).

Aliases: `ls`.

```text
leapmux tab status list [tab] [flags]
```

| Argument | Description |
|---|---|
| `[tab]` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux tab status list
leapmux tab status list 87a19d5f
```

## `leapmux agent`

Start, message, watch and configure coding agents.

Aliases: `ag`.

### `leapmux agent list`

List your agents with their states.

Aliases: `ls`.

```text
leapmux agent list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the agents of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | list only the agents hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--state string` | list only the agents in this activity state (one of: idle, working, waiting_for_user) |
| `--provider string` | list only the agents of this provider (such as claude, codex) |
| `--parent string` | list only the direct child subagents of this parent agent (an id, prefix, title, or "self") |
| `--all` | include inactive or non-active agent process statuses in the table |

```sh
leapmux agent list

# List the agents of the workspace that this tab lives in.
leapmux agent list --workspace self
```

### `leapmux agent get`

Show an agent's state and settings (default: this agent).

```text
leapmux agent get [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--options` | include every option group with its choices |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent get

# Show every option that the agent offers.
leapmux agent get 87a19d5f --options
```

### `leapmux agent open`

Start a new agent in a new tab.

--resume continues an earlier session of the provider in the new tab. "agent
sessions" lists the sessions that it takes, for the same --provider, --worker
and --dir.

--request-id makes a retry safe. A second run with the same request id and
the same arguments opens no second agent: it prints the agent that the first
run opened, and it queues the --message no second time. A run with the same
request id and other arguments fails with conflict.

This command changes state.

```text
leapmux agent open [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux agent providers" lists them (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--model string` | model |
| `--effort string` | reasoning effort |
| `--permission-mode string` | permission mode |
| `--option string` | provider option as key=value; repeat it for more than one; "agent get --options" lists them (repeatable) |
| `--title string` | tab title (default: the worker picks one) |
| `--name string` | unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `--message string` | queue a first message with the agent creation ("-" reads stdin) |
| `--file string` | read the first message from this file ("-" reads stdin) |
| `--attach string` | attach this file to the first message; repeat it for more than one (repeatable) |
| `--resume string` | resume this earlier session of the provider; "agent sessions" lists them |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait until the agent settles (and print its reply when a message was sent) |
| `--all` | with --wait and a message, print every reply, not only the last one |
| `--timeout duration` | with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit (default: 0s) |
| `--output-schema string` | validate the reply against a JSON Schema (file path, inline JSON object, or "-" for stdin); requires --wait |
| `--schema-retries int` | with --output-schema, maximum corrective follow-up turns on validation failure (0-10) (default: 2) |
| `--dir string` | working directory of the agent (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
# Start an agent beside this tab and keep its id.
A=$(leapmux agent open --provider claude --message "Run the tests" -q)

# Start an agent in a new git worktree.
leapmux agent open --worktree fix-flaky-spec --base main

# Continue an earlier session that "agent sessions" lists.
leapmux agent open --provider claude --resume 4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90

# Open an agent that a retry of the same command does not open twice.
leapmux agent open --provider codex --message "Review the diff" --request-id nightly-review-2026-11-19
```

### `leapmux agent send`

Send a message to an agent.

The text is the second argument, "-" to read it from stdin, or --file. A
message that starts with "-" needs "--" before it, so that a misspelled flag
never reaches the agent as text.

The agent's input queue holds the message until the agent reads it.
"agent queue list" shows the queue.

A message that a process in an agent tab sends reaches the recipient as that
agent's message, not as the user's. The recipient reads it after a line that
states the sending agent and that the message is not from its operator, and
LeapMux shows the sender beside it. The Worker states the sender from the
tab's own credentials, so no flag sets or hides it. A message that an agent
sends to itself, such as a reminder that --at holds, reaches it as its own
message too. A message from a terminal tab, or from outside LeapMux, reaches
the agent as the user's.

--attach attaches a file, once for each file. A message with an attachment
needs no text. The CLI refuses a file that it cannot read, and a message
whose text and attachments hold more than 10 MiB together. The Worker infers
the type of each file from its name and its content, and it refuses a file
that the agent's provider cannot read with invalid_argument. The error
states the file.

--at holds the message in the queue until a time:
  - An RFC 3339 time with its offset, such as 2026-11-20T09:00:00+09:00 or
    2026-11-20T00:00:00Z. The CLI assumes no time zone, so a time without an
    offset fails.
  - "+" and a duration from now, such as +10m or +1h30m.
A new schedule must lie in the future. The result states it in UTC.

--request-id makes a retry safe. A second run with the same request id, the
same agent and the same message queues nothing new, and with --wait it
prints the reply to the message of the first run. This holds also after
someone edited the message in the queue: the retry repeats the message that
the first run sent, and the queue keeps the edit. It holds also after
someone removed the message from the queue: for 7 days after the removal, a
retry queues nothing, and with --wait it fails with not_found and exits 6.
A run with the same request id and a message other than the first run's
fails with conflict, even when its message equals the edited one. A retry of
a scheduled message repeats its RFC 3339 timestamp, even after that time has
passed. A relative --at changes on each run and cannot use --request-id.

With --wait, a new message to an agent waiting for permission exits 5.
An explicit --request-id can still match an input the Worker recorded.
The command waits until the agent settles and prints the last reply as the
transcript shows it, or every reply with --all. It exits 6 if the agent
waits for permission or --timeout passes after the Worker acknowledges
the input. The output reports the last confirmed delivery state.
The timeout counts from the Worker's enqueue acknowledgment.

This command changes state.

```text
leapmux agent send <agent> [text] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the message from this file ("-" reads stdin) |
| `--attach string` | attach this file; repeat it for more than one (repeatable) |
| `--at string` | hold the message until this time: an RFC 3339 time with its offset, or + and a duration from now, such as +10m |
| `--steer` | inject the message into the agent's active turn immediately (requires a steerable active turn) |
| `--preempt` | interrupt the agent's active turn so this message dispatches next |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait until the agent settles after the message, then print its last reply |
| `--all` | with --wait, print every reply after the message, not only the last one |
| `--timeout duration` | with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit (default: 0s) |
| `--output-schema string` | validate the reply against a JSON Schema (file path, inline JSON object, or "-" for stdin); requires --wait |
| `--schema-retries int` | with --output-schema, maximum corrective follow-up turns on validation failure (0-10) (default: 2) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent send 87a19d5f "Run the tests"

# Send the contents of a file.
leapmux agent send 87a19d5f --file prompt.md

# Send a message that starts with a dash.
leapmux agent send 87a19d5f -- "-v is the verbose flag"

# Ask a question and print the answer.
leapmux agent send 87a19d5f "Which tests fail?" --wait --timeout 30m

# Attach a screenshot and a log.
leapmux agent send 87a19d5f "Why does the page look like this?" --attach screenshot.png --attach console.log

# Send the message in ten minutes.
leapmux agent send 87a19d5f "Check the deploy" --at +10m

# Send a message that a retry of the same command does not send twice.
leapmux agent send 87a19d5f "Run the release checklist" --request-id release-2026-11-19
```

### `leapmux agent wait`

Wait until each agent reaches a requested state, then print the state of each.

Repeat --until to accept several states. Each agent can reach a different
requested state. A state reported at any moment during the wait counts,
including the agent's initial state.
"settled" is "idle" or "waiting_for_user": the agent works no more, because it
finished or because it waits for an answer to a permission prompt. An agent
whose input queue holds a message that it will read is neither idle nor
settled yet, as an agent that "agent open --message" just started.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, or an agent closed before it reached a requested state.
It prints the state of each agent in both cases.

```text
leapmux agent wait <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |

| Flag | Description |
|---|---|
| `--until string` | a state to wait for; repeat to accept alternatives (one of: settled, idle, working, waiting_for_user; default: [settled]; repeatable) |
| `--any` | end the wait as soon as one agent reaches a requested state |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent wait 87a19d5f

# Wait for an agent to start working or ask for input.
leapmux agent wait 87a19d5f --until working --until waiting_for_user

# Wait until the first of two agents waits for an answer, for 10 minutes at most.
leapmux agent wait 87a19d5f 5c2e0b91 --until waiting_for_user --any --timeout 10m
```

### `leapmux agent requests`

List the pending control requests of agents: permission prompts, plans
to approve, questions, MCP elicitation forms and extension dialogs.

Each request prints with its handle. "agent approve", "agent deny" and
"agent answer" take the agent and the handle, or a unique prefix of the
handle. A request that the agent replaces gets a new handle, so an answer
never reaches a request that you did not read.

The text form prints each request in full, with the numbers that --option
takes. --json prints each prompt as data.

Aliases: `req`.

```text
leapmux agent requests [agent]... [flags]
```

| Argument | Description |
|---|---|
| `[agent]...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. Without one, the command lists the requests of every agent. |

| Flag | Description |
|---|---|
| `--workspace string` | scope listing or target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent requests

# List the requests of one agent, as JSON.
leapmux agent requests 87a19d5f --json
```

### `leapmux agent approve`

Approve a pending request:

  - A permission prompt: its one-time approval. Choose another option, such
    as an approval for the session, with "agent answer --option".
  - A plan: the approval. --permission-mode and --clear-context set how the
    agent implements it.
  - An MCP elicitation: the accept, with the form's default values. Fill a
    form with "agent answer --field".
  - A confirm dialog: the confirmation.

When [request] is omitted and the agent has exactly one pending request, it
approves that request automatically. A single globally unique request handle
also resolves its owning agent automatically.

An agent cannot answer its own requests.

This command changes state.

```text
leapmux agent approve <agent> [request] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent approve 87a19d5f 3kQ9xZ1b

# Approve the agent's sole pending request.
leapmux agent approve 87a19d5f

# Approve a plan, and implement it in a fresh context.
leapmux agent approve 87a19d5f 3kQ9 --permission-mode acceptEdits --clear-context
```

### `leapmux agent deny`

Refuse a pending request:

  - A permission prompt: its one-time refusal, with --message as the reason.
  - A plan: the refusal, with --message as what to change.
  - A question: the refusal to answer, with --message as the reason.
  - An MCP elicitation: the decline.
  - A dialog: the refusal of a confirm dialog, or the cancel of another.

When [request] is omitted and the agent has exactly one pending request, it
refuses that request automatically. A single globally unique request handle
also resolves its owning agent automatically.

An agent cannot answer its own requests.

This command changes state.

```text
leapmux agent deny <agent> [request] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--message string` | the reason of the refusal, which the agent reads |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent deny 87a19d5f 3kQ9xZ1b --message "Run the unit tests only"
```

### `leapmux agent answer`

Answer a pending request with any answer that it offers. "agent requests"
numbers the options of each request. When [request] is omitted and the agent
has exactly one pending request, it answers that request automatically.

  - A question: --option chooses an option, and a text argument is a
    typed answer. A multiple-choice question takes both: the answer then
    holds the chosen options and the text. With several questions, write
    each value as "&lt;question&gt;:&lt;value&gt;", such as --option 2:1 or
    "1:Use the cache". A question that no --option answers keeps the
    options that the agent selected for it, which "agent requests" shows
    as its default. With no value at all, the command sends those
    defaults, which must answer every question that needs an answer.
  - A permission prompt: --option chooses any option, such as an approval
    for the session. --message gives the reason of a refusing option.
  - A plan: --option chooses one of its choices. --message gives what to
    change, and --permission-mode and --clear-context apply to an approval.
  - An MCP elicitation: --field fills the form, once for each field, or
    once for each value of a list field. A field that "agent requests"
    lists with a condition takes a value only when the condition holds,
    and a field that it does not list keeps its default. --option chooses
    an accept choice.
  - An input or editor dialog: the text.

An agent cannot answer its own requests.

This command changes state.

```text
leapmux agent answer <agent> [request] [text]... [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |
| `[text]...` | A typed answer, or "-" to read it from standard input. With several questions, write "&lt;question&gt;:&lt;text&gt;". |

| Flag | Description |
|---|---|
| `--option string` | an option by its number, or "&lt;question&gt;:&lt;number&gt;" with several questions; repeat it for more than one (repeatable) |
| `--field string` | a form field as &lt;name&gt;=&lt;value&gt;; repeat it for more than one (repeatable) |
| `--message string` | the reason of a refusing option or choice, which the agent reads |
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
# Choose the second option of a question.
leapmux agent answer 87a19d5f 3kQ9 --option 2

# Choose two options of a multiple-choice question, and type one more answer.
leapmux agent answer 87a19d5f 3kQ9 --option 1 --option 3 "Metrics"

# Answer two questions.
leapmux agent answer 87a19d5f 3kQ9 --option 1:2 "2:Use the staging database"

# Approve a command for the rest of the session.
leapmux agent answer 87a19d5f 3kQ9 --option 2

# Fill an MCP form.
leapmux agent answer 87a19d5f 3kQ9 --field repository=leapmux --field private=true
```

### `leapmux agent interrupt`

Stop the current turn of one or more agents.

This command changes state.

```text
leapmux agent interrupt <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--reason string` | a reason that the agent's transcript records |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent interrupt 87a19d5f
```

### `leapmux agent clear`

Clear an agent's context. The command queues a clear. When the agent reads
it, the Worker stops the agent's process and starts the provider again in a
fresh session, in the same tab. The transcript keeps the earlier messages.

The clear waits in the agent's input queue behind the inputs before it, and
it waits for the current turn to end, so it never cuts off a reply. "agent
queue list" shows it, and "agent queue remove" takes it back while it waits.
A clear also restarts an agent whose start failed.

A subagent refuses a clear.

This command changes state.

```text
leapmux agent clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent clear 87a19d5f

# Clear the context, then give the agent its next task.
leapmux agent clear 87a19d5f && leapmux agent send 87a19d5f "Review the diff of the last commit"
```

### `leapmux agent compact`

Ask an agent to compact its context: the provider replaces the conversation
so far with a summary, which frees room in the context window. The command
queues the request. A provider without a compaction command of its own
receives "/compact" as a message.

The request waits in the agent's input queue behind the inputs before it,
and it waits for the current turn to end. "agent queue list" shows it, and
"agent queue remove" takes it back while it waits.

A subagent refuses a compaction.

This command changes state.

```text
leapmux agent compact <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent compact 87a19d5f
```

### `leapmux agent set`

Change an agent's model, effort, permission mode or another option.

This command changes state.

```text
leapmux agent set <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--model string` | model id |
| `--effort string` | effort id |
| `--permission-mode string` | permission mode |
| `--option string` | provider option as key=value; repeat it for more than one; "agent get --options" lists them (repeatable) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent set 87a19d5f --model opus --effort high

# Set an option that only one provider has.
leapmux agent set 87a19d5f --option sandbox=workspace-write
```

### `leapmux agent messages`

Print an agent's messages, or follow new ones.

A message that another agent sent prints its sender after its source, and
--json states the sender as sender.

Aliases: `msg`.

```text
leapmux agent messages [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--anchor string` | page to print (one of: latest, oldest, before, after; default: latest) |
| `--cursor-seq int` | the seq that --anchor before or after starts from; the page excludes it |
| `--limit int` | messages per page (default: 50) |
| `--follow` | print new messages as they arrive, until interrupted |
| `--raw` | print raw provider JSON payloads in text mode |
| `--full` | do not truncate long message lines in text mode |
| `--markdown` | render messages as Markdown sections |
| `--summary` | append current goal, todos, and active background tasks after the messages |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent messages 87a19d5f

# Print new messages as they arrive, one JSON envelope per line.
leapmux agent messages 87a19d5f --follow --json
```

### `leapmux agent queue`

Inspect, edit, steer, preempt, retry, reorder, pause, and clear an agent's input queue.

Aliases: `q`.

#### `leapmux agent queue list`

List the inputs that wait in an agent's input queue, first to last: the
messages that "agent send" sent, the clears and the compactions.

A message that a process in another agent's tab sent is an agent_message.
FROM states the agent that sent it, and --json states it as sender.

Each input has a state:
  - queued: the input waits for its turn.
  - dispatching: the agent reads the input now.
  - awaiting_echo: the agent took the input into its running turn, and
    LeapMux waits for the agent to confirm it.
  - failed: the input did not reach the agent. The error states why.
  - delivery_uncertain: the input may not have reached the agent.

The queue holds while it is paused, while its first input failed or may not
have reached the agent, and while LeapMux has its first input open in an
editor. The result states a pause and its reason, and the command states
each other cause on stderr.

-q prints the id of each input.

Aliases: `ls`.

```text
leapmux agent queue list [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue list 87a19d5f

# Print the queue as data, with the reorder segment of each input.
leapmux agent queue list 87a19d5f --json
```

#### `leapmux agent queue remove`

Remove one or more inputs from an agent's input queue. The agent never reads them.

An input that the agent already reads stays: the command fails with
conflict. A removal also ends an edit of the input that LeapMux has open.

A retry of the "agent send --request-id" run that queued the input queues
nothing for 7 days after the removal.

Aliases: `rm`.

This command changes state.

```text
leapmux agent queue remove <agent> <input>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<input>...` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue remove 87a19d5f 3kQ9xZ1b

# Remove two queued inputs in one call.
leapmux agent queue remove 87a19d5f 3kQ9xZ1b 9Lm2Pq7c
```

#### `leapmux agent queue move`

Move an input to another place in an agent's input queue. --before, --first
or --last gives the place.

An input moves only among the inputs of its reorder segment, as in LeapMux:
  - The inputs that the agent reads as soon as it can form one segment, and
    the inputs that --at holds until one time form another. An input never
    moves past an input of another time.
  - An input that the agent already reads is locked. It stays in its place,
    and the inputs on its two sides form two segments.
--first and --last mean the first and the last place of the segment. "agent
queue list --json" states the segment of each input as reorder_segment.

Aliases: `mv`.

This command changes state.

```text
leapmux agent queue move <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--before string` | move the input before this input, as an id or a unique prefix |
| `--after string` | move the input after this input, as an id or a unique prefix |
| `--first` | move the input to the first place of its segment |
| `--last` | move the input to the last place of its segment |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
# Move an input before another one.
leapmux agent queue move 87a19d5f 3kQ9xZ1b --before 9Lm2Pq7c

# Make an input the next one that the agent reads.
leapmux agent queue move 87a19d5f 3kQ9xZ1b --first
```

#### `leapmux agent queue get`

Read the full text and metadata of one queued input.

Does not support `-q` / `--quiet`.

```text
leapmux agent queue get <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue get 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue edit`

Replace the text or schedule of a queued input.

This command changes state.

```text
leapmux agent queue edit <agent> <input> [text] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |
| `[text]` | Replacement message text, or "-" for stdin. |

| Flag | Description |
|---|---|
| `--file string` | read replacement text from this file ("-" reads stdin) |
| `--at string` | set a new schedule (RFC 3339 with offset, or +duration) |
| `--clear-schedule` | remove the input's scheduled send-not-before time |
| `--takeover` | take over an active edit lease held by another client |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue edit 87a19d5f 3kQ9xZ1b "Run only unit tests"
leapmux agent queue edit 87a19d5f 3kQ9xZ1b --at +15m
```

#### `leapmux agent queue steer`

Inject a queued input into the agent's active turn immediately.

This command changes state.

```text
leapmux agent queue steer <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, steers the head steerable input. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue steer 87a19d5f
leapmux agent queue steer 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue preempt`

Cancel the active turn so a queued input dispatches next.

This command changes state.

```text
leapmux agent queue preempt <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, preempts with the head preemptable input. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue preempt 87a19d5f
leapmux agent queue preempt 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue advance`

Run one queue advance step (steer, preempt, or resume as appropriate).

This command changes state.

```text
leapmux agent queue advance <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue advance 87a19d5f
```

#### `leapmux agent queue retry`

Retry a failed or delivery-uncertain queued input.

This command changes state.

```text
leapmux agent queue retry <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, retries the failed or uncertain head input. |

| Flag | Description |
|---|---|
| `--confirm-uncertain` | confirm retrying an input whose earlier delivery state was uncertain |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue retry 87a19d5f
leapmux agent queue retry 87a19d5f 3kQ9xZ1b --confirm-uncertain
```

#### `leapmux agent queue clear`

Remove all unlocked inputs waiting in an agent's queue.

This command changes state.

```text
leapmux agent queue clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue clear 87a19d5f
```

#### `leapmux agent queue pause`

Pause an agent's input queue. The agent finishes its current turn, and then
reads no queued input until the queue resumes. New inputs still join the
queue.

LeapMux pauses a queue by itself too: when you interrupt the agent, when the
agent stops, and when an input does not reach the agent. "agent queue list"
states why a queue is paused.

This command changes state.

```text
leapmux agent queue pause <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue pause 87a19d5f
```

#### `leapmux agent queue resume`

Resume an agent's input queue, whatever paused it, so that the agent reads
the next input when it can.

A failed input keeps its failure. While the first input failed, or while
LeapMux has it open in an editor, the queue still holds, and the command
says so on stderr. Remove the failed input with "agent queue remove", or
retry it with "agent queue retry".

This command changes state.

```text
leapmux agent queue resume <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent queue resume 87a19d5f
```

### `leapmux agent goal`

Read, set, pause, resume, or clear an agent's standing session goal.

#### `leapmux agent goal get`

Show an agent's current standing goal (default: this agent).

Does not support `-q` / `--quiet`.

```text
leapmux agent goal get [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent goal get
leapmux agent goal get 87a19d5f
```

#### `leapmux agent goal set`

Set a standing session goal on a running agent.

This command changes state.

```text
leapmux agent goal set <agent> <objective> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<objective>` | The standing goal objective text. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent goal set 87a19d5f "Fix all failing tests and keep CI green"
```

#### `leapmux agent goal pause`

Pause an agent's active standing goal.

This command changes state.

```text
leapmux agent goal pause <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent goal pause 87a19d5f
```

#### `leapmux agent goal resume`

Resume a paused standing goal.

This command changes state.

```text
leapmux agent goal resume <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent goal resume 87a19d5f
```

#### `leapmux agent goal clear`

Clear an agent's standing goal.

This command changes state.

```text
leapmux agent goal clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent goal clear 87a19d5f
```

### `leapmux agent todos`

List an agent's current to-do items (default: this agent).

```text
leapmux agent todos [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent todos
leapmux agent todos 87a19d5f
```

### `leapmux agent tasks`

List an agent's background tasks and subagent runs (default: this agent).

```text
leapmux agent tasks [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--active` | show only running or pending background tasks |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent tasks
leapmux agent tasks 87a19d5f --active
```

### `leapmux agent usage`

Show an agent's live cost, context usage, rate limits, and running tool (default: this agent).

Does not support `-q` / `--quiet`.

```text
leapmux agent usage [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent usage
leapmux agent usage 87a19d5f --json
```

### `leapmux agent tree`

Show an agent and all its descendant subagents, including unopened ones (default: this agent).

```text
leapmux agent tree [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent tree
leapmux agent tree 87a19d5f
```

### `leapmux agent open-tab`

Open or revive a subagent's transcript as a tab in a workspace.

This command changes state.

```text
leapmux agent open-tab <subagent> [flags]
```

| Argument | Description |
|---|---|
| `<subagent>` | The subagent id (from "agent tree" or "agent tasks"). |

| Flag | Description |
|---|---|
| `--workspace string` | workspace to open the tab in (default: this tab's workspace, or sole active workspace) |
| `--tile string` | tile to open the tab in (default: the workspace's first tile) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux agent open-tab 5e9a01bc
leapmux agent open-tab 5e9a01bc --workspace self
```

### `leapmux agent startup`

Respond to a pending agent startup prompt (for example, OS Keychain confirmation).

#### `leapmux agent startup approve`

Proceed with a pending agent startup prompt.

This command changes state.

```text
leapmux agent startup approve <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--prompt-id string` | prompt id to answer (default: the agent's current pending startup prompt) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent startup approve 87a19d5f
```

#### `leapmux agent startup cancel`

Cancel a pending agent startup prompt.

This command changes state.

```text
leapmux agent startup cancel <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--prompt-id string` | prompt id to answer (default: the agent's current pending startup prompt) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent startup cancel 87a19d5f
```

### `leapmux agent retry-turn`

Stop a slow turn and retry it on the offered faster model.

This command changes state.

```text
leapmux agent retry-turn <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--turn-id string` | turn id from the retry offer (default: auto-read from live session info) |
| `--model string` | model id from the retry offer (default: auto-read from live session info) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent retry-turn 87a19d5f
leapmux agent retry-turn 87a19d5f --turn-id turn-1 --model gpt-5-fast
```

### `leapmux agent approve-review`

Record approval for an action denied by the automatic reviewer.

This command changes state.

```text
leapmux agent approve-review <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--message-id string` | transcript message id containing the denied review (required) |
| `--review-id string` | review id within that message (required) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent approve-review 87a19d5f --message-id msg-1 --review-id rev-1
```

### `leapmux agent message`

Fetch a single transcript message by sequence number.

Does not support `-q` / `--quiet`.

```text
leapmux agent message <agent> <seq> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<seq>` | Positive message sequence number. |

| Flag | Description |
|---|---|
| `--raw` | print raw provider JSON payload in text mode |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent message 87a19d5f 42
leapmux agent message self 42 --json
```

### `leapmux agent span`

Fetch all transcript messages belonging to a tool or subagent span.

Does not support `-q` / `--quiet`.

```text
leapmux agent span <agent> <span-id> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `<span-id>` | Tool or subagent span id (for example, toolu_01...). |

| Flag | Description |
|---|---|
| `--session-id string` | optional provider session id to disambiguate spans across sessions |
| `--raw` | print raw provider JSON payloads in text mode |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent span 87a19d5f toolu_01AbCd
```

### `leapmux agent marks`

List transcript jump marks (user prompts, plans, errors) and sequence bounds (default: this agent).

```text
leapmux agent marks [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent marks
leapmux agent marks 87a19d5f
```

### `leapmux agent export`

Export an agent's complete transcript as Markdown, text, or JSON (default: this agent).

Does not support `-q` / `--quiet`.

```text
leapmux agent export [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--style string` | export style (one of: markdown, text, json; default: markdown) |
| `-o, --output string` | write exported transcript to this file instead of stdout |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent export 87a19d5f --output transcript.md
leapmux agent export self --style text
```

### `leapmux agent recoveries`

List agents whose input queue is paused waiting for process recovery approval.

```text
leapmux agent recoveries [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | filter recovery targets by agent provider |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux agent recoveries
leapmux agent recoveries --provider claude
```

### `leapmux agent recover`

Approve process recovery for an agent paused for login or process recovery.

This command changes state.

```text
leapmux agent recover <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent recover 87a19d5f
```

### `leapmux agent search`

Search agent conversation transcripts across one or more workers.

Plaintext transcripts stay on each worker and are searched locally over E2EE;
the hub never sees transcript contents. Omit --worker and --agent to fan out
across every online worker in parallel.

```text
leapmux agent search <query> [flags]
```

| Flag | Description |
|---|---|
| `--agent string` | search only this agent (id, prefix, title, or "self") |
| `--provider string` | search only agents using this provider (such as claude, codex) |
| `--since string` | include only messages at or after this RFC 3339 time or lookback duration (such as 24h, 30m) |
| `--workspace string` | search only agents belonging to this workspace |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--regex` | treat &lt;query&gt; as an RE2 regular expression |
| `--case-sensitive` | match case-sensitively (default is case-insensitive) |
| `--limit int` | maximum number of hits to return (max 200) (default: 50) |

```sh
leapmux agent search "race condition"
leapmux agent search "v[0-9]+\.[0-9]+" --regex --provider claude --since 24h
leapmux agent search "timeout" --agent 87a19d5f --limit 20
```

### `leapmux agent fork`

Clone an existing agent's provider, model, effort, permission mode, provider
options, and working directory into a new agent tab.

Pass --worktree &lt;branch&gt; to fork the agent directly into a fresh git worktree,
or override individual settings (--model, --effort, --permission-mode, --option,
--dir, --title, --name).

This command changes state.

```text
leapmux agent fork <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--model string` | override model id |
| `--effort string` | override reasoning effort |
| `--permission-mode string` | override permission mode |
| `--option string` | override provider option as key=value; repeat for more than one (repeatable) |
| `--title string` | tab title (default: "&lt;source title&gt; (fork)") |
| `--name string` | unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `--message string` | queue a first message on the forked agent ("-" reads stdin) |
| `--file string` | read the first message from this file ("-" reads stdin) |
| `--attach string` | attach this file to the first message; repeat for more than one (repeatable) |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait until the forked agent settles (and print its reply when a message was sent) |
| `--all` | with --wait and a message, print every reply, not only the last one |
| `--timeout duration` | with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit (default: 0s) |
| `--output-schema string` | validate the reply against a JSON Schema (file path, inline JSON object, or "-" for stdin); requires --wait |
| `--schema-retries int` | with --output-schema, maximum corrective follow-up turns on validation failure (0-10) (default: 2) |
| `--dir string` | override working directory (default: source agent's working directory) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
leapmux agent fork 87a19d5f
leapmux agent fork 87a19d5f --worktree alt-approach --base main --message "Try an alternative implementation"
leapmux agent fork self --model opus --name reviewer
```

### `leapmux agent request-status`

Inspect the durable Worker state of a previous "agent send" by its --request-id
(whether explicit or auto-generated).

Reports whether the request is not_found, queued, dispatching, waiting_for_user,
in_progress, completed, interrupted, failed, or unproved, together with any
replies recorded for that input.

```text
leapmux agent request-status <agent> <request-id> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--all` | include every reply message for this request, not only the last one |

```sh
leapmux agent request-status 87a19d5f release-2026-11-19
leapmux agent request-status 87a19d5f 550e8400-e29b-41d4-a716-446655440000 --json
```

### `leapmux agent providers`

List the agent providers that a worker offers.

```text
leapmux agent providers [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux agent providers
leapmux agent providers --worker name:build-box
```

### `leapmux agent sessions`

List the earlier sessions of a provider in one directory, newest first: the
sessions that "agent open --resume" continues with the same --provider,
--worker and --dir.

The Worker lists the sessions that LeapMux started and the sessions that the
provider's own store holds, such as a session that you started in a terminal.
It leaves out each session that an open agent runs, because a second process
on one session damages the session. It returns only the newest sessions, up
to a limit of its own.

A session's title is empty when the provider stores none.

```text
leapmux agent sessions [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--dir string` | the directory that the sessions ran in (default: $LEAPMUX_CONTROL_WORKING_DIR) |

```sh
# List the sessions of this tab's provider in this tab's directory.
leapmux agent sessions

# List the Claude Code sessions of a directory on another worker.
leapmux agent sessions --provider claude --worker name:build-box --dir ~/src/app

# Keep the id of the newest session, for agent open --resume.
S=$(leapmux agent sessions --provider claude -q | head -1)
```

### `leapmux agent close`

Close one or more agent tabs and stop their processes.

Aliases: `rm`.

This command changes state.

```text
leapmux agent close <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux agent close 87a19d5f --worktree keep
```

### `leapmux agent rename`

Rename an agent tab or set its unique --name slug.

This command changes state.

```text
leapmux agent rename <agent> [title] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--name string` | set a unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent rename self "Release notes"

# Assign a unique short slug so commands can target the agent by name.
leapmux agent rename 87a19d5f --name reviewer
```

### `leapmux agent reveal`

Show a tab in every LeapMux window that you have open: each window switches
to the tab's workspace, and the tab becomes the active tab of its tile, as a
click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own
state, so a window that you open later shows what it showed before, and a
window that does not know the tab yet ignores the command.

This command changes state.

```text
leapmux agent reveal <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux agent reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux agent reveal self
```

## `leapmux terminal`

Open, type into, read and resize terminals.

Aliases: `term`.

### `leapmux terminal list`

List your terminals.

Aliases: `ls`.

```text
leapmux terminal list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the terminals of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | list only the terminals hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--state string` | list only the terminals in this status (one of: starting, ready, startup_failed, exited) |

```sh
leapmux terminal list
```

### `leapmux terminal get`

Show a terminal's status, size and directory (default: this terminal).

```text
leapmux terminal get [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal get
leapmux terminal get 4cbb3f34
```

### `leapmux terminal open`

Open a new terminal in a new tab. Without --workspace and --tile, the new
tab opens beside the tab that runs leapmux.

--command runs a command in the original interactive shell after its startup
files run. On Unix, use Bash, zsh, fish, sh, Dash, Elvish, Nushell, Xonsh,
or Yash. PowerShell also works.
If Elvish has a startup file, its directory must allow the Worker to create
a temporary link. The Worker reports a startup failure if the shell cannot
acknowledge the handoff.
A restart of the terminal does not run it again. If the Worker stops after
claiming the command, recovery reports an unverified outcome without running
it again. The command is the value of --command, or "-" to read it from stdin.
Read its output with "terminal read", or wait for it with "terminal wait".

This command changes state.

```text
leapmux terminal open [flags]
```

| Flag | Description |
|---|---|
| `--command string` | evaluate this command in the original shell after startup ("-" reads stdin) |
| `--shell string` | shell to start (default: the worker's default shell) |
| `--start-dir string` | directory the shell starts in (default: --dir) |
| `--title string` | tab title (default: the worker picks one) |
| `--wait` | wait after opening until --exit, --text, --regex, or --change holds |
| `--exit` | wait until the shell of the terminal ends, and print its exit code |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | wait until a line of the terminal matches this regular expression, in RE2 syntax |
| `--change` | wait until the visible screen text changes, and print the updated screen |
| `--after-offset int` | with --change, return immediately if screen_end_offset already exceeds this byte offset |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--dir string` | working directory of the terminal (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
# Open a terminal beside this tab and keep its id.
T=$(leapmux terminal open -q)

# Open a terminal in a new git worktree.
leapmux terminal open --worktree try-fix --dir ~/src/app

# Run the tests in a new terminal and wait until its shell exits.
leapmux terminal open --command 'make test; exit' --wait --exit
```

### `leapmux terminal send`

Type text into a terminal, then press keys. The text reaches the shell as it
is, with no Enter added: add --key enter, or use "terminal run". The text is
the second argument, "-" to read it from stdin, or --file. A text that starts
with "-" needs "--" before it.

--key presses a key after the text. Repeat it to press several keys in order.
The Worker encodes each key for the modes that the program in the terminal
enabled, as the browser does, so "up" moves the cursor in a shell and in a
full-screen program alike. A modifier on a cursor, editing or function key
takes xterm's encoding, such as ESC [1;5D for ctrl-left. Key names: enter,
tab, escape or esc, backspace, space, up, down, right, left, home, end,
insert, delete, pageup, pagedown, f1 to f12, or one printable ASCII character,
after any of ctrl-, alt- and shift-, such as ctrl-c, alt-b, shift-tab or
ctrl-left; the names are case-insensitive, and a character keeps its case.

--paste sends the text as the browser sends a paste: each line ending becomes
a carriage return, and when the program enabled bracketed paste, the text
arrives between the paste markers, so the program inserts it instead of
running each line. A paste end marker inside the text is removed, so the text
cannot end its own paste.

The text and the keys travel in one request, so no other input can arrive
between them.

This command changes state.

```text
leapmux terminal send <terminal> [text] [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the bytes from this file ("-" reads stdin) |
| `--key string` | press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order (repeatable) |
| `--paste` | send the text as a paste, between bracketed-paste markers when the program enabled them |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal send 4cbb3f34 $'make test\n'

# Stop the program in the foreground.
leapmux terminal send 4cbb3f34 --key ctrl-c

# Choose the second entry of a menu.
leapmux terminal send 4cbb3f34 --key down --key enter

# Paste a file into the program, as the browser pastes it.
leapmux terminal send 4cbb3f34 --paste --file snippet.py

# Type the bytes of a file, such as escape sequences.
leapmux terminal send 4cbb3f34 --file keys.bin
```

### `leapmux terminal run`

Run a command in a terminal: send the command line and press Enter, in one
request, so no other input can arrive between them. The command is the second
argument, "-" to read it from stdin, or --file. The line endings at its end are
dropped, because the command presses Enter itself.

The command line arrives as a paste, as "terminal send --paste" sends it. A
line editor that enabled bracketed paste therefore inserts it as it is: an
abbreviation, an automatic closing bracket or a vi key binding cannot change
it. A program without bracketed paste reads it as typed text. To type the
command key by key in any case, run
"terminal send &lt;terminal&gt; &lt;command&gt; --key enter".

By default the command returns as soon as the input is written. Pass --wait
with --exit, --text, or --regex to wait until that condition holds, or read
output later with "terminal read".

This command changes state.

```text
leapmux terminal run <terminal> [command] [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |
| `[command]` | The command line, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the command line from this file ("-" reads stdin) |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait after running until --exit, --text, --regex, or --change holds |
| `--exit` | wait until the shell of the terminal ends, and print its exit code |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | wait until a line of the terminal matches this regular expression, in RE2 syntax |
| `--change` | wait until the visible screen text changes, and print the updated screen |
| `--after-offset int` | with --change, return immediately if screen_end_offset already exceeds this byte offset |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal run 4cbb3f34 'make test'

# Run the command line that stdin holds.
echo 'go test ./...' | leapmux terminal run 4cbb3f34 -

# Run a command and wait for a sentinel line.
leapmux terminal run 4cbb3f34 'make test; echo DONE' --wait --regex '^DONE$' --timeout 10m
```

### `leapmux terminal read`

Print what a terminal shows, as plain text. The CLI replays the bytes that
the Worker retains through a terminal emulator and applies recorded size
changes in order. The emulator is a Go port of xterm.js, which the browser
uses. Colors and other attributes do not print.

Each row loses its trailing blanks, and the empty rows at the end of the
screen do not print, so a short output does not end in empty lines.

--scrollback also prints the lines above the screen: at most 1000, as in the
browser, and only those that the bytes the Worker retains still hold. A
full-screen program on the alternate screen has no scrollback, as in the
browser, so --scrollback adds nothing there. --lines N keeps the last N lines
of what the command prints otherwise.

--json adds the cursor, whose row and column count from 0 at the top left of
the screen, and alternate_screen, which is true while a full-screen program
uses the alternate screen. --raw prints the retained bytes as they are, with
their escape sequences, so that "less -R" shows them.

Aliases: `cat`.

```text
leapmux terminal read [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--lines int` | print only the last N lines |
| `--scrollback` | also print the lines above the screen |
| `--raw` | print the retained bytes as they are, with their escape sequences |
| `-f, --follow` | stream live output until Ctrl-C or shell exit |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal read 4cbb3f34

# Print the last 200 lines, the scrollback included.
leapmux terminal read 4cbb3f34 --scrollback --lines 200

# Page through the retained bytes with their colors.
leapmux terminal read 4cbb3f34 --raw | less -R

# Follow live terminal output until Ctrl-C or shell exit.
leapmux terminal read 4cbb3f34 --follow
```

### `leapmux terminal attach`

Attach this terminal interactively to a LeapMux terminal over the encrypted
Worker channel. Keystrokes travel straight to the remote PTY, output streams
live to stdout, and local window resizes forward automatically.

Press Ctrl-] (ASCII 0x1d, or the key set by --detach) to detach cleanly
without closing the remote shell.

Does not support `-q` / `--quiet`.

```text
leapmux terminal attach [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--detach string` | key sequence that detaches without closing the remote shell (e.g. ctrl-], ctrl-b, ctrl-a) (default: ctrl-]) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal attach 4cbb3f34

# Attach with Ctrl-b as the detach key.
leapmux terminal attach 4cbb3f34 --detach ctrl-b
```

### `leapmux terminal ps`

List foreground and background processes running inside one or more terminals.

```text
leapmux terminal ps [terminal...] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]...` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--all` | inspect every open terminal instead of a single target |
| `--workspace string` | inspect terminals in this workspace (implies --all when no targets are given) |

```sh
leapmux terminal ps
leapmux terminal ps 4cbb3f34

# Inspect every terminal in the workspace.
leapmux terminal ps --all
```

### `leapmux terminal wait`

Wait until a terminal meets one condition, then print what met it:

  - --exit waits until the shell of the terminal ends, and prints its exit
    code.
  - --text waits until a line of the terminal holds the text.
  - --regex waits until a line of the terminal matches the regular
    expression, in RE2 syntax.
  - --change waits until the visible screen text changes (event-driven over
    the live Worker output stream, with no polling), and prints the updated
    screen. Pass --after-offset with screen_end_offset from a prior
    "terminal read --json" or "terminal wait --change --json" to return
    immediately if the terminal already produced output since that offset.

The CLI replays the output of the terminal through the emulator of "terminal
read", so a line is the text that the terminal shows: an escape sequence, or
a text that arrives in two parts, cannot break a match. The match is per
line. A line that the terminal wraps at its width counts as one line, and it
loses its trailing blanks, as "terminal read" prints it, so ^ and $ match at
its ends. The command prints the line that matched. When several lines match
at once, it prints the lowest one.

The text that the screen shows when the wait begins counts, so a wait for a
text that the terminal already shows ends at once. Each line that appears
later counts too, even one that scrolls off the screen before the CLI reads
it. The wait follows size changes and applies them in order after reconnect.
Use --scrollback to include the retained history from before the wait.
To wait for the output of a new command only, use the default screen scope
and clear the screen first, or wait for a line that the command text does not
show. In the second example, the echoed command line holds "echo DONE".
The line of output matches '^DONE$'.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, the shell ended before the terminal showed the
text, or the shell failed to start. --json prints the status of the terminal
in each case, and the exit code of the shell once it ended.

--exit ends at once for a shell that ended before the wait began, and prints
its exit code, which the Worker keeps. When the Worker holds no record of the
code, because it was killed while the shell ran, the command prints no exit
code, says so on stderr, and still exits 0. --exit refuses the terminal that
runs the command, because its shell cannot end while the wait runs in it.

"leapmux events --kind terminal" prints the bells, the titles, the
notifications, the progress reports and the exits of terminals as they
happen.

```text
leapmux terminal wait <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--exit` | wait until the shell of the terminal ends, and print its exit code |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | wait until a line of the terminal matches this regular expression, in RE2 syntax |
| `--change` | wait until the visible screen text changes, and print the updated screen |
| `--after-offset int` | with --change, return immediately if screen_end_offset already exceeds this byte offset |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
# Wait until the shell ends, and keep its exit code.
CODE=$(leapmux terminal wait 4cbb3f34 --exit)

# Run the tests, then wait for the line that the last command prints.
leapmux terminal run 4cbb3f34 'make test; echo DONE' && leapmux terminal wait 4cbb3f34 --regex '^DONE$' --timeout 10m

# Wait until a server reports that it listens, for 2 minutes at most.
leapmux terminal wait 4cbb3f34 --text 'Listening on' --timeout 2m

# Look for a completed build in the terminal's retained output.
leapmux terminal wait 4cbb3f34 --text 'Build complete' --scrollback
```

### `leapmux terminal resize`

Resize a terminal.

This command changes state.

```text
leapmux terminal resize <terminal> <cols> <rows> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal resize 4cbb3f34 120 40
```

### `leapmux terminal restart`

Restart a terminal's shell.

This command changes state.

```text
leapmux terminal restart <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--replace` | stop the running shell before starting a new one in the same tab |
| `--command string` | run this command in the new shell after its startup files, or read it from stdin with - |
| `--allow-self` | replace this tab's shell even though it ends this command's own process |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal restart 4cbb3f34
leapmux terminal restart 4cbb3f34 --replace --command 'npm run dev'
```

### `leapmux terminal shells`

List the shells that a worker offers.

```text
leapmux terminal shells [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal shells
```

### `leapmux terminal quake`

Show, hide, inspect, read, and drive the quake terminal of a directory.

#### `leapmux terminal quake open`

Show the quake terminal.

This command changes state.

```text
leapmux terminal quake open [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake open
leapmux terminal quake open --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake close`

Hide the quake terminal.

This command changes state.

```text
leapmux terminal quake close [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake close
leapmux terminal quake close --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake toggle`

Show the quake terminal if it is hidden, and hide it otherwise.

This command changes state.

```text
leapmux terminal quake toggle [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake toggle
leapmux terminal quake toggle --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake get`

Show the status and details of the quake terminal for a directory.

```text
leapmux terminal quake get [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to inspect (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake get
leapmux terminal quake get --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake read`

Print what the quake terminal of a directory shows.

Aliases: `cat`.

```text
leapmux terminal quake read [flags]
```

| Flag | Description |
|---|---|
| `--lines int` | print only the last N lines |
| `--scrollback` | also print the lines above the screen |
| `--raw` | print the retained bytes as they are, with their escape sequences |
| `-f, --follow` | stream live output until Ctrl-C or shell exit |
| `--dir string` | the directory whose quake terminal to read (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake read
leapmux terminal quake read --dir ~/src/app --lines 50
```

#### `leapmux terminal quake run`

Run a command in the quake terminal of a directory (starts the quake shell if needed).

This command changes state.

```text
leapmux terminal quake run [command] [flags]
```

| Argument | Description |
|---|---|
| `[command]` | The command line, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the command line from this file ("-" reads stdin) |
| `--dir string` | the directory whose quake terminal to run in (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake run 'go test ./...'
leapmux terminal quake run 'make build' --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake send`

Type text or press keys in the quake terminal of a directory.

This command changes state.

```text
leapmux terminal quake send [text] [flags]
```

| Argument | Description |
|---|---|
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the bytes from this file ("-" reads stdin) |
| `--key string` | press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order (repeatable) |
| `--paste` | send the text as a paste, between bracketed-paste markers when the program enabled them |
| `--dir string` | the directory whose quake terminal to type into (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux terminal quake send --key ctrl-c
leapmux terminal quake send "ls -la" --key enter
```

### `leapmux terminal close`

Close one or more terminal tabs and stop their shells.

Aliases: `rm`.

This command changes state.

```text
leapmux terminal close <terminal>... [flags]
```

| Argument | Description |
|---|---|
| `<terminal>...` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux terminal close 87a19d5f --worktree keep
```

### `leapmux terminal rename`

Rename a terminal tab.

This command changes state.

```text
leapmux terminal rename <terminal> <title> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal rename self "Release notes"
```

### `leapmux terminal reveal`

Show a tab in every LeapMux window that you have open: each window switches
to the tab's workspace, and the tab becomes the active tab of its tile, as a
click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own
state, so a window that you open later shows what it showed before, and a
window that does not know the tab yet ignores the command.

This command changes state.

```text
leapmux terminal reveal <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux terminal reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux terminal reveal self
```

## `leapmux events`

Print what happens in your workspaces as it happens: tabs that open, close
and move, what each agent does and asks, what each terminal reports, and the
health of the stream itself. Each line is the local time, the kind, the ids,
and a summary; --json prints one {"data": …} envelope per line instead, with
the full ids and a field for each fact. Ctrl-C ends the stream with exit
status 0.

The stream watches every workspace, or the one that --workspace selects. A
tab that opens while the stream runs joins it, and a tab that closes leaves
it. With --tab, the stream watches that tab only and follows its moves between
workspaces. If --workspace also selects a workspace, the stream ends when
the tab leaves it. The stream also ends when the selected tab closes or
the selected workspace is deleted.

The first stream.ready line reports the sources that the stream watches and
which of them can deliver. Read the state that you need after it, such as
"leapmux agent list". A new run without --checkpoint starts from
the present.

Use --tab with --checkpoint to resume the retained events of one agent or
terminal. The command saves its position after writing each selected event.
Reuse the same file and --kind selection on the next run. A record can
appear again if the process stops after writing its line and before saving
its cursor. Use journal_id, tab_stream_id and sequence to deduplicate.
The Worker reports gaps when it cannot supply retained history. A replayed
agent request has a request_instance that correlates it with its end. It
has a request handle only while that exact instance still needs an answer.
With --checkpoint, --limit counts only the printed tab events. This mode
follows the tab across workspace moves and does not take --workspace.
After the Worker retires an agent or terminal, resume with its full ID
and the same checkpoint. The stream exits after stream.retired confirms
the final Worker position. Closing a subagent transcript keeps its
history open. A terminal's shell can also exit while its tab stays open.

Use --checkpoint without --tab or --workspace to resume account events.
The checkpoint stores a separate position for each selected Worker event
family and for the Hub layout when selected. A membership change can
leave a gap in Worker history. The stream reports the gap before
recording the new inventory.

Use --workspace with --checkpoint to resume that workspace's committed
Hub events. Resume with the same scope and kinds. For a deleted
workspace, pass its full ID. The command exits after it reaches the
final layout position. The Hub retains tabs that opened, moved, or
closed between runs.

In a stream without --checkpoint, when the Hub or a Worker cannot deliver, it prints
stream.unavailable and retries. When the source delivers again, it prints
stream.gap with the kinds that it may have lost, then each change of an
agent's activity and status, and of the tabs and workspaces, that happened
during the gap, marked as reconciled. --kind does not hide these lines: a
stream event that it does not select prints on stderr.

Kinds:
  - workspace.created: a workspace was created
  - workspace.renamed: a workspace has a new title
  - workspace.deleted: a workspace was deleted
  - tab.opened: a tab opened, or it moved into the workspace that --workspace selects
  - tab.closed: a tab closed
  - tab.moved: a tab moved to another workspace
  - tab.renamed: a tab was renamed on its Worker
  - tab.status: a tab's status pill was set or cleared
  - agent.activity: an agent became idle, working or waiting for the user
  - agent.status: the status of an agent's process changed to starting, active, inactive or startup_failed
  - agent.turn_end: an agent ended a turn
  - agent.request: an agent asks a question or a permission; agent approve, deny and answer take its request
  - agent.request_ended: a request of an agent was answered or withdrawn
  - agent.todos: an agent's to-do list changed
  - agent.background_tasks: an agent's background tasks changed
  - agent.goal: an agent's standing goal was set, paused, resumed, or cleared
  - agent.queue: an agent's queued input changed
  - terminal.status: the status of a terminal's shell changed to starting, ready or startup_failed, or to exited without a terminal.exit
  - terminal.title: a program set the title of a terminal
  - terminal.bell: a terminal rang its bell
  - terminal.notification: a program in a terminal asked for a desktop notification
  - terminal.progress: a program in a terminal reported its progress
  - terminal.exit: a terminal's shell exited, with its exit code
  - notification.created: a live toast notification was published on a Worker
  - notification.cleared: a notification was cleared on a Worker
  - stream.ready: the stream watches every source of its scope
  - stream.unavailable: the hub, a Worker, or a Worker for one tab cannot deliver events; retrying says whether the stream tries again
  - stream.gap: a source delivers again; events of the kinds in lost, between since and time, may be lost
  - stream.heartbeat: the stream runs; only --heartbeat prints it
  - stream.retired: the Worker finished the checkpointed tab's event history

Aliases: `ev`.

Does not support `-q` / `--quiet`.

```text
leapmux events [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | watch only this workspace: an id, a prefix, "title:&lt;title&gt;" or "self" (default: every workspace) |
| `--tab string` | watch only this tab: an id, a prefix, "id:&lt;id&gt;", "title:&lt;title&gt;" or "self" |
| `--kind string` | print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind) (one of: workspace, tab, agent, terminal, notification, stream, workspace.created, workspace.renamed, workspace.deleted, tab.opened, tab.closed, tab.moved, tab.renamed, tab.status, agent.activity, agent.status, agent.turn_end, agent.request, agent.request_ended, agent.todos, agent.background_tasks, agent.goal, agent.queue, terminal.status, terminal.title, terminal.bell, terminal.notification, terminal.progress, terminal.exit, notification.created, notification.cleared, stream.ready, stream.unavailable, stream.gap, stream.heartbeat, stream.retired; repeatable) |
| `--limit int` | end the stream after this many events; 0 runs without a limit |
| `--timeout duration` | stop the stream cleanly after this long, such as 30s or 5m; 0 runs without a limit (default: 0s) |
| `--snapshot` | emit stream.ready (after any checkpointed backlog) and exit cleanly |
| `--heartbeat duration` | print a stream.heartbeat event at this interval, such as 30s; 0 prints none (default: 0s) |
| `--reconnect` | reconnect automatically if the Hub or a Worker disconnects (default: true) |
| `--checkpoint string` | save and resume retained events for a tab, workspace, or account |

```sh
leapmux events

# Print the turn ends and the requests of the agents in this workspace, as JSON.
leapmux events --workspace self --kind agent.turn_end --kind agent.request --json

# Wait for the next request of any agent, and print it.
leapmux events --kind agent.request --limit 1 --json

# Watch every terminal, with a heartbeat that shows the stream runs.
leapmux events --kind terminal --heartbeat 30s

# Watch a single terminal as it moves between workspaces.
leapmux events --tab 87a19d5f --kind terminal --json

# Resume one terminal's event history across CLI runs.
leapmux events --tab 87a19d5f --checkpoint terminal-events.json --json

# Resume permission and question events for one agent.
leapmux events --tab 87a19d5f --checkpoint agent-events.json --kind agent.request,agent.request_ended --json

# Resume terminal events from every Worker in the account.
leapmux events --checkpoint account-events.json --kind terminal --json
```

## `leapmux notify`

Show a notification in every LeapMux window that you have open: a toast with
the title and the body. While a window is hidden, it also shows a desktop
notification, when "Desktop notifications" is on in Preferences.

The Worker identifies the sending tab when the command runs inside one.
Use --tab or --workspace to attach a destination the user can open.

With --tab, the notification goes through the destination's Worker. For
other notifications, the command uses the sending tab's Worker. Outside a
tab, pass --worker. A window hears a Worker while one of your tabs runs there.

The command returns the notification ID. Use "notification list" to read the
Worker's recent notifications, and "notification clear" to remove one by ID.
A window that connects later does not show an old toast.

The Worker refuses a title or a body that is too long, and states the limit.

This command changes state.

```text
leapmux notify <title> [flags]
```

| Argument | Description |
|---|---|
| `<title>` | A short title, such as "Tests passed". |

| Flag | Description |
|---|---|
| `--body string` | more text below the title ("-" reads stdin) |
| `--tab string` | open this tab from the notification |
| `--workspace string` | open this workspace from the notification |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux notify "Tests passed"

# Say what failed, with the last lines of the log.
tail -n 5 build.log | leapmux notify "Build failed" --body -

# Notify through another Worker from outside a tab.
leapmux notify "Deploy finished" --body "v1.4.2 is live" --worker name:build-box

# Attach the deployment terminal.
leapmux notify "Deploy finished" --tab title:Deployment
```

## `leapmux notification`

Send, list and clear recent Worker notifications.

Aliases: `notif`.

### `leapmux notification send`

Show a notification in every LeapMux window that you have open: a toast with
the title and the body. While a window is hidden, it also shows a desktop
notification, when "Desktop notifications" is on in Preferences.

The Worker identifies the sending tab when the command runs inside one.
Use --tab or --workspace to attach a destination the user can open.

With --tab, the notification goes through the destination's Worker. For
other notifications, the command uses the sending tab's Worker. Outside a
tab, pass --worker. A window hears a Worker while one of your tabs runs there.

The command returns the notification ID. Use "notification list" to read the
Worker's recent notifications, and "notification clear" to remove one by ID.
A window that connects later does not show an old toast.

The Worker refuses a title or a body that is too long, and states the limit.

This command changes state.

```text
leapmux notification send <title> [flags]
```

| Argument | Description |
|---|---|
| `<title>` | A short title, such as "Tests passed". |

| Flag | Description |
|---|---|
| `--body string` | more text below the title ("-" reads stdin) |
| `--tab string` | open this tab from the notification |
| `--workspace string` | open this workspace from the notification |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux notification send "Tests passed"

# Say what failed, with the last lines of the log.
tail -n 5 build.log | leapmux notification send "Build failed" --body -

# Notify through another Worker from outside a tab.
leapmux notification send "Deploy finished" --body "v1.4.2 is live" --worker name:build-box

# Attach the deployment terminal.
leapmux notification send "Deploy finished" --tab title:Deployment
```

### `leapmux notification list`

List the recent notifications on a Worker.

Aliases: `ls`.

```text
leapmux notification list [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux notification list --worker name:build-box
```

### `leapmux notification clear`

Remove one or more notifications from a Worker and dismiss their toasts.

Aliases: `rm`.

This command changes state.

```text
leapmux notification clear [notification-id]... [flags]
```

| Argument | Description |
|---|---|
| `[notification-id]...` | One or more IDs returned by notify or notification list (or pass --all). |

| Flag | Description |
|---|---|
| `--all` | clear every recent notification on the Worker |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux notification clear 7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a --worker name:build-box

# Clear every recent notification on the Worker.
leapmux notification clear --all --worker name:build-box
```

## `leapmux file`

List, read, search, glob, and open files on a worker.

### `leapmux file list`

List a directory (default: the working directory of this tab).

Aliases: `ls`.

```text
leapmux file list [path] [flags]
```

| Flag | Description |
|---|---|
| `--max-depth int` | merge a chain of directories that each hold one directory, up to this depth |
| `--dirs-only` | list only directories |
| `--from-root string` | also list every directory from this ancestor down to &lt;path&gt; |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file list
leapmux file list ~/src/app

# List only the directories, and every directory from the home directory down.
leapmux file list ~/src/app --dirs-only --from-root ~
```

### `leapmux file read`

Print a file.

Aliases: `cat`.

Does not support `-q` / `--quiet`.

```text
leapmux file read <path> [flags]
```

| Flag | Description |
|---|---|
| `--offset int` | the byte to start at |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file read ~/src/app/go.mod

# Read 4 KiB from byte 1024.
leapmux file read ~/src/app/log.txt --offset 1024 --limit 4096
```

### `leapmux file stat`

Show a file's size, time and permissions.

```text
leapmux file stat <path> [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file stat ~/src/app/go.mod
```

### `leapmux file roots`

List the filesystem roots of a worker.

```text
leapmux file roots [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file roots
```

### `leapmux file open`

Open a file in a new tab.

This command changes state.

```text
leapmux file open <path> [flags]
```

| Flag | Description |
|---|---|
| `--display-mode string` | how a document shows at first (one of: render, source, split) |
| `--view-mode string` | which version of the file shows at first (one of: working, head, staged, unified_diff, split_diff) |
| `--diff-base string` | base git ref or revision for diff view modes |
| `--dir string` | the git context of the tab; outside a LeapMux tab, the directory of the file (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
leapmux file open ~/src/app/README.md

# Open a file as its staged diff.
leapmux file open ~/src/app/main.go --view-mode unified_diff
```

### `leapmux file set`

Change a file tab's display mode, view mode, or diff base.

This command changes state.

```text
leapmux file set <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--display-mode string` | how the document renders (one of: render, source, split) |
| `--view-mode string` | which version or diff view of the file shows (one of: working, head, staged, unified_diff, split_diff) |
| `--diff-base string` | base git ref or revision for diff view modes |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux file set 11aa22bb --view-mode unified_diff --diff-base HEAD
leapmux file set 11aa22bb --display-mode split
```

### `leapmux file search`

Search text files under [path] (default: the working directory of this tab)
directly on the worker over E2EE. Binary files, .git, and node_modules are
skipped automatically.

Aliases: `grep`.

```text
leapmux file search <pattern> [path] [flags]
```

| Flag | Description |
|---|---|
| `--glob string` | restrict matches to paths matching this glob (such as "**/*.go") |
| `--regex` | treat &lt;pattern&gt; as an RE2 regular expression |
| `-i, --ignore-case` | match case-insensitively |
| `--limit int` | maximum number of matching lines to return (max 1000) (default: 100) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file search "NewLeaf" /path/to/repo/backend/internal/cli
leapmux file search "TODO|FIXME" --regex --glob "**/*.go" -i
```

### `leapmux file glob`

List relative file paths under [path] (default: the working directory of this
tab) matching &lt;pattern&gt; (supports *, ?, [...], and ** multi-directory globs).

Aliases: `find`.

```text
leapmux file glob <pattern> [path] [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | maximum number of paths to return (max 2000) (default: 200) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux file glob "**/*.go"
leapmux file glob "*.proto" /path/to/repo/proto/leapmux/v1
```

## `leapmux git`

Read and change the git state of a directory on a worker.

### `leapmux git status`

Show the branch and the changed files of a working tree.

Does not support `-q` / `--quiet`.

```text
leapmux git status [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git status
leapmux git status --dir ~/src/app --worker name:build-box
```

### `leapmux git read`

Print a file as the last commit or the index holds it. The path is the file's
absolute path on the worker; the worker finds its repository.

Aliases: `cat`.

Does not support `-q` / `--quiet`.

```text
leapmux git read <path> [flags]
```

| Flag | Description |
|---|---|
| `--ref string` | the version to read (one of: head, staged; default: head) |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git read ~/src/app/main.go
leapmux git read ~/src/app/main.go --ref staged
```

### `leapmux git checkout`

Check out a branch.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux git checkout <branch> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git checkout main --dir ~/src/app

# Check out a remote branch.
leapmux git checkout origin/feature --dir ~/src/app
```

### `leapmux git branch`

List, create, inspect and delete branches.

#### `leapmux git branch list`

List the branches of a repository.

Aliases: `ls`.

```text
leapmux git branch list [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git branch list
```

#### `leapmux git branch create`

Create a branch and check it out.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux git branch create <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--base string` | the branch to start from (default: the current branch) |

```sh
leapmux git branch create fix-login --base main --dir ~/src/app
```

#### `leapmux git branch delete`

Check out another branch, then delete this one.

Aliases: `rm`.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux git branch delete <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--switch-to string` | the branch to check out before the deletion (required) |

```sh
leapmux git branch delete fix-login --switch-to main --dir ~/src/app
```

#### `leapmux git branch inspect`

Check whether the current branch or worktree has uncommitted or unpushed work before deletion.

Does not support `-q` / `--quiet`.

```text
leapmux git branch inspect [branch] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git branch inspect
leapmux git branch inspect fix-login --dir ~/src/app
```

### `leapmux git push`

Push the current branch, with a work-in-progress commit when the tree has changes.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux git push [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux git push --dir ~/src/app
```

### `leapmux git diff`

Show unstaged, staged, or base-ref git diff for a working tree.

Does not support `-q` / `--quiet`.

```text
leapmux git diff [path]... [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--staged` | show staged changes (--cached) |
| `--base string` | diff against this base ref (such as main or HEAD~1) |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |

```sh
leapmux git diff
leapmux git diff --staged --dir ~/src/app
leapmux git diff --base main backend/cmd/leapmux
```

### `leapmux git log`

Show recent commits of a repository.

```text
leapmux git log [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `-n, --limit int` | maximum number of commits to show (max 200) (default: 20) |
| `--ref string` | starting ref (default: HEAD) |

```sh
leapmux git log
leapmux git log -n 10 --ref main --dir ~/src/app
```

### `leapmux git commit`

Create a git commit in a working tree.

This command changes state.

```text
leapmux git commit [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `-m, --message string` | commit message (required) |
| `-a, --all` | stage all modified and untracked files (git add -A) before committing |

```sh
leapmux git commit -m "Fix timeout handling" --dir ~/src/app
leapmux git commit -a -m "Checkpoint changes" --dir ~/src/app
```

## `leapmux worktree`

List, inspect, create, and remove git worktrees on a worker.

Aliases: `wt`.

### `leapmux worktree list`

List the worktrees of a repository.

Aliases: `ls`.

```text
leapmux worktree list [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux worktree list
leapmux worktree list --dir ~/src/app
```

### `leapmux worktree get`

Use a bare path, path:&lt;path&gt;, branch:&lt;branch&gt;, or self (the default). The
worker matches the attached branch in the repository selected by --dir. A local
tab can supply the repository directory for a branch on the same worker. An
explicit path can select its own repository. Use self to find the calling local
tab's checkout.

```text
leapmux worktree get [selector] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | repository directory on the selected worker (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux worktree get
leapmux worktree get branch:feature/login --dir ~/src/app
leapmux worktree get /path/to/worktree
```

### `leapmux worktree create`

Create a new linked git worktree on a new branch.

This command changes state.

```text
leapmux worktree create <branch> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | repository directory on the worker (required) |
| `--base string` | base branch or commit to branch from (default: current branch) |
| `--path string` | explicit filesystem path for the new worktree (default: worker managed location) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux worktree create feat/cli-v2 --dir ~/src/app
leapmux worktree create feat/cli-v2 --base main --path ~/wt/cli-v2 --dir ~/src/app
```

### `leapmux worktree remove`

Remove a linked git worktree from disk.

Aliases: `rm`, `delete`.

This command changes state.

```text
leapmux worktree remove <path> [flags]
```

| Flag | Description |
|---|---|
| `--force` | remove even if the worktree contains modified or untracked files |
| `--delete-branch` | also delete the local branch checked out in the removed worktree |
| `--dir string` | repository directory when resolving a branch:&lt;branch&gt; selector |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux worktree remove ~/wt/cli-v2
leapmux worktree remove ~/wt/cli-v2 --force
```

### `leapmux worktree inspect`

Check whether a worktree is locked, dirty, or has unpushed commits before removal.

Does not support `-q` / `--quiet`.

```text
leapmux worktree inspect [path] [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux worktree inspect
leapmux worktree inspect ~/wt/cli-v2
```

## `leapmux tile`

Split, close and arrange the tiles of a workspace.

### `leapmux tile split`

Split a tile in two; its tabs stay in the first half.

This command changes state.

```text
leapmux tile split <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--direction string` | the divider: "vertical" puts the new tile on the right, "horizontal" puts it below (one of: vertical, horizontal; default: vertical) |

```sh
# Split the tile of this tab, with the new tile on the right.
leapmux tile split self

# Split a tile with the new tile below.
leapmux tile split ab12cd34 --direction horizontal
```

### `leapmux tile close`

Close a tile and give its space to its neighbour.

Aliases: `rm`.

This command changes state.

```text
leapmux tile close <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--with-tabs string` | what happens to the tabs on the tile: "close" closes them; "move" moves them to the neighbouring tile; required when the tile has tabs (one of: close, move) |
| `--recursive` | close a split or a grid with every tile in it |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tile even when this tab sits on it, which ends this command's own process |

```sh
leapmux tile close ab12cd34

# Close a tile and move its tabs to the neighbouring tile.
leapmux tile close ab12cd34 --with-tabs move
```

### `leapmux tile make-grid`

Turn a tile into a grid of rows and columns.

This command changes state.

```text
leapmux tile make-grid <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--rows int` | rows of the grid, from 1 to 20 (required) |
| `--cols int` | columns of the grid, from 1 to 20 (required) |

```sh
leapmux tile make-grid ab12cd34 --rows 2 --cols 2
```

### `leapmux tile remove-grid`

Remove a grid and every tile in it.

This command changes state.

```text
leapmux tile remove-grid <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--with-tabs string` | what happens to the tabs in the grid: "close" closes them; "move" turns the grid into one tile that holds them; required when the grid has tabs (one of: close, move) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | remove the grid even when this tab sits in it, which ends this command's own process |

```sh
# Turn a grid back into one tile that keeps its tabs.
leapmux tile remove-grid ab12cd34 --with-tabs move
```

### `leapmux tile set-ratios`

Set the sizes of the parts of a split.

This command changes state.

```text
leapmux tile set-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--ratios string` | the size of each part, separated by commas; the CLI scales them to sum to 1 (required) |

```sh
leapmux tile set-ratios ab12cd34 --ratios 0.3,0.7
```

### `leapmux tile set-grid-ratios`

Set the sizes of the rows or the columns of a grid.

This command changes state.

```text
leapmux tile set-grid-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--row-ratios string` | the size of each row, separated by commas |
| `--col-ratios string` | the size of each column, separated by commas |

```sh
leapmux tile set-grid-ratios ab12cd34 --row-ratios 0.6,0.4
```

### `leapmux tile resize`

Resize a tile inside its parent split to a target ratio (0 &lt; ratio &lt; 1).

This command changes state.

```text
leapmux tile resize <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--ratio number` | new share of the parent split for this tile, between 0 and 1 (exclusive) (required) |

```sh
leapmux tile resize self --ratio 0.65
leapmux tile resize ab12cd34 --ratio 0.3
```

### `leapmux tile swap`

Swap the positions of two tiles in the same workspace.

This command changes state.

```text
leapmux tile swap <tile-a> <tile-b> [flags]
```

| Argument | Description |
|---|---|
| `<tile-a>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |
| `<tile-b>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

```sh
leapmux tile swap ab12cd34 ef56gh78
leapmux tile swap self ef56gh78
```

## `leapmux window`

List, create, position, resize, and close floating windows.

Aliases: `win`.

### `leapmux window list`

List live floating windows (default: all workspaces).

Aliases: `ls`.

```text
leapmux window list [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux window list
leapmux window list self
```

### `leapmux window create`

Create a floating window in a workspace.

This command changes state.

```text
leapmux window create <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--x number` | horizontal position in pixels (default: 100) |
| `--y number` | vertical position in pixels (default: 100) |
| `--width number` | width in pixels (must be &gt; 0) (default: 640) |
| `--height number` | height in pixels (must be &gt; 0) (default: 420) |
| `--opacity number` | window opacity from 0.0 to 1.0 (default: 1) |

```sh
leapmux window create self --x 120 --y 80 --width 640 --height 420
```

### `leapmux window move`

Move a floating window to new coordinates or another workspace.

Aliases: `mv`.

This command changes state.

```text
leapmux window move <window> [flags]
```

| Flag | Description |
|---|---|
| `--x number` | new horizontal position in pixels |
| `--y number` | new vertical position in pixels |
| `--workspace string` | move the window to this workspace |

```sh
leapmux window move 7aB1c2d3 --x 200 --y 150
leapmux window move 7aB1c2d3 --workspace "Release 1.4"
```

### `leapmux window resize`

Resize a floating window.

This command changes state.

```text
leapmux window resize <window> [flags]
```

| Flag | Description |
|---|---|
| `--width number` | new width in pixels (must be &gt; 0) |
| `--height number` | new height in pixels (must be &gt; 0) |

```sh
leapmux window resize 7aB1c2d3 --width 800 --height 600
```

### `leapmux window set-opacity`

Set the opacity of a floating window (0.0 to 1.0).

This command changes state.

```text
leapmux window set-opacity <window> <opacity> [flags]
```

```sh
leapmux window set-opacity 7aB1c2d3 0.85
```

### `leapmux window close`

Close a floating window and its inner tiles.

Aliases: `rm`.

This command changes state.

```text
leapmux window close <window> [flags]
```

| Flag | Description |
|---|---|
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the window even when this tab lives in it |

```sh
leapmux window close 7aB1c2d3
```

## `leapmux layout`

Read, replace and watch the whole tile tree of a workspace.

### `leapmux layout get`

Print the tile tree of a workspace with its tabs (default: the workspace of this tab).

```text
leapmux layout get [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
# Save a layout, and restore it later.
leapmux layout get --json > layout.json
```

### `leapmux layout set`

Replace the main tile tree of a workspace. Main-tree tabs move to the new first
tile, and tabs inside floating windows stay in place. The file is "-" or absent
for stdin. It holds a tree as "layout get" prints it: the whole --json
envelope, its data, or the tree alone.

This command changes state.

```text
leapmux layout set <workspace> [file] [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux layout set self layout.json
leapmux layout get --json | leapmux layout set 9f3kQ2aB -
```

### `leapmux layout watch`

Print every change of your layouts as it happens: the CRDT operations, the
presence of other clients, and the creation, renaming and deletion of
workspaces. The first event is the snapshot. Without a workspace, it watches
every workspace.

Does not support `-q` / `--quiet`.

```text
leapmux layout watch [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |

```sh
leapmux layout watch --json
```

## `leapmux whoami`

Show the identity and tab context that this CLI acts as.

Does not support `-q` / `--quiet`.

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

Does not support `-q` / `--quiet`.

```text
leapmux doctor [flags]
```

```sh
leapmux doctor

# Inspect diagnostics as JSON.
leapmux doctor --json
```

## `leapmux auth`

Sign this CLI in to a hub, and manage its credentials.

### `leapmux auth login`

Sign this CLI in to a hub through a browser.

This command changes state.

Does not support `-q` / `--quiet`.

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

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux auth logout [flags]
```

```sh
leapmux auth logout --hub https://hub.example.com
```

### `leapmux auth list`

List the hubs that this machine holds a credential for.

Aliases: `ls`.

```text
leapmux auth list [flags]
```

```sh
leapmux auth list
```

### `leapmux auth use`

Choose the hub that a command uses without --hub.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux auth use <hub> [flags]
```

```sh
leapmux auth use https://hub.example.com
```

### `leapmux auth status`

Show the user, the expiry and the permissions of the hub's credential.

Does not support `-q` / `--quiet`.

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

This command changes state.

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

Does not support `-q` / `--quiet`.

```text
leapmux context show [flags]
```

```sh
leapmux context show
```

### `leapmux context use`

Save default hub, worker, workspace, or provider for commands outside a tab.

This command changes state.

Does not support `-q` / `--quiet`.

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

This command changes state.

Does not support `-q` / `--quiet`.

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

Aliases: `ls`.

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

This command changes state.

```text
leapmux settings set <key> <value> [flags]
```

```sh
leapmux settings set appearance '{"theme":"dark"}'
```

### `leapmux settings reset`

Reset an account setting back to its default value.

This command changes state.

```text
leapmux settings reset <key> [flags]
```

```sh
leapmux settings reset appearance
```

## `leapmux worker`

List workers, read what they report, and register new ones.

### `leapmux worker list`

List your workers with their names.

Aliases: `ls`.

```text
leapmux worker list [flags]
```

```sh
leapmux worker list
```

### `leapmux worker get`

Show what the hub knows about a worker (default: the worker of this tab).

```text
leapmux worker get [worker] [flags]
```

| Argument | Description |
|---|---|
| `[worker]` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command. |

```sh
leapmux worker get
leapmux worker get name:build-box
```

### `leapmux worker info`

Show what an online worker reports about itself: name, OS, home and build.

```text
leapmux worker info [worker] [flags]
```

| Argument | Description |
|---|---|
| `[worker]` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command. |

```sh
leapmux worker info
```

### `leapmux worker ping`

Measure end-to-end encrypted round-trip latency to an online worker.

```text
leapmux worker ping [worker] [flags]
```

| Argument | Description |
|---|---|
| `[worker]` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command. |

```sh
leapmux worker ping
leapmux worker ping name:build-box
```

### `leapmux worker deregister`

Deregister one of your workers from the hub.

Aliases: `rm`.

This command changes state.

```text
leapmux worker deregister <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command. |

```sh
leapmux worker deregister name:old-box
```

### `leapmux worker key`

Create, extend, revoke, and email registration keys for new workers.

#### `leapmux worker key create`

Create a registration key that a new worker uses once, within five minutes.

This command changes state.

```text
leapmux worker key create [flags]
```

```sh
# Register a worker on another machine with the key.
K=$(leapmux worker key create --json | jq -r .data.key)
```

#### `leapmux worker key extend`

Extend a registration key's expiry by five minutes.

This command changes state.

```text
leapmux worker key extend <key> [flags]
```

```sh
leapmux worker key extend lmx_reg_...
```

#### `leapmux worker key revoke`

Revoke an unused registration key immediately.

Aliases: `rm`.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux worker key revoke <key> [flags]
```

```sh
leapmux worker key revoke lmx_reg_...
```

#### `leapmux worker key email`

Email worker registration instructions to your verified email address.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux worker key email <key> [flags]
```

| Flag | Description |
|---|---|
| `--command string` | custom registration command line to embed in the email |

```sh
leapmux worker key email lmx_reg_...
```

### `leapmux worker pins`

Inspect and reset the worker keys that this machine trusts.

#### `leapmux worker pins list`

List the worker keys that this machine pinned for the hub.

Aliases: `ls`.

```text
leapmux worker pins list [flags]
```

```sh
leapmux worker pins list
```

#### `leapmux worker pins get`

Show the pinned key of one worker.

```text
leapmux worker pins get <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, or a unique prefix of 4 or more characters of one, as "leapmux worker pins list" prints it. |

```sh
leapmux worker pins get 5f2c9a1d
```

#### `leapmux worker pins remove`

Forget a worker's pinned key, so the next connection pins it again.

Aliases: `rm`.

This command changes state.

```text
leapmux worker pins remove <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, or a unique prefix of 4 or more characters of one, as "leapmux worker pins list" prints it. |

```sh
# Forget the key of a worker that was reinstalled.
leapmux worker pins remove 5f2c9a1d
```

## `leapmux tunnel`

Forward TCP ports or connect stdio to a host on a worker over E2EE.

Aliases: `tun`.

### `leapmux tunnel forward`

Open a Noise_NK end-to-end encrypted channel to a Worker, listen on a local TCP
address, and forward every accepted connection to &lt;target-host&gt;:&lt;target-port&gt;
on that Worker's network.

The spec is [bind-addr:]&lt;local-port&gt;:&lt;target-host&gt;:&lt;target-port&gt;. When
bind-addr is omitted, the listener binds 127.0.0.1. Pass local port 0 to let
the OS pick a free port. Press Ctrl-C to stop forwarding.

Aliases: `fwd`.

Does not support `-q` / `--quiet`.

```text
leapmux tunnel forward <spec> [flags]
```

| Argument | Description |
|---|---|
| `<spec>` | Port forward specification: "[bind:]&lt;local-port&gt;:&lt;target-host&gt;:&lt;target-port&gt;" (for example "5432:127.0.0.1:5432" or "0.0.0.0:8080:localhost:3000"). |

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
# Forward local port 5432 to Postgres on a remote Worker.
leapmux tunnel forward 5432:127.0.0.1:5432 --worker name:db-box

# Bind on all interfaces and forward to a dev server.
leapmux tunnel forward 0.0.0.0:8080:localhost:3000 --worker name:dev-box
```

### `leapmux tunnel connect`

Open a Noise_NK end-to-end encrypted channel to a Worker, dial
&lt;host&gt;:&lt;port&gt; from that Worker, and bridge stdin and stdout directly to the
TCP stream. Useful with "ssh -o ProxyCommand='leapmux tunnel connect %h:%p --worker &lt;name&gt;'".

Aliases: `nc`.

Does not support `-q` / `--quiet`.

```text
leapmux tunnel connect <target> [flags]
```

| Argument | Description |
|---|---|
| `<target>` | Target address in "&lt;host&gt;:&lt;port&gt;" form (for example "127.0.0.1:22" or "[::1]:8080"). |

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
# Use as an SSH ProxyCommand to reach a remote Worker behind NAT.
ssh -o ProxyCommand="leapmux tunnel connect %h:%p --worker name:build-box" user@127.0.0.1
```

## `leapmux admin`

Administer the hub; needs an administrator's login (offline repair: "leapmux recover").

### `leapmux admin settings`

Read and change the hub's settings: sign-in policy, SMTP, timeouts, limits.

#### `leapmux admin settings list`

List every setting with its effective value.

Aliases: `ls`.

```text
leapmux admin settings list [flags]
```

```sh
leapmux admin settings list
```

#### `leapmux admin settings get`

Show one setting, its value and its shape.

```text
leapmux admin settings get <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |

```sh
leapmux admin settings get session_duration_seconds
```

#### `leapmux admin settings set`

Set the public value of a setting from JSON or a bare value.

This command changes state.

```text
leapmux admin settings set <key> <value> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |
| `<value>` | A JSON value. A word that is not JSON is stored as a string. |

```sh
leapmux admin settings set signup_enabled false

# Set two fields of a setting that holds a document.
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587}'
```

#### `leapmux admin settings set-secret`

Set the secret fields of a setting; the hub stores them encrypted.

This command changes state.

```text
leapmux admin settings set-secret <key> <json> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |
| `<json>` | A JSON object of the secret fields, such as {"password":"..."}. |

```sh
leapmux admin settings set-secret smtp '{"password":"..."}'
```

#### `leapmux admin settings reset`

Restore the default value of a setting.

This command changes state.

```text
leapmux admin settings reset <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |

```sh
leapmux admin settings reset session_duration_seconds
```

### `leapmux admin user`

Manage users.

#### `leapmux admin user list`

List users.

Aliases: `ls`.

```text
leapmux admin user list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--query string` | search query (empty = list all) |

```sh
leapmux admin user list

# Search by name or email.
leapmux admin user list --query alice
```

#### `leapmux admin user get`

Show a user.

```text
leapmux admin user get <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin user get alice

# Select a user by id.
leapmux admin user get id:Ab3dE9
```

#### `leapmux admin user create`

Create a user.

This command changes state.

```text
leapmux admin user create <username> [flags]
```

| Flag | Description |
|---|---|
| `--password string` | password (prompted for when omitted, so it stays out of the shell history) |
| `--display-name string` | display name |
| `--email string` | email address |
| `--email-verified` | mark email as verified |
| `--admin` | grant admin privileges |

```sh
leapmux admin user create alice --email alice@example.com
```

#### `leapmux admin user update`

Change a user's name, email or verification.

This command changes state.

```text
leapmux admin user update <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--display-name string` | new display name |
| `--email string` | new email address |
| `--email-verified string` | mark email as verified (one of: true, false) |
| `--clear-pending-email` | clear any in-flight email verification (token + attempt counter) |

```sh
leapmux admin user update alice --display-name "Alice Kim"
```

#### `leapmux admin user delete`

Delete a user.

Aliases: `rm`.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin user delete <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--force` | required to delete an admin user |

```sh
leapmux admin user delete alice
```

#### `leapmux admin user grant-admin`

Make a user an administrator.

This command changes state.

```text
leapmux admin user grant-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin user grant-admin alice
```

#### `leapmux admin user revoke-admin`

Take a user's administrator access away.

This command changes state.

```text
leapmux admin user revoke-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--force` | required to remove your own administrator access |

```sh
leapmux admin user revoke-admin alice
```

#### `leapmux admin user reset-password`

Set a user's password and revoke every credential they hold.

This command changes state.

```text
leapmux admin user reset-password <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--password string` | new password (prompted for when omitted, so it stays out of the shell history) |

```sh
# Prompt for the new password.
leapmux admin user reset-password alice
```

### `leapmux admin session`

List and revoke sign-in sessions.

#### `leapmux admin session list`

List active sessions, optionally filtered to one user.

Aliases: `ls`.

```text
leapmux admin session list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |

```sh
leapmux admin session list

# List one user's active sessions.
leapmux admin session list --user alice
```

#### `leapmux admin session revoke`

Revoke a session.

This command changes state.

```text
leapmux admin session revoke <session> [flags]
```

| Argument | Description |
|---|---|
| `<session>` | A session id or unique prefix, as "leapmux admin session list" prints it. |

```sh
leapmux admin session revoke 4f1aC2
```

#### `leapmux admin session revoke-user`

Revoke every session of a user.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin session revoke-user <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin session revoke-user alice
```

#### `leapmux admin session purge-expired`

Delete every expired session.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin session purge-expired [flags]
```

```sh
leapmux admin session purge-expired
```

### `leapmux admin worker`

Manage the workers of every user.

#### `leapmux admin worker list`

List the workers of every user.

Aliases: `ls`.

```text
leapmux admin worker list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--status string` | worker status filter (one of: active, deregistering, deleted, all; default: active) |

```sh
leapmux admin worker list

# Include the workers that are gone.
leapmux admin worker list --status all
```

#### `leapmux admin worker get`

Show a worker of any user.

```text
leapmux admin worker get <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id or unique prefix of 4 or more characters, as "leapmux admin worker list" prints it. |

```sh
leapmux admin worker get 5f2c9a1d
```

#### `leapmux admin worker deregister`

Deregister a worker of any user.

This command changes state.

```text
leapmux admin worker deregister <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id or unique prefix of 4 or more characters, as "leapmux admin worker list" prints it. |

```sh
leapmux admin worker deregister 5f2c9a1d
```

#### `leapmux admin worker reg-key`

Manage worker registration keys.

##### `leapmux admin worker reg-key list`

List worker registration keys.

Aliases: `ls`.

```text
leapmux admin worker reg-key list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--include-expired` | include revoked or expired keys (forensics; default shows only live keys) |

```sh
leapmux admin worker reg-key list
```

##### `leapmux admin worker reg-key revoke`

Revoke a worker registration key.

This command changes state.

```text
leapmux admin worker reg-key revoke <reg-key> [flags]
```

| Argument | Description |
|---|---|
| `<reg-key>` | A registration key id or unique prefix, as "leapmux admin worker reg-key list" prints it. |

```sh
leapmux admin worker reg-key revoke 9Zt4Kp
```

##### `leapmux admin worker reg-key purge-expired`

Delete every expired or revoked registration key.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin worker reg-key purge-expired [flags]
```

```sh
leapmux admin worker reg-key purge-expired
```

### `leapmux admin app`

Manage app registrations: what a consent screen authorizes.

#### `leapmux admin app list`

List app registrations.

Aliases: `ls`.

```text
leapmux admin app list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--include-revoked` | include retired apps |

```sh
leapmux admin app list
```

#### `leapmux admin app register`

Register an app; the hub shows its secret once.

This command changes state.

```text
leapmux admin app register <name> [flags]
```

| Flag | Description |
|---|---|
| `--uri string` | the app's home page |
| `--redirect-uri string` | comma-separated addresses the authorization may return to (required for the authorization_code grant) |
| `--scope string` | space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write (required) |
| `--grant-type string` | comma-separated OAuth grant types; empty takes the RFC 7591 default of authorization_code and refresh_token |
| `--type string` | public (a binary a user holds; PKCE protects it) or confidential (a server that can keep a secret) (one of: public, confidential; default: public) |
| `--visibility string` | hub-wide (every account may authorize it; administrators only) or private (yours alone) (one of: hub-wide, private; default: hub-wide) |
| `--allow-elevation` | let this app run the step-up stage, which is what a sensitive change needs |

```sh
leapmux admin app register "Release bot" --scope "workspace:read" --redirect-uri http://127.0.0.1/callback
```

#### `leapmux admin app update`

Change the editable fields of an app.

This command changes state.

```text
leapmux admin app update <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

| Flag | Description |
|---|---|
| `--name string` | new app name |
| `--uri string` | new home page |
| `--redirect-uri string` | replace the redirect list (comma-separated) |
| `--scope string` | replace the permission ceiling; space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write |
| `--grant-type string` | replace the grant-type list (comma-separated) |

```sh
leapmux admin app update lmx_app_x1y2 --name "Release bot 2"
```

#### `leapmux admin app verify`

Vouch for an app, so that its consent screen stops warning.

This command changes state.

```text
leapmux admin app verify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app verify lmx_app_x1y2
```

#### `leapmux admin app unverify`

Withdraw a vouch for an app.

This command changes state.

```text
leapmux admin app unverify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app unverify lmx_app_x1y2
```

#### `leapmux admin app allow-elevation`

Let an app ask a user to prove a factor again.

This command changes state.

```text
leapmux admin app allow-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app allow-elevation lmx_app_x1y2
```

#### `leapmux admin app deny-elevation`

Take that permission away; every open elevation ends at the next call.

This command changes state.

```text
leapmux admin app deny-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app deny-elevation lmx_app_x1y2
```

#### `leapmux admin app revoke`

Retire an app and revoke every credential it holds.

This command changes state.

```text
leapmux admin app revoke <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app revoke lmx_app_x1y2
```

#### `leapmux admin app delete`

Delete an app that never held a credential.

Aliases: `rm`.

This command changes state.

```text
leapmux admin app delete <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app delete lmx_app_x1y2
```

### `leapmux admin idp`

Manage the OAuth and OIDC providers that users sign in with.

#### `leapmux admin idp add`

Add an OAuth or OIDC sign-in provider.

This command changes state.

```text
leapmux admin idp add [flags]
```

| Flag | Description |
|---|---|
| `--type string` | provider type (one of: github, google, apple, oidc; required) |
| `--name string` | display name (defaults to the preset name) |
| `--client-id string` | OAuth client ID |
| `--client-secret string` | OAuth client secret |
| `--issuer-url string` | OIDC issuer URL |
| `--scopes string` | space-separated scopes |
| `--trust-email string` | trust verified provider emails (required for generic OIDC) (one of: true, false) |

```sh
leapmux admin idp add --type github --client-id Iv1.abc --client-secret s3cr3t
```

#### `leapmux admin idp list`

List the sign-in providers.

Aliases: `ls`.

```text
leapmux admin idp list [flags]
```

```sh
leapmux admin idp list
```

#### `leapmux admin idp remove`

Remove a sign-in provider.

Aliases: `rm`.

This command changes state.

```text
leapmux admin idp remove <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

| Flag | Description |
|---|---|
| `--force` | required to remove a provider that is the last login method of an account |

```sh
leapmux admin idp remove 3hKd9s
```

#### `leapmux admin idp enable`

Enable a sign-in provider.

This command changes state.

```text
leapmux admin idp enable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

```sh
leapmux admin idp enable 3hKd9s
```

#### `leapmux admin idp disable`

Disable a sign-in provider.

This command changes state.

```text
leapmux admin idp disable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

```sh
leapmux admin idp disable 3hKd9s
```

### `leapmux admin captcha`

Manage the bot protection: ALTCHA, reCAPTCHA v3 or Turnstile (a solo hub never asks).

#### `leapmux admin captcha get`

Show the captcha configuration in effect.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha get [flags]
```

```sh
leapmux admin captcha get
```

#### `leapmux admin captcha set`

Change the captcha settings, or switch the provider.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha set [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | provider alias; omit to tune the active provider (one of: altcha, recaptcha_v3, turnstile) |
| `--algorithm string` | ALTCHA algorithm (one of: ARGON2ID, PBKDF2/SHA-256, PBKDF2/SHA-384, PBKDF2/SHA-512, SCRYPT, SHA-256, SHA-384, SHA-512) |
| `--cost int` | algorithm cost parameter; 0 restores the default |
| `--memory-cost int` | algorithm memory cost; 0 restores the default |
| `--parallelism int` | algorithm parallelism; 0 restores the default |
| `--expires int` | challenge expiry seconds; 0 restores the default |
| `--site-key string` | provider site key |
| `--secret string` | provider secret (stored encrypted) |
| `--min-score number` | recaptcha_v3 minimum score (0 &lt; v &lt;= 1); 0 restores the default |

```sh
leapmux admin captcha set --provider turnstile --site-key 0x4AAA --secret s3cr3t
```

#### `leapmux admin captcha enable`

Turn captcha verification on.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha enable [flags]
```

```sh
leapmux admin captcha enable
```

#### `leapmux admin captcha disable`

Turn captcha verification off; the honeypot check stays.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha disable [flags]
```

```sh
leapmux admin captcha disable
```

#### `leapmux admin captcha reset`

Restore the captcha defaults of every provider, or of one.

This command changes state.

```text
leapmux admin captcha reset [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | reset one provider's row (omit = all captcha settings) (one of: altcha, recaptcha_v3, turnstile) |

```sh
leapmux admin captcha reset

# Reset one provider.
leapmux admin captcha reset --provider altcha
```

### `leapmux admin rate-limit`

Manage the rate limit of each user operation.

#### `leapmux admin rate-limit list`

List the rate limits of every operation.

Aliases: `ls`.

```text
leapmux admin rate-limit list [flags]
```

```sh
leapmux admin rate-limit list
```

#### `leapmux admin rate-limit set`

Change the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit set <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

| Flag | Description |
|---|---|
| `--max-attempts int` | failed attempts allowed per window (1-1000); 0 restores the default |
| `--window int` | window seconds (60-86400); 0 restores the default |

```sh
leapmux admin rate-limit set elevation --max-attempts 5 --window 900
```

#### `leapmux admin rate-limit enable`

Turn on the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit enable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit enable elevation
```

#### `leapmux admin rate-limit disable`

Turn off the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit disable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit disable elevation
```

#### `leapmux admin rate-limit reset`

Restore the default rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit reset <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit reset elevation
```

### `leapmux admin api-token`

Manage durable API tokens for scripts and integrations.

#### `leapmux admin api-token list`

List API tokens.

Aliases: `ls`.

```text
leapmux admin api-token list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--client-id string` | filter by app (empty = all apps) |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |

```sh
leapmux admin api-token list

# List one user's tokens.
leapmux admin api-token list --user alice
```

#### `leapmux admin api-token issue`

Issue an API token that &lt;user&gt; owns.

This command changes state.

```text
leapmux admin api-token issue <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--client-id string` | the app this credential belongs to (empty = the built-in service-account registration) |
| `--installation-name string` | human-visible name for this installation, e.g. ci-runner-3 (required) |
| `--ttl int` | fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh) |
| `--scope string` | space- or comma-separated permissions, e.g. "file:read git:read" (empty = everything except admin:*) |

```sh
leapmux admin api-token issue ci-bot --installation-name ci-runner-3 --scope "workspace:read"
```

#### `leapmux admin api-token revoke`

Revoke an API token.

This command changes state.

```text
leapmux admin api-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id or unique prefix, as the list command of this group prints it. |

```sh
leapmux admin api-token revoke 7Qx2Lm
```

### `leapmux admin delegation-token`

Manage the delegation tokens that workers mint.

#### `leapmux admin delegation-token list`

List the delegation tokens that workers minted.

Aliases: `ls`.

```text
leapmux admin delegation-token list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |

```sh
leapmux admin delegation-token list
```

#### `leapmux admin delegation-token revoke`

Revoke a delegation token.

This command changes state.

```text
leapmux admin delegation-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id or unique prefix, as the list command of this group prints it. |

```sh
leapmux admin delegation-token revoke 7Qx2Lm
```

### `leapmux admin network`

Inspect the Hub's live TCP listeners, IPC sockets, and host network interfaces.

#### `leapmux admin network status`

Show the Hub's live TCP listeners, local IPC endpoints, and machine interfaces.

Does not support `-q` / `--quiet`.

```text
leapmux admin network status [flags]
```

```sh
leapmux admin network status
leapmux admin network status --json
```

## `leapmux rpc`

Call one procedure of the hub or of a Worker, and print its response. Use it
for a call that no other command makes.

The request is protobuf JSON: the second argument, "-" to read it from
standard input, or {} when the command line omits it. The command refuses a
field that the request type does not declare, before it sends anything. The
response prints as protobuf JSON with the field names of the .proto files,
indented. --json prints it as the data of one envelope instead. A field that
holds its default value does not print, as protobuf JSON states.

The command passes the request directly to the Hub or Worker. The receiving
service applies its own authorization and validation. Check what the
procedure does before calling one that changes state.

The procedures are the methods of the services in proto/leapmux/v1. A
procedure of a hub service runs on the hub. A procedure of a Worker service
needs --worker, the Worker that serves the call. A procedure that streams
does not run here. "leapmux events" prints the events of agents and
terminals, and "leapmux agent messages --follow" prints the messages
of an agent.

Inside a LeapMux tab, the socket of the tab relays these hub procedures
alone:
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

This command changes state.

Does not support `-q` / `--quiet`.

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

## `leapmux skill`

Inspect and install LeapMux agent skills locally or on a Worker.

Aliases: `skills`.

### `leapmux skill list`

List bundled LeapMux agent skills and their install state.

Aliases: `ls`.

```text
leapmux skill list [flags]
```

| Flag | Description |
|---|---|
| `--target string` | target directory: agents (~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt; (default: agents) |
| `--scope string` | local install scope: user (home directory) or project (current working directory) (one of: user, project; default: user) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

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
| `--scope string` | local install scope: user (home directory) or project (current working directory) (one of: user, project; default: user) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux skill status
leapmux skill status --worker name:build-box --json
```

### `leapmux skill install`

Install bundled LeapMux SKILL.md playbooks (plus companion references and
Codex metadata) into standard coding-agent skill directories so agents (Claude
Code, Codex, OpenCode, Gemini CLI, Antigravity, Cursor, and others) can invoke
them directly.

By default, installs into ~/.agents/skills (universal user scope discovered by
Codex, OpenCode, Gemini CLI, Antigravity, and Cursor). Pass --target
claude|codex|cursor|gemini|dir:&lt;path&gt; or --scope project to target another
location. Pass --worker &lt;worker&gt; to enable and sync skills on a remote Worker
over E2EE (with optional --auto-sync to keep them updated on Worker startup).

Installing via the leapmux CLI always matches the installed binary offline;
installing via "npx skills add leapmux/leapmux" tracks the main branch.

Locally edited or unmanaged skill files are never overwritten unless --force
is passed.

Aliases: `add`.

This command changes state.

```text
leapmux skill install [skill]... [flags]
```

| Argument | Description |
|---|---|
| `[skill]...` | bundled skill names to install (or pass --all) |

| Flag | Description |
|---|---|
| `--target string` | install target: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt;; repeat for multiple (repeatable) |
| `--scope string` | local install scope: user (home directory) or project (current working directory) (one of: user, project; default: user) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
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

Aliases: `rm`, `remove`.

This command changes state.

```text
leapmux skill uninstall [skill]... [flags]
```

| Argument | Description |
|---|---|
| `[skill]...` | bundled skill names to remove (or pass --all) |

| Flag | Description |
|---|---|
| `--target string` | install target: agents (default, ~/.agents/skills), claude, codex, cursor, gemini, or dir:&lt;path&gt;; repeat for multiple (repeatable) |
| `--scope string` | local install scope: user (home directory) or project (current working directory) (one of: user, project; default: user) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--all` | select every bundled skill |
| `--force` | also remove locally modified or unmanaged skill files |

```sh
leapmux skill uninstall leapmux-council
leapmux skill uninstall --all --worker name:build-box
```

## `leapmux version`

Print the version of this CLI, and of the hub and worker when connected.

Does not support `-q` / `--quiet`.

```text
leapmux version [flags]
```

```sh
leapmux version
leapmux version --hub https://hub.example.com
```
