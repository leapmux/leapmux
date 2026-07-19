---
title: "Quick Start"
description: "Go from zero to a working coding agent in minutes: launch LeapMux in solo mode, open an agent, send your first message, and open a terminal beside it."
type: docs
weight: 4
---

This chapter gets you from zero to a working coding agent in a few minutes. Pick one of two launch paths, then follow the walkthrough to open your first agent, send a message, and open a terminal beside it.

> **Note:** Both paths run LeapMux in **solo mode** — a Hub and a Worker running together in a single local process, with no login and your data kept on your machine. This is the fastest way to try LeapMux. For multi-user or remote setups, see [Running LeapMux](/docs/operating/running-leapmux/) and [Managing Workers](/docs/operating/managing-workers/).

## Before you begin

LeapMux launches agents by running the provider's own command-line tool on the Worker. A provider only appears in the picker if its CLI is already installed and on your `PATH`. For this quick start, install at least one supported agent CLI — for example **Claude Code** (the `claude` binary). The full list of supported providers and their binaries is in [Coding Agents](/docs/using/coding-agents/).

## Path A — Desktop app

This is the simplest path on macOS, Linux, or Windows.

1. Install **LeapMux Desktop** for your platform (see [Installation](/docs/getting-started/installation/) for download details).
2. Launch the app. On first launch it shows a launcher titled **"LeapMux"** with the subtitle **"Choose how you'd like to connect"** and two cards:
   - **Solo** — "Run LeapMux entirely on this machine. A Hub and Worker start together in a single process — no network setup required. Your data stays local. Ideal for personal use, local development, or trying out LeapMux."
   - **Distributed** — connect to a remote Hub by URL (covered in [Running LeapMux](/docs/operating/running-leapmux/)).
3. Select **Solo** and click **Connect**.

> **Note (macOS):** Solo mode needs **Full Disk Access** so the Worker can traverse your home directory. If it isn't granted, the launcher shows a **"Full Disk Access Required"** card with an **"Open System Settings"** button. Grant access there; the app detects it and restarts automatically.

The app remembers your choice and reconnects to Solo on the next launch. To return to the launcher later, open the user menu and choose **"Switch mode..."**.

## Path B — CLI solo

If you installed the `leapmux` binary (see [Installation](/docs/getting-started/installation/)), start solo mode from a terminal:

```bash
leapmux solo
```

This runs a Hub and a Worker in one process, listening on loopback only at **`127.0.0.1:4327`**. Open the printed URL in your browser:

```
http://127.0.0.1:4327
```

No login is required — solo mode auto-authenticates every request as the admin.

A few useful flags (full reference in [Configuration](/docs/operating/configuration/) and [CLI Reference](/docs/reference/cli-reference/)):

| Flag | Purpose | Default |
| --- | --- | --- |
| `-listen` | TCP listen address | `127.0.0.1:4327` |
| `-data-dir` | Data directory | `.` (under `~/.config/leapmux/solo`) |
| `-log-level` | `debug`, `info`, `warn`, `error` | `info` |
| `-config` | Path to the config file | `~/.config/leapmux/solo/solo.yaml` |

> **Warning:** Solo mode trusts every request as the admin. If you change `-listen` to a non-loopback address, anyone who can reach that port has full admin access without credentials. For how to expose solo mode safely, see [Security & Threat Model](/docs/operating/security/).

> **Tip:** `leapmux solo` writes its database and keys under `~/.config/leapmux/solo`, so your workspaces and sessions persist across restarts.

## The walkthrough

Once the app or browser tab is open, you'll see the LeapMux UI. The rest of this chapter walks through your first agent.

### 1. Orient to the UI

The window has a **titlebar** across the top, a **sidebar** down each side, and the **tiling area** in the middle:

- **Titlebar** — the **app menu** (account and app controls) is on the left; **"Open in…"** (open the working directory in an external editor) and the **left/right sidebar toggles** are on the right.
- **Left sidebar** — your **workspaces**, grouped into **In progress**, any custom sections, and **Archived**, plus a **Workers** section. Each workspace expands into a tree of its open tabs, grouped by **Repo** then **Branch**.
- **Tiling area** — the center, where tabs (agents, terminals, file viewers) are laid out under a **tab bar**; tiles can be split, gridded, and resized (see [Tabs & Layout](/docs/using/tabs-and-layout/)). For an agent, the **input area** (message composer) sits at the bottom.
- **Right sidebar** — the **Files** browser for the active tab (see [File Browser](/docs/using/file-browser/)).

**The main areas of the window:**

```text
┌─────────────────────────────────────────────────────────────────┐
│ App menu                   Titlebar         Open in… · sidebars │
├──────────────┬───────────────────────────────┬──────────────────┤
│              │            Tab bar            │                  │
│              ├───────────────────────────────┤                  │
│ Left sidebar │                               │ Right sidebar    │
│ (workspaces) │          Tiling area          │ (Files)          │
│              │                               │                  │
│              ├───────────────────────────────┤                  │
│              │           Input area          │                  │
└──────────────┴───────────────────────────────┴──────────────────┘
```

> **Note:** The **"Open in…"** button is only available in the desktop app running in solo mode — it launches an editor on your local machine, which only applies when the files are local. Everything else is identical in the browser: the web app renders the same titlebar, sidebars, and tiling area inside the browser tab, just with the browser's own chrome around the page.

### 2. Create or open a workspace

A **workspace** is the top-level container that holds your tiling layout. To create one, click the **"+"** button in the **In progress** section header. This opens the **"New workspace"** dialog:

