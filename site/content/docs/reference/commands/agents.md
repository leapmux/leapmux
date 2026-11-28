---
title: "Agents"
description: "Reference for leapmux agent commands: start, message, queue, watch, and configure coding agents."
type: docs
weight: 1
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--timeout duration` | with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit (default: 0s) |
| `--output-schema string` | validate the reply against a JSON Schema (file path, inline JSON object, or "-" for stdin); requires --wait |
| `--schema-retries int` | with --output-schema, maximum corrective follow-up turns on validation failure (0-10) (default: 2) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `[request]` | The request's handle, as "agent requests" prints it, or a unique prefix of it. |
| `<input>` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. |

## `leapmux agent`

Start, message, watch and configure coding agents.

Aliases: `ag`.

### `leapmux agent list`

List your agents with their states.

_Aliases: `ls`._

```text
leapmux agent list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the agents of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | list only the agents hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--state string` | list only the agents in this activity state (one of: idle, working, waiting_for_user) |
| `--provider string` | list only the agents of this provider (such as claude, codex) |
| `--parent string` | list only direct child subagents or spawned agents of this parent agent (an id, prefix, title, or "self") |
| `--all` | always show the STATUS column even when every listed agent is active |

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
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--options` | include every option group with its choices |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent get

# Show every option that the agent offers.
leapmux agent get 87a19d5f --options
```

### `leapmux agent open`

Start a new agent in a new tab.

--resume continues an earlier session of the provider in the new tab. "agent sessions" lists the sessions that it takes, for the same --provider, --worker and --dir.

--request-id makes a retry safe. A second run with the same request id and the same arguments opens no second agent: it prints the agent that the first run opened, and it queues the --message no second time. A run with the same request id and other arguments fails with conflict.

_Mutates state._

```text
leapmux agent open [prompt] [flags]
```

| Argument | Description |
|---|---|
| `[prompt]` | Optional first message to send to the new agent ("-" reads stdin). Mutually exclusive with --message and --file. |

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux agent providers" lists them (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--model string` | model |
| `--effort string` | reasoning effort |
| `--permission-mode string` | permission mode |
| `--option string` | provider option as key=value; repeat it for more than one; "agent get --options" lists them (repeatable) |
| `--title string` | tab title (default: the worker picks one) |
| `--name string` | unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `-m, --message string` | queue a first message with the agent creation ("-" reads stdin) |
| `--file string` | read the first message from this file ("-" reads stdin) |
| `--attach string` | attach this file to the first message; repeat it for more than one (repeatable) |
| `--resume string` | resume this earlier session of the provider; "agent sessions" lists them |
| `--request-id string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--wait` | wait until the agent settles (and print its reply when a message was sent) |
| `--all` | with --wait and a message, print every reply, not only the last one |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--output-schema string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--schema-retries int` | See [common arguments and flags](#common-arguments-and-flags). |
| `--dir string` | working directory of the agent (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
# Start an agent beside this tab and keep its id.
A=$(leapmux agent open --provider claude --message "Run the tests" -q)

# Start an agent with a positional prompt.
leapmux agent open --provider claude "Review the diff"

# Start an agent in a new git worktree.
leapmux agent open --worktree fix-flaky-spec --base main

# Continue an earlier session that "agent sessions" lists.
leapmux agent open --provider claude --resume 4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90

# Open an agent that a retry of the same command does not open twice.
leapmux agent open --provider codex --message "Review the diff" --request-id nightly-review-2026-11-19
```

### `leapmux agent send`

Send a message to an agent.

The text is the second argument, "-" to read it from stdin, or --file. A message that starts with "-" needs "--" before it, so that a misspelled flag never reaches the agent as text.

The agent's input queue holds the message until the agent reads it. "agent queue list" shows the queue.

A message that a process in an agent tab sends reaches the recipient as that agent's message, not as the user's. The recipient reads it after a line that states the sending agent and that the message is not from its operator, and LeapMux shows the sender beside it. The Worker states the sender from the tab's own credentials, so no flag sets or hides it. A message that an agent sends to itself, such as a reminder that --at holds, reaches it as its own message too. A message from a terminal tab, or from outside LeapMux, reaches the agent as the user's.

--attach attaches a file, once for each file. A message with an attachment needs no text. The CLI refuses a file that it cannot read, and a message whose text and attachments hold more than 10 MiB together. The Worker infers the type of each file from its name and its content, and it refuses a file that the agent's provider cannot read with invalid_argument. The error states the file.

