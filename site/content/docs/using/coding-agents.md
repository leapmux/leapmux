---
title: "Coding Agents"
description: "Run coding-assistant CLIs like Claude Code and Codex in LeapMux: open an agent, chat, read tool calls, answer permission prompts, and switch models mid-session."
type: docs
weight: 3
---

Coding agents are the core feature of LeapMux. Each agent is a real coding-assistant CLI (Claude Code, Codex, and others) running on a Worker, wrapped in a chat tab so you can talk to it, watch its tool calls, and approve its actions. This chapter covers which agents are supported and how to open, chat with, and configure one.

For where agents live in the workspace layout, see [Tabs & Layout](/docs/using/tabs-and-layout/). For the git side of opening an agent in a branch or worktree, see [Worktrees & Branches](/docs/using/worktrees-and-branches/). To drive agents from a script instead of the browser, see [Control CLI](/docs/using/control-cli/).

## Supported agents

LeapMux integrates ten coding-agent providers:

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
| ZCode | `zcode`, or the desktop application (see [below](#zcode-is-found-through-its-desktop-application)) |

All ten are first-class: each one supports the core workflow — chat, streamed tool calls, permission prompts, and session resume. The plan/todo sidebar only appears for agents whose CLI emits task or todo updates. The available models, settings, and prompt styles vary from provider to provider (each CLI exposes its own); the rest of this chapter covers those per-provider details.

### Which agents you can actually open

A provider only appears in the picker if its CLI is installed on the selected Worker. When you choose a Worker, LeapMux probes its shell for each provider's binary (`command -v <binary>`) and shows only the providers it finds. ZCode is the exception: it ships no command, so LeapMux looks for its desktop installation instead (see [ZCode is found through its desktop application](#zcode-is-found-through-its-desktop-application)).

While that probe is still loading, LeapMux shows a default list of all ten providers, sorted alphabetically by label; once the probe completes, the list narrows to the providers actually installed on the Worker.

If no provider is detected, the picker shows a disabled **No agents available** button. Install the relevant CLI on the Worker and use the **Refresh available providers** button to re-probe.

#### ZCode is found through its desktop application

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
The dialog has **no model, effort, or permission-mode fields**. A new agent always starts with the provider's defaults; you change the model, reasoning effort, and permission mode afterward from the composer's status-bar chips or its **[+]** menu (see [Changing settings mid-session](#changing-settings-mid-session)).
{{< /callout >}}

LeapMux remembers your most recently used provider and pre-selects it (when it is available on the chosen Worker), so you usually only have to pick a directory and click **Create**.

### Quick-open (no dialog)

If you trigger "new agent" from a tab that already has a Worker and working directory, LeapMux skips the dialog and opens an agent directly, reusing the active tab's provider (or your most-recent provider). It only falls back to the full dialog when the Worker, directory, or provider can't be inferred.

### Where the new agent lands

The Worker assigns a friendly title from a shared name pool (you'll see titles like "Agent <Name>"); you can rename the tab later. For how tabs are placed, split, and tiled, see [Tabs & Layout](/docs/using/tabs-and-layout/).

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

You can attach files with **[+] > Attach file...**, or by pasting or dropping them into the editor. Pending attachments appear in a strip above the editor. What you can attach depends on the provider:

| Provider | Text | Image | PDF | Other binary |
| --- | --- | --- | --- | --- |
| Claude Code | yes | yes | yes | no |
| Codex | yes | yes | no | no |
| Pi | yes | yes | no | no |
| ZCode | yes | model-dependent | no | no |
| Reasonix | yes | no | no | no |
| Cursor, GitHub Copilot, Goose, OpenCode, Kilo | yes | yes | yes | yes |

ZCode accepts an image only on a model that declares image input — of the models Z.ai ships today that is GLM-5.3-Flash. Attaching one to a text-only model is refused with a message naming the model, because ZCode would otherwise accept the image and never show it to the model.

### Message persistence and offline behavior

Your messages appear immediately (optimistically) and are reconciled when the server echoes them back. If you send while the agent subprocess is still starting, the message is queued and delivered once the agent is ready. Optimistic messages survive a page refresh; if delivery fails, you can retry or delete the message.

### Interrupting a turn

While the agent is actively working — and there is no pending permission prompt — an **Interrupt** button (a square icon) appears. Click it to stop the current turn. LeapMux asks the agent to stop via its native interrupt mechanism rather than killing the process.

{{< callout type="info" >}}
The **Interrupt** button is hidden whenever the agent is waiting on you with a permission or question prompt — answer the prompt instead (see [Permission and approval prompts](#permission-and-approval-prompts)).
{{< /callout >}}

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

- **SVG** is never rendered. An SVG can carry script, and the transcript does not sandbox it.
- **Images above about 5 MB** show a placeholder instead of the picture.
- **An image the agent gives by URL** shows an **open ↗** link instead of the picture. Rendering it would fetch from that host, which the transcript never does on its own.

Which providers can return an image at all depends on the provider — Claude Code, Codex, Pi, OpenCode, Kilo, and Goose all can.

### The todo / plan sidebar

When an agent produces a task plan or todo list, LeapMux shows it in a persistent sidebar with each item's status (pending, in progress, completed). Codex turn plans, Claude Code's and ZCode's todo tracking, Claude Code's task tools, and other providers' plan updates all feed this sidebar. The list is server-authoritative, so it stays correct across reconnects.

### Subagents and the Background tasks sidebar

When an agent spawns a subagent (Claude Code's Task tool, ZCode's Agent tool) or runs a background shell, LeapMux tracks it in a **Background tasks** sidebar section. Each row shows the task's live status and, for subagents that own a transcript, is clickable to open the subagent in its own tab alongside its parent. A small chip on the thinking indicator shows the active count and opens the same list as a popover.

Closing a subagent tab closes only the tab. The transcript and registry survive, and you can reopen the tab from the section later. Only providers whose CLIs expose subagent activity appear here; the registry lives in the worker's local database and never reaches the hub.


### Turn boundaries and notifications

The end of each turn is marked by a divider that may carry a label such as a duration ("Took 2.1s") or an error ("API Error: 529 …"). LeapMux also surfaces notifications for events like rate limits, context compaction, retries, and settings changes, collapsing repeated or no-op notifications so they don't flood the transcript.

## Permission and approval prompts

When an agent needs your approval — to run a command, edit a file, or proceed with a plan — or wants to ask you a question, LeapMux shows a **control request** banner directly above the editor. The banner has its own action buttons, and the editor placeholder changes to hint at what to type:

- For a question: **"Type a custom answer..."**
- For any other request: **"Type a rejection reason..."**

If several prompts queue up, you answer them one at a time. LeapMux de-duplicates requests and remembers answered ones, so a reconnect never re-asks something you already handled.

The exact buttons depend on the provider.

### Claude Code

**Tool permission** — title **Permission Required: \<toolName\>**, with the tool input shown as collapsible JSON. Buttons:

- **Reject** — becomes **Send Feedback** if you've typed a reason.
- **Allow** — approve this one request.
- **& Bypass Permissions** — allow this request *and* switch the agent into its bypass mode (tooltip: "Allow this request and stop asking for permissions").

**Plan review** — when Claude Code finishes planning, the banner is titled **Plan Ready for Review** and lists requested permissions grouped by tool. Buttons are **Reject** / **Send Feedback** and **Approve**. The Approve action includes checkboxes to clear context or switch the agent into its bypass mode.

**Questions** — when the agent asks you something, the banner is titled **Agent Question**. Single questions show options as radio buttons (single-select, auto-advancing) or checkboxes (multi-select); multi-question prompts show **Question N of M** with pagination dots. You can also type a custom answer. Footer buttons:

- **Stop** — abandon the question (sends a "User stopped" denial).
- **YOLO** — auto-fill every unanswered question with "Go with the recommended option." and submit (tooltip: "Auto-fill unanswered questions and submit").
- **Submit** — disabled until every question is answered.

### Codex

Codex approval banners are titled by the kind of request: **Command Execution**, **File Change**, **Permission Request**, or **Approval Required**, and show the reason, command (collapsible), and working directory. The buttons come from the request itself; depending on the request you may see:

- **Allow** — approve this one request.
- **Allow for Session** — approve and stop asking for the same kind of request for the rest of the session.
- **Reject** — deny the request.
- **Cancel** — dismiss the request without approving it.
- **Allow & Remember** — approve and remember the amended execution policy for similar commands.
- **Apply Network Policy** — approve and apply the proposed network-access amendment.

An **& Bypass Permissions** option is also available (it switches Codex to Full Auto). Codex's plan-mode prompt is titled **Implement the proposed plan?** with **Stay in Plan Mode** / **Send Feedback** and **Implement Plan**.

### Pi

Pi shows method-specific dialogs: **confirm** (Deny / Approve), **input** (an inline text field; Cancel / Send), **editor** (an inline textarea; Cancel / Send), and **select** (uses the shared question UI). Some Pi prompts show a timeout hint ("Auto-resolves in Ns if no response.").

### ZCode

ZCode's tool prompts carry the options the app-server offered, typically **Allow once**, **Always allow in this project** and **Deny**, together with the risk level and the reason it asked. Choosing an "always" option sends back ZCode's own permission rule, so the same command stops asking for the rest of the project.

Its plan prompt is titled from the **ExitPlanMode** tool and shows the plan the agent wrote, with the shared plan-approval buttons. Questions use the shared question UI.

### Other providers

Cursor, GitHub Copilot, Goose, OpenCode, Kilo, and Reasonix render a permission banner whose title comes from the tool call (default **Permission Request**) and whose buttons come from the options the agent offered. An **& Bypass Permissions** option appears only for Goose and GitHub Copilot, which declare a bypass mode; Cursor, OpenCode, Kilo, and Reasonix declare none. Cursor, OpenCode, and Kilo plug in their own richer question handling where they support it.

## Changing settings mid-session

Beneath the editor box is a status bar with one chip per setting axis — the git branch, and the agent's current model, reasoning effort, and mode. Click a chip to change that axis.

The **[+]** menu holds every axis, including the provider-specific options that get no chip, each as a submenu. It also holds **Agent info** (context usage, rate limits, session). You can hide the status bar with **[+] > Show status bar**; the **[+]** menu still reaches everything the bar shows.

{{< callout type="info" >}}
Most settings changes apply **live**, without a restart: a concrete model or effort change and a permission-mode change take effect in place (the change is optimistic and rolls back if it fails). A restart happens when the provider can't apply the change to the running process — typically switching effort back to **Auto** or the Claude Code model back to **Default (recommended)**, which must relaunch the CLI without the flag — and for providers that fix the model at launch (Reasonix).
{{< /callout >}}

A picker shows radio items for up to 7 options and switches to a searchable list above that.

### Reasoning effort and the "Auto" default

For the providers whose effort LeapMux manages — Claude Code, Codex, and Pi — effort defaults to **Auto**, meaning "let the CLI pick." When effort is Auto, LeapMux omits the effort flag entirely, so older CLI versions that don't recognize newer effort names still work. You only need to set effort explicitly if you want to force a particular tier.

### Plan mode shortcut

For providers that support a plan mode, **Shift+Tab** in the editor toggles between plan mode and the previous mode. (Goose has no plan mode.)

### Per-provider settings

**Claude Code** — Extended Thinking, Effort, Model, Fast Mode, Output Style, Permission Mode.

- Default model **Default (recommended)** (the CLI's own pick); also offered: Fable 5, Opus (1M context), Sonnet, Sonnet (1M context), Haiku.
- Effort tiers depend on the model:
  - **Fable 5**, **Opus (1M context)**, **Sonnet**, and **Sonnet (1M context)** offer the full set: Auto, Ultracode, Max, Extra High, High, Medium, Low.
  - **Haiku** has no effort tiers at all — the effort selector is hidden entirely when Haiku is the model, and the Worker never sends an effort flag for Haiku.
- Permission modes: **Default** (the default), **Plan Mode**, **Accept Edits**, **Bypass Permissions**, **Don't Ask**, **Auto Mode**.

**Codex** — Fast Mode, Effort, Model, Workflow, Network Access, Sandbox Policy, Approval Policy, plus a **Bypass permissions** item.

- Default model **Default (recommended)** (your Codex account's own pick); also offered: GPT-5.6-Sol, GPT-5.6-Terra, GPT-5.6-Luna, GPT-5.5, GPT-5.4, GPT-5.4-Mini, GPT-5.3-Codex-Spark. A running agent lists whatever models your Codex account offers, so an account-specific model appears here too.
- Effort tiers depend on the model:
  - **GPT-5.6-Sol** and **GPT-5.6-Terra** offer the full set: Auto, Ultra, Max, Extra High, High, Medium, Low.
  - **GPT-5.6-Luna** offers Auto, Max, Extra High, High, Medium, Low.
  - The other models offer Auto, Extra High, High, Medium, Low.
- Approval Policy: **Full Auto** (`never`), **Suggest & Approve** (`on-request`, the default), **Auto-edit** (`untrusted`).
- Sandbox defaults to **Workspace Write** (also Full Access / Read Only); Network defaults to **Restricted** (also Enabled).
- The **Bypass permissions** item sets network = enabled, sandbox = full access, and approval = Full Auto in one click.

**Pi** — **Thinking Level** (effort) and **Model**. Default model **glm-5.3**. Pi has no permission mode, no plan mode, and no bypass.

**ZCode** — **Thought Level** (effort), **Model**, and **Mode**.

- The models come from your own `~/.zcode/v2/config.json`, so the list is whatever that installation is signed in to. Each entry names its ZCode provider, which is what tells two rows apart when a plan and an API key both reach the same model. LeapMux orders them the way ZCode does and starts on the first — for the Z.ai plans that is **GLM-5.3**.
- Thought levels are per model and also come from that configuration: **Low / High / Max** on GLM-5.3 and GLM-5.3-Flash, **Enabled / Off** on GLM-5-Turbo. **Auto** means the model's own default rather than no level at all.
- Modes: **Plan**, **Build** (the default), **Edit**, **Yolo**. Shift+Tab toggles Plan, and Yolo is the bypass mode. ZCode's own `auto` mode is not offered: the shipped build denies every tool call under it.

**Other providers** — a single option group plus a model selector. Each axis gets its own chip, and each chip shows the current value.

| Provider | Default model | Default mode | Notes |
| --- | --- | --- | --- |
| Cursor | `auto` | `agent` | Has plan mode. |
| GitHub Copilot | (CLI default) | `agent` | Has plan and autopilot. |
| Goose | (CLI default) | `auto` | Bypass = `auto` — Goose's default mode already is its bypass mode; **no plan mode**. |
| OpenCode | (CLI default) | Primary Agent `build` | Has plan mode. |
| Kilo | (CLI default) | Primary Agent `code` | Has plan mode. |

In the UI you pick these as named radio options (**Auto**, **Agent**, **Autopilot**, **Build**, **Code**, and so on); the literal mode IDs above are only typed directly when driving an agent with `leapmux control agent set --permission-mode`.

**Reasonix** — a **Model** selector only; it has no permission mode, no plan mode, and no bypass. Default model **DeepSeek Flash** (`deepseek-flash`); also offered: DeepSeek Pro, MiMo Pro, MiMo Flash (the MiMo models need `MIMO_API_KEY`). Reasonix fixes its model at launch, so switching the model restarts the agent. It is text-only — image, PDF, and binary attachments aren't supported — and still shows per-request approval banners.

## Resuming an existing session

To continue a previous conversation, pick it from the **Resume an existing session** field in the New agent dialog. The field lists the sessions the Worker finds for the selected directory and provider, newest first, each labelled with its title and how long ago it ran. A filter box narrows the list, and the refresh button beside the label asks the Worker again.

The list comes from two places at once: LeapMux's own record of the agents it ran, and the agent CLI's own session history on that machine. So a session you started by running Claude Code or Codex directly in a terminal appears here too. Where both know a session, LeapMux's record wins.

Two kinds of session are left out. A session already open in a tab isn't offered, because two processes against one session store corrupt it — close the tab first. And a session belonging to another directory isn't offered, because the list follows the **Directory** field: change the directory or the provider and the list changes with it. Changing either also clears a session you had already picked, since a session ID means nothing in another directory.

Leave the field on **Start a new session** to begin fresh. It is a real entry in the menu, so it is also how you take back a session you picked.

The last entry, **Enter a session ID…**, swaps the menu for a text box. Use it for a session the list cannot hold — one from another machine, one a tab still holds open, or one older than the newest fifty. The field also falls back to that box on its own when the Worker finds no sessions at all. Three things cause that: a directory with no history, a provider whose store this machine doesn't have, and a Worker that can't answer. The box checks what you type and reports a session ID it cannot use.

Once you submit, the Worker resumes the prior session using that provider's own resume mechanism, picking up where the earlier conversation left off. If a session can't be resumed, the agent doesn't start and the tab reports why. Send `/clear` in the chat to start a fresh session instead.

### Resume across restarts and reconnects

Picking a session is the manual path; most resumption happens automatically. Agent sessions are durable: they resume across Hub restarts, Worker restarts, and client reconnects without you doing anything. When an agent's process has to be respawned — for example after a Worker restarts or after a model/effort change — LeapMux reconnects it to the prior session using that provider's own resume mechanism, and the transcript continues where it left off. As with manual resume, an agent whose resume fails doesn't start, so an empty session never replaces the conversation.

## Per-provider differences worth knowing

- **Defaults vary by provider.** Claude Code starts in **Default** permission mode (it will ask before risky actions); Codex starts in **Suggest & Approve**. Both ask before doing dangerous things unless you bypass.
- **Bypass is a deliberate, sticky choice.** The "& Bypass Permissions" / "Bypass permissions" actions stop the agent asking for approval for the rest of the session (Codex's button also opens the sandbox and network). Use them only when you trust the working directory and the task.
- **Attachment support differs by provider** (see the [attachments table](#attachments)) — every provider takes text, but image, PDF and other-binary support varies. Reasonix takes text only, and ZCode takes an image only on a model that declares image input.
- **Pi is minimal** — model and thinking level only, no permission/plan/bypass controls.
- **ZCode borrows the desktop application's account.** Its models, credentials and thought levels all come from `~/.zcode/v2/config.json`, so what an agent can run matches what the ZCode application itself can run on that machine.
- **Strict provider dispatch.** LeapMux never tries to render or encode one provider's messages with another provider's code. If a provider plugin is missing it surfaces a clear warning rather than guessing.

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
