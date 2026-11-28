---
title: "Quick Start"
description: "Go from zero to your first parallel coding-agent session in under five minutes: launch LeapMux in solo mode, open two agents in isolated git worktrees, and inspect their diffs alongside a terminal."
type: docs
weight: 3
---

Run your first parallel coding-agent session in under five minutes. You will launch LeapMux in **solo mode**, open two agents in isolated git worktrees on the same repository, and verify their work in an integrated terminal.

## Before you begin

Install at least one supported coding-agent command-line interface (CLI) on your machine (for example `claude` or `codex`) and confirm that your shell resolves it on `PATH`, an alias, or a shell function. Check [Supported Agents](/docs/reference/supported-agents/) for the full provider roster.

## Run your first parallel session

{{% steps %}}

### Launch LeapMux in solo mode

Solo mode runs a Hub and a Worker together in one local process. Pick the launch method that matches your installation:

{{< tabs >}}
{{< tab name="Desktop App" selected=true >}}
1. Open **LeapMux Desktop** (see [Installation](/docs/getting-started/installation/#desktop-app) for download steps and macOS Full Disk Access setup).
2. In the **LeapMux** launcher, choose **Solo** and click **Connect**.

The desktop app connects to its local service over inter-process communication (IPC) with no login required, and remembers your choice next time. To connect to a remote Hub later, open the app menu and choose **Switch mode...**.
{{< /tab >}}

{{< tab name="CLI / Browser" >}}
If you installed the standalone `leapmux` binary, start solo mode as a background service:

```bash
leapmux up
```

`leapmux up` is a shortcut for `leapmux daemon solo start`. On the first run, LeapMux asks whether to start automatically at login or boot. Open **`http://127.0.0.1:4327`** in your browser and set a password for the single `solo` account; subsequent browser visits require that password.

For custom listen addresses, data directories, and daemon options, see [Running LeapMux](/docs/admin/running-leapmux/) and [Security & Auth](/docs/admin/security-and-auth/).
{{< /tab >}}
{{< /tabs >}}

### Create a workspace and start your first agent

The LeapMux window has four areas: the **titlebar** at the top, the **left sidebar** (workspaces under **In progress** and **Archived**, plus **Workers**), the **center tiling area** for tabs, and the **right sidebar** for the git-aware **Files** browser and diff viewer.

1. Open the **`⋯` actions menu** on **In progress** in the left sidebar and choose **New workspace...**.
2. Keep the pre-filled random workspace title or type your own.
3. Confirm the **Worker** (your local Worker is preselected), pick an **Agent Provider**, and choose a git repository as the **Working Directory**.
4. Under **Git options**, select **Create new worktree** and enter a branch name (for example `feat/refactor-parser`).
5. Click **Create**. LeapMux creates the workspace, creates the linked git worktree, and automatically opens its first agent inside it.

{{< screenshot src="workspace-section-menu" alt="LeapMux In progress section header actions menu showing New workspace, New workspace in recent repositories, Filter workspaces, Sort by, and New section" caption="Click the ⋯ menu on In progress to create a new workspace or jump straight into a recent repository." width="1006" height="588" >}}

{{< screenshot src="new-workspace-dialog" alt="LeapMux New workspace dialog showing Worker, Agent Provider, Title, Working Directory tree, Resume session, and Git options" caption="Configure your Worker, provider, directory, and linked git worktree in the New workspace dialog." width="996" height="792" >}}

### Open a second agent and a shell terminal side by side

1. Click **`+`** on the tab bar to open the tab menu, choose **New agent...**, pick the same repository directory, select **Create new worktree** with a second branch name (for example `feat/add-tests`), and click **Create**.
2. Drag the second tab to the right edge of the center pane (or use the tile split menu) so both agents sit side by side.
3. Click **`+`** on the tab bar again, choose **New terminal...**, select either agent's worktree directory, and click **Create**. Split the terminal below your agents to run tests or check `git status`.

{{< screenshot src="tab-bar-more-menu" alt="LeapMux tab bar More options menu showing agent provider icons, New agent, New terminal, detected shells with default marker, and Advanced toggles" caption="Use the tab bar + menu to launch another coding agent or shell terminal alongside your active tabs." width="480" height="712" >}}

Because each agent runs in its own linked worktree and branch, their edits never collide. See [Worktrees & Branches](/docs/using/worktrees-and-branches/) for every git mode.

### Send prompts, tune settings, and inspect diffs

1. Type a focused prompt into each agent's composer and press **Send** — for example, ask one agent to refactor a module and the other to add unit tests.
2. Adjust settings mid-session from the status-bar chips below the composer or from the composer's **`[+]`** menu. Available chips depend on the provider — such as branch, model, reasoning effort, and provider-specific modes like **Permission Mode** in Claude Code.
3. Answer permission prompts or clarifying questions inline in the control banner above the composer. Click **Interrupt** to stop a turn early.
4. Watch changed files appear in the right-hand **Files** sidebar and click any file to inspect its inline diff.

{{< video-clip src="parallel-worktrees" poster="hero-workspace" alt="Animated walkthrough of switching between parallel coding agents in isolated git worktrees, toggling Split diff view, and opening the Quake terminal" caption="Switch between isolated worktree branches, toggle split diffs, and toggle the Quake terminal across your workspace." width="1280" height="800" >}}

{{% /steps %}}

{{< callout type="info" >}}
**Persistence and sync in Solo Desktop:** Closing the **LeapMux Desktop** window minimizes it to the system tray so live agents and terminals keep running. Quitting the desktop app completely stops the embedded sidecar's live processes; when you relaunch, your workspace layout, agent transcripts, and last terminal screens return (press **Enter** in a terminal to start a fresh shell). For background persistence across app quits or logouts, run `leapmux up` (`leapmux daemon solo start`) or Distributed mode. For live cross-device layout sync, enable **Network Access** in Solo mode or connect your devices to a Distributed Hub.
{{< /callout >}}

## Where to go next

- [Concepts & Topologies](/docs/getting-started/concepts/) — decide when to move from Solo Desktop to Solo Daemon or a Distributed Hub with remote Workers.
- [Run Parallel Coding Agents](/docs/recipes/parallel-agents/) — fan out across three agents with the `/leapmux-delegate` skill.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — learn branch checkout modes, pushing, and dirty-worktree protection on close.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — navigate splits, tabs, and workspaces without leaving the keyboard.
- [CLI Guide](/docs/using/cli-guide/) — script agents, terminals, and worktrees from your shell.
