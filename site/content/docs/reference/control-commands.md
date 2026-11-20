---
title: "Control Command Reference"
description: "Every leapmux control command with its arguments, flags and examples, generated from the command tree of the binary."
type: docs
weight: 2
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

This page lists every command of `leapmux control` with its arguments, its flags and its examples. The binary prints the same text with `--help`, and `leapmux commands --json` prints it for a program. [Control CLI](/docs/using/control-cli/) explains the concepts: how a command selects its target, what it prints, how it fails, and how it signs in.

## Flags of every command

Every command under `leapmux control` takes these flags.

| Flag | Description |
|---|---|
| `--hub string` | hub URL (default: the socket of this tab, else $LEAPMUX_HUB, else the default hub of the stored credentials) |
| `--json` | print the result as one JSON envelope per line |
| `-q, --quiet` | print only the ids of the result, one per line |

### `leapmux control whoami`

Show the identity that this CLI acts as.

```text
leapmux control whoami [flags]
```

```sh
leapmux control whoami

# Inside a tab, print the id of this tab.
leapmux control whoami --json | jq -r .data.tab_id
```

### `leapmux control version`

Print the version of this CLI, and of the hub when one is selected.

```text
leapmux control version [flags]
```

```sh
leapmux control version
leapmux control version --hub https://hub.example.com
```

### `leapmux control guide`

Print the guide for an agent that drives LeapMux.

```text
leapmux control guide [topic] [flags]
```

| Argument | Description |
|---|---|
| `[topic]` | a topic ID from --topics |

| Flag | Description |
|---|---|
| `--topics` | list the available guide topics |

```sh
leapmux control guide
leapmux control guide --topics --json
leapmux control guide targets
```

## `leapmux control auth`

Sign this CLI in to a hub, and manage its credentials.

### `leapmux control auth login`

Sign this CLI in to a hub through a browser.

This command changes state.

```text
leapmux control auth login [flags]
```

| Flag | Description |
|---|---|
| `--device-name string` | a label for this credential; the account's list of connected apps shows it (default: &lt;user&gt;@&lt;host&gt; of this machine) |
| `--device-code` | sign in with a code that you enter on any device with a browser (for SSH and containers) |
| `--scope string` | permissions to ask for, separated by spaces or commas, such as "file:read git:read" (default: everything except admin:*) |

```sh
leapmux control auth login --hub https://hub.example.com

# Sign in from a machine without a browser.
leapmux control auth login --hub https://hub.example.com --device-code
```

### `leapmux control auth logout`

Revoke this CLI's credential for a hub and delete it.

This command changes state.

```text
leapmux control auth logout [flags]
```

```sh
leapmux control auth logout --hub https://hub.example.com
```

### `leapmux control auth list`

List the hubs that this machine holds a credential for.

```text
leapmux control auth list [flags]
```

```sh
leapmux control auth list
```

### `leapmux control auth use`

Choose the hub that a command uses without --hub.

This command changes state.

```text
leapmux control auth use <hub> [flags]
```

```sh
leapmux control auth use https://hub.example.com
```

### `leapmux control auth status`

Show the user, the expiry and the permissions of the hub's credential.

```text
leapmux control auth status [flags]
```

```sh
leapmux control auth status
```

### `leapmux control auth credentials`

List every credential that can reach your account on the hub.

```text
leapmux control auth credentials [flags]
```

```sh
leapmux control auth credentials
```

## `leapmux control workspace`

List, create, rename and delete workspaces.

### `leapmux control workspace list`

List your workspaces.

```text
leapmux control workspace list [flags]
```

```sh
leapmux control workspace list

# Print only the ids.
leapmux control workspace list -q
```

### `leapmux control workspace get`

Show one workspace (default: the workspace of this tab).

```text
leapmux control workspace get [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
leapmux control workspace get
leapmux control workspace get "title:Release 1.4"
```

### `leapmux control workspace create`

Create a workspace.

This command changes state.

```text
leapmux control workspace create <title> [flags]
```

```sh
# Create a workspace and keep its id.
W=$(leapmux control workspace create "Release 1.4" -q)
```

### `leapmux control workspace rename`

Rename a workspace.

This command changes state.

```text
leapmux control workspace rename <workspace> <title> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
leapmux control workspace rename self "Release 1.5"
```

### `leapmux control workspace delete`

Delete a workspace and close every tab in it.

This command changes state.

```text
leapmux control workspace delete <workspace> [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--allow-self` | delete the workspace even when this tab lives in it, which ends this tab's process too |

```sh
leapmux control workspace delete 9f3kQ2aB
```

## `leapmux control tab`

List, close, rename, move and reveal tabs of every type, and set their status.

### `leapmux control tab list`

List your tabs with their titles and states.

```text
leapmux control tab list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the tabs of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--type string` | list only the tabs of this type (one of: agent, terminal, file, image) |

```sh
leapmux control tab list

# List the agent tabs of the workspace that this tab lives in.
leapmux control tab list --workspace self --type agent
```

### `leapmux control tab get`

Show one tab (default: this tab).

```text
leapmux control tab get [tab] [flags]
```

| Argument | Description |
|---|---|
| `[tab]` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

```sh
leapmux control tab get
leapmux control tab get 87a19d5f
```

### `leapmux control tab close`

Close a tab and stop what runs in it.

This command changes state.

```text
leapmux control tab close <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |

```sh
leapmux control tab close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux control tab close 87a19d5f --worktree keep
```

### `leapmux control tab rename`

Rename an agent or terminal tab.

This command changes state.

```text
leapmux control tab rename <tab> <title> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

```sh
leapmux control tab rename self "Release notes"
```

### `leapmux control tab move`

Move a tab to another tile or workspace, or reorder it.

This command changes state.

```text
leapmux control tab move <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

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
leapmux control tab move 87a19d5f --workspace "title:Release 1.4"

# Put a tab before another tab of the same tile.
leapmux control tab move 87a19d5f --before 4cbb3f34
```

### `leapmux control tab reveal`

Show a tab in every LeapMux window that you have open: each window switches
to the tab's workspace, and the tab becomes the active tab of its tile, as a
click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own
state, so a window that you open later shows what it showed before, and a
window that does not know the tab yet ignores the command.

This command changes state.

```text
leapmux control tab reveal <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

```sh
leapmux control tab reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux control tab reveal self
```

## `leapmux control tab status`

Show or remove a short status beside a tab's title.

### `leapmux control tab status set`

Show a short status beside a tab's title, in the tab bar and in the sidebar of
every LeapMux window that you have open. A window that you open later shows it
too. A new status replaces the old one.

--progress adds a progress ring, from 0 to 100. Without it, the status shows
no progress. --tone colors the status: neutral (the default), success,
warning or error.

The status stays until "tab status clear" removes it, the tab closes, or the
tab's Worker stops. The Worker refuses a text that is too long, and states the
limit.

This command changes state.

```text
leapmux control tab status set <tab> <text> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |
| `<text>` | A short text, such as "Running the tests", or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--progress int` | how far the work is, from 0 to 100 (default: no progress) |
| `--tone string` | the color of the status (one of: neutral, success, warning, error; default: neutral) |

```sh
leapmux control tab status set self "Running the tests"

# Report how far the work is.
leapmux control tab status set 87a19d5f "Migrating rows" --progress 40