- **Title** — defaults to a random three-word name; click **"Generate random name"** to regenerate, or type your own.
- **Worker** — which machine hosts the agent. In solo mode there is one local Worker, already selected.
- **Agent Provider** — the agent backend (for example **Claude Code**).
- **Directory** — the working directory on the Worker.
- **Resume an existing session** *(optional)* — a prior session ID to resume.
- **Git options** — appears once a Worker is chosen (see step 5).

Click **"Create"**. LeapMux creates the workspace, spawns its first agent, and navigates to it. To work in an existing workspace instead, click its row in the sidebar. Full workspace management — rename, move, archive, delete — is covered in [Workspaces](/docs/using/workspaces/).

### 3. Open a New Agent

If you already have a workspace open and want another agent in it, open the tab bar's add menu and choose **"New agent..."** (or double-click an empty area of the tab bar). This opens the **"New agent"** dialog:

- **Worker** — the host machine (the local Worker in solo mode).
- **Agent Provider** — choose your provider; the dropdown lists only providers whose CLI was detected, sorted alphabetically by name. If none are found, the button reads **"No agents available"** — install an agent CLI and use the provider section's refresh control (**"Refresh available providers"**).
- **Directory** — the working directory (use the directory tree to pick it).
- **Resume an existing session** *(optional)* — paste a session ID (placeholder **"Session ID"**) to continue a prior conversation via the CLI's resume support.
- **Git options** — your branch/worktree choice (step 5).

Click **"Create"** (it shows **"Creating..."** while the agent starts).

> **Note:** The New agent dialog does **not** include model, effort, or permission-mode fields. A new agent starts with the provider's defaults — for Claude Code that's the **Opus (1M context)** model with effort **Auto** and the **Default** permission mode. You change all of these mid-session from the in-chat settings dropdown. See [Coding Agents](/docs/using/coding-agents/) for the full settings reference.

### 4. (Optional) Choose model and effort after the agent starts

Once the agent is running, the editor footer shows a settings trigger displaying the current model and effort. Open it to pick a different **Model** (for Claude Code: Opus, Opus (1M context), Sonnet, Sonnet (1M context), Haiku), an **Effort** tier, a **Permission Mode**, and other per-provider options. Changing the model or effort restarts the agent process; for Claude Code, permission-mode changes apply live. Details and the per-provider option matrix are in [Coding Agents](/docs/using/coding-agents/).

### 5. (Optional) Work in a branch or worktree

The **Git options** panel (header **"Git options"**) lets you decide where the agent runs relative to git. For your first session, two modes cover the common cases:

- **Use current state** — run against the working directory as-is. This is the default.
- **Create new worktree** — spin up a fresh linked git worktree on its own branch so this agent works in isolation.

Worktrees keep each agent's changes on their own branch and directory, so several agents can work on the same repo at once without stepping on each other. The full set of modes, the worktree path layout, and branch operations — switching, pushing (**"Push"** / **"Commit and Push"**), and deletion — are covered in [Worktrees & Branches](/docs/using/worktrees-and-branches/).

### 6. Send your first message

The agent's chat panel has a markdown editor at the bottom. Type a message — for example:

```
Summarize what this repository does and list the main entry points.
```

Send it with the **Send** button (or the editor's send key). While the agent is starting up, messages are queued and delivered once it's ready.

### 7. Watch the turn run

As the agent works, the conversation streams in:

- **Assistant text** and (where supported) **thinking** appear as they arrive.
- **Tool calls** render as rows — for example file reads, edits, and shell commands. Edit/Write results can show a diff with a split/unified toggle and a copy button.
- When the agent needs your decision, a **control request** banner appears above the editor — a permission prompt (for example **"Permission Required: <toolName>"** with **Allow** / **Reject** buttons) or a question. Answer it inline to let the turn continue.
- At the end of a turn, a divider shows the result (for example "Turn ended" with timing).

To stop a running turn, click **"Interrupt"** (it appears while the agent is working and there's no pending control request).

### 8. Open a terminal alongside it

To get a shell next to your agent, open the tab bar's add menu and choose **"New terminal..."**. The **"New terminal"** dialog lets you pick the **Worker**, a **Shell** (the Worker's default shell is marked **(default)**), and the working **Directory**, with the same **Git options** panel. Click **"Create"** to open it.

You can split the tile so the agent and terminal sit side by side, or float the terminal as a separate window. Tiling, splitting, and floating are covered in [Tabs & Layout](/docs/using/tabs-and-layout/); terminal behavior and persistence in [Terminals](/docs/using/terminals/).

> **Tip:** Your workspace, tabs, and agent sessions persist across restarts — close the browser tab or quit the desktop app, relaunch, and they come back. Terminals come back too, showing their last screen; if the Worker was restarted, press **Enter** in a terminal to start its shell again.

## Where to go next

You now have a working agent and a terminal. From here:

- [Concepts & Architecture](/docs/getting-started/concepts/) — how the Hub, Worker, and Frontend fit together, and what solo vs. distributed means.
- [Workspaces](/docs/using/workspaces/) — create, rename, move, archive, and delete workspaces.
- [Tabs & Layout](/docs/using/tabs-and-layout/) — tiling, splits, grids, floating windows, and drag-and-drop.
- [Coding Agents](/docs/using/coding-agents/) — providers, models, effort, control prompts, tool rendering, and resuming sessions.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — branch and worktree workflows in depth.
- [Terminals](/docs/using/terminals/) — shell selection, persistence, and remote terminals.
- [File Browser](/docs/using/file-browser/) — the file tree, git status, and diffs.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — speed up everything above.
- [Running LeapMux](/docs/operating/running-leapmux/) — move beyond solo mode to a shared Hub and remote Workers.
