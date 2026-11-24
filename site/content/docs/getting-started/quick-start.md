---
title: "Quick Start"
description: "Go from zero to your first parallel coding-agent session in under five minutes: launch LeapMux in solo mode, open two agents in isolated git worktrees, and inspect their changes alongside a terminal."
type: docs
weight: 3
---

Run your first parallel coding-agent session in under five minutes. You will launch LeapMux in **solo mode**, open two agents in isolated git worktrees on the same repository, and verify their output in an integrated terminal.

## Before you begin

Install at least one supported coding-agent command-line interface (CLI) on your machine (for example `claude` or `codex`) and confirm that your shell can resolve it from `PATH`, an alias, or a shell function. See [Coding Agents](/docs/using/coding-agents/) for the full list of supported providers.

## Launch LeapMux in solo mode

Solo mode runs a Hub and a Worker together in one local process. Pick the launch method that matches your install:

{{< tabs >}}
{{< tab name="Desktop App" selected=true >}}
1. Install and open **LeapMux Desktop** (see [Installation](/docs/getting-started/installation/#desktop-app) for download steps and macOS Full Disk Access setup).
2. In the **LeapMux** launcher, choose **Solo** and click **Connect**.

The desktop app talks to its local service over inter-process communication (IPC) with no login required, and remembers your choice on the next launch. To switch to a remote Hub later, open the app menu and choose **Switch mode...**.
{{< /tab >}}

{{< tab name="CLI / Browser" >}}
If you installed the standalone `leapmux` binary, start solo mode as a background service:

```bash
leapmux daemon solo start
```

The first `start` asks whether LeapMux should start automatically at login or boot. Solo mode listens on loopback at **`http://localhost:4327`**. Open that URL in your browser and set the password for the single `solo` account; subsequent browser visits require that password.

Useful flags (full reference in [CLI Reference](/docs/reference/cli-reference/)):

| Flag | Purpose | Default |
| --- | --- | --- |
| `--listen` | Listen address, repeatable: TCP or local IPC (`unix:`, `npipe:`) | `127.0.0.1:4327` plus local IPC |
| `--data-dir` | Data directory | `.` (resolves to `~/.config/leapmux/solo`) |
| `--log-level` | `debug`, `info`, `warn`, `error` | `info` |
| `--config` | Path to the config file | `~/.config/leapmux/solo/solo.yaml` |

{{< callout type="warning" >}}
If you override `--listen` to a non-loopback address before setup, the first TCP caller can claim the `solo` account by setting its password. Set the password from a trusted connection and restrict network access with a firewall or tunnel. See [Security & Threat Model](/docs/admin/security/).
{{< /callout >}}
{{< /tab >}}
{{< /tabs >}}

## Your first parallel agent session

{{% steps %}}

### 1. Orient to the workspace

When LeapMux opens, the window has four areas:

- **Titlebar** — app menu on the left; **Open in …** (solo desktop mode only) and sidebar toggles on the right.
- **Left sidebar** — your workspaces under **In progress** and **Archived**, plus the **Workers** section. Expanding a workspace groups its open tabs by repository and branch.
- **Center tiling area** — your agent, terminal, and file tabs arranged in splits, grids, or floating windows.
- **Right sidebar** — the git-aware **Files** browser and inline diff viewer for the active tab.

{{< screenshot src="hero-workspace" alt="LeapMux workspace showing the left sidebar, tiled coding agents and terminal in the center, and the file browser on the right" caption="The LeapMux workspace: sidebar on the left, tiled agents and terminal in the center, and git-aware file browser on the right." width="1440" height="860" >}}

### 2. Open two agents in isolated git worktrees

1. Hover over **In progress** in the left sidebar and click **+** to open the **New workspace** dialog.
2. Confirm the **Worker** (your local Worker is preselected), pick an **Agent Provider**, and choose a git repository as the **Directory**.
3. Under **Git options**, select **Create new worktree** and give it a branch name (for example `feat/refactor-parser`).
4. Click **Create**. LeapMux creates the workspace, creates the linked git worktree, and starts the first agent inside it.
5. Open a second agent on the same repository: click **+** on the tab bar, choose **New agent...**, pick the same repository directory, select **Create new worktree** with a second branch name (for example `feat/add-tests`), and click **Create**.
6. Drag the second tab to the right edge of the center pane (or use the tile split menu) so both agents sit side by side.

Because each agent runs in its own linked worktree and branch, their edits never collide. See [Worktrees & Branches](/docs/using/worktrees-and-branches/) for all git modes.

### 3. Send prompts, tune settings, and run a terminal check

1. Type a focused prompt into each agent's composer and press **Send** — for example, ask the first agent to refactor a module and the second agent to add unit tests for another area.
2. Adjust settings mid-session from the status-bar chips below the composer (branch, **Model**, **Effort**, **Permission Mode**) or the composer's **[+]** menu. New agents start on the provider's defaults.
3. Answer permission prompts or questions inline in the control banner above the composer. Click **Interrupt** if you need to stop a turn early.
4. Watch modified files appear in the right-hand **Files** sidebar and click any file to view its inline diff.
5. Open a shell beside your agents: click **+** on the tab bar, choose **New terminal...**, select the agent's worktree directory, and click **Create**. Split it below your agents to run tests or inspect `git status`.

{{% /steps %}}

{{< callout >}}
Your workspace, layout, and agent sessions persist across restarts. Close the browser tab or quit the desktop app, relaunch, and everything returns. Terminals restore their last screen; after a Worker restart, press **Enter** in a terminal to start its shell again.
{{< /callout >}}

## Where to go next

- [Ways to Run LeapMux](/docs/getting-started/ways-to-run/) — decide when to move from Solo to remote Workers or a distributed Hub.
- [Concepts & Architecture](/docs/getting-started/concepts/) — understand the Hub, Worker, Frontend, and end-to-end encryption model.
- [Run Parallel Coding Agents](/docs/recipes/parallel-agents/) — scale up to three agents with the `/leapmux-delegate` skill.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — worktree lifecycle, pushing, and dirty-worktree protection on close.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — navigate splits, tabs, and workspaces without leaving the keyboard.