# Mark the tab when the work fails.
leapmux control tab status set self "Tests failed" --tone error
```

### `leapmux control tab status clear`

Remove the status of a tab.

This command changes state.

```text
leapmux control tab status clear <tab> [flags]
```

| Argument | Description |
|---|---|
| `<tab>` | A tab id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the tab that runs this command. |

```sh
leapmux control tab status clear self
leapmux control tab status clear 87a19d5f
```

## `leapmux control agent`

Start, message, watch and configure coding agents.

### `leapmux control agent list`

List your agents with their states.

```text
leapmux control agent list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the agents of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux control agent list

# List the agents of the workspace that this tab lives in.
leapmux control agent list --workspace self
```

### `leapmux control agent get`

Show an agent's state and settings (default: this agent).

```text
leapmux control agent get [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--options` | print every option group with its choices; --json always includes them |

```sh
leapmux control agent get

# Show every option that the agent offers.
leapmux control agent get 87a19d5f --options
```

### `leapmux control agent open`

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
leapmux control agent open [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux control agent providers" lists them (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--model string` | model |
| `--effort string` | reasoning effort |
| `--permission-mode string` | permission mode |
| `--title string` | tab title (default: the worker picks one) |
| `--message string` | queue a first message with the agent creation |
| `--resume string` | resume this earlier session of the provider; "agent sessions" lists them |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
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

```sh
# Start an agent beside this tab and keep its id.
A=$(leapmux control agent open --provider claude --message "Run the tests" -q)

# Start an agent in a new git worktree.
leapmux control agent open --worktree fix-flaky-spec --base main

# Continue an earlier session that "agent sessions" lists.
leapmux control agent open --provider claude --resume 4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90

# Open an agent that a retry of the same command does not open twice.
leapmux control agent open --provider codex --message "Review the diff" --request-id nightly-review-2026-11-19
```

### `leapmux control agent send`

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
leapmux control agent send <agent> [text] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the message from this file ("-" reads stdin) |
| `--attach string` | attach this file; repeat it for more than one (repeatable) |
| `--at string` | hold the message until this time: an RFC 3339 time with its offset, or + and a duration from now, such as +10m |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait until the agent settles after the message, then print its last reply |
| `--all` | with --wait, print every reply after the message, not only the last one |
| `--timeout duration` | with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit (default: 0s) |

```sh
leapmux control agent send 87a19d5f "Run the tests"

# Send the contents of a file.
leapmux control agent send 87a19d5f --file prompt.md

# Send a message that starts with a dash.
leapmux control agent send 87a19d5f -- "-v is the verbose flag"

# Ask a question and print the answer.
leapmux control agent send 87a19d5f "Which tests fail?" --wait --timeout 30m

# Attach a screenshot and a log.
leapmux control agent send 87a19d5f "Why does the page look like this?" --attach screenshot.png --attach console.log

# Send the message in ten minutes.
leapmux control agent send 87a19d5f "Check the deploy" --at +10m

# Send a message that a retry of the same command does not send twice.
leapmux control agent send 87a19d5f "Run the release checklist" --request-id release-2026-11-19
```

### `leapmux control agent wait`

Wait until each agent reaches a state, then print the state of each.

An agent reaches the state when the Worker reports it at any moment after the
wait began, so an agent that is in the state already ends the wait at once.
"settled" is "idle" or "waiting_for_user": the agent works no more, because it
finished or because it waits for an answer to a permission prompt. An agent
whose input queue holds a message that it will read is neither idle nor
settled yet, as an agent that "agent open --message" just started.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, or an agent closed before it reached the state.
It prints the state of each agent in both cases.

```text
leapmux control agent wait <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "title:&lt;title&gt;". |

| Flag | Description |
|---|---|
| `--until string` | the state to wait for (one of: settled, idle, working, waiting_for_user; default: settled) |
| `--any` | end the wait when one agent reaches the state, not every agent |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |

```sh
leapmux control agent wait 87a19d5f

# Wait until the first of two agents waits for an answer, for 10 minutes at most.
leapmux control agent wait 87a19d5f 5c2e0b91 --until waiting_for_user --any --timeout 10m
```

### `leapmux control agent requests`

List the pending control requests of agents: permission prompts, plans
to approve, questions, MCP elicitation forms and extension dialogs.

Each request prints with its handle. "agent approve", "agent deny" and
"agent answer" take the agent and the handle, or a unique prefix of the
handle. A request that the agent replaces gets a new handle, so an answer
never reaches a request that you did not read.

The text form prints each request in full, with the numbers that --option
takes. --json prints each prompt as data.

```text
leapmux control agent requests [agent]... [flags]
```

| Argument | Description |
|---|---|
| `[agent]...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. Without one, the command lists the requests of every agent. |

| Flag | Description |
|---|---|
| `--workspace string` | without an agent, list only the agents of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux control agent requests

# List the requests of one agent, as JSON.
leapmux control agent requests 87a19d5f --json
```

### `leapmux control agent approve`

Approve a pending request:

  - A permission prompt: its one-time approval. Choose another option, such
    as an approval for the session, with "agent answer --option".
  - A plan: the approval. --permission-mode and --clear-context set how the
    agent implements it.
  - An MCP elicitation: the accept, with the form's default values. Fill a
    form with "agent answer --field".
  - A confirm dialog: the confirmation.

An agent cannot answer its own requests.

This command changes state.

```text
leapmux control agent approve <agent> <request> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "title:&lt;title&gt;". |
| `<request>` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |

```sh
leapmux control agent approve 87a19d5f 3kQ9xZ1b

# Approve a plan, and implement it in a fresh context.
leapmux control agent approve 87a19d5f 3kQ9 --permission-mode acceptEdits --clear-context
```

### `leapmux control agent deny`

Refuse a pending request:

  - A permission prompt: its one-time refusal, with --message as the reason.
  - A plan: the refusal, with --message as what to change.
  - A question: the refusal to answer, with --message as the reason.
  - An MCP elicitation: the decline.
  - A dialog: the refusal of a confirm dialog, or the cancel of another.

An agent cannot answer its own requests.

This command changes state.

```text
leapmux control agent deny <agent> <request> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "title:&lt;title&gt;". |
| `<request>` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--message string` | the reason of the refusal, which the agent reads |

```sh
leapmux control agent deny 87a19d5f 3kQ9xZ1b --message "Run the unit tests only"
```

### `leapmux control agent answer`

Answer a pending request with any answer that it offers. "agent requests"
numbers the options of each request.

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
leapmux control agent answer <agent> <request> [text]... [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "title:&lt;title&gt;". |
| `<request>` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |
| `[text]...` | A typed answer, or "-" to read it from standard input. With several questions, write "&lt;question&gt;:&lt;text&gt;". |

| Flag | Description |
|---|---|
| `--option string` | an option by its number, or "&lt;question&gt;:&lt;number&gt;" with several questions; repeat it for more than one (repeatable) |
| `--field string` | a form field as &lt;name&gt;=&lt;value&gt;; repeat it for more than one (repeatable) |
| `--message string` | the reason of a refusing option or choice, which the agent reads |
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |

```sh
# Choose the second option of a question.
leapmux control agent answer 87a19d5f 3kQ9 --option 2

# Choose two options of a multiple-choice question, and type one more answer.
leapmux control agent answer 87a19d5f 3kQ9 --option 1 --option 3 "Metrics"

# Answer two questions.
leapmux control agent answer 87a19d5f 3kQ9 --option 1:2 "2:Use the staging database"

# Approve a command for the rest of the session.
leapmux control agent answer 87a19d5f 3kQ9 --option 2

# Fill an MCP form.
leapmux control agent answer 87a19d5f 3kQ9 --field repository=leapmux --field private=true
```

### `leapmux control agent interrupt`

Stop an agent's current turn.

This command changes state.

```text
leapmux control agent interrupt <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--reason string` | a reason that the agent's transcript records |

```sh
leapmux control agent interrupt 87a19d5f
```

### `leapmux control agent clear`

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
leapmux control agent clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

```sh
leapmux control agent clear 87a19d5f

# Clear the context, then give the agent its next task.
leapmux control agent clear 87a19d5f && leapmux control agent send 87a19d5f "Review the diff of the last commit"
```

### `leapmux control agent compact`

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
leapmux control agent compact <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

```sh
leapmux control agent compact 87a19d5f
```

### `leapmux control agent set`

Change an agent's model, effort, permission mode or another option.

This command changes state.

```text
leapmux control agent set <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--model string` | model id |
| `--effort string` | effort id |
| `--permission-mode string` | permission mode |
| `--option string` | provider option as key=value; repeat it for more than one; "agent get --options" lists them (repeatable) |

```sh
leapmux control agent set 87a19d5f --model opus --effort high

# Set an option that only one provider has.
leapmux control agent set 87a19d5f --option sandbox=workspace-write
```

### `leapmux control agent messages`

Print an agent's messages, or follow new ones.

A message that another agent sent prints its sender after its source, and
--json states the sender as sender.

```text
leapmux control agent messages [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

| Flag | Description |
|---|---|
| `--anchor string` | page to print (one of: latest, oldest, before, after; default: latest) |
| `--cursor-seq int` | the seq that --anchor before or after starts from; the page excludes it |
| `--limit int` | messages per page (default: 50) |
| `--follow` | print new messages as they arrive, until interrupted |

```sh
leapmux control agent messages 87a19d5f

# Print new messages as they arrive, one JSON envelope per line.
leapmux control agent messages 87a19d5f --follow --json
```

## `leapmux control agent queue`

List, remove and reorder the inputs that wait for an agent, and pause or resume its queue.

### `leapmux control agent queue list`

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

```text
leapmux control agent queue list [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

```sh
leapmux control agent queue list 87a19d5f

# Print the queue as data, with the reorder segment of each input.
leapmux control agent queue list 87a19d5f --json
```

### `leapmux control agent queue remove`

Remove an input from an agent's input queue. The agent never reads it.

An input that the agent already reads stays: the command fails with
conflict. A removal also ends an edit of the input that LeapMux has open.

A retry of the "agent send --request-id" run that queued the input queues
nothing for 7 days after the removal.

This command changes state.

```text
leapmux control agent queue remove <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

```sh
leapmux control agent queue remove 87a19d5f 3kQ9xZ1b

# Remove every input that waits.
for i in $(leapmux control agent queue list 87a19d5f -q); do leapmux control agent queue remove 87a19d5f "$i"; done
```

### `leapmux control agent queue move`

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

This command changes state.

```text
leapmux control agent queue move <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

| Flag | Description |
|---|---|
| `--before string` | move the input before this input, as an id or a unique prefix |
| `--first` | move the input to the first place of its segment |
| `--last` | move the input to the last place of its segment |

```sh
# Move an input before another one.
leapmux control agent queue move 87a19d5f 3kQ9xZ1b --before 9Lm2Pq7c

# Make an input the next one that the agent reads.
leapmux control agent queue move 87a19d5f 3kQ9xZ1b --first
```

### `leapmux control agent queue pause`

Pause an agent's input queue. The agent finishes its current turn, and then
reads no queued input until the queue resumes. New inputs still join the
queue.

LeapMux pauses a queue by itself too: when you interrupt the agent, when the
agent stops, and when an input does not reach the agent. "agent queue list"
states why a queue is paused.

This command changes state.

```text
leapmux control agent queue pause <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

```sh
leapmux control agent queue pause 87a19d5f
```

### `leapmux control agent queue resume`

Resume an agent's input queue, whatever paused it, so that the agent reads
the next input when it can.

A failed input keeps its failure. While the first input failed, or while
LeapMux has it open in an editor, the queue still holds, and the command
says so on stderr. Remove the failed input with "agent queue remove", or
retry it in LeapMux.

This command changes state.

```text
leapmux control agent queue resume <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the agent tab that runs this command. |

```sh
leapmux control agent queue resume 87a19d5f
```

### `leapmux control agent providers`

List the agent providers that a worker offers.

```text
leapmux control agent providers [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control agent providers
leapmux control agent providers --worker name:build-box
```

### `leapmux control agent sessions`

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
leapmux control agent sessions [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--dir string` | the directory that the sessions ran in (default: $LEAPMUX_CONTROL_WORKING_DIR) |

```sh
# List the sessions of this tab's provider in this tab's directory.
leapmux control agent sessions

# List the Claude Code sessions of a directory on another worker.
leapmux control agent sessions --provider claude --worker name:build-box --dir ~/src/app

# Keep the id of the newest session, for agent open --resume.
S=$(leapmux control agent sessions --provider claude -q | head -1)
```

## `leapmux control terminal`

Open, type into, read and resize terminals.

### `leapmux control terminal list`

List your terminals.

```text
leapmux control terminal list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the terminals of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |

```sh
leapmux control terminal list
```

### `leapmux control terminal get`

Show a terminal's size, shell and directory (default: this terminal).

```text
leapmux control terminal get [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |

```sh
leapmux control terminal get
leapmux control terminal get 4cbb3f34
```

### `leapmux control terminal open`

Open a new terminal in a new tab. Without --workspace and --tile, the new
tab opens beside the tab that runs leapmux.

--command runs a command line in the new shell once the shell is ready: the
Worker pastes the command and presses Enter, as "terminal run" does. The
shell is ready when its line editor turns on bracketed paste, which zsh, fish
and bash 5.1 or later do. A shell that never does, such as sh, gets the
command after a short wait. Only the new shell runs the command, so a restart
of the terminal does not run it again. The command line is the value of
--command, or "-" to read it from stdin. --command does not wait for the
program to finish: read its output with "terminal read", or wait with
"terminal wait".

This command changes state.

```text
leapmux control terminal open [flags]
```

| Flag | Description |
|---|---|
| `--command string` | run this command line once the shell is ready, as "terminal run" runs one ("-" reads stdin) |
| `--shell string` | shell to start (default: the worker's default shell) |
| `--start-dir string` | directory the shell starts in (default: --dir) |
| `--title string` | tab title (default: the worker picks one) |
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

```sh
# Open a terminal beside this tab and keep its id.
T=$(leapmux control terminal open -q)

# Open a terminal in a new git worktree.
leapmux control terminal open --worktree try-fix --dir ~/src/app

# Run the tests in a new terminal, then print the exit code of its shell.
T=$(leapmux control terminal open -q --command 'make test; exit') && leapmux control terminal wait "$T" --exit
```

### `leapmux control terminal send`

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
leapmux control terminal send <terminal> [text] [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the bytes from this file ("-" reads stdin) |
| `--key string` | press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order (repeatable) |
| `--paste` | send the text as a paste, between bracketed-paste markers when the program enabled them |

```sh
leapmux control terminal send 4cbb3f34 $'make test\n'

# Stop the program in the foreground.
leapmux control terminal send 4cbb3f34 --key ctrl-c

# Choose the second entry of a menu.
leapmux control terminal send 4cbb3f34 --key down --key enter

# Paste a file into the program, as the browser pastes it.
leapmux control terminal send 4cbb3f34 --paste --file snippet.py

# Type the bytes of a file, such as escape sequences.
leapmux control terminal send 4cbb3f34 --file keys.bin
```

### `leapmux control terminal run`

Run a command in a terminal: send the command line and press Enter, in one
request, so no other input can arrive between them. The command is the second
argument, or "-" to read it from stdin. The line endings at its end are
dropped, because the command presses Enter itself.

The command line arrives as a paste, as "terminal send --paste" sends it. A
line editor that enabled bracketed paste therefore inserts it as it is: an
abbreviation, an automatic closing bracket or a vi key binding cannot change
it. A program without bracketed paste reads it as typed text. To type the
command key by key in any case, run
"terminal send &lt;terminal&gt; &lt;command&gt; --key enter".

The command does not wait for the program to finish. Read its output with
"terminal read".

This command changes state.

```text
leapmux control terminal run <terminal> <command> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |
| `<command>` | The command line, or "-" to read it from standard input. |

```sh
leapmux control terminal run 4cbb3f34 'make test'

# Run the command line that stdin holds.
echo 'go test ./...' | leapmux control terminal run 4cbb3f34 -
```

### `leapmux control terminal read`

Print what a terminal shows, as plain text. The CLI replays the bytes that
the Worker retains through a terminal emulator at the terminal's size. The
emulator is a Go port of xterm.js, which the browser uses, so the text is the
text that the browser shows. Colors and other attributes do not print.

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

```text
leapmux control terminal read [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--lines int` | print only the last N lines |
| `--scrollback` | also print the lines above the screen |
| `--raw` | print the retained bytes as they are, with their escape sequences |

```sh
leapmux control terminal read 4cbb3f34

# Print the last 200 lines, the scrollback included.
leapmux control terminal read 4cbb3f34 --scrollback --lines 200

# Page through the retained bytes with their colors.
leapmux control terminal read 4cbb3f34 --raw | less -R
```

### `leapmux control terminal wait`

Wait until a terminal meets one condition, then print what met it:

  - --exit waits until the shell of the terminal ends, and prints its exit
    code.
  - --text waits until a line of the terminal holds the text.
  - --regex waits until a line of the terminal matches the regular
    expression, in RE2 syntax.

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
it. Use --scrollback to include the retained history from before the wait.
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

"leapmux control events --kind terminal" prints the bells, the titles, the
notifications, the progress reports and the exits of terminals as they
happen.

```text
leapmux control terminal wait <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |

| Flag | Description |
|---|---|
| `--exit` | wait until the shell of the terminal ends, and print its exit code |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | wait until a line of the terminal matches this regular expression, in RE2 syntax |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |

```sh
# Wait until the shell ends, and keep its exit code.
CODE=$(leapmux control terminal wait 4cbb3f34 --exit)

# Run the tests, then wait for the line that the last command prints.
leapmux control terminal run 4cbb3f34 'make test; echo DONE' && leapmux control terminal wait 4cbb3f34 --regex '^DONE$' --timeout 10m

# Wait until a server reports that it listens, for 2 minutes at most.
leapmux control terminal wait 4cbb3f34 --text 'Listening on' --timeout 2m

# Look for a completed build in the terminal's retained output.
leapmux control terminal wait 4cbb3f34 --text 'Build complete' --scrollback
```

### `leapmux control terminal resize`

Resize a terminal.

This command changes state.

```text
leapmux control terminal resize <terminal> <cols> <rows> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |

```sh
leapmux control terminal resize 4cbb3f34 120 40
```

### `leapmux control terminal restart`

Restart a terminal's shell.

This command changes state.

```text
leapmux control terminal restart <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the terminal that runs this command. |

```sh
leapmux control terminal restart 4cbb3f34
```

### `leapmux control terminal shells`

List the shells that a worker offers.

```text
leapmux control terminal shells [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control terminal shells
```

## `leapmux control terminal quake`

Show or hide the quake terminal of a directory in your open browsers.

### `leapmux control terminal quake open`

Show the quake terminal.

This command changes state.

```text
leapmux control terminal quake open [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control terminal quake open
leapmux control terminal quake open --dir ~/src/app --worker name:build-box
```

### `leapmux control terminal quake close`

Hide the quake terminal.

This command changes state.

```text
leapmux control terminal quake close [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control terminal quake close
leapmux control terminal quake close --dir ~/src/app --worker name:build-box
```

### `leapmux control terminal quake toggle`

Show the quake terminal if it is hidden, and hide it otherwise.

This command changes state.

```text
leapmux control terminal quake toggle [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control terminal quake toggle
leapmux control terminal quake toggle --dir ~/src/app --worker name:build-box
```

## `leapmux control file`

Read the files of a worker, and open them in tabs.

### `leapmux control file list`

List a directory.

```text
leapmux control file list <path> [flags]
```

| Flag | Description |
|---|---|
| `--max-depth int` | merge a chain of directories that each hold one directory, up to this depth |
| `--dirs-only` | list only directories |
| `--from-root string` | also list every directory from this ancestor down to &lt;path&gt; |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control file list ~/src/app

# List only the directories, and every directory from the home directory down.
leapmux control file list ~/src/app --dirs-only --from-root ~
```

### `leapmux control file read`

Print a file.

```text
leapmux control file read <path> [flags]
```

| Flag | Description |
|---|---|
| `--offset int` | the byte to start at |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control file read ~/src/app/go.mod

# Read 4 KiB from byte 1024.
leapmux control file read ~/src/app/log.txt --offset 1024 --limit 4096
```

### `leapmux control file stat`

Show a file's size, time and permissions.

```text
leapmux control file stat <path> [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control file stat ~/src/app/go.mod
```

### `leapmux control file roots`

List the filesystem roots of a worker.

```text
leapmux control file roots [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control file roots
```

### `leapmux control file open`

Open a file in a new tab.

This command changes state.

```text
leapmux control file open <path> [flags]
```

| Flag | Description |
|---|---|
| `--display-mode string` | how a document shows at first (one of: render, source, split) |
| `--view-mode string` | which version of the file shows at first (one of: working, head, staged, unified_diff, split_diff) |
| `--dir string` | the git context of the tab; outside a LeapMux tab, the directory of the file (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |

```sh
leapmux control file open ~/src/app/README.md

# Open a file as its staged diff.
leapmux control file open ~/src/app/main.go --view-mode unified_diff
```

## `leapmux control git`

Read and change the git state of a directory on a worker.

### `leapmux control git status`

Show the branch and the changed files of a working tree.

```text
leapmux control git status [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git status
leapmux control git status --dir ~/src/app --worker name:build-box
```

### `leapmux control git branches`

List the branches of a repository.

```text
leapmux control git branches [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git branches
```

### `leapmux control git worktrees`

List the worktrees of a repository.

```text
leapmux control git worktrees [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git worktrees
```

### `leapmux control git read`

Print a file as the last commit or the index holds it. The path is the file's
absolute path on the worker; the worker finds its repository.

```text
leapmux control git read <path> [flags]
```

| Flag | Description |
|---|---|
| `--ref string` | the version to read (one of: head, staged; default: head) |
| `--limit int` | the most bytes to read (0 reads the default of 256 KiB) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git read ~/src/app/main.go
leapmux control git read ~/src/app/main.go --ref staged
```

### `leapmux control git checkout`

Check out a branch.

This command changes state.

```text
leapmux control git checkout <branch> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git checkout main --dir ~/src/app

# Check out a remote branch.
leapmux control git checkout origin/feature --dir ~/src/app
```

## `leapmux control git branch`

Create and delete branches.

### `leapmux control git branch create`

Create a branch and check it out.

This command changes state.

```text
leapmux control git branch create <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--base string` | the branch to start from (default: the current branch) |

```sh
leapmux control git branch create fix-login --base main --dir ~/src/app
```

### `leapmux control git branch delete`

Check out another branch, then delete this one.

This command changes state.

```text
leapmux control git branch delete <name> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--switch-to string` | the branch to check out before the deletion (required) |

```sh
leapmux control git branch delete fix-login --switch-to main --dir ~/src/app
```

### `leapmux control git push`

Push the current branch, with a work-in-progress commit when the tree has changes.

This command changes state.

```text
leapmux control git push [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | a directory inside the git working tree (required) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git push --dir ~/src/app
```

## `leapmux control git worktree`

Find an existing checked-out worktree.

### `leapmux control git worktree get`

Use path:&lt;path&gt;, branch:&lt;branch&gt;, or self. The worker matches the attached
branch in the repository selected by --dir. A local tab can supply the
repository directory for a branch on the same worker. An explicit path can
select its own repository. Use self to find the calling local tab's checkout.

```text
leapmux control git worktree get <selector> [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | repository directory on the selected worker (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control git worktree get branch:feature/login --dir ~/src/app
leapmux control git worktree get self
```

## `leapmux control tile`

Split, close and arrange the tiles of a workspace.

### `leapmux control tile list`

Print the tile tree of a workspace (default: the workspace of this tab).

```text
leapmux control tile list [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
leapmux control tile list
```

### `leapmux control tile split`

Split a tile in two; its tabs stay in the first half.

This command changes state.

```text
leapmux control tile split <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--direction string` | the divider: "vertical" puts the new tile on the right, "horizontal" puts it below (one of: vertical, horizontal; default: vertical) |

```sh
# Split the tile of this tab, with the new tile on the right.
leapmux control tile split self

# Split a tile with the new tile below.
leapmux control tile split ab12cd34 --direction horizontal
```

### `leapmux control tile close`

Close a tile and give its space to its neighbour.

This command changes state.

```text
leapmux control tile close <tile> [flags]
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
leapmux control tile close ab12cd34

# Close a tile and move its tabs to the neighbouring tile.
leapmux control tile close ab12cd34 --with-tabs move
```

### `leapmux control tile make-grid`

Turn a tile into a grid of rows and columns.

This command changes state.

```text
leapmux control tile make-grid <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--rows int` | rows of the grid, from 1 to 20 (required) |
| `--cols int` | columns of the grid, from 1 to 20 (required) |

```sh
leapmux control tile make-grid ab12cd34 --rows 2 --cols 2
```

### `leapmux control tile remove-grid`

Remove a grid and every tile in it.

This command changes state.

```text
leapmux control tile remove-grid <tile> [flags]
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
leapmux control tile remove-grid ab12cd34 --with-tabs move
```

### `leapmux control tile set-ratios`

Set the sizes of the parts of a split.

This command changes state.

```text
leapmux control tile set-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--ratios string` | the size of each part, separated by commas; the CLI scales them to sum to 1 (required) |

```sh
leapmux control tile set-ratios ab12cd34 --ratios 0.3,0.7
```

### `leapmux control tile set-grid-ratios`

Set the sizes of the rows or the columns of a grid.

This command changes state.

```text
leapmux control tile set-grid-ratios <tile> [flags]
```

| Argument | Description |
|---|---|
| `<tile>` | A tile id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, or "self" for the tile of the tab that runs this command. |

| Flag | Description |
|---|---|
| `--row-ratios string` | the size of each row, separated by commas |
| `--col-ratios string` | the size of each column, separated by commas |

```sh
leapmux control tile set-grid-ratios ab12cd34 --row-ratios 0.6,0.4
```

## `leapmux control layout`

Read, replace and watch the whole tile tree of a workspace.

### `leapmux control layout get`

Print the tile tree of a workspace with its tabs (default: the workspace of this tab).

```text
leapmux control layout get [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
# Save a layout, and restore it later.
leapmux control layout get --json > layout.json
```

### `leapmux control layout set`

Replace the tile tree of a workspace. The file is "-" or absent for stdin. It
holds a tree as "layout get" prints it: the whole --json envelope, its data,
or the tree alone.

This command changes state.

```text
leapmux control layout set <workspace> [file] [flags]
```

| Argument | Description |
|---|---|
| `<workspace>` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
leapmux control layout set self layout.json
leapmux control layout get --json | leapmux control layout set 9f3kQ2aB -
```

### `leapmux control layout watch`

Print every change of your layouts as it happens: the CRDT operations, the
presence of other clients, and the creation, renaming and deletion of
workspaces. The first event is the snapshot. Without a workspace, it watches
every workspace.

```text
leapmux control layout watch [workspace] [flags]
```

| Argument | Description |
|---|---|
| `[workspace]` | A workspace id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "title:&lt;title&gt;", or "self" for the workspace of the tab that runs this command. |

```sh
leapmux control layout watch --json
```

## `leapmux control worker`

List workers, read what they report, and register new ones.

### `leapmux control worker list`

List your workers with their names.

```text
leapmux control worker list [flags]
```

```sh
leapmux control worker list
```

### `leapmux control worker get`

Show what the hub knows about a worker (default: the worker of this tab).

```text
leapmux control worker get [worker] [flags]
```

| Argument | Description |
|---|---|
| `[worker]` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;", or "self" for the worker of the tab that runs this command. |

```sh
leapmux control worker get
leapmux control worker get name:build-box
```

### `leapmux control worker info`

Show what an online worker reports about itself: name, OS, home and build.

```text
leapmux control worker info [worker] [flags]
```

| Argument | Description |
|---|---|
| `[worker]` | A worker id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for a literal id, "name:&lt;name&gt;", or "self" for the worker of the tab that runs this command. |

```sh
leapmux control worker info
```

## `leapmux control worker key`

Create keys that register new workers.

### `leapmux control worker key create`

Create a registration key that a new worker uses once, within five minutes.

This command changes state.

```text
leapmux control worker key create [flags]
```

```sh
# Register a worker on another machine with the key.
K=$(leapmux control worker key create --json | jq -r .data.key)
```

## `leapmux control worker pins`

Inspect and reset the worker keys that this machine trusts.

### `leapmux control worker pins list`

List the worker keys that this machine pinned for the hub.

```text
leapmux control worker pins list [flags]
```

```sh
leapmux control worker pins list
```

### `leapmux control worker pins get`

Show the pinned key of one worker.

```text
leapmux control worker pins get <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, or a unique prefix of 4 or more characters of one, as "leapmux control worker pins list" prints it. |

```sh
leapmux control worker pins get 5f2c9a1d
```

### `leapmux control worker pins remove`

Forget a worker's pinned key, so the next connection pins it again.

This command changes state.

```text
leapmux control worker pins remove <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, or a unique prefix of 4 or more characters of one, as "leapmux control worker pins list" prints it. |

```sh
# Forget the key of a worker that was reinstalled.
leapmux control worker pins remove 5f2c9a1d
```

### `leapmux control events`

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
"leapmux control agent list". A new run starts from the present.

When the hub or a Worker cannot deliver, the stream prints
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
  - agent.activity: an agent became idle, working or waiting for the user
  - agent.status: the status of an agent's process changed to starting, active, inactive or startup_failed
  - agent.turn_end: an agent ended a turn
  - agent.request: an agent asks a question or a permission; agent approve, deny and answer take its request
  - agent.request_ended: a request of an agent was answered or withdrawn
  - agent.todos: an agent's to-do list changed
  - agent.background_tasks: an agent's background tasks changed
  - terminal.status: the status of a terminal's shell changed to starting, ready or startup_failed, or to exited without a terminal.exit
  - terminal.title: a program set the title of a terminal
  - terminal.bell: a terminal rang its bell
  - terminal.notification: a program in a terminal asked for a desktop notification
  - terminal.progress: a program in a terminal reported its progress
  - terminal.exit: a terminal's shell exited, with its exit code
  - stream.ready: the stream watches every source of its scope
  - stream.unavailable: the hub, a Worker, or a Worker for one tab cannot deliver events; retrying says whether the stream tries again
  - stream.gap: a source delivers again; events of the kinds in lost, between since and time, may be lost
  - stream.heartbeat: the stream runs; only --heartbeat prints it

```text
leapmux control events [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | watch only this workspace: an id, a prefix, "title:&lt;title&gt;" or "self" (default: every workspace) |
| `--tab string` | watch only this tab: an id, a prefix, "id:&lt;id&gt;", "title:&lt;title&gt;" or "self" |
| `--kind string` | print only this kind of event, or every kind of this family, such as "agent"; repeat it, or separate the values with commas (default: every kind) (one of: workspace, tab, agent, terminal, stream, workspace.created, workspace.renamed, workspace.deleted, tab.opened, tab.closed, tab.moved, agent.activity, agent.status, agent.turn_end, agent.request, agent.request_ended, agent.todos, agent.background_tasks, terminal.status, terminal.title, terminal.bell, terminal.notification, terminal.progress, terminal.exit, stream.ready, stream.unavailable, stream.gap, stream.heartbeat; repeatable) |
| `--limit int` | end the stream after this many events; 0 runs without a limit |
| `--heartbeat duration` | print a stream.heartbeat event at this interval, such as 30s; 0 prints none (default: 0s) |

```sh
leapmux control events

# Print the turn ends and the requests of the agents in this workspace, as JSON.
leapmux control events --workspace self --kind agent.turn_end --kind agent.request --json

# Wait for the next request of any agent, and print it.
leapmux control events --kind agent.request --limit 1 --json

# Watch every terminal, with a heartbeat that shows the stream runs.
leapmux control events --kind terminal --heartbeat 30s

# Watch a single terminal as it moves between workspaces.
leapmux control events --tab 87a19d5f --kind terminal --json
```

### `leapmux control notify`

Show a notification in every LeapMux window that you have open: a toast with
the title and the body. While a window is hidden, it also shows a desktop
notification, when "Desktop notifications" is on in Preferences.

The notification names the tab that runs the command, so you see which agent
or terminal sent it. Nothing stores a notification: a window that you open
later does not show it.

The command goes to the Worker of the tab that runs it. Outside a tab, pass
--worker. A window hears a Worker only while one of your tabs runs on that
Worker. A notification sent from outside a tab names no tab.

The Worker refuses a title or a body that is too long, and states the limit.

This command changes state.

```text
leapmux control notify <title> [flags]
```

| Argument | Description |
|---|---|
| `<title>` | A short title, such as "Tests passed". |

| Flag | Description |
|---|---|
| `--body string` | more text below the title ("-" reads stdin) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |

```sh
leapmux control notify "Tests passed"

# Say what failed, with the last lines of the log.
tail -n 5 build.log | leapmux control notify "Build failed" --body -

# Notify through another Worker from outside a tab.
leapmux control notify "Deploy finished" --body "v1.4.2 is live" --worker name:build-box
```

### `leapmux control rpc`

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
does not run here. "leapmux control events" prints the events of agents and
terminals, and "leapmux control agent messages --follow" prints the messages
of an agent.

Inside a LeapMux tab, the socket of the tab relays these hub procedures
alone:
  - leapmux.v1.SystemService/GetSystemInfo
  - leapmux.v1.UserCRDT/GetMaterialized
  - leapmux.v1.UserCRDT/SubmitOps
  - leapmux.v1.UserCRDT/UpdatePresence
  - leapmux.v1.WorkerManagementService/GetWorker
  - leapmux.v1.WorkerManagementService/ListWorkers
  - leapmux.v1.WorkspaceService/CreateWorkspace
  - leapmux.v1.WorkspaceService/DeleteWorkspace
  - leapmux.v1.WorkspaceService/GetWorkspace
  - leapmux.v1.WorkspaceService/ListTabs
  - leapmux.v1.WorkspaceService/ListWorkspaces
  - leapmux.v1.WorkspaceService/LocateTab
  - leapmux.v1.WorkspaceService/LocateTile
  - leapmux.v1.WorkspaceService/RenameWorkspace

Pass --hub &lt;url&gt; to call another hub procedure with your own login.

This command changes state.

```text
leapmux control rpc <procedure> [request] [flags]
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
leapmux control rpc leapmux.v1.WorkspaceService/ListTabs '{"workspace_ids": ["87a19d5f-4838-4e34-9f0f-56130f0a62b1"]}'

# Read one agent from the Worker of this tab.
leapmux control rpc leapmux.v1.WorkerAgentService/ListAgents '{"tab_ids": ["4cbb3f34-3c1e-4f0e-9a51-2a6a3b1f0d7e"]}' --worker self

# Send the request that a file holds, and print the response as one JSON envelope.
leapmux control rpc WorkerTerminalService/ListAvailableShells - --worker name:build-box --json < request.json
```

## `leapmux control admin`

Administer the hub; needs an administrator's login (offline repair: "leapmux recover").

## `leapmux control admin settings`

Read and change the hub's settings: sign-in policy, SMTP, timeouts, limits.

### `leapmux control admin settings list`

List every setting with its effective value.

```text
leapmux control admin settings list [flags]
```

```sh
leapmux control admin settings list
```

### `leapmux control admin settings get`

Show one setting, its value and its shape.

```text
leapmux control admin settings get <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux control admin settings list" prints it. |

```sh
leapmux control admin settings get session_duration_seconds
```

### `leapmux control admin settings set`

Set the public value of a setting from JSON or a bare value.

This command changes state.

```text
leapmux control admin settings set <key> <value> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux control admin settings list" prints it. |
| `<value>` | A JSON value. A word that is not JSON is stored as a string. |

```sh
leapmux control admin settings set signup_enabled false

# Set two fields of a setting that holds a document.
leapmux control admin settings set smtp '{"host":"smtp.example.com","port":587}'
```

### `leapmux control admin settings set-secret`

Set the secret fields of a setting; the hub stores them encrypted.

This command changes state.

```text
leapmux control admin settings set-secret <key> <json> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux control admin settings list" prints it. |
| `<json>` | A JSON object of the secret fields, such as {"password":"..."}. |

```sh
leapmux control admin settings set-secret smtp '{"password":"..."}'
```

### `leapmux control admin settings reset`

Restore the default value of a setting.

This command changes state.

```text
leapmux control admin settings reset <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux control admin settings list" prints it. |

```sh
leapmux control admin settings reset session_duration_seconds
```

## `leapmux control admin user`

Manage users.

### `leapmux control admin user list`

List users.

```text
leapmux control admin user list [flags]
```

| Flag | Description |
|---|---|
| `--query string` | search query (empty = list all) |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin user list

# Search by name or email.
leapmux control admin user list --query alice
```

### `leapmux control admin user get`

Show a user.

```text
leapmux control admin user get <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

```sh
leapmux control admin user get alice

# Select a user by id.
leapmux control admin user get id:Ab3dE9
```

### `leapmux control admin user create`

Create a user.

This command changes state.

```text
leapmux control admin user create <username> [flags]
```

| Flag | Description |
|---|---|
| `--password string` | password (prompted for when omitted, so it stays out of the shell history) |
| `--display-name string` | display name |
| `--email string` | email address |
| `--email-verified` | mark email as verified |
| `--admin` | grant admin privileges |

```sh
leapmux control admin user create alice --email alice@example.com
```

### `leapmux control admin user update`

Change a user's name, email or verification.

This command changes state.

```text
leapmux control admin user update <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--display-name string` | new display name |
| `--email string` | new email address |
| `--email-verified string` | mark email as verified (true/false) |
| `--clear-pending-email` | clear any in-flight email verification (token + attempt counter) |

```sh
leapmux control admin user update alice --display-name "Alice Kim"
```

### `leapmux control admin user delete`

Delete a user.

This command changes state.

```text
leapmux control admin user delete <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--force` | required to delete an admin user |

```sh
leapmux control admin user delete alice
```

### `leapmux control admin user grant-admin`

Make a user an administrator.

This command changes state.

```text
leapmux control admin user grant-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

```sh
leapmux control admin user grant-admin alice
```

### `leapmux control admin user revoke-admin`

Take a user's administrator access away.

This command changes state.

```text
leapmux control admin user revoke-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--force` | required to remove your own administrator access |

```sh
leapmux control admin user revoke-admin alice
```

### `leapmux control admin user reset-password`

Set a user's password and revoke every credential they hold.

This command changes state.

```text
leapmux control admin user reset-password <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--password string` | new password (prompted for when omitted, so it stays out of the shell history) |

```sh
# Prompt for the new password.
leapmux control admin user reset-password alice
```

### `leapmux control admin user list-sessions`

List a user's active sessions.

```text
leapmux control admin user list-sessions <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin user list-sessions alice
```

## `leapmux control admin session`

List and revoke sign-in sessions.

### `leapmux control admin session list`

List every active session.

```text
leapmux control admin session list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin session list
```

### `leapmux control admin session revoke`

Revoke a session.

This command changes state.

```text
leapmux control admin session revoke <session> [flags]
```

| Argument | Description |
|---|---|
| `<session>` | A session id, as "leapmux control admin session list" prints it. |

```sh
leapmux control admin session revoke 4f1aC2
```

### `leapmux control admin session revoke-user`

Revoke every session of a user.

This command changes state.

```text
leapmux control admin session revoke-user <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

```sh
leapmux control admin session revoke-user alice
```

### `leapmux control admin session purge-expired`

Delete every expired session.

This command changes state.

```text
leapmux control admin session purge-expired [flags]
```

```sh
leapmux control admin session purge-expired
```

## `leapmux control admin worker`

Manage the workers of every user.

### `leapmux control admin worker list`

List the workers of every user.

```text
leapmux control admin worker list [flags]
```

| Flag | Description |
|---|---|
| `--user-id string` | filter by user ID (soft-deleted users included; empty = all users) |
| `--username string` | filter by username |
| `--status string` | worker status filter (active, deregistering, deleted, all) (default: active) |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin worker list

# Include the workers that are gone.
leapmux control admin worker list --status all
```

### `leapmux control admin worker get`

Show a worker of any user.

```text
leapmux control admin worker get <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, as "leapmux control admin worker list" prints it. |

```sh
leapmux control admin worker get 5f2c9a1d
```

### `leapmux control admin worker deregister`

Deregister a worker of any user.

This command changes state.

```text
leapmux control admin worker deregister <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id, as "leapmux control admin worker list" prints it. |

```sh
leapmux control admin worker deregister 5f2c9a1d
```

## `leapmux control admin worker reg-key`

Manage worker registration keys.

### `leapmux control admin worker reg-key list`

List worker registration keys.

```text
leapmux control admin worker reg-key list [flags]
```

| Flag | Description |
|---|---|
| `--include-expired` | include revoked or expired keys (forensics; default shows only live keys) |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin worker reg-key list
```

### `leapmux control admin worker reg-key revoke`

Revoke a worker registration key.

This command changes state.

```text
leapmux control admin worker reg-key revoke <reg-key> [flags]
```

| Argument | Description |
|---|---|
| `<reg-key>` | A registration key id, as "leapmux control admin worker reg-key list" prints it. |

```sh
leapmux control admin worker reg-key revoke 9Zt4Kp
```

### `leapmux control admin worker reg-key purge-expired`

Delete every expired or revoked registration key.

This command changes state.

```text
leapmux control admin worker reg-key purge-expired [flags]
```

```sh
leapmux control admin worker reg-key purge-expired
```

## `leapmux control admin app`

Manage app registrations: what a consent screen authorizes.

### `leapmux control admin app list`

List app registrations.

```text
leapmux control admin app list [flags]
```

| Flag | Description |
|---|---|
| `--include-revoked` | include retired apps |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin app list
```

### `leapmux control admin app register`

Register an app; the hub shows its secret once.

This command changes state.

```text
leapmux control admin app register <name> [flags]
```

| Flag | Description |
|---|---|
| `--uri string` | the app's home page |
| `--redirect-uri string` | comma-separated addresses the authorization may return to (required for the authorization_code grant) |
| `--scope string` | space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write (required) |
| `--grant-type string` | comma-separated OAuth grant types; empty takes the RFC 7591 default of authorization_code and refresh_token |
| `--type string` | public (a binary a user holds; PKCE protects it) or confidential (a server that can keep a secret) (default: public) |
| `--visibility string` | hub-wide (every account may authorize it; administrators only) or private (yours alone) (default: hub-wide) |
| `--allow-elevation` | let this app run the step-up stage, which is what a sensitive change needs |

```sh
leapmux control admin app register "Release bot" --scope "workspace:read" --redirect-uri http://127.0.0.1/callback
```

### `leapmux control admin app update`

Change the editable fields of an app.

This command changes state.

```text
leapmux control admin app update <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

| Flag | Description |
|---|---|
| `--name string` | new app name |
| `--uri string` | new home page |
| `--redirect-uri string` | replace the redirect list (comma-separated) |
| `--scope string` | replace the permission ceiling; space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write |
| `--grant-type string` | replace the grant-type list (comma-separated) |

```sh
leapmux control admin app update lmx_app_x1y2 --name "Release bot 2"
```

### `leapmux control admin app verify`

Vouch for an app, so that its consent screen stops warning.

This command changes state.

```text
leapmux control admin app verify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app verify lmx_app_x1y2
```

### `leapmux control admin app unverify`

Withdraw a vouch for an app.

This command changes state.

```text
leapmux control admin app unverify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app unverify lmx_app_x1y2
```

### `leapmux control admin app allow-elevation`

Let an app ask a user to prove a factor again.

This command changes state.

```text
leapmux control admin app allow-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app allow-elevation lmx_app_x1y2
```

### `leapmux control admin app deny-elevation`

Take that permission away; every open elevation ends at the next call.

This command changes state.

```text
leapmux control admin app deny-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app deny-elevation lmx_app_x1y2
```

### `leapmux control admin app revoke`

Retire an app and revoke every credential it holds.

This command changes state.

```text
leapmux control admin app revoke <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app revoke lmx_app_x1y2
```

### `leapmux control admin app delete`

Delete an app that never held a credential.

This command changes state.

```text
leapmux control admin app delete <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id, as "leapmux control admin app list" prints it. |

```sh
leapmux control admin app delete lmx_app_x1y2
```

## `leapmux control admin idp`

Manage the OAuth and OIDC providers that users sign in with.

### `leapmux control admin idp add`

Add an OAuth or OIDC sign-in provider.

This command changes state.

```text
leapmux control admin idp add [flags]
```

| Flag | Description |
|---|---|
| `--type string` | provider type (github, google, apple, oidc) |
| `--name string` | display name (defaults to the preset name) |
| `--client-id string` | OAuth client ID |
| `--client-secret string` | OAuth client secret |
| `--issuer-url string` | OIDC issuer URL |
| `--scopes string` | space-separated scopes |
| `--trust-email string` | trust verified provider emails (true/false; required for generic OIDC) |

```sh
leapmux control admin idp add --type github --client-id Iv1.abc --client-secret s3cr3t
```

### `leapmux control admin idp list`

List the sign-in providers.

```text
leapmux control admin idp list [flags]
```

```sh
leapmux control admin idp list
```

### `leapmux control admin idp remove`

Remove a sign-in provider.

This command changes state.

```text
leapmux control admin idp remove <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id, as "leapmux control admin idp list" prints it. |

| Flag | Description |
|---|---|
| `--force` | required to remove a provider that is the last login method of an account |

```sh
leapmux control admin idp remove 3hKd9s
```

### `leapmux control admin idp enable`

Enable a sign-in provider.

This command changes state.

```text
leapmux control admin idp enable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id, as "leapmux control admin idp list" prints it. |

```sh
leapmux control admin idp enable 3hKd9s
```

### `leapmux control admin idp disable`

Disable a sign-in provider.

This command changes state.

```text
leapmux control admin idp disable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id, as "leapmux control admin idp list" prints it. |

```sh
leapmux control admin idp disable 3hKd9s
```

## `leapmux control admin captcha`

Manage the bot protection: ALTCHA, reCAPTCHA v3 or Turnstile (a solo hub never asks).

### `leapmux control admin captcha get`

Show the captcha configuration in effect.

```text
leapmux control admin captcha get [flags]
```

```sh
leapmux control admin captcha get
```

### `leapmux control admin captcha set`

Change the captcha settings, or switch the provider.

This command changes state.

```text
leapmux control admin captcha set [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | provider alias (altcha, recaptcha_v3, turnstile); omit to tune the active provider |
| `--algorithm string` | ALTCHA algorithm |
| `--cost int` | algorithm cost parameter; 0 restores the default |
| `--memory-cost int` | algorithm memory cost; 0 restores the default |
| `--parallelism int` | algorithm parallelism; 0 restores the default |
| `--expires int` | challenge expiry seconds; 0 restores the default |
| `--site-key string` | provider site key |
| `--secret string` | provider secret (stored encrypted) |
| `--min-score number` | recaptcha_v3 minimum score (0 &lt; v &lt;= 1); 0 restores the default |

```sh
leapmux control admin captcha set --provider turnstile --site-key 0x4AAA --secret s3cr3t
```

### `leapmux control admin captcha enable`

Turn captcha verification on.

This command changes state.

```text
leapmux control admin captcha enable [flags]
```

```sh
leapmux control admin captcha enable
```

### `leapmux control admin captcha disable`

Turn captcha verification off; the honeypot check stays.

This command changes state.

```text
leapmux control admin captcha disable [flags]
```

```sh
leapmux control admin captcha disable
```

### `leapmux control admin captcha reset`

Restore the captcha defaults of every provider, or of one.

This command changes state.

```text
leapmux control admin captcha reset [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | reset one provider's row (omit = all captcha settings) |

```sh
leapmux control admin captcha reset

# Reset one provider.
leapmux control admin captcha reset --provider altcha
```

## `leapmux control admin rate-limit`

Manage the rate limit of each user operation.

### `leapmux control admin rate-limit list`

List the rate limits of every operation.

```text
leapmux control admin rate-limit list [flags]
```

```sh
leapmux control admin rate-limit list
```

### `leapmux control admin rate-limit set`

Change the rate limit of an operation.

This command changes state.

```text
leapmux control admin rate-limit set <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

| Flag | Description |
|---|---|
| `--max-attempts int` | failed attempts allowed per window (1-1000); 0 restores the default |
| `--window int` | window seconds (60-86400); 0 restores the default |

```sh
leapmux control admin rate-limit set elevation --max-attempts 5 --window 900
```

### `leapmux control admin rate-limit enable`

Turn on the rate limit of an operation.

This command changes state.

```text
leapmux control admin rate-limit enable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux control admin rate-limit enable elevation
```

### `leapmux control admin rate-limit disable`

Turn off the rate limit of an operation.

This command changes state.

```text
leapmux control admin rate-limit disable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux control admin rate-limit disable elevation
```

### `leapmux control admin rate-limit reset`

Restore the default rate limit of an operation.

This command changes state.

```text
leapmux control admin rate-limit reset <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux control admin rate-limit reset elevation
```

## `leapmux control admin api-token`

Manage durable API tokens for scripts and integrations.

### `leapmux control admin api-token list`

List API tokens.

```text
leapmux control admin api-token list [flags]
```

| Flag | Description |
|---|---|
| `--user-id string` | filter by user ID (soft-deleted users included; empty = all users) |
| `--username string` | filter by username |
| `--client-id string` | filter by app (empty = all apps) |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin api-token list

# List one user's tokens.
leapmux control admin api-token list --username alice
```

### `leapmux control admin api-token issue`

Issue an API token that &lt;user&gt; owns.

This command changes state.

```text
leapmux control admin api-token issue <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, or "id:&lt;id&gt;" for a user id. |

| Flag | Description |
|---|---|
| `--client-id string` | the app this credential belongs to (empty = the built-in service-account registration) |
| `--installation-name string` | human-visible name for this installation, e.g. ci-runner-3 (required) |
| `--ttl int` | fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh) |
| `--scope string` | space- or comma-separated permissions, e.g. "file:read git:read" (empty = everything except admin:*) |

```sh
leapmux control admin api-token issue ci-bot --installation-name ci-runner-3 --scope "workspace:read"
```

### `leapmux control admin api-token revoke`

Revoke an API token.

This command changes state.

```text
leapmux control admin api-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id, as the list command of this group prints it. |

```sh
leapmux control admin api-token revoke 7Qx2Lm
```

## `leapmux control admin delegation-token`

Manage the delegation tokens that workers mint.

### `leapmux control admin delegation-token list`

List the delegation tokens that workers minted.

```text
leapmux control admin delegation-token list [flags]
```

| Flag | Description |
|---|---|
| `--user-id string` | filter by user ID (soft-deleted users included; empty = all users) |
| `--username string` | filter by username |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |

```sh
leapmux control admin delegation-token list
```

### `leapmux control admin delegation-token revoke`

Revoke a delegation token.

This command changes state.

```text
leapmux control admin delegation-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id, as the list command of this group prints it. |

```sh
leapmux control admin delegation-token revoke 7Qx2Lm
```
