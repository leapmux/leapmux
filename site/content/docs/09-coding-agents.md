---
title: "Coding Agents"
type: docs
weight: 9
---

Coding agents are the heart of LeapMux. Each agent is a real coding-assistant CLI (Claude Code, Codex, and others) running on a worker, wrapped in a chat tab so you can talk to it, watch its tool calls, approve its actions, and steer it without leaving the browser. This chapter covers which agents are supported, how to open one, how to chat with it, how tool calls render, how to answer permission prompts, and how to change models and settings mid-session.

For where agents live in the workspace layout, see [Tabs & Layout](/docs/08-tabs-and-layout/). For the git side of opening an agent in a branch or worktree, see [Worktrees & Branches](/docs/10-worktrees-and-branches/). To drive agents from a script instead of the browser, see [Remote Control CLI](/docs/16-remote-control-cli/).

## Supported agents

LeapMux integrates nine coding-agent providers:

| Provider | CLI binary detected on the worker |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex` (or `codex-x86_64-pc-windows-msvc`) |
| Gemini CLI | `gemini` |
| Cursor | `cursor-agent` |
| GitHub Copilot | `copilot` |
| Kilo | `kilo` |
| OpenCode | `opencode` |
| Goose | `goose` |
| Pi | `pi` |

### Integration depth

All nine providers are fully functional. They differ only in how much bespoke, hand-tuned rendering and settings UI each one has:

- **Rich, hand-written integrations:** Claude Code, Codex, and Pi. These have custom message classifiers, tool-call renderers, control-prompt dialogs, and per-provider settings panels.
- **Shared protocol integrations:** Gemini CLI, Cursor, GitHub Copilot, and Goose share LeapMux's Agent Client Protocol (ACP) layer; OpenCode and Kilo share an OpenCode-protocol layer that itself rides on ACP. These providers chat, render tool calls, and prompt for permissions through the shared layer rather than bespoke code, so their chat surface is a little plainer — but they are real, working integrations, not placeholders.

**The three integration tiers feeding the common chat UI:**

```text
   Bespoke tier             ACP shared layer
 (hand-written)        (shared render / permissions)
┌────────────────┐     ┌───────────────────────────┐
│  Claude Code   │     │  Gemini CLI     Cursor    │
│  Codex         │     │  GitHub Copilot Goose     │
│  Pi            │     └─────────────┬─────────────┘
└───────┬────────┘                    ▲
        │                             │ rides on
        │              ┌─────────────┴─────────────┐
        │              │  OpenCode-protocol layer  │
        │              │  OpenCode       Kilo      │
        │              └─────────────┬─────────────┘
        │                            │
        └─────────────┬──────────────┘
                      ▼
           ┌──────────────────────┐
           │    Common chat UI    │
           │  (transcript, tool   │
           │   rows, prompts)     │
           └──────────────────────┘
