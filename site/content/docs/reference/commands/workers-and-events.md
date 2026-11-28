---
title: "Workers & Events"
description: "Reference for leapmux worker, tunnel, events, notify, and notification commands."
type: docs
weight: 6
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `<worker>` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "name:&lt;name&gt;" or a bare case-insensitive worker name, or "self" for the worker of the tab that runs this command. |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

## `leapmux worker`

List workers, read what they report, and register new ones.

Aliases: `wk`.

### `leapmux worker list`

List your workers with their names.

_Aliases: `ls`._

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

_Common: `[worker]` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux worker get
leapmux worker get name:build-box
```

### `leapmux worker info`

Show what an online worker reports about itself: name, OS, home and build.

```text
leapmux worker info [worker] [flags]
```

_Common: `[worker]` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux worker info
```

### `leapmux worker ping`

Measure end-to-end encrypted round-trip latency to an online worker.

```text
leapmux worker ping [worker] [flags]
```

_Common: `[worker]` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux worker ping
leapmux worker ping name:build-box
```

### `leapmux worker deregister`

Deregister one of your workers from the hub.

_Aliases: `rm` · Mutates state._

```text
leapmux worker deregister <worker> [flags]
```

_Common: `<worker>` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux worker deregister name:old-box
```

### `leapmux worker key`

Create, extend, revoke, and email registration keys for new workers.

#### `leapmux worker key create`

Create a registration key that a new worker uses once, within five minutes.

_Mutates state._

```text
leapmux worker key create [flags]
```

```sh
# Register a worker on another machine with the key.
K=$(leapmux worker key create --json | jq -r .data.key)
```

#### `leapmux worker key extend`

Extend a registration key's expiry by five minutes.

_Mutates state._

```text
leapmux worker key extend <key> [flags]
```

```sh
leapmux worker key extend lmx_reg_...
```

#### `leapmux worker key revoke`

Revoke an unused registration key immediately.

_Aliases: `rm` · Mutates state · Does not support `-q` / `--quiet`._

```text
leapmux worker key revoke <key> [flags]
```

```sh
leapmux worker key revoke lmx_reg_...
```

#### `leapmux worker key email`

Email worker registration instructions to your verified email address.

_Mutates state · Does not support `-q` / `--quiet`._

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

_Aliases: `ls`._

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
| `<worker>` | A worker id, a unique prefix of 4 or more characters of one, or "id:&lt;id&gt;", as "leapmux worker pins list" prints it. |

```sh
leapmux worker pins get 5f2c9a1d
```

#### `leapmux worker pins remove`

Forget a worker's pinned key, so the next connection pins it again.

_Aliases: `rm` · Mutates state._

```text
leapmux worker pins remove <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, a unique prefix of 4 or more characters of one, or "id:&lt;id&gt;", as "leapmux worker pins list" prints it. |

```sh
# Forget the key of a worker that was reinstalled.
leapmux worker pins remove 5f2c9a1d
```

## `leapmux tunnel`

Forward TCP ports or connect stdio to a host on a worker over E2EE.

Aliases: `tun`.

### `leapmux tunnel forward`

Open a Noise_NK end-to-end encrypted channel to a Worker, listen on a local TCP address, and forward every accepted connection to &lt;target-host&gt;:&lt;target-port&gt; on that Worker's network.

The spec is [bind-addr:]&lt;local-port&gt;:&lt;target-host&gt;:&lt;target-port&gt;. When bind-addr is omitted, the listener binds 127.0.0.1. Pass local port 0 to let the OS pick a free port. Press Ctrl-C to stop forwarding.

_Aliases: `fwd` · Does not support `-q` / `--quiet`._

```text
leapmux tunnel forward <spec> [flags]
```

| Argument | Description |
|---|---|
| `<spec>` | Port forward specification: "[bind:]&lt;local-port&gt;:&lt;target-host&gt;:&lt;target-port&gt;" (for example "5432:127.0.0.1:5432" or "0.0.0.0:8080:localhost:3000"). |

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
# Forward local port 5432 to Postgres on a remote Worker.
leapmux tunnel forward 5432:127.0.0.1:5432 --worker name:db-box

# Bind on all interfaces and forward to a dev server.
leapmux tunnel forward 0.0.0.0:8080:localhost:3000 --worker name:dev-box
```