--at holds the message in the queue until a time:

- An RFC 3339 time with its offset, such as 2026-11-20T09:00:00+09:00 or 2026-11-20T00:00:00Z. The CLI assumes no time zone, so a time without an offset fails.
- "+" and a duration from now, such as +10m or +1h30m.

A new schedule must lie in the future. The result states it in UTC.

--request-id makes a retry safe. A second run with the same request id, the same agent and the same message queues nothing new, and with --wait it prints the reply to the message of the first run. This holds also after someone edited the message in the queue: the retry repeats the message that the first run sent, and the queue keeps the edit. It holds also after someone removed the message from the queue: for 7 days after the removal, a retry queues nothing, and with --wait it fails with not_found and exits 6. A run with the same request id and a message other than the first run's fails with conflict, even when its message equals the edited one. A retry of a scheduled message repeats its RFC 3339 timestamp, even after that time has passed. A relative --at changes on each run and cannot use --request-id.

With --wait, a new message to an agent waiting for permission exits 5. An explicit --request-id can still match an input the Worker recorded. The command waits until the agent settles and prints the last reply as the transcript shows it, or every reply with --all. It exits 6 if the agent waits for permission or --timeout passes after the Worker acknowledges the input. The output reports the last confirmed delivery state. The timeout counts from the Worker's enqueue acknowledgment.

_Mutates state._

