---
title: "Workspaces & Tabs"
description: "Reference for leapmux workspace, section, tab, and tree commands."
type: docs
weight: 3
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the workspace of the tab that runs this command. |
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the tab that runs this command. |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

## `leapmux workspace`

List, create, organize, export, apply, and delete workspaces.

Aliases: `ws`.

### `leapmux workspace list`

List your workspaces.

_Aliases: `ls`._

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
| `[workspace]` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux workspace get
leapmux workspace get "title:Release 1.4"
```

### `leapmux workspace create`

Create a workspace.

_Mutates state._

```text
leapmux workspace create <title> [flags]
```

```sh
# Create a workspace and keep its id.
W=$(leapmux workspace create "Release 1.4" -q)
```

### `leapmux workspace rename`

Rename a workspace.

_Mutates state._

```text
leapmux workspace rename <workspace> <title> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux workspace rename self "Release 1.5"
```

### `leapmux workspace archive`

Archive a workspace (moves it to the Archived section and pauses its tabs).

_Mutates state._

```text
leapmux workspace archive <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux workspace archive 9f3kQ2aB
```

### `leapmux workspace unarchive`

Restore an archived workspace back to an active section.

_Mutates state._

```text
leapmux workspace unarchive <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--section string` | destination active section (default: "In progress") |

```sh
leapmux workspace unarchive 9f3kQ2aB
leapmux workspace unarchive 9f3kQ2aB --section "In progress"
```

### `leapmux workspace move`

Move a workspace to another sidebar section or reorder it within a section.

_Aliases: `mv` · Mutates state._

```text
leapmux workspace move <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

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

_Aliases: `rm` · Mutates state._

```text
leapmux workspace delete <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | delete the workspace even when this tab lives in it, which ends this tab's process too |

```sh
leapmux workspace delete 9f3kQ2aB
```

### `leapmux workspace export`

Export a workspace's main tile tree, floating windows, and live tab definitions (agents with provider/model/options/working_dir, terminals, and file viewers) into a declarative YAML or JSON template that "workspace apply" can reproduce.

_Does not support `-q` / `--quiet`._

```text
leapmux workspace export [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | See [common arguments and flags](#common-arguments-and-flags). |

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

Apply a declarative workspace template produced by "workspace export" (or written by hand). Reads YAML or JSON from &lt;file&gt; ("-" reads stdin).

Creates a new workspace unless --workspace targets an existing one, applies the main tile layout and any floating windows, and opens each declared agent, terminal, and file tab in its mapped tile.

_Mutates state._

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

_Aliases: `ls`._

```text
leapmux section list [flags]
```

```sh
leapmux section list
leapmux section list --json
```

### `leapmux section create`

Create a custom workspace section.

_Mutates state._

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

_Mutates state._

```text
leapmux section rename <section> <name> [flags]
```

```sh
leapmux section rename "Client Work" "Active Clients"
```

### `leapmux section move`

Move a section to another sidebar or reorder it.

_Aliases: `mv` · Mutates state._

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

_Aliases: `rm` · Mutates state._

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

_Aliases: `ls`._

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
| `[tab]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab get
leapmux tab get 87a19d5f
```

### `leapmux tab close`

Close one or more tabs and stop what runs in them.

_Aliases: `rm` · Mutates state._

```text
leapmux tab close <tab>... [flags]
```

| Argument | Description |
|---|---|
| `<tab>...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux tab close 87a19d5f --worktree keep
```

### `leapmux tab rename`

Rename an agent or terminal tab.

_Mutates state._

```text
leapmux tab rename <tab> <title> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab rename self "Release notes"
```

### `leapmux tab move`

Move a tab to another tile or workspace, or reorder it.

_Aliases: `mv` · Mutates state._

```text
leapmux tab move <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | See [common arguments and flags](#common-arguments-and-flags). |

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

Show a tab in every LeapMux window that you have open: each window switches to the tab's workspace, and the tab becomes the active tab of its tile, as a click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own state, so a window that you open later shows what it showed before, and a window that does not know the tab yet ignores the command.

_Mutates state._

```text
leapmux tab reveal <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux tab reveal self
```

### `leapmux tab open-image`

Open an image from an agent message in its own tab.

_Mutates state._

```text
leapmux tab open-image <agent> <seq> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
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

Show a short status beside a tab's title, in the tab bar and in the sidebar of every LeapMux window that you have open. A window that you open later shows it too. The most recently set status is visible.

Give each reporter a --key to keep its status separate. Setting a key again replaces its previous status. Without --key, the command uses "default".

--progress adds a progress ring, from 0 to 100. Without it, the status shows no progress. --tone colors the status: neutral (the default), success, warning or error.

The status stays until "tab status clear" removes its key, the tab closes, or the tab's Worker stops. Use "tab status list" to see all keys. The Worker reports its limits on the status text and keys when it refuses a request.

_Mutates state._

```text
leapmux tab status set <tab> <text> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<text>` | A short text, such as "Running the tests", or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--key string` | which status entry to change (default: default) |
| `--progress int` | how far the work is, from 0 to 100 (default: no progress) |
| `--tone string` | the color of the status (one of: neutral, success, warning, error; default: neutral) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab status set self "Running the tests"

# Report progress for a separate task.
leapmux tab status set 87a19d5f "Migrating rows" --key migration --progress 40

# Mark the tab when the work fails.
leapmux tab status set self "Tests failed" --tone error
```

#### `leapmux tab status clear`

Remove one or more status entries from a tab.

_Mutates state._

```text
leapmux tab status clear <tab> [key]... [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[key]...` | One or more status keys to clear (default: the default key, or --all for every key). |

| Flag | Description |
|---|---|
| `--key string` | which status entry to change (default: default) |
| `--all` | remove every status entry on the tab |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab status clear self
leapmux tab status clear 87a19d5f --key migration

# Remove every status entry on the tab.
leapmux tab status clear self --all
```

#### `leapmux tab status list`

List a tab's status entries, with the visible one first (default: this tab).

_Aliases: `ls`._

```text
leapmux tab status list [tab] [flags]
```

| Argument | Description |
|---|---|
| `[tab]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux tab status list
leapmux tab status list 87a19d5f
```

## `leapmux tree`

Show a hierarchical overview of sections, workspaces, tiles, agents, terminals and files.

_Aliases: `overview`, `ps`._

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