### `leapmux tunnel connect`

Open a Noise_NK end-to-end encrypted channel to a Worker, dial &lt;host&gt;:&lt;port&gt; from that Worker, and bridge stdin and stdout directly to the TCP stream. Useful with "ssh -o ProxyCommand='leapmux tunnel connect %h:%p `--worker` &lt;name&gt;'".

_Aliases: `nc` · Does not support `-q` / `--quiet`._

```text
leapmux tunnel connect <target> [flags]
```

| Argument | Description |
|---|---|
| `<target>` | Target address in "&lt;host&gt;:&lt;port&gt;" form (for example "127.0.0.1:22" or "[::1]:8080"). |

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
# Use as an SSH ProxyCommand to reach a remote Worker behind NAT.
ssh -o ProxyCommand="leapmux tunnel connect %h:%p --worker name:build-box" user@127.0.0.1
```

## `leapmux events`

Print what happens in your workspaces as it happens: tabs that open, close and move, what each agent does and asks, what each terminal reports, and the health of the stream itself. Each line is the local time, the kind, the ids, and a summary; `--json` prints one {"data": …} envelope per line instead, with the full ids and a field for each fact. Ctrl-C ends the stream with exit status 0.

The stream watches every workspace, or the one that `--workspace` selects. A tab that opens while the stream runs joins it, and a tab that closes leaves it. With `--tab`, the stream watches that tab only and follows its moves between workspaces. If `--workspace` also selects a workspace, the stream ends when the tab leaves it. The stream also ends when the selected tab closes or the selected workspace is deleted.

The first stream.ready line reports the sources that the stream watches and which of them can deliver. Read the state that you need after it, such as "leapmux agent list". A new run without `--checkpoint` starts from the present.

Use `--tab` with `--checkpoint` to resume the retained events of one agent or terminal. The command saves its position after writing each selected event. Reuse the same file and `--kind` selection on the next run. A record can appear again if the process stops after writing its line and before saving its cursor. Use journal_id, tab_stream_id and sequence to deduplicate. The Worker reports gaps when it cannot supply retained history. A replayed agent request has a request_instance that correlates it with its end. It has a request handle only while that exact instance still needs an answer. With `--checkpoint`, `--limit` counts only the printed non-stream events. A `--tab` checkpoint follows the tab across workspace moves and does not take `--workspace`. After the Worker retires an agent or terminal, resume with its full ID and the same checkpoint. The stream exits after stream.retired confirms the final Worker position. Closing a subagent transcript keeps its history open. A terminal's shell can also exit while its tab stays open.

Use `--checkpoint` without `--tab` or `--workspace` to resume account events when `--kind` selects Worker event families (or when `--kind` is omitted). The checkpoint stores a separate position for each selected Worker event family and for the Hub layout when selected. A membership change can leave a gap in Worker history. The stream reports the gap before recording the new inventory. When `--kind` selects only Hub layout kinds (workspace.* or tab.*), an unscoped `--checkpoint` uses a Hub-wide layout checkpoint so a workspace:read credential works without worker:read.

Use `--workspace` with `--checkpoint` to resume that workspace's committed Hub events. Resume with the same scope and kinds. For a deleted workspace, pass its full ID. The command exits after it reaches the final layout position. The Hub retains tabs that opened, moved, or closed between runs.

In a stream without `--checkpoint`, when the Hub or a Worker cannot deliver, it prints stream.unavailable and retries. When the source delivers again, it prints stream.gap with the kinds that it may have lost, then each change of an agent's activity and status, and of the tabs and workspaces, that happened during the gap, marked as reconciled. `--kind` does not hide these lines: a stream event that it does not select prints on stderr.

Kinds:

- workspace.created: a workspace was created
- workspace.renamed: a workspace has a new title
- workspace.deleted: a workspace was deleted
- tab.opened: a tab opened, or it moved into the workspace that `--workspace` selects
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
- stream.heartbeat: the stream runs; only `--heartbeat` prints it
- stream.retired: the Worker finished the checkpointed tab's event history

_Aliases: `ev` · Does not support `-q` / `--quiet`._

```text
leapmux events [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | watch only this workspace: an id, a prefix, "title:&lt;title&gt;" or "self" (default: every workspace) |
| `--tab string` | watch only this tab: an id, a prefix, "id:&lt;id&gt;", "title:&lt;title&gt;" or "self" |
| `--kind string` | print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind) (one of: workspace, tab, agent, terminal, notification, stream, workspace.created, workspace.renamed, workspace.deleted, tab.opened, tab.closed, tab.moved, tab.renamed, tab.status, agent.activity, agent.status, agent.turn_end, agent.request, agent.request_ended, agent.todos, agent.background_tasks, agent.goal, agent.queue, terminal.status, terminal.title, terminal.bell, terminal.notification, terminal.progress, terminal.exit, notification.created, notification.cleared, stream.ready, stream.unavailable, stream.gap, stream.heartbeat, stream.retired; repeatable) |
| `-n, --limit int` | end the stream after this many events; 0 runs without a limit |
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
leapmux events --tab 87a19d5f --checkpoint .leapmux/tmp/terminal-events.json --json

