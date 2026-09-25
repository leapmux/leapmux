---
title: "Coding Agents"
description: "Run coding-assistant CLIs like Claude Code and Codex in LeapMux: open an agent, chat, read tool calls, answer permission prompts, and switch models mid-session."
type: docs
weight: 3
---

Coding agents are the core feature of LeapMux. Each agent is a real coding-assistant CLI (Claude Code, Codex, and others) running on a Worker, wrapped in a chat tab so you can talk to it, watch its tool calls, and approve its actions. This chapter covers which agents are supported and how to open, chat with, and configure one.

For where agents live in the workspace layout, see [Tabs & Layout](/docs/using/tabs-and-layout/). For the git side of opening an agent in a branch or worktree, see [Worktrees & Branches](/docs/using/worktrees-and-branches/). To drive agents from a script instead of the browser, see [Control CLI](/docs/using/control-cli/).

## Choosing a provider

### Supported providers

LeapMux integrates nineteen coding-agent providers:

| Provider | CLI binary detected on the Worker |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex` (or `codex-x86_64-pc-windows-msvc`) |
| Cursor | `cursor-agent` |
| GitHub Copilot | `copilot` |
| Kilo | `kilo` |
| OpenCode | `opencode` |
| Goose | `goose` |
| Pi | `pi` |
| Reasonix | `reasonix` |
| ZCode | `zcode`, or the desktop application (see [below](#zcode-setup)) |
| Codewhale | `codewhale` |
| Kimi Code | `kimi` |
| MiMo Code | `mimo` |
| Qwen Code | `qwen` |
| Oh My Pi | `omp` |
| Grok Build | `grok` |
| Kiro | `kiro-cli-chat` |
| Amp | `amp` |
| Cline | `cline` |

All nineteen are first-class: each one supports the core workflow — chat, streamed tool calls, permission prompts, and session resume. The Goals & To-dos sidebar appears for an agent that has a to-do list, and for an agent whose CLI has a session goal. The available models, settings, and prompt styles vary from provider to provider (each CLI exposes its own); the rest of this chapter covers those per-provider details.

### Providers installed on a Worker

A provider only appears in the picker if its CLI is installed on the selected Worker. When you choose a Worker, LeapMux probes its shell for each provider's binary (`command -v <binary>`) and shows only the providers it finds. ZCode is the exception: it ships no command, so LeapMux looks for its desktop installation instead (see [ZCode setup](#zcode-setup)).

While that probe is still loading, LeapMux shows a default list of all nineteen providers, sorted alphabetically by label; once the probe completes, the list narrows to the providers actually installed on the Worker.

If no provider is detected, the picker shows a disabled **No agents available** button. Install the relevant CLI on the Worker and use the **Refresh available providers** button to re-probe.

### ZCode setup

ZCode ships no command of its own, so LeapMux looks for it in three steps and takes the first that answers:

1. The `LEAPMUX_ZCODE_SCRIPT` environment variable, which points straight at a `zcode.cjs`. Pair it with `LEAPMUX_ZCODE_NODE` to name the interpreter as well.
2. A `zcode` command on the Worker's `PATH`. Your own wrapper script wins over the installed application.
3. The `zcode.cjs` inside the ZCode desktop installation — under `ZCode.app` on macOS, `Programs\ZCode` or `Program Files\ZCode` on Windows, and `~/.local/share/ZCode`, `/opt/ZCode`, `/usr/share/zcode` or `/usr/lib/zcode` on Linux. LeapMux runs it with an interpreter that provides `node:sqlite`: a `node` on `PATH`, or the installation's own bundled runtime.

The `node:sqlite` requirement is not cosmetic — ZCode keeps its session store in it. An interpreter without that module is rejected during the probe rather than failing on the first message.

ZCode also reads its credentials and its model list from the desktop application's own configuration at `~/.zcode/v2/config.json`. Sign in to ZCode once and LeapMux picks the same providers and models up; LeapMux only reads that file and never writes it. Without it, the provider reports that ZCode is not configured instead of starting an agent that fails on every turn.

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

To continue a previous conversation, pick it from the **Resume an existing session** field in the New agent dialog. The field lists the sessions the Worker finds for the selected directory and provider, newest first, each labelled with its title and how long ago it ran. A filter box narrows the list, and the refresh button beside the label asks the Worker again.

The list comes from two places at once: LeapMux's own record of the agents it ran, and the agent CLI's own session history on that machine. So a session you started by running Claude Code or Codex directly in a terminal appears here too. Where both know a session, LeapMux's record wins.

Two kinds of session are left out. A session already open in a tab isn't offered, because two processes against one session store corrupt it — close the tab first. And a session belonging to another directory isn't offered, because the list follows the **Directory** field: change the directory or the provider and the list changes with it. Changing either also clears a session you had already picked, since a session ID means nothing in another directory.

Leave the field on **Start a new session** to begin fresh. It is a real entry in the menu, so it is also how you take back a session you picked.

The last entry, **Enter a session ID…**, swaps the menu for a text box. Use it for a session the list cannot hold — one from another machine, one a tab still holds open, or one older than the newest fifty. The field also falls back to that box on its own when the Worker finds no sessions at all. Three things cause that: a directory with no history, a provider whose store this machine doesn't have, and a Worker that can't answer. The box checks what you type and reports a session ID it cannot use.

Once you submit, the Worker resumes the prior session using that provider's own resume mechanism, picking up where the earlier conversation left off. If a session can't be resumed, the agent doesn't start and the tab reports why. Send `/clear` in the chat to start a fresh session instead.

### Automatic resume

Picking a session is the manual path; most resumption happens automatically. Agent sessions are durable: they resume across Hub restarts, Worker restarts, and client reconnects without you doing anything. When an agent's process has to be respawned — for example after a Worker restarts or after a model/effort change — LeapMux reconnects it to the prior session using that provider's own resume mechanism, and the transcript continues where it left off. As with manual resume, an agent whose resume fails doesn't start, so an empty session never replaces the conversation.

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

ZCode accepts an image only on a model that declares image input — of the models Z.ai ships today that is GLM-5.3-Flash. Attaching one to a text-only model is refused with a message naming the model, because ZCode would otherwise accept the image and never show it to the model.

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

## The chat view

### Tool calls and results

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

What a tool result can carry differs by provider (see the [feature matrix](#feature-matrix)). A ZCode tool result arrives as text, and LeapMux restores the picture from ZCode's stored attachment records. Codewhale and Cline build tool results as text only. Amp renders the image of a file that `Read` opened; an Amp MCP result carries text only.

### Turn boundaries and notifications

The end of each turn is marked by a divider that may carry a label such as a duration ("Took 2.1s") or an error ("API Error: 529 …"). LeapMux also surfaces notifications for events like rate limits, context compaction, retries, and settings changes, collapsing repeated or no-op notifications so they don't flood the transcript.

### The Goals & To-dos sidebar

This section holds two things an agent works toward: its session goal, and its to-do list.

The **session goal** is a standing objective. The agent re-tests it at the end of every turn and keeps working while the condition does not hold. The card shows the objective, its status, and the counters that the agent reports. Set a goal from the card, and change, pause, resume, or clear it from the card's menu. What you can do depends on the provider. See the [feature matrix](#feature-matrix).

Oh My Pi shows its goal, but you start and change the goal from Oh My Pi itself. Claude Code, Goose, Kilo, Qwen Code, and Grok Build receive a goal change as a message. Kiro receives a new goal as a message. That message enters the agent's input queue and uses a turn.

The **to-do list** shows each item's status (pending, in progress, completed). The agent's own to-do or plan tool feeds it. The list comes from the Worker, so it stays correct across reconnects.

A chip on the thinking indicator shows the to-do count and opens the same section as a popover.

### Subagents and the Background tasks sidebar

When an agent spawns a subagent (for example, Claude Code's Task tool) or runs a background shell, LeapMux tracks it in a **Background tasks** sidebar section. Each row shows the task's live status and, for subagents that own a transcript, is clickable to open the subagent in its own tab alongside its parent. A small chip on the thinking indicator shows the active count and opens the same list as a popover.

A dynamic workflow runs many subagents for one job, for example a Claude Code workflow, a Kimi Code agent swarm, a Kiro workflow, or a Cline agent team. Its subagents appear together under the workflow's row.

Closing a subagent tab closes only the tab. The transcript and registry survive, and you can reopen the tab from the section later. Only providers whose CLIs expose subagent activity appear here; the registry lives in the worker's local database and never reaches the hub.


## Permission and approval prompts

When an agent needs your approval — to run a command, edit a file, or proceed with a plan — or wants to ask you a question, LeapMux shows a **control request** banner directly above the editor. The turn waits on the banner: the agent does nothing with the request until you answer it.

### The banner

Every banner has the same shape:

- a title that states the request,
- the request body: what the agent wants to do, often as collapsible JSON,
- one button per answer that the provider offers,
- the editor, whose placeholder hints at what to type: **"Type a custom answer..."** for a question, **"Type a rejection reason..."** for anything else.

The buttons and their names come from the provider's own protocol. Most providers ask about one call at a time. Some also offer a scope that lasts the rest of the session, or a rule that the CLI remembers. Each button states what it covers, so check the scope before you allow a call. A denial can carry feedback: text typed with the deny reaches the agent as the reason.

When the provider has a permission shortcut (see [Changing settings mid-session](#changing-settings-mid-session)), the banner also shows a **Permissions** choice: **Unchanged**, **Smart**, and **Bypass**. Only the shortcuts that the provider has appear. When you allow the request, LeapMux also switches the session to the shortcut that you chose. The choice starts on the shortcut that the session already uses. A plan-approval banner starts on **Smart** when the provider has it.

### Questions

A question shows its options as radio buttons (single-select) or checkboxes (multi-select), and you can type a custom answer instead. A prompt that carries several questions shows **Question N of M**, and one submission answers all of them. An MCP server that asks the user for input gets a form on the providers that carry one (see the [feature matrix](#feature-matrix)).

### Plan approvals

When an agent finishes planning, the banner shows the plan. **Approve** starts the work, and **Reject** keeps plan mode with your feedback. Some providers add a **Clear Context** switch, which starts the work in a fresh context. Some providers end plan mode themselves instead of asking (see the [feature matrix](#feature-matrix)).

### Several prompts at once

If several prompts queue up, you answer them one at a time. LeapMux de-duplicates requests and remembers answered ones, so a reconnect never re-asks something you already handled.

The exact buttons and their names come from the provider's own protocol.

## Changing settings mid-session

### The status bar and the [+] menu

Beneath the editor box is a status bar with one chip per setting axis — the git branch, and the agent's current model, reasoning effort, and mode. Click a chip to change that axis.

The **[+]** menu holds every axis, including provider-specific options that have no chip. Each axis has a submenu. The menu also holds **Agent info** for context usage, rate limits, and the session.

LeapMux exposes every axis that the agent's CLI exposes: model, effort, mode, permissions, and provider-specific options. The values on each axis come from the CLI's own configuration. In the UI you pick them as named radio options. For `leapmux control agent set`, `--permission-mode` takes the permission-mode axis and `--option` takes another axis.

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

Pi has plan mode through a plan extension, not through a setting, so **Shift+Tab** does not toggle it. Type the extension's `/plan` command to start planning. When the plan is ready, LeapMux shows a plan-approval banner. **Approve** implements the plan, and **Reject** stays in plan mode. Turn on **Clear Context** to implement the plan in a fresh session.

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

Every provider runs the core workflow: chat, streamed tool calls, permission prompts, and session resume. Beyond that, support differs. Each cell is ✅ when LeapMux supports the feature for that provider, and ❌ when it does not. A small number after a symbol marks a note below the table.

| Feature | Claude Code | Codex | Cursor | GitHub Copilot | Kilo | OpenCode | Goose | Pi¹ | Reasonix | ZCode | Codewhale | Kimi Code | MiMo Code | Qwen Code | Oh My Pi | Grok Build | Kiro | Amp | Cline |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Text attachments | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Image attachments | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅² | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| PDF attachments | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ | ❌ | ❌ |
| Other binary attachments | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ |
| Images in tool results | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅² | ❌³ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅⁴ | ❌³ |
| Thinking in the transcript | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Context usage | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Compaction notice | ✅ | ✅ | ❌⁵ | ✅ | ❌⁵ | ❌⁵ | ❌⁵ | ✅ | ❌⁵ | ❌⁵ | ❌⁵ | ✅ | ❌⁵ | ❌⁵ | ✅ | ❌⁵ | ❌⁵ | ❌⁵ | ✅ |
| Rate-limit state | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌⁶ | ❌ | ❌ |
| Session resume | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅²⁰ |
| Permission prompts | ✅ | ✅¹⁸ | ✅ | ✅¹⁸ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅¹⁸ | ✅¹⁸ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅¹⁹ | ✅¹⁸ | ✅ |
| Plan mode | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌⁷ | ✅ | ✅ | ❌⁴ | ✅ |
| Plan approval banner | ✅ | ✅ | ✅ | ✅ | ❌⁸ | ❌⁸ | ❌ | ✅ | ❌⁸ | ✅ | ❌⁸ | ✅ | ✅ | ✅ | ❌⁷ | ✅ | ❌⁸ | ❌⁴ | ✅⁸ |
| Agent questions | ✅ | ✅ | ✅ | ✅⁹ | ✅ | ✅ | ❌¹⁰ | ✅ | ❌¹⁰ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌⁴ | ✅⁹ |
| MCP input form | ✅ | ✅ | ✅ | ✅ | ❌¹¹ | ❌¹¹ | ✅ | ✅ | ✅ | ❌¹¹ | ❌¹¹ | ❌¹¹ | ✅ | ❌¹¹ | ❌¹¹ | ✅ | ✅ | ❌⁴ | ❌¹¹ |
| Smart permissions shortcut | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ✅ | ❌ | ✅ | ❌ | ❌ | ❌ |
| Bypass permissions shortcut | ✅ | ✅ | ❌¹² | ✅ | ❌¹² | ❌¹² | ✅ | ❌¹² | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Model chip | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌¹³ | ✅ |
| Reasoning-effort chip | ✅¹⁴ | ✅ | ❌¹³ | ✅¹⁴ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅¹⁴ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅¹⁴ | ❌¹³ | ✅¹⁴ |
| Mode chip | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅¹³ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅¹³ | ✅ |
| Session goal: set and clear | ✅¹⁵ | ✅ | ❌ | ✅ | ✅¹⁵ | ❌ | ✅¹⁵ | ✅ | ✅¹⁵ | ✅ | ✅ | ✅¹⁵ | ✅ | ✅¹⁵ | ❌⁷ | ✅¹⁵ | ✅¹⁵ | ❌⁴ | ❌ |
| Session goal: pause and resume | ❌ | ✅ | ❌ | ✅ | ✅¹⁵ | ❌ | ❌ | ✅ | ❌ | ✅ | ❌ | ✅¹⁵ | ❌ | ✅¹⁵ | ❌⁷ | ✅¹⁵ | ✅¹⁵ | ❌⁴ | ❌ |
| To-do sidebar | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌⁴ | ❌ |
| Background tasks sidebar | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅²¹ |
| Subagent transcript tab | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌⁴ | ✅ |
| Send to a subagent | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Interrupt a subagent | ❌ | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ❌ | ❌¹⁶ | ❌ | ❌¹⁶ | ❌ | ❌ | ❌ |
| Steer mid-turn | ✅ | ✅ | ❌¹⁷ | ✅ | ✅ | ✅ | ✅¹⁷ | ✅ | ✅¹⁷ | ✅ | ✅ | ✅ | ✅ | ✅¹⁷ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Workflow grouping in Background tasks | ✅ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | ❌⁴ | ✅ |

**Notes**

1. Pi extensions provide these features:
   - Plan mode and plan approval: [pi-plan-mode](https://pi.dev/packages/@narumitw/pi-plan-mode)
   - Session goal: [pi-goal-x](https://pi.dev/packages/pi-goal-x)
   - To-do list: [rpiv-todo](https://pi.dev/packages/@juicesharp/rpiv-todo)
   - Agent questions: [rpiv-ask-user-question](https://pi.dev/packages/@juicesharp/rpiv-ask-user-question)
   - MCP prompts: [pi-mcp-adapter](https://pi.dev/packages/pi-mcp-adapter)
   - Subagent tabs: [pi-subagents](https://pi.dev/packages/@tintinweb/pi-subagents)
2. ZCode takes an image attachment only on a model that declares image input (see [Attachments](#attachments)). A tool result arrives as text, and LeapMux restores the picture from ZCode's stored attachment records.
3. Codewhale and Cline build tool results as text only, so no tool result can carry a picture.
4. Amp has no plan mode, to-do tool, session goal or MCP input request. Its question tool (`ask_user_choice`) cannot be answered in stream-JSON mode: the session ends, so LeapMux keeps the tool off. The stream also carries no child messages and no workflow activity. An Amp MCP result is text only; a `Read` of an image file does render.
5. The provider's protocol reports no compaction size. The Agent Client Protocol family reports only that compaction runs; ZCode and Amp report none; MiMo Code reports a summary without a size; Codewhale compacts as a turn of its own.
6. Kiro shows a rate-limit notice row ("The model service is busy"), not rate-limit state.
7. Oh My Pi has plan mode and a session goal, but LeapMux cannot reach them. The `rpc-ui` mode that LeapMux drives has no mode command, and `/plan` and `/goal` are TUI-only. Tracked upstream: [oh-my-pi#8171](https://github.com/can1357/oh-my-pi/issues/8171) and [oh-my-pi#9230](https://github.com/can1357/oh-my-pi/issues/9230).
8. For Kilo, OpenCode, Reasonix and Codewhale, the plan arrives with no approval request. Switch the mode back to start the work. Kiro switches back to Default mode and starts the work itself. Codewhale's plan mode refuses edits and commands. Cline discusses the plan in the chat first: say it is good and the banner appears.
9. GitHub Copilot and Cline carry one question per request.
10. Goose and Reasonix raise no question request.
11. Kimi Code's MCP clients negotiate no elicitation, so an MCP server cannot ask for input. OpenCode and Kilo declare no elicitation capability (tracked upstream: [opencode#23066](https://github.com/anomalyco/opencode/issues/23066)). Qwen Code does not implement it (upstream work on the `feat/mcp-elicitation-support` branch). Codewhale, ZCode, Oh My Pi and Cline carry no MCP input request that LeapMux renders.
12. Cursor offers agent, plan and ask modes only. OpenCode and Kilo have no permission-mode axis, and Pi has no permission controls.
13. Amp's Mode picks the model and the effort, so Amp shows no Model or Effort chip. A thread keeps Amp's mode of its first message; start a new session to use another mode. Cursor's effort rides its model ids, so Cursor shows no Effort chip. ZCode's own `auto` mode is not offered: the shipped build denies every tool call under it.
14. Claude Code (Haiku), GitHub Copilot, ZCode, Kiro and Cline show the Effort chip only when the chosen model offers levels.
15. Claude Code, Goose, Kilo, Qwen Code and Grok Build take a goal only when their CLI advertises its `goal` command. Kimi Code takes one only while the engine's goal feature runs. Reasonix sets a goal only in Goal mode; otherwise it can only clear one. Kiro needs a live goal run for clear, pause and resume, and works on a goal for at most five rounds before it pauses it; resume the goal to continue. Qwen Code's `/loop` command and its scheduled prompts are off in LeapMux.
16. The worker can stop these subagents, but the subagent tab shows no Interrupt button.
17. Cursor's protocol surface exposes no steer method. Goose and Reasonix steer only when the CLI advertises a steer method. Qwen Code takes a steer at its next tool gap.
18. A prompt's buttons, scopes and timeouts come from the provider. Codex's **Allow as** offers **Once**, **Session**, **Command rule**, or **Host rule**. ZCode's "always" option writes a permission rule for the project. Codewhale denies an approval that nobody answers within 300 seconds. Text that you type before Amp's **Deny** reaches the agent as the reason. When a Copilot CLI refuses LeapMux's safe default, LeapMux reopens the session in Manual.
19. Kiro's prompts follow its own rules. Its scope applies to **Deny** as well: **Always** with **Deny** refuses the same call in every workspace until you remove the rule in Kiro's settings. Kiro runs the working directory's hooks (`.kiro/hooks/`) without asking. In Supervised Autopilot, a turn ends with a review of its file changes: **Allow** keeps them, **Deny** restores each file. Its **Content Collection** setting decides whether Kiro may use your session content to improve its service.
20. Cline uses your own Cline settings, credentials, and stored sessions. Do not continue one session in your own Cline and in LeapMux at the same time: each one rewrites the stored conversation.
21. A Cline agent can run your scheduled Cline automations, and its subagents run their tools without a prompt, so approve a spawn only for a task you trust. Cline hooks and plugins run only in **Auto-approve**, and Cline runs them without asking.