```

> **Note:** What you will notice in practice between a "rich" and a "shared" integration is the polish of the chat view (custom diff toggles, plan dialogs, richer settings panels) — not whether the agent works. Every provider can send messages, run tools, ask for permission, and resume.

### Which agents you can actually open

A provider only appears in the picker if its CLI is installed on the selected worker. When you choose a worker, LeapMux probes its shell for each provider's binary (`command -v <binary>`) and shows only the providers it finds.

While that probe is still loading, LeapMux shows a default list of all nine providers, sorted alphabetically by label; once the probe completes, the list narrows to the providers actually installed on the worker.

If no provider is detected, the picker shows a disabled **No agents available** button. Install the relevant CLI on the worker and use the **Refresh available providers** button to re-probe.

## Opening a new agent

Open the **New agent** dialog from the workspace, then fill in the fields below and click **Create** (the button shows **Creating...** while the agent spins up).

### Dialog fields

| Field | What it does |
| --- | --- |
| **Worker** | The machine that will run the agent. Determines which providers are available and where the working directory lives. See [Managing Workers](/docs/19-managing-workers/). |
| **Agent Provider** | Which agent CLI to launch. Shows the provider icon, label, and a chevron; a check marks the current choice. |
| **Directory** | The working directory for the agent, chosen from a directory tree on the worker. |
| **Resume an existing session** | Optional. Paste a prior Session ID to continue an earlier conversation (see [Resuming a session](#resuming-an-existing-session)). |
| **Git options** | Appears once a worker is selected. Lets you start the agent on the current branch, switch branches, create a branch, or create/use a worktree. See [Worktrees & Branches](/docs/10-worktrees-and-branches/). |

> **Note:** The dialog has **no model, effort, or permission-mode fields**. A new agent always starts with the provider's defaults; you change the model, reasoning effort, and permission mode afterward from the in-chat settings dropdown (see [Changing settings mid-session](#changing-settings-mid-session)).

LeapMux remembers your most recently used provider and pre-selects it (when it is available on the chosen worker), so you usually only have to pick a directory and click **Create**.

### Quick-open (no dialog)

If you trigger "new agent" from a tab that already has a worker and working directory, LeapMux skips the dialog and opens an agent directly, reusing the active tab's provider (or your most-recent provider). It only falls back to the full dialog when the worker, directory, or provider can't be inferred.

### Where the new agent lands

The worker assigns a friendly title from a shared name pool (you'll see titles like "Agent <Name>"); you can rename the tab later. For how tabs are placed, split, and tiled, see [Tabs & Layout](/docs/08-tabs-and-layout/).

## Chatting with an agent

The chat tab has the conversation transcript above and a Markdown editor at the bottom.

### Composing and sending

The editor is a full Markdown editor with a formatting toolbar. Type your message and send it with the **Send** button (the paper-plane icon) or with the keyboard. While the message is in flight, the Send icon is replaced by a spinner.

Send is disabled when the editor is empty and there are no attachments.

#### Enter-key send mode

A toggle in the editor toolbar controls what the **Enter** key does. The two modes are:

| Mode | Enter | Modifier+Enter |
| --- | --- | --- |
| **Enter sends** | Sends the message | (Shift+Enter for a new line) |
| **Cmd/Ctrl+Enter sends** (default) | Inserts a new line | Cmd+Enter (macOS) / Ctrl+Enter (other platforms) sends |

The default is **Cmd/Ctrl+Enter sends**, so plain Enter adds a newline. Click the mode label in the toolbar to switch; the choice is saved as a [preference](/docs/14-settings-and-preferences/) and persists across sessions.

### Attachments

You can attach files by clicking the upload (paperclip) button, or by pasting or dropping them into the editor. Pending attachments appear in a strip above the editor. What you can attach depends on the provider:

| Provider | Text | Image | PDF | Other binary |
| --- | --- | --- | --- | --- |
| Claude Code | yes | yes | yes | no |
| Codex | yes | yes | no | no |
| Pi | yes | yes | no | no |
| Gemini CLI, Cursor, GitHub Copilot, Goose, OpenCode, Kilo | yes | yes | yes | yes |

### Message persistence and offline behavior

Your messages appear immediately (optimistically) and are reconciled when the server echoes them back. If you send while the agent subprocess is still starting, the message is queued and delivered once the agent is ready. Optimistic messages survive a page refresh; if delivery fails, you can retry or delete the message.

### Interrupting a turn

While the agent is actively working — and there is no pending permission prompt — an **Interrupt** button (a square icon) appears. Click it to stop the current turn; it shows **Interrupting...** while the stop is in flight. LeapMux sends the provider's native interrupt signal under the hood, so the agent stops cleanly rather than being killed.

> **Note:** The **Interrupt** button is hidden whenever the agent is waiting on you with a permission or question prompt — answer the prompt instead (see [Permission and approval prompts](#permission-and-approval-prompts)).

## How tool calls and results render

As an agent works, the transcript shows its assistant text, its thinking (where the provider exposes it), and a row for every tool call it makes, followed by that tool's result. The exact set of tools depends on the provider, but you will commonly see:

- **File reads** — the file the agent opened.
- **Edits and writes** — rendered with a diff. The result toolbar offers a **split / unified** diff toggle.
- **Bash / command execution** — the command and its output.
- **Search / grep / glob** — the query and matches.
- **Web fetch / web search** — the URL or query and what came back.
- **Todo / plan updates** — feed a persistent todo sidebar (see below).
- **MCP tool calls** — calls into Model Context Protocol servers the agent has access to, rendered like any other tool call.

Long tool results are collapsible (an **Expand** button), and most rows have a **Copy** button. Where it makes sense, a row also offers a **Quote** button (tooltip "Quote", pulls the row's text into the editor as a quoted reply) and a **Copy Markdown** button (tooltip "Copy Markdown"), and the control banner has a **Copy Raw JSON** button for debugging.

> **Tip:** Some rows are intentionally hidden to keep the transcript readable — for example, Claude Code suppresses its internal todo-list and tool-search bookkeeping rows. The information still drives the UI (the todo sidebar), it just isn't repeated inline.

### The todo / plan sidebar

When an agent produces a task plan or todo list, LeapMux shows it in a persistent sidebar with each item's status (pending, in progress, completed). Codex turn plans, Claude Code's todo and task tracking, and ACP plan updates all feed this sidebar. The list is server-authoritative, so it stays correct across reconnects.

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

**Plan review** — when Claude Code finishes planning, the banner is titled **Plan Ready for Review** and lists requested permissions grouped by tool. Buttons are **Reject** / **Send Feedback** and **Approve**. The Approve action includes checkboxes to clear context or switch out of bypass permission mode.

**Questions** — when the agent asks you something, the banner is titled **Agent Question**. Single questions show options as radio buttons (single-select, auto-advancing) or checkboxes (multi-select); multi-question prompts show **Question N of M** with pagination dots. You can also type a custom answer. Footer buttons:

- **Stop** — abandon the question (sends a "User stopped" denial).
- **YOLO** — auto-fill every unanswered question with "Go with the recommended option." and submit (tooltip: "Auto-fill unanswered questions and submit").
- **Submit** — disabled until every question is answered.

### Codex

Codex approval banners are titled by the kind of request: **Command Execution**, **File Change**, **Permission Request**, or **Approval Required**, and show the reason, command (collapsible), and working directory. The buttons come from the request itself; common labels are:

| Decision | Button |
| --- | --- |
| accept | **Allow** |
| acceptForSession | **Allow for Session** |
| decline | **Reject** |
| cancel | **Cancel** |
| acceptWithExecpolicyAmendment | **Allow & Remember** |
| applyNetworkPolicyAmendment | **Apply Network Policy** |

An **& Bypass Permissions** option is also available (it switches Codex to Full Auto). Codex's plan-mode prompt is titled **Implement the proposed plan?** with **Stay in Plan Mode** / **Send Feedback** and **Implement Plan**.

### Pi

Pi shows method-specific dialogs: **confirm** (Deny / Approve), **input** (an inline text field; Cancel / Send), **editor** (an inline textarea; Cancel / Send), and **select** (uses the shared question UI). Some Pi prompts show a timeout hint ("Auto-resolves in Ns if no response.").

### Shared-protocol providers (Gemini, Cursor, Copilot, Goose, OpenCode, Kilo)

These render a permission banner whose title comes from the tool call (default **Permission Request**) and whose buttons come from the options the agent offered. An **& Bypass Permissions** option appears when the provider declares a bypass mode. Cursor, OpenCode, and Kilo plug in their own richer question handling where they support it.

## Changing settings mid-session

The editor footer (when no prompt is active) has a settings dropdown showing the agent's current model, an effort icon, and mode. Open it to change the agent's model, reasoning effort, permission mode, and provider-specific options.

> **Note:** Changing the **model** or **effort** restarts the agent process (the change is optimistic and rolls back if it fails). Changing Claude Code's **permission mode** is live — no restart. For Codex and the shared-protocol providers, a permission-mode change restarts the agent.

The model picker shows radio buttons for up to 7 models and switches to a searchable list above that.

### Reasoning effort and the "Auto" default

Every effort-capable provider defaults effort to **Auto** — "let the CLI pick." When effort is Auto, LeapMux omits the effort flag entirely, so older CLI versions that don't recognize newer effort names still work. You only need to set effort explicitly if you want to force a particular tier.

### Plan mode shortcut

For providers that support a plan mode, **Shift+Tab** in the editor toggles between plan mode and the previous mode. (Goose has no plan mode and doesn't wire this.)

### Per-provider settings

**Claude Code** — Extended Thinking, Effort, Model (left); Fast Mode, Output Style, Permission Mode (right).

- Default model **Opus (1M context)** (`opus[1m]`); also offered: Opus, Sonnet, Sonnet (1M context), Haiku.
- Effort tiers depend on the model:
  - **Opus** and **Opus (1M context)** offer the full set: Auto, Ultracode, Max, X-High, High, Medium, Low.
  - **Sonnet** and **Sonnet (1M context)** offer Auto, Max, High, Medium, Low (no Ultracode or X-High).
  - **Haiku** has no effort tiers at all — the effort selector is hidden entirely when Haiku is the model, and the worker never sends an effort flag for Haiku.
- Permission modes: **Default** (the default), **Plan Mode**, **Accept Edits**, **Bypass Permissions**, **Don't Ask**, **Auto Mode**.

**Codex** — Fast Mode, Reasoning Effort, Model (left); Workflow, Network Access, Sandbox, Approval Policy, plus a **Bypass permissions** button (right).

- Default model **GPT-5.4** (`gpt-5.4`); also offered: gpt-5.4-mini, gpt-5.3-codex, gpt-5.2-codex, gpt-5.2, gpt-5.1-codex-max, gpt-5.1-codex-mini.
- Effort tiers: Auto, Extra High, High, Medium, Low, Minimal, None.
- Approval Policy: **Full Auto** (`never`), **Suggest & Approve** (`on-request`, the default), **Auto-edit** (`untrusted`).
- Sandbox defaults to **Workspace Write** (also Full Access / Read Only); Network defaults to **Restricted** (also Enabled).
- The **Bypass permissions** button sets network = enabled, sandbox = full access, and approval = Full Auto in one click.

**Pi** — a single column with **Thinking Level** (effort) and **Model**. Default model **gpt-5.5**. Pi has no permission mode, no plan mode, and no bypass.

**Shared-protocol providers** — a single option group plus a model selector. The trigger label reads `<model> · <option>`.

| Provider | Default model | Default mode | Notes |
| --- | --- | --- | --- |
| Cursor | `auto` | `agent` | Has plan mode. |
| Gemini CLI | `auto` | `default` | Bypass mode is `yolo`; has plan mode. |
| GitHub Copilot | (CLI default) | `agent` | Modes are ACP session-mode URIs; has plan and autopilot. |
| Goose | (CLI default) | `auto` | Bypass = `auto`; **no plan mode**. |
| OpenCode | (CLI default) | Primary Agent `build` | Has plan mode. |
| Kilo | (CLI default) | Primary Agent `code` | Has plan mode. |

## Resuming an existing session

To continue a previous conversation, paste its Session ID into the **Resume an existing session** field (placeholder **"Session ID"**) in the New agent dialog. LeapMux validates the ID as you type:

- Input that contains control characters or any of the characters `"`, `\`, `$`, and `%` produces **"Session ID contains invalid characters."**
- Input longer than 128 characters produces **"Name must be at most 128 characters"**.

