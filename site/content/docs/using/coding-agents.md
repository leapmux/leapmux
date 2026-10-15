---
title: "Coding Agents"
description: "Run coding-assistant CLIs like Claude Code and Codex in LeapMux: open an agent, chat, read tool calls, answer permission prompts, and switch models mid-session."
type: docs
weight: 3
---

Coding agents are the core feature of LeapMux. Each agent is a real coding-assistant CLI (Claude Code, Codex, and others) running on a Worker, wrapped in a chat tab so you can talk to it, watch its tool calls, and approve its actions. This chapter covers which agents are supported and how to open, chat with, and configure one.

For where agents live in the workspace layout, see [Tabs & Layout](/docs/using/tabs-and-layout/). For the git side of opening an agent in a branch or worktree, see [Worktrees & Branches](/docs/using/worktrees-and-branches/). To drive agents from a script instead of the browser, see [Control CLI](/docs/using/control-cli/).

## Choosing a provider

LeapMux integrates {{< agent-provider-count >}} coding-agent providers. It detects the installed coding agents on the Worker and lists them in the New agent dialog.

{{< agent-logos >}}

LeapMux turns off the self-update of each agent CLI that offers a switch for it in the environment, so an update does not replace a CLI while a session uses it. Update a CLI with its own installer or package manager between sessions.