# Resume permission and question events for one agent.
leapmux events --tab 87a19d5f --checkpoint .leapmux/tmp/agent-events.json --kind agent.request,agent.request_ended --json

# Resume terminal events from every Worker in the account.
leapmux events --checkpoint .leapmux/tmp/account-events.json --kind terminal --json
```

## `leapmux notify`

Show a notification in every LeapMux window that you have open: a toast with the title and the body. While a window is hidden, it also shows a desktop notification, when "Desktop notifications" is on in Preferences.

The Worker identifies the sending tab when the command runs inside one. Use `--tab` or `--workspace` to attach a destination the user can open.

With `--tab`, the notification goes through the destination's Worker. For other notifications, the command uses the sending tab's Worker. Outside a tab, pass `--worker`. A window hears a Worker while one of your tabs runs there.

The command returns the notification ID. Use "notification list" to read the Worker's recent notifications, and "notification clear" to remove one by ID. A window that connects later does not show an old toast.

The Worker refuses a title or a body that is too long, and states the limit.

_Mutates state._

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

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

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

Show a notification in every LeapMux window that you have open: a toast with the title and the body. While a window is hidden, it also shows a desktop notification, when "Desktop notifications" is on in Preferences.

The Worker identifies the sending tab when the command runs inside one. Use `--tab` or `--workspace` to attach a destination the user can open.

With `--tab`, the notification goes through the destination's Worker. For other notifications, the command uses the sending tab's Worker. Outside a tab, pass `--worker`. A window hears a Worker while one of your tabs runs there.

The command returns the notification ID. Use "notification list" to read the Worker's recent notifications, and "notification clear" to remove one by ID. A window that connects later does not show an old toast.

The Worker refuses a title or a body that is too long, and states the limit.

_Mutates state._

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

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

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

_Aliases: `ls`._

```text
leapmux notification list [flags]
```

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux notification list --worker name:build-box
```

### `leapmux notification clear`

Remove one or more notifications from a Worker and dismiss their toasts.

_Aliases: `rm` · Mutates state._

```text
leapmux notification clear [notification-id]... [flags]
```

| Argument | Description |
|---|---|
| `[notification-id]...` | One or more IDs returned by notify or notification list (or pass `--all`). |

| Flag | Description |
|---|---|
| `--all` | clear every recent notification on the Worker |

_Common: `--worker` (see [common arguments and flags](#common-arguments-and-flags))._

```sh
leapmux notification clear 7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a7fe1879a --worker name:build-box

# Clear every recent notification on the Worker.
leapmux notification clear --all --worker name:build-box
```