Leave the field empty to start a fresh session.

Once you submit, the worker resumes the prior session using each provider's own resume mechanism — they all pick up where the earlier conversation left off, but they don't all use the same command-line flag:

| Provider | How it resumes |
| --- | --- |
| Claude Code | Passes the Session ID as the CLI's `--resume` argument. |
| Codex | Sends the `thread/resume` JSON-RPC method with the ID as `threadId`. |
| Gemini CLI, Cursor, GitHub Copilot, Goose | Send the ACP `session/load` JSON-RPC method. |
| OpenCode, Kilo | Send the OpenCode-protocol `session/resume` JSON-RPC method. |
| Pi | Sends a `switch_session` command with the ID as the session path. |

> **Note:** For the ACP and OpenCode-protocol providers, if resume fails the worker automatically falls back to starting a fresh session rather than erroring out.

> **Tip:** Session IDs for Claude Code, Codex, and the other CLIs come from those tools' own session bookkeeping. If you've run the same CLI directly in a terminal, you can resume that session inside LeapMux by pasting its ID here.

## Per-provider differences worth knowing

- **Defaults vary by provider.** Claude Code starts in **Default** permission mode (it will ask before risky actions); Codex starts in **Suggest & Approve**. Both ask before doing dangerous things unless you bypass.
- **Bypass is a deliberate, sticky choice.** The "& Bypass Permissions" / "Bypass permissions" actions stop the agent asking for approval for the rest of the session (Codex's button also opens the sandbox and network). Use them only when you trust the working directory and the task.
- **Attachment support differs** (see the [attachments table](#attachments)) — only Claude Code accepts PDFs among the rich providers, while all shared-protocol providers accept PDFs and arbitrary binaries.
- **Pi is minimal** — model and thinking level only, no permission/plan/bypass controls.
- **Strict provider dispatch.** LeapMux never tries to render or encode one provider's messages with another provider's code. If a provider plugin is missing it surfaces a clear warning rather than guessing.

## Driving agents from a script

Everything in this chapter has a programmatic counterpart in the `leapmux remote` CLI, which agents themselves can call (the worker injects credentials into each spawned agent's environment). The most relevant commands:

```bash
# Send a message to an agent tab
leapmux remote agent send --tab-id <id> --message "Refactor the auth module"

# Interrupt the current turn
leapmux remote agent interrupt --tab-id <id> --reason "wrong file"

# Change model / effort / permission mode mid-session
leapmux remote agent set --tab-id <id> --model gpt-5.4 --effort high

# Open a new agent in a tab (provider, model, working dir, worktree, etc.)
leapmux remote tab open --type agent --worker-id <id> --provider "Claude Code" \
  --working-dir /repo --initial-message "Start on the bug fix"

# Answer a Claude-Code-style control request
leapmux remote agent send-control-response --tab-id <id> --content '<raw JSON>'
```

See [Remote Control CLI](/docs/16-remote-control-cli/) for the full command tree, entity-ID resolution, and the JSON output contract.