All {{< agent-provider-count >}} providers support chat and streamed tool calls. The [feature matrix](#feature-matrix) shows approval requests and native model context on resume. The Goals & To-dos sidebar appears when an agent has a to-do list or a session goal. Each provider offers its own models and settings, and its prompt style varies too.

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

LeapMux remembers your most recently used provider and pre-selects it (when it is available on the chosen Worker), so you usually only have to pick a directory and click **Create**.

### Quick-open (no dialog)

If you trigger "new agent" from a tab that already has a Worker and working directory, LeapMux skips the dialog and opens an agent directly, reusing the active tab's provider (or your most-recent provider). It only falls back to the full dialog when the Worker, directory, or provider can't be inferred.

### Where the new agent lands

The Worker assigns a friendly title from a shared name pool (you'll see titles like "Agent <Name>"); you can rename the tab later. For how tabs are placed, split, and tiled, see [Tabs & Layout](/docs/using/tabs-and-layout/).

### Resuming an existing session

To reopen a native session, pick it from the **Resume an existing session** field in the New agent dialog. The field lists recent sessions for the selected provider. It filters by directory when the native store records one. Each entry shows a title and age. A filter box narrows the list, and the refresh button asks the Worker again.

The list combines LeapMux's agent records with each CLI's session history on that machine. A session you started in a terminal can appear here too. Where both stores know a session, LeapMux's record wins.

A session already open in a tab isn't offered, because a second process against one native session store can corrupt it — close the tab first. Providers that record a session's directory also exclude sessions from another directory; some native stores record no directory and cannot apply that filter. Changing the directory or provider clears a session you had picked, since a session ID means nothing in another directory.

Leave the field on **Start a new session** to begin fresh. It is a real entry in the menu, so it is also how you take back a session you picked.

The last entry, **Enter a session ID…**, swaps the menu for a text box. Use it for a session from another machine, or one older than the newest fifty. The field also falls back to that box on its own when the Worker finds no sessions at all — a directory with no history, a provider whose store this machine doesn't have, or a Worker that can't answer. The box checks what you type and reports a session ID it cannot use.

Once you submit, the Worker asks the provider to reopen the native session. The new tab shows the chat rows an earlier LeapMux tab saved on this Worker; an external native session can resume without them, because its provider replays its own history. Whether the provider restores prior model context states the [matrix](#feature-matrix).

If native resume fails, the agent doesn't start and the tab reports why. Send `/clear` in the chat to start a fresh session instead.

### Automatic resume

Most resumption happens automatically: the tab and its Worker transcript survive a Hub restart, a Worker restart, and a client reconnect. When LeapMux has to respawn an agent's process, it reconnects it to the prior session through the provider's own resume mechanism — whether that restores the provider's prior model context states the [matrix](#feature-matrix). If native resume fails, LeapMux reports the failure and keeps the transcript, so an empty session never replaces the conversation.

## Chatting with an agent

The chat tab has the conversation transcript above and a Markdown editor at the bottom.

### Composing and sending

The editor is a full Markdown editor in a single input box. Type your message and send it with the **Send** button (the paper-plane icon) or with the keyboard. While the message is in flight, a spinner replaces the Send icon.

The box starts one line tall, with the **[+]** menu at the left end and the send controls at the right. It expands into a taller layout, with those controls on their own row beneath the text, as soon as the message needs more than one line.

Send is disabled when the editor is empty and there are no attachments.

Markdown shortcuts apply as you type: `**bold**`, `` `code` ``, `# heading`, `- list`, ` ``` ` for a code block, and `[text](url)` for a link. Click a link to open a small editor for its URL, with **Save** and a remove button. Use it whenever a URL is wrong — editing the link's visible text does not change where it points.

#### Enter-key send mode

An item in the composer's **[+]** menu controls what the **Enter** key does. The two modes are:

| Mode | Enter | Modifier+Enter |
| --- | --- | --- |
| **Enter sends** | Sends the message | (Shift+Enter for a new line) |
| **Cmd/Ctrl+Enter sends** (default) | Inserts a new line | Cmd+Enter (macOS) / Ctrl+Enter (other platforms) sends |

The default is **Cmd/Ctrl+Enter sends**, so plain Enter adds a newline. Open the **[+]** menu and click **Send with Cmd/Ctrl+Enter** to switch; the choice is saved as a [preference](/docs/using/settings/) and persists across sessions.

### Attachments

You can attach files with **[+] > Attach file...**, or by pasting or dropping them into the editor. Pending attachments appear in a strip above the editor. What you can attach depends on the provider. See the [feature matrix](#feature-matrix).

ZCode accepts an image only when the selected model declares image input. LeapMux refuses an image on a text-only model and identifies that model. Without this check, ZCode accepts the image but does not send it to the model.

### Message persistence and offline behavior

Your messages appear immediately (optimistically) and are reconciled when the server echoes them back. If you send while the agent subprocess is still starting, the message is queued and delivered once the agent is ready. Optimistic messages survive a page refresh; if delivery fails, you can retry or delete the message. Everything you send passes through [the input queue](#the-input-queue), which is what makes that durable.

### Interrupting a turn

While the agent is actively working — and there is no pending permission prompt — an **Interrupt** button (a square icon) appears. Click it to stop the current turn. LeapMux asks the agent to stop via its native interrupt mechanism rather than killing the process.

{{< callout type="info" >}}
The **Interrupt** button is hidden whenever the agent is waiting on you with a permission or question prompt — answer the prompt instead (see [Permission and approval prompts](#permission-and-approval-prompts)).
{{< /callout >}}

## The input queue

Everything you send an agent goes into its input queue first: a message, an
attachment, a `/clear`, a plan execution, an answer to a permission prompt. The
queue lives on the Worker, so it survives a page refresh, a reconnect, and a
Worker restart, and every device you are signed in on sees the same one.

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

The queue pauses itself when something goes wrong — a delivery fails, delivery
is uncertain, or the agent stopped — and a banner above the composer says which.
Pause it yourself with **Pause Queue** to stack up several messages before
letting the agent have them. **Resume** starts delivery again.

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

## How tool calls and results render

As an agent works, the transcript shows its assistant text, its thinking (where the provider exposes it), and a row for every tool call it makes, followed by that tool's result. The exact set of tools depends on the provider, but you will commonly see:

- **File reads** — the file the agent opened.
- **Edits and writes** — rendered with a diff. The result toolbar offers a **split / unified** diff toggle.
- **Bash / command execution** — the command and its output.
- **Search / grep / glob** — the query and matches.
- **Web fetch / web search** — the URL or query and what came back.
- **Todo / plan updates** — feed a persistent todo sidebar (see below).
- **MCP tool calls** — calls into Model Context Protocol servers the agent has access to, rendered like any other tool call.

Long tool results are collapsible (an **Expand** button), and most rows have a **Copy** button. Where it makes sense, a row's header also offers a **Quote** button (tooltip "Quote", pulls the row's text into the editor as a quoted reply), a **Copy Markdown** button (tooltip "Copy Markdown"), and a **Copy Raw JSON** button for debugging. The permission-prompt banner (see [Permission and approval prompts](#permission-and-approval-prompts)) carries its own **Copy Raw JSON** action too.

{{< callout >}}
Some rows are intentionally hidden to keep the transcript readable — for example, Claude Code suppresses its internal todo-list and tool-search bookkeeping rows. The information still drives the UI (the todo sidebar), it just isn't repeated inline.
{{< /callout >}}

### Images in tool results

When a tool returns an image — a screenshot from an MCP browser tool, a `Read` on a PNG, a generated picture — the row shows the picture itself, scaled to fit the transcript.

Click one to open it in its own tab, where you can zoom it (fit, 100%, or any step in between) and pan. When the agent said which file the image came from, LeapMux opens that file instead, so you see it at full resolution straight from the Worker.

Not every image renders inline:

- **Images above about 5 MB** show a placeholder instead of the picture.
- **An image the agent gives by URL** shows an **open ↗** link instead of the picture. Rendering it would fetch from that host, which the transcript never does on its own.

What a tool result can carry depends on the provider — see the [feature matrix](#feature-matrix). A few providers are worth calling out:

- ZCode's app server turns an image part into a placeholder string before it reaches LeapMux, so LeapMux instead recovers the image from ZCode's stored attachment records.
- Codewhale publishes immutable native image records; LeapMux stores verified image bytes beside the native tool event.
- Cline supplies text-only tool results.
- Amp can display an image from a file that `Read` opens; its MCP results supply text only.
- Muse Code hands an image's bytes to the next model request, but its tool result states the file and media type as text alone.

### The Goals & To-dos sidebar

This section holds two things an agent works toward: its session goal, and its to-do list.

The **session goal** is a standing objective. The agent re-tests it at the end of every turn and keeps working while the condition does not hold. The card shows the objective, its status, and the counters the CLI reports; the [feature matrix](#feature-matrix) states which providers report one and which controls each offers.

Set a goal from the card, and use the card's menu to change, pause, resume, or clear it where the provider supports that. A few providers differ in how a change travels: Claude Code, Goose, Kilo, and Grok Build take it as a message, which enters the agent's input queue and costs a turn (Grok Build runs a whole goal inside one turn, so its goal messages are sent to the agent at once rather than through the queue). Qwen Code receives goal changes through its goal control — a change takes effect at once, even while a goal round runs. Oh My Pi only displays its goal; start or change it from Oh My Pi itself.

The **to-do list** shows each item's status (pending, in progress, completed). Whatever to-do or plan tracking the provider's own tools report feeds it. The list is server-authoritative, so it stays correct across reconnects.

A chip on the thinking indicator shows the to-do count and opens the same section as a popover.

### Subagents and the Background tasks sidebar

When an agent spawns a subagent (a Task or Agent tool) or runs a background shell it reports, LeapMux tracks it in a **Background tasks** sidebar section. Each row shows the task's live status and, for subagents that own a transcript, is clickable to open the subagent in its own tab alongside its parent. A small chip on the thinking indicator shows the active count and opens the same list as a popover. A dynamic workflow that runs many subagents for one job groups them under the workflow's row.

Closing a subagent tab closes only the tab. The transcript and registry survive, and you can reopen the tab from the section later. Only providers whose CLIs report subagent activity appear here; the registry lives in the Worker's local database and never reaches the Hub.

### Turn boundaries and notifications

The end of each turn is marked by a divider that may carry a label such as a duration ("Took 2.1s") or an error ("API Error: 529 …"). LeapMux also surfaces notifications for events like rate limits, context compaction, retries, and settings changes, collapsing repeated or no-op notifications so they don't flood the transcript.

## Permission and approval prompts

When an agent needs your approval — to run a command, edit a file, or proceed with a plan — or wants to ask you a question, LeapMux shows a **control request** banner directly above the editor. Every banner has the same shape: a title stating the request, a body explaining the action (often as collapsible JSON), one button for each answer the provider offers, and the editor for a custom answer. The editor placeholder changes to hint at what to type — **"Type a custom answer..."** for a question, **"Type a rejection reason..."** for any other request.

The provider's protocol supplies the buttons and labels. Most providers ask about one call at a time. Some offer approval for the rest of the session. Others offer a rule that the CLI remembers. Check each button's scope before you allow a call. Text that you submit with a denial reaches the agent as its rejection reason.

The banner shows a **Permissions** choice when the provider supplies permission shortcuts. See [Changing settings mid-session](#changing-settings-mid-session). It offers **Unchanged**, **Smart**, or **Bypass** (only the shortcuts the session supports appear). An approval applies the selected shortcut to the session as well; the initial choice matches the session's current shortcut, and a plan-approval banner starts with **Smart** when the provider offers it.

### Questions

A question uses radio buttons for a single answer or checkboxes for several; you can also type a custom answer. A prompt with several questions shows **Question N of M**, and one submission answers all of them. Requested MCP input is collected through a form or the provider's native questions (see the [feature matrix](#feature-matrix)).

### Plan approvals

When an agent finishes planning, the banner shows the plan. **Approve** starts the work. **Reject** keeps plan mode and sends your feedback. **Clear Context** starts the work in a fresh session when the provider offers that choice. Some providers end plan mode without asking (see the [feature matrix](#feature-matrix)).

If several prompts queue up, you answer them one at a time. LeapMux de-duplicates requests and remembers answered ones, so a reconnect never re-asks something you already handled.

The exact buttons and their names come from the provider's own protocol.

## Changing settings mid-session

Beneath the editor box is a status bar with one chip per setting axis — the git branch, and the agent's current model, reasoning effort, and mode. Click a chip to change that axis.

The **[+]** menu holds every axis, including provider-specific options that have no chip. Each axis has a submenu. The menu also holds **Agent info** for context usage, rate limits, and the session.

LeapMux exposes the native settings its provider adapter supports — model, reasoning effort, mode, permissions, and other provider-specific axes — with the values the CLI's own configuration offers. You pick these as named radio options in the interface; when driving an agent with `leapmux control agent set`, `--permission-mode` sets the permission axis and `--option` any other one.

You can hide the status bar with **[+] > Show status bar**. The **[+]** menu still gives access to all status bar settings.

{{< callout type="info" >}}
Most settings changes apply **live**. LeapMux restarts the provider when a launch flag must change. Examples include a permission-mode change, effort **Auto**, and a model change to a model that does not offer the current effort; a model change keeps the current effort when the new model offers it. The interface applies each change optimistically and restores the prior value after a failure.
{{< /callout >}}

A picker shows radio items for up to 7 options and switches to a searchable list above that.

### Permission shortcuts

Providers can add two adjacent permission shortcuts. **Smart permissions** selects the provider's safety-assisted mode. **Bypass permissions** disables permission prompts. Smart permissions is always directly above Bypass permissions. A shortcut appears only when the session offers every setting and value that the preset needs.

Each safety-assisted mode approves the calls that it judges safe. The provider's mode decides what happens to the other calls: it asks you, or it denies the call.

For Codex, **Smart permissions** selects Codex's auto-review, which Codex itself calls **Approve for me**. A reviewer agent decides each approval request in place of you, and the transcript shows each decision with the reviewer's reason. A row for a denied action has an **Approve** button. The button records your approval, so the agent can retry that exact action once. The button does not run the action. The row keeps the approval, also after a reload. Codex accepts this approval only in the main agent, so a subagent's denied row shows no button.

**Bypass** is a deliberate choice that stays set: it stops the agent from asking for approval. Use it only when you trust the working directory and the task. For Codex, Bypass also approves the requests of work that started before the switch. A turn that is running keeps the approval policy that it started with, so Codex still sends those requests. The transcript states how many waiting requests Bypass approved.

### Reasoning effort and the "Auto" default

For the providers whose effort LeapMux manages, effort defaults to **Auto**, meaning "let the CLI pick." When effort is Auto, LeapMux omits the effort flag entirely, so older CLI versions that don't recognize newer effort names still work. You only need to set effort explicitly if you want to force a particular tier.

### Plan mode shortcut

For providers that support a plan mode, **Shift+Tab** in the editor toggles between plan mode and the previous mode. Which providers those are states the [feature matrix](#feature-matrix).

## Driving agents from a script

Everything in this chapter has a programmatic counterpart in the `leapmux control` CLI, which agents themselves can call (the Worker injects credentials into each spawned agent's environment). The most relevant commands:
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
