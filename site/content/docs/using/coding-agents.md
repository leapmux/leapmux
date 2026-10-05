---
title: "Coding Agents"
description: "Run coding-assistant CLIs like Claude Code and Codex in LeapMux: open an agent, chat, read tool calls, answer permission prompts, and switch models mid-session."
type: docs
weight: 3
---

Coding agents are the core feature of LeapMux. Each agent runs a native coding-assistant command-line interface (CLI) on a Worker. Claude Code and Codex are examples. Use its chat tab to send messages and read tool calls. Answer approval requests there also. This chapter covers supported agents and their controls.

See these chapters for related controls:

- [Tabs & Layout](/docs/using/tabs-and-layout/) explains the workspace layout.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) explains agent branches and worktrees.
- [Control CLI](/docs/using/control-cli/) explains how to control agents from a script.

## Choosing a provider

LeapMux integrates {{< agent-provider-count >}} coding-agent providers. It detects the installed coding agents on the Worker and lists them in the New agent dialog.

{{< agent-logos >}}

LeapMux turns off the self-update of each agent CLI that offers a switch for it in the environment, so an update does not replace a CLI while a session uses it. Update a CLI with its own installer or package manager between sessions.

All {{< agent-provider-count >}} providers support chat and streamed tool calls. The [feature matrix](#feature-matrix) shows approval requests and native model context on resume. The Goals & To-dos sidebar appears when an agent has a to-do list or a session goal. Each provider offers its own models and settings. Prompt style varies also.

## Opening a new agent

Open the **New agent** dialog from the workspace, then fill in the fields below and click **Create**.

### Dialog fields

| Field | What it does |
| --- | --- |
| **Worker** | The machine that will run the agent. Determines which providers are available and where the working directory lives. See [Managing Workers](/docs/admin/managing-workers/). |
| **Agent Provider** | Which agent CLI to launch. Shows the provider icon, label, and a chevron; a check marks the current choice. |
| **Directory** | The working directory for the agent, chosen from a directory tree on the Worker. A text box above the tree shows the selected path; type a path and press Enter to go there. It is the same picker the New Terminal dialog uses — see [Working Directory](/docs/using/terminals/#the-full-new-terminal-dialog) for the full behavior, including the path-style hint for a Windows Worker. |
| **Resume an existing session** | Optional. A menu of recent sessions for the selected directory and provider, with a filter box, a refresh button, and an **Enter a session ID…** row for a handle the list does not hold. Leave it on **Start a new session** to begin fresh (see [Resuming a session](#resuming-an-existing-session)). |
| **Title** | The tab name. Pre-filled with a random `Agent <Name>`; the refresh button beside the label picks another. Type your own to replace it. It cannot be empty. |
| **Git options** | Appears once a Worker is selected. Lets you start the agent on the current branch, switch branches, create a branch, or create/use a worktree. See [Worktrees & Branches](/docs/using/worktrees-and-branches/). |

{{< callout type="info" >}}
The dialog has **no model, effort, or permission-mode fields**. A new session takes the models and effort that your provider configuration states, and starts with a permission mode that asks before risky actions.

A resumed session keeps its stored settings. Change settings from the composer after the session starts (see [Changing settings mid-session](#changing-settings-mid-session)).
{{< /callout >}}

LeapMux remembers your most recently used provider. It pre-selects that provider when the chosen Worker offers it. Then you can pick a directory and click **Create**.

### Quick-open (no dialog)

If the active tab has a Worker and working directory, **New agent** opens an agent without a dialog. It uses that tab's provider or your most recent provider. LeapMux opens the full dialog when it cannot determine the Worker, directory, or provider.

### Where the new agent lands

The Worker assigns a friendly title from a shared name pool (you'll see titles like "Agent <Name>"); you can rename the tab later. For how tabs are placed, split, and tiled, see [Tabs & Layout](/docs/using/tabs-and-layout/).

### Resuming an existing session

To reopen a native session, pick it from the **Resume an existing session** field in the New agent dialog. The field lists recent sessions for the selected provider. It filters by directory when the native store records one. Each entry shows a title and age. A filter box narrows the list, and the refresh button asks the Worker again.

The list combines LeapMux's agent records with each CLI's session history on that machine. A session you started in a terminal can appear here too. Where both stores know a session, LeapMux's record wins.

The list excludes a session that an open Worker tab owns. A second process can corrupt that native session, so close its tab first. A provider that records each session's directory also excludes sessions from another directory. Some native stores record no directory and cannot apply that filter. Changing the directory or provider clears a session you picked.

Leave the field on **Start a new session** to begin fresh. It is a real entry in the menu, so it is also how you take back a session you picked.

The last entry, **Enter a session ID…**, swaps the menu for a text box. Use it for a session from another machine or one older than the newest fifty. Manual entry also rejects a session that an open Worker tab owns.

The field shows the box when the Worker finds no sessions. A directory with no history leaves the list empty. An absent native store or a failed Worker request can do the same. The box checks the ID and reports one it cannot use.

The Worker asks the provider to reopen the native session after you submit. The new tab shows chat rows that an earlier LeapMux tab saved on this Worker. An external native session can resume without earlier Worker chat rows. Its provider can replay native history. The [matrix](#feature-matrix) states whether the provider restores prior model context.

If native resume fails, the tab reports the cause and the agent does not start. Send `/clear` to start a fresh session.

### Automatic resume

Most session resumption occurs automatically. A Hub restart keeps the existing tab and Worker transcript. A Worker restart or client reconnect keeps them also. When LeapMux restarts an agent process, it asks the provider to reopen the native session. The matrix states whether that provider restores prior model context also. If native resume fails, LeapMux reports the failure and keeps the transcript.

## Chatting with an agent

The chat tab has the conversation transcript above and a Markdown editor at the bottom.

### Composing and sending

The editor is a full Markdown editor in a single input box. Type your message and send it with the **Send** button (the paper-plane icon) or with the keyboard. While the message is in flight, a spinner replaces the Send icon.

The box starts one line tall, with the **[+]** menu on the left and send controls on the right. For longer messages, it grows and puts the controls below the text.

Send is disabled when the editor is empty and there are no attachments.

Use Markdown shortcuts as you type:

- `**bold**` for bold text.
- `` `code` `` for inline code.
- `# heading` for a heading.
- `- list` for a list.
- ` ``` ` for a code block.
- `[text](url)` for a link.

Click a link to edit its URL. The editor supplies **Save** and a remove button. Changing the link's visible text does not change its URL.

#### Enter-key send mode

An item in the composer's **[+]** menu controls what the **Enter** key does. The two modes are:

| Mode | Enter | Modifier+Enter |
| --- | --- | --- |
| **Enter sends** | Sends the message | (Shift+Enter for a new line) |
| **Cmd/Ctrl+Enter sends** (default) | Inserts a new line | Cmd+Enter (macOS) / Ctrl+Enter (other platforms) sends |

The default is **Cmd/Ctrl+Enter sends**, so plain Enter adds a newline. Open **[+]** and choose **Send with Cmd/Ctrl+Enter** to switch. LeapMux saves the choice as a [preference](/docs/using/settings/) across sessions.

### Attachments

You can attach files with **[+] > Attach file...**, or by pasting or dropping them into the editor. Pending attachments appear in a strip above the editor. What you can attach depends on the provider. See the [feature matrix](#feature-matrix).

ZCode accepts an image only when the selected model declares image input. LeapMux refuses an image on a text-only model and identifies that model. Without this check, ZCode accepts the image but does not send it to the model.

### Message persistence and offline behavior

LeapMux shows your message immediately and confirms it when the server returns it. If the agent process is not ready, LeapMux queues the message until startup completes. These pending messages survive a page refresh. Retry or delete a message if delivery fails. [The input queue](#the-input-queue) keeps every submitted item until delivery.

### Interrupting a turn

The **Interrupt** button appears while the agent works and no permission prompt waits for an answer. Click the square icon to stop the current turn. LeapMux uses the agent's native interrupt mechanism.

{{< callout type="info" >}}
The **Interrupt** button is hidden while the agent waits for your permission or answer. Answer that prompt instead (see [Permission and approval prompts](#permission-and-approval-prompts)).
{{< /callout >}}

## The input queue

These submitted items enter the agent's input queue first:

- A message.
- An attachment.
- A `/clear` command.
- A plan execution.
- An answer to a permission prompt.

The Worker stores the queue. It survives a page refresh or reconnect. It survives a Worker restart also. Every device that uses your account sees the same queue.

An item leaves the queue when the agent takes it. While the agent works,
anything you send waits its turn, and the queue appears above the composer with
one row per waiting item. Each row shows a preview, what kind of input it is,
and its delivery state.

### Working with queued items

| Control | What it does |
| --- | --- |
| **Move up** / **Move down** | Reorder a waiting item. Drag a row by its grip for the same effect. |
| **Edit** | Load the item back into the composer to change it. Another device editing it shows **Take Over** instead. |
| **Delete** | Drop the item. Click twice to confirm. |
| **Retry** | Send an item again after a delivery failure. |
| **Steer** | Hand the first item to the turn already running, instead of waiting for it to finish. |
| **Pause Queue** | Stop delivering. The Send button reads **Queue** while paused. |

An item already on its way to the agent cannot be moved, reordered around, or deleted.

### Pausing

The queue pauses after any of these conditions:

- Delivery fails.
- Delivery becomes uncertain.
- The agent stops.

A banner above the composer states the cause. Use **Pause Queue** to collect several messages before delivery. Use **Resume** to start delivery again.

### Steering

Steering hands the queue's **first** item to the turn the agent is already
running, rather than letting it wait. It is how you correct an agent
mid-thought: queue "use the existing helper instead", steer it, and the agent
takes it into the work in progress.

Press **`Cmd/Ctrl+Enter`** while the composer is empty, or click **Steer** on the
first row. The shortcut and the button offer the same thing under the same
conditions, and nothing happens when any of them is unmet:

- the agent's provider has to accept a steer while it runs;
- a turn has to be in progress, and it has to be an ordinary turn rather than a
  `/clear` or a `/compact`;
- the first item has to be waiting — not on its way to the agent, and not open for edit.

The shortcut acts only on an **empty** composer, because `Cmd/Ctrl+Enter` sends
whatever the composer holds. Text, an attachment, or a permission prompt waiting
for approval all count as something to send, so in each of those cases the chord
sends as usual.

## The chat view

### Tool calls and results

As an agent works, the transcript shows its text and any thinking the provider exposes. Visible tool calls and their results appear in rows. The available tools depend on the provider. Common tool rows include:

- **File reads** — the file the agent opened.
- **Edits and writes** — rendered with a diff. The result toolbar offers a **split / unified** diff toggle.
- **Bash / command execution** — the command and its output.
- **Search / grep / glob** — the query and matches.
- **Web fetch / web search** — the URL or query and what came back.
- **Todo / plan updates** — feed a persistent todo sidebar (see below).
- **MCP tool calls** — calls into Model Context Protocol servers the agent has access to, rendered like any other tool call.

Use **Expand** to open a long tool result. Most rows have a **Copy** button. Some row headers also offer these controls:

- **Quote** copies the row's text into the editor as a quoted reply.
- **Copy Markdown** copies the row as Markdown.
- **Copy Raw JSON** copies the native data for debugging.

The permission banner also offers **Copy Raw JSON** (see [Permission and approval prompts](#permission-and-approval-prompts)).

{{< callout >}}
Some rows are intentionally hidden to keep the transcript readable — for example, Claude Code suppresses its internal todo-list and tool-search bookkeeping rows. The information still drives the UI (the todo sidebar), it just isn't repeated inline.
{{< /callout >}}

### Images in tool results

When a tool returns an image, its row shows the picture scaled to fit the transcript.

Click the picture to open it in its own tab. There you can zoom and pan. When the agent identifies its source file, LeapMux opens that file at full resolution from the Worker.

Not every image renders inline:

- **Images above about 5 MB** show a placeholder instead of the picture.
- **An image the agent gives by URL** shows an **open ↗** link instead of the picture. Rendering it would fetch from that host, which the transcript never does on its own.

Tool-result content depends on the provider. See the [feature matrix](#feature-matrix).

- ZCode sends a text result. LeapMux recovers images from ZCode's stored attachment records.
- Codewhale publishes immutable native image records. LeapMux stores verified image bytes beside the native tool event.
- Cline supplies text-only tool results.
- Amp can display an image from a file that `Read` opens. Its MCP results supply text only.

### Turn boundaries and notifications

A divider marks the end of each turn. It can show a duration or an error. LeapMux also shows provider notifications. It combines repeated or empty notices so they do not fill the transcript.

### The Goals & To-dos sidebar

This section holds two things an agent works toward: its session goal, and its to-do list.

The **session goal** is a standing objective. The agent checks it at the end of every turn and continues while the condition does not hold. The card shows the objective and status. It shows the counters that the agent reports also.

Set a goal from the card. Its menu supplies these controls where the provider supports them:

- Change the goal.
- Pause the goal.
- Resume the goal.
- Clear the goal.

See the [feature matrix](#feature-matrix) for each provider's controls.

Oh My Pi shows its goal. Start or change that goal from Oh My Pi itself. These providers receive goal changes as messages:

- Claude Code.
- Goose.
- Kilo.
- Grok Build.

Qwen Code receives goal changes through its goal control, not as messages. A change takes effect at once, also while a goal round runs. Grok Build runs a whole goal inside one turn, so LeapMux sends its goal messages to the agent at once and not through the input queue.

Kiro receives a new goal as a message. Each such message enters the agent's input queue and uses a turn.

The **to-do list** shows each item's status (pending, in progress, completed). The agent's own to-do or plan tool feeds it. The list comes from the Worker, so it stays correct across reconnects.

A chip on the thinking indicator shows the to-do count and opens the same section as a popover.

### Subagents and the Background tasks sidebar

LeapMux lists a subagent when its CLI reports the child. It lists a background shell when the CLI reports a stable process identity and status. Each row in the **Background tasks** sidebar shows the task's live status. A subagent row opens its transcript in a tab beside its parent. A chip on the thinking indicator shows the active count and opens the same list as a popover.

A dynamic workflow runs many subagents for one job. LeapMux groups those subagents under the workflow row.

Closing a subagent tab closes only the tab. LeapMux keeps its transcript and registry record. Reopen the tab from the sidebar. This section includes only providers whose CLIs report subagent activity. The Worker stores the registry in its local database. The Hub never receives it.


## Permission and approval prompts

LeapMux shows a **control request** banner above the editor when the agent asks for approval or an answer. The agent waits until you answer it.

### The banner

Every banner has the same shape:

- A title that states the request.
- A request body that explains the action, often as collapsible JSON.
- One button for each answer that the provider offers.
- An editor for a custom answer or rejection reason.
  Its placeholder reads **"Type a custom answer..."** for a question.
  It reads **"Type a rejection reason..."** for other requests.

The provider's protocol supplies the buttons and labels. Most providers ask about one call at a time. Some offer approval for the rest of the session. Others offer a rule that the CLI remembers. Check each button's scope before you allow a call. Text that you submit with a denial reaches the agent as its rejection reason.

The banner shows a **Permissions** choice when the provider supplies permission shortcuts. See [Changing settings mid-session](#changing-settings-mid-session). It can offer these choices:

- **Unchanged**.
- **Smart**.
- **Bypass**.

Only supported shortcuts appear. An approval applies the selected shortcut to the session also. The initial choice matches the session's current shortcut. A plan-approval banner starts with **Smart** when the provider offers it.

### Questions

A question uses radio buttons for one answer or checkboxes for several answers. You can type a custom answer also. A prompt with several questions shows **Question N of M**. One submission answers all its questions. LeapMux collects requested MCP input through a form or the provider's native questions. See the [feature matrix](#feature-matrix).

### Plan approvals

When an agent finishes planning, the banner shows the plan. **Approve** starts the work. **Reject** keeps plan mode and sends your feedback. **Clear Context** starts the work in a fresh session when the provider offers that choice. Some providers end plan mode without asking (see the [feature matrix](#feature-matrix)).

### Several prompts at once

Answer queued prompts one at a time. LeapMux removes duplicate requests and remembers answers. A reconnect does not repeat a request that you already answered.

The exact buttons and their names come from the provider's own protocol.

## Changing settings mid-session

### The status bar and the [+] menu

The status bar below the editor shows chips for common settings:

- Branch.
- Model.
- Reasoning effort.
- Mode.

Click a chip to change its setting.

The **[+]** menu holds every settings axis, including options without a chip. Each axis has a submenu. Open **Agent info** for these details:

- Context usage.
- Rate limits.
- Session information.

LeapMux exposes the native settings that its provider adapter supports. These can include:

- Model.
- Reasoning effort.
- Mode.
- Permissions.
- Other provider-specific settings.

The available values come from the CLI's configuration. Choose a radio option in the interface. For `leapmux control agent set`, `--permission-mode` sets the permission axis. Use `--option` for another axis.

You can hide the status bar with **[+] > Show status bar**. The **[+]** menu still gives access to all status bar settings.

{{< callout type="info" >}}
Most settings changes apply **live**. LeapMux restarts the provider when a launch flag must change. Examples include a permission-mode change, effort **Auto**, and a model change. The interface applies each change optimistically and restores the prior value after a failure.
{{< /callout >}}

A picker shows radio items for up to 7 options and switches to a searchable list above that.

### Permission shortcuts

Providers can add two adjacent permission shortcuts. **Smart permissions** selects the provider's safety-assisted mode. **Bypass permissions** disables permission prompts. Smart permissions is always directly above Bypass permissions. A shortcut appears only when the session offers every setting and value that the preset needs.

Each safety-assisted mode approves the calls it judges safe and asks about the others.

**Bypass** is a deliberate choice that stays set: it stops the agent from asking for approval. Use it only when you trust the working directory and the task.

### Reasoning effort and the "Auto" default

**Auto** lets the CLI pick the tier. Set effort explicitly only when you want to force a particular tier.

### Plan mode shortcut

For providers that have a plan-mode setting, **Shift+Tab** in the editor toggles between plan mode and the previous mode. The toggle works only for a provider that has a plan-mode setting (see the [feature matrix](#feature-matrix)).

## Driving agents from a script

You can also use the `leapmux control` CLI for these actions. Agents can call it because the Worker gives each agent its credentials. Common commands include:

```bash
# Send a message to an agent tab
leapmux control agent send --tab-id <id> --message "Refactor the auth module"

# Interrupt the current turn
leapmux control agent interrupt --tab-id <id> --reason "wrong file"

# Change model / effort / permission mode mid-session
leapmux control agent set --tab-id <id> --model gpt-5.4 --effort high

# Open a new agent in a tab (provider, model, working dir, worktree, etc.)
leapmux control tab open --type agent --worker-id <id> --provider "Claude Code" \
  --working-dir /repo --initial-message "Start on the bug fix"

# Answer a Claude-Code-style control request
leapmux control agent send-control-response --tab-id <id> --content '<raw JSON>'
```

See [Control CLI](/docs/using/control-cli/) for the full command tree, entity-ID resolution, and the JSON output contract.

## Feature matrix

Every provider supports chat and streamed tool calls. Each matrix cell shows one of three states. The legend above the first table explains each symbol. The page splits the agents over several tables, and each table lists the features in groups. The first group header of a table shows no logos, because the table head shows them. Each later group header repeats the logos. Select a feature label for its exact definition. A † link beside a symbol opens a short note about that cell.

{{< matrix >}}