```text
leapmux agent send <agent> [text] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the message from this file ("-" reads stdin) |
| `--attach string` | attach this file; repeat it for more than one (repeatable) |
| `--at string` | hold the message until this time: an RFC 3339 time with its offset, or + and a duration from now, such as +10m |
| `--steer` | inject the message into the agent's active turn immediately (requires a steerable active turn) |
| `--preempt` | interrupt the agent's active turn so this message dispatches next |
| `--request-id string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--wait` | wait until the agent settles after the message, then print its last reply |
| `--all` | with --wait, print every reply after the message, not only the last one |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--output-schema string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--schema-retries int` | See [common arguments and flags](#common-arguments-and-flags). |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

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

Repeat --until to accept several states. Each agent can reach a different requested state. A state reported at any moment during the wait counts, including the agent's initial state. "settled" is "idle" or "waiting_for_user": the agent works no more, because it finished or because it waits for an answer to a permission prompt. An agent whose input queue holds a message that it will read is neither idle nor settled yet, as an agent that "agent open --message" just started.

The command exits 0 when the condition holds. It exits 6 when the wait ended without it: --timeout passed, or an agent closed before it reached a requested state. It prints the state of each agent in both cases.

```text
leapmux agent wait <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |

| Flag | Description |
|---|---|
| `--until string` | a state to wait for; repeat to accept alternatives (one of: settled, idle, working, waiting_for_user; default: [settled]; repeatable) |
| `--any` | end the wait as soon as one agent reaches a requested state |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent wait 87a19d5f

# Wait for an agent to start working or ask for input.
leapmux agent wait 87a19d5f --until working --until waiting_for_user

# Wait until the first of two agents waits for an answer, for 10 minutes at most.
leapmux agent wait 87a19d5f 5c2e0b91 --until waiting_for_user --any --timeout 10m
```

### `leapmux agent requests`

List the pending control requests of agents: permission prompts, plans to approve, questions, MCP elicitation forms and extension dialogs.

Each request prints with its handle. "agent approve", "agent deny" and "agent answer" take the agent and the handle, or a unique prefix of the handle. A request that the agent replaces gets a new handle, so an answer never reaches a request that you did not read.

The text form prints each request in full, with the numbers that --option takes. --json prints each prompt as data.

_Aliases: `req`._

```text
leapmux agent requests [agent]... [flags]
```

| Argument | Description |
|---|---|
| `[agent]...` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, "parent" for the calling agent's parent, or "self" for the agent tab that runs this command. Without one, the command lists the requests of every agent. |

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

- A permission prompt: its one-time approval. Choose another option, such as an approval for the session, with "agent answer --option".
- A plan: the approval. --permission-mode and --clear-context set how the agent implements it.
- An MCP elicitation: the accept, with the form's default values. Fill a form with "agent answer --field".
- A confirm dialog: the confirmation.

When [request] is omitted and the agent has exactly one pending request, it approves that request automatically. A single globally unique request handle also resolves its owning agent automatically.

An agent cannot answer its own requests.

_Mutates state._

```text
leapmux agent approve <agent> [request] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

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

When [request] is omitted and the agent has exactly one pending request, it refuses that request automatically. A single globally unique request handle also resolves its owning agent automatically.

An agent cannot answer its own requests.

_Mutates state._

```text
leapmux agent deny <agent> [request] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `-m, --message string` | the reason of the refusal, which the agent reads |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent deny 87a19d5f 3kQ9xZ1b --message "Run the unit tests only"
```

### `leapmux agent answer`

Answer a pending request with any answer that it offers. "agent requests" numbers the options of each request. When [request] is omitted and the agent has exactly one pending request, it answers that request automatically.

- A question: --option chooses an option, and --text (or a text argument) gives a typed answer. Use --text when omitting [request] so the typed text is never parsed as a request handle. A multiple-choice question takes both: the answer then holds the chosen options and the text. With several questions, write each value as "&lt;question&gt;:&lt;value&gt;", such as --option 2:1 or --text "1:Use the cache". A question that no --option answers keeps the options that the agent selected for it, which "agent requests" shows as its default. With no value at all, the command sends those defaults, which must answer every question that needs an answer.
- A permission prompt: --option chooses any option, such as an approval for the session. --message gives the reason of a refusing option.
- A plan: --option chooses one of its choices. --message gives what to change, and --permission-mode and --clear-context apply to an approval.
- An MCP elicitation: --field fills the form, once for each field, or once for each value of a list field. A field that "agent requests" lists with a condition takes a value only when the condition holds, and a field that it does not list keeps its default. --option chooses an accept choice.
- An input or editor dialog: --text or a text argument.

An agent cannot answer its own requests.

_Mutates state._

```text
leapmux agent answer <agent> [request] [text]... [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | An agent id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title or --name slug, or "parent" for the calling agent's parent. |
| `[request]` | See [common arguments and flags](#common-arguments-and-flags). |
| `[text]...` | A typed answer, or "-" to read it from standard input. With several questions, write "&lt;question&gt;:&lt;text&gt;". |

| Flag | Description |
|---|---|
| `--option string` | an option by its number, or "&lt;question&gt;:&lt;number&gt;" with several questions; repeat it for more than one (repeatable) |
| `--text string` | a typed answer (or "-" to read stdin); repeat for more than one (repeatable) |
| `--field string` | a form field as &lt;name&gt;=&lt;value&gt;; repeat it for more than one (repeatable) |
| `-m, --message string` | the reason of a refusing option or choice, which the agent reads |
| `--permission-mode string` | with a plan's approval, the permission mode to implement it in (default: the agent's own) |
| `--clear-context` | with a plan's approval, implement it in a fresh context |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
# Choose the second option of a question.
leapmux agent answer 87a19d5f 3kQ9 --option 2

# Choose an option and add typed text without specifying the sole request handle.
leapmux agent answer 87a19d5f --option 1 --text "Also check metrics"

# Answer an agent's sole pending question or input dialog with typed text.
leapmux agent answer 87a19d5f --text "v2.4.0"

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

_Mutates state._

```text
leapmux agent interrupt <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--reason string` | a reason that the agent's transcript records |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent interrupt 87a19d5f
```

### `leapmux agent clear`

Clear an agent's context. The command queues a clear. When the agent reads it, the Worker stops the agent's process and starts the provider again in a fresh session, in the same tab. The transcript keeps the earlier messages.

The clear waits in the agent's input queue behind the inputs before it, and it waits for the current turn to end, so it never cuts off a reply. "agent queue list" shows it, and "agent queue remove" takes it back while it waits. A clear also restarts an agent whose start failed.

A subagent refuses a clear.

_Mutates state._

```text
leapmux agent clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent clear 87a19d5f

# Clear the context, then give the agent its next task.
leapmux agent clear 87a19d5f && leapmux agent send 87a19d5f "Review the diff of the last commit"
```

### `leapmux agent compact`

Ask an agent to compact its context: the provider replaces the conversation so far with a summary, which frees room in the context window. The command queues the request. A provider without a compaction command of its own receives "/compact" as a message.

The request waits in the agent's input queue behind the inputs before it, and it waits for the current turn to end. "agent queue list" shows it, and "agent queue remove" takes it back while it waits.

A subagent refuses a compaction.

_Mutates state._

```text
leapmux agent compact <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent compact 87a19d5f
```

### `leapmux agent set`

Change an agent's model, effort, permission mode or another option.

_Mutates state._

```text
leapmux agent set <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--model string` | model id |
| `--effort string` | effort id |
| `--permission-mode string` | permission mode |
| `--option string` | provider option as key=value; repeat it for more than one; "agent get --options" lists them (repeatable) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent set 87a19d5f --model opus --effort high

# Set an option that only one provider has.
leapmux agent set 87a19d5f --option sandbox=workspace-write
```

### `leapmux agent messages`

Print an agent's messages, or follow new ones.

A message that another agent sent prints its sender after its source, and --json states the sender as sender.

_Aliases: `msg`._

```text
leapmux agent messages [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--anchor string` | page to print (one of: latest, oldest, before, after; default: latest) |
| `--cursor-seq int` | the seq that --anchor before or after starts from; the page excludes it |
| `-n, --limit int` | messages per page (default: 50) |
| `-f, --follow` | print new messages as they arrive, until interrupted |
| `--raw` | print raw provider JSON payloads in text mode |
| `--full` | do not truncate long message lines in text mode |
| `--markdown` | render messages as Markdown sections |
| `--summary` | append current goal, todos, and active background tasks after the messages |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent messages 87a19d5f

# Print new messages as they arrive, one JSON envelope per line.
leapmux agent messages 87a19d5f --follow --json
```

### `leapmux agent queue`

Inspect, edit, steer, preempt, retry, reorder, pause, and clear an agent's input queue.

Aliases: `q`.

#### `leapmux agent queue list`

List the inputs that wait in an agent's input queue, first to last: the messages that "agent send" sent, the clears and the compactions.

A message that a process in another agent's tab sent is an agent_message. FROM states the agent that sent it, and --json states it as sender.

Each input has a state:

- queued: the input waits for its turn.
- dispatching: the agent reads the input now.
- awaiting_echo: the agent took the input into its running turn, and LeapMux waits for the agent to confirm it.
- failed: the input did not reach the agent. The error states why.
- delivery_uncertain: the input may not have reached the agent.

The queue holds while it is paused, while its first input failed or may not have reached the agent, and while LeapMux has its first input open in an editor. The result states a pause and its reason, and the command states each other cause on stderr.

-q prints the id of each input.

_Aliases: `ls`._

```text
leapmux agent queue list [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue list 87a19d5f

# Print the queue as data, with the reorder segment of each input.
leapmux agent queue list 87a19d5f --json
```

#### `leapmux agent queue remove`

Remove one or more inputs from an agent's input queue. The agent never reads them.

An input that the agent already reads stays: the command fails with conflict. A removal also ends an edit of the input that LeapMux has open.

A retry of the "agent send --request-id" run that queued the input queues nothing for 7 days after the removal.

_Aliases: `rm` · Mutates state._

```text
leapmux agent queue remove <agent> <input>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<input>...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue remove 87a19d5f 3kQ9xZ1b

# Remove two queued inputs in one call.
leapmux agent queue remove 87a19d5f 3kQ9xZ1b 9Lm2Pq7c
```

#### `leapmux agent queue move`

Move an input to another place in an agent's input queue. --before, --first or --last gives the place.

An input moves only among the inputs of its reorder segment, as in LeapMux:

- The inputs that the agent reads as soon as it can form one segment, and the inputs that --at holds until one time form another. An input never moves past an input of another time.
- An input that the agent already reads is locked. It stays in its place, and the inputs on its two sides form two segments.

--first and --last mean the first and the last place of the segment. "agent queue list --json" states the segment of each input as reorder_segment.

_Aliases: `mv` · Mutates state._

```text
leapmux agent queue move <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<input>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--before string` | move the input before this input, as an id or a unique prefix |
| `--after string` | move the input after this input, as an id or a unique prefix |
| `--first` | move the input to the first place of its segment |
| `--last` | move the input to the last place of its segment |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
# Move an input before another one.
leapmux agent queue move 87a19d5f 3kQ9xZ1b --before 9Lm2Pq7c

# Make an input the next one that the agent reads.
leapmux agent queue move 87a19d5f 3kQ9xZ1b --first
```

#### `leapmux agent queue get`

Read the full text and metadata of one queued input.

_Does not support `-q` / `--quiet`._

```text
leapmux agent queue get <agent> <input> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<input>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue get 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue edit`

Replace the text or schedule of a queued input.

_Mutates state._

```text
leapmux agent queue edit <agent> <input> [text] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<input>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[text]` | Replacement message text, or "-" for stdin. |

| Flag | Description |
|---|---|
| `--file string` | read replacement text from this file ("-" reads stdin) |
| `--at string` | set a new schedule (RFC 3339 with offset, or +duration) |
| `--clear-schedule` | remove the input's scheduled send-not-before time |
| `--takeover` | take over an active edit lease held by another client |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue edit 87a19d5f 3kQ9xZ1b "Run only unit tests"
leapmux agent queue edit 87a19d5f 3kQ9xZ1b --at +15m
```

#### `leapmux agent queue steer`

Inject a queued input into the agent's active turn immediately.

_Mutates state._

```text
leapmux agent queue steer <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, steers the head steerable input. |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue steer 87a19d5f
leapmux agent queue steer 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue preempt`

Cancel the active turn so a queued input dispatches next.

_Mutates state._

```text
leapmux agent queue preempt <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, preempts with the head preemptable input. |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue preempt 87a19d5f
leapmux agent queue preempt 87a19d5f 3kQ9xZ1b
```

#### `leapmux agent queue advance`

Run one queue advance step (steer, preempt, or resume as appropriate).

_Mutates state._

```text
leapmux agent queue advance <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue advance 87a19d5f
```

#### `leapmux agent queue retry`

Retry a failed or delivery-uncertain queued input.

_Mutates state._

```text
leapmux agent queue retry <agent> [input] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[input]` | A queued input's id, as "agent queue list" prints it, or a unique prefix of it. When omitted, retries the failed or uncertain head input. |

| Flag | Description |
|---|---|
| `--confirm-uncertain` | confirm retrying an input whose earlier delivery state was uncertain |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue retry 87a19d5f
leapmux agent queue retry 87a19d5f 3kQ9xZ1b --confirm-uncertain
```

#### `leapmux agent queue clear`

Remove all unlocked inputs waiting in an agent's queue.

_Mutates state._

```text
leapmux agent queue clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue clear 87a19d5f
```

#### `leapmux agent queue pause`

Pause an agent's input queue. The agent finishes its current turn, and then reads no queued input until the queue resumes. New inputs still join the queue.

LeapMux pauses a queue by itself too: when you interrupt the agent, when the agent stops, and when an input does not reach the agent. "agent queue list" states why a queue is paused.

_Mutates state._

```text
leapmux agent queue pause <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue pause 87a19d5f
```

#### `leapmux agent queue resume`

Resume an agent's input queue, whatever paused it, so that the agent reads the next input when it can.

A failed input keeps its failure. While the first input failed, or while LeapMux has it open in an editor, the queue still holds, and the command says so on stderr. Remove the failed input with "agent queue remove", or retry it with "agent queue retry".

_Mutates state._

```text
leapmux agent queue resume <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent queue resume 87a19d5f
```

### `leapmux agent goal`

Read, set, pause, resume, or clear an agent's standing session goal.

#### `leapmux agent goal get`

Show an agent's current standing goal (default: this agent).

_Does not support `-q` / `--quiet`._

```text
leapmux agent goal get [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent goal get
leapmux agent goal get 87a19d5f
```

#### `leapmux agent goal set`

Set a standing session goal on a running agent.

_Mutates state._

```text
leapmux agent goal set <agent> <objective> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<objective>` | The standing goal objective text. |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent goal set 87a19d5f "Fix all failing tests and keep CI green"
```

#### `leapmux agent goal pause`

Pause an agent's active standing goal.

_Mutates state._

```text
leapmux agent goal pause <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent goal pause 87a19d5f
```

#### `leapmux agent goal resume`

Resume a paused standing goal.

_Mutates state._

```text
leapmux agent goal resume <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent goal resume 87a19d5f
```

#### `leapmux agent goal clear`

Clear an agent's standing goal.

_Mutates state._

```text
leapmux agent goal clear <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

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
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

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
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--active` | show only running or pending background tasks |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent tasks
leapmux agent tasks 87a19d5f --active
```

### `leapmux agent usage`

Show an agent's live cost, context usage, rate limits, and running tool (default: this agent).

_Does not support `-q` / `--quiet`._

```text
leapmux agent usage [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent usage
leapmux agent usage 87a19d5f --json
```

### `leapmux agent tree`

Show an agent and its descendant subagents and spawned agents (default: this agent).

```text
leapmux agent tree [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent tree
leapmux agent tree 87a19d5f
```

### `leapmux agent open-tab`

Open or revive a subagent's transcript as a tab in a workspace.

_Mutates state._

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
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent open-tab 5e9a01bc
leapmux agent open-tab 5e9a01bc --workspace self
```

### `leapmux agent startup`

Respond to a pending agent startup prompt (for example, OS Keychain confirmation).

#### `leapmux agent startup approve`

Proceed with a pending agent startup prompt.

_Mutates state._

```text
leapmux agent startup approve <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--prompt-id string` | prompt id to answer (default: the agent's current pending startup prompt) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent startup approve 87a19d5f
```

#### `leapmux agent startup cancel`

Cancel a pending agent startup prompt.

_Mutates state._

```text
leapmux agent startup cancel <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--prompt-id string` | prompt id to answer (default: the agent's current pending startup prompt) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent startup cancel 87a19d5f
```

### `leapmux agent retry-turn`

Stop a slow turn and retry it on the offered faster model.

_Mutates state._

```text
leapmux agent retry-turn <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--turn-id string` | turn id from the retry offer (default: auto-read from live session info) |
| `--model string` | model id from the retry offer (default: auto-read from live session info) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent retry-turn 87a19d5f
leapmux agent retry-turn 87a19d5f --turn-id turn-1 --model gpt-5-fast
```

### `leapmux agent approve-review`

Record approval for an action denied by the automatic reviewer.

_Mutates state._

```text
leapmux agent approve-review <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--message-id string` | transcript message id containing the denied review (required) |
| `--review-id string` | review id within that message (required) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent approve-review 87a19d5f --message-id msg-1 --review-id rev-1
```

### `leapmux agent message`

Fetch a single transcript message by sequence number.

_Does not support `-q` / `--quiet`._

```text
leapmux agent message <agent> <seq> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<seq>` | Positive message sequence number. |

| Flag | Description |
|---|---|
| `--raw` | print raw provider JSON payload in text mode |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent message 87a19d5f 42
leapmux agent message self 42 --json
```

### `leapmux agent span`

Fetch all transcript messages belonging to a tool or subagent span.

_Does not support `-q` / `--quiet`._

```text
leapmux agent span <agent> <span-id> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `<span-id>` | Tool or subagent span id (for example, toolu_01...). |

| Flag | Description |
|---|---|
| `--session-id string` | optional provider session id to disambiguate spans across sessions |
| `--raw` | print raw provider JSON payloads in text mode |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent span 87a19d5f toolu_01AbCd
```

### `leapmux agent marks`

List transcript jump marks (user prompts, plans, errors) and sequence limits (default: this agent).

```text
leapmux agent marks [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent marks
leapmux agent marks 87a19d5f
```

### `leapmux agent export`

Export an agent's complete transcript as Markdown, text, or JSON (default: this agent).

_Does not support `-q` / `--quiet`._

```text
leapmux agent export [agent] [flags]
```

| Argument | Description |
|---|---|
| `[agent]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--style string` | export style (one of: markdown, text, json; default: markdown) |
| `-o, --output string` | write exported transcript to this file instead of stdout |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

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
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent recoveries
leapmux agent recoveries --provider claude
```

### `leapmux agent recover`

Approve process recovery for an agent paused for login or process recovery.

_Mutates state._

```text
leapmux agent recover <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent recover 87a19d5f
```

### `leapmux agent search`

Search agent conversation transcripts across one or more workers.

Plaintext transcripts stay on each worker and are searched locally over E2EE; the hub never sees transcript contents. Omit --worker and --agent to fan out across every online worker in parallel.

```text
leapmux agent search <query> [flags]
```

| Flag | Description |
|---|---|
| `--agent string` | search only this agent (id, prefix, title, or "self") |
| `--provider string` | search only agents using this provider (such as claude, codex) |
| `--since string` | include only messages at or after this RFC 3339 time or lookback duration (such as 24h, 30m) |
| `--workspace string` | search only agents belonging to this workspace |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--regex` | treat &lt;query&gt; as an RE2 regular expression |
| `--case-sensitive` | match case-sensitively (default is case-insensitive) |
| `-n, --limit int` | maximum number of hits to return (max 200) (default: 50) |

```sh
leapmux agent search "race condition"
leapmux agent search "v[0-9]+\.[0-9]+" --regex --provider claude --since 24h
leapmux agent search "timeout" --agent 87a19d5f --limit 20
```

### `leapmux agent fork`

Fork an existing agent into a new agent tab, cloning its provider session history and Worker transcript while preserving the provider prompt cache.

Pass --fresh to clone only the agent's settings (provider, model, effort, permission mode, provider options, and working directory) into a fresh empty session without copying conversation history. --fresh also allows launching the clone on another Worker with --worker.

Pass --worktree &lt;branch&gt; to fork the agent directly into a fresh git worktree, or override individual settings (--model, --effort, --permission-mode, --option, --dir, --title, --name).

_Mutates state._

```text
leapmux agent fork <agent> [prompt] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[prompt]` | Optional first message to send to the forked agent ("-" reads stdin). Mutually exclusive with --message and --file. |

| Flag | Description |
|---|---|
| `--model string` | override model id |
| `--effort string` | override reasoning effort |
| `--permission-mode string` | override permission mode |
| `--option string` | override provider option as key=value; repeat for more than one (repeatable) |
| `--title string` | tab title (default: "&lt;source title&gt; (fork)") |
| `--name string` | unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `--fresh` | clone settings into a fresh empty session without copying conversation history |
| `-m, --message string` | queue a first message on the forked agent ("-" reads stdin) |
| `--file string` | read the first message from this file ("-" reads stdin) |
| `--attach string` | attach this file to the first message; repeat for more than one (repeatable) |
| `--request-id string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--wait` | wait until the forked agent settles (and print its reply when a message was sent) |
| `--all` | with --wait and a message, print every reply, not only the last one |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--output-schema string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--schema-retries int` | See [common arguments and flags](#common-arguments-and-flags). |
| `--dir string` | override working directory (default: source agent's working directory) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
leapmux agent fork 87a19d5f
leapmux agent fork 87a19d5f "Try an alternative implementation" --worktree alt-approach --base main
leapmux agent fork self --fresh --model opus --name reviewer
```

### `leapmux agent request-status`

Inspect the durable Worker state of a previous "agent send" by its --request-id (whether explicit or auto-generated).

Reports whether the request is not_found, queued, dispatching, waiting_for_user, in_progress, completed, interrupted, failed, or unproved, together with any replies recorded for that input.

```text
leapmux agent request-status <agent> <request-id> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |
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
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent providers
leapmux agent providers --worker name:build-box
```

### `leapmux agent sessions`

List the earlier sessions of a provider in one directory, newest first: the sessions that "agent open --resume" continues with the same --provider, --worker and --dir.

The Worker lists the sessions that LeapMux started and the sessions that the provider's own store holds, such as a session that you started in a terminal. It leaves out each session that an open agent runs, because a second process on one session damages the session. It returns only the newest sessions, up to a limit of its own.

A session's title is empty when the provider stores none.

```text
leapmux agent sessions [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers (default: $LEAPMUX_CONTROL_AGENT_PROVIDER) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
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

_Aliases: `rm` · Mutates state._

```text
leapmux agent close <agent>... [flags]
```

| Argument | Description |
|---|---|
| `<agent>...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux agent close 87a19d5f --worktree keep
```

### `leapmux agent rename`

Rename an agent tab or set its unique --name slug.

_Mutates state._

```text
leapmux agent rename <agent> [title] [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--name string` | set a unique short agent slug (lowercase letters, digits, hyphens, underscores) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent rename self "Release notes"

# Assign a unique short slug so commands can target the agent by name.
leapmux agent rename 87a19d5f --name reviewer
```

### `leapmux agent reveal`

Show a tab in every LeapMux window that you have open: each window switches to the tab's workspace, and the tab becomes the active tab of its tile, as a click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own state, so a window that you open later shows what it showed before, and a window that does not know the tab yet ignores the command.

_Mutates state._

```text
leapmux agent reveal <agent> [flags]
```

| Argument | Description |
|---|---|
| `<agent>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux agent reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux agent reveal self
```
