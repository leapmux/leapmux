---
title: "Workspaces"
description: "Create, organize, archive, delete, and sync workspaces across devices in LeapMux."
type: docs
weight: 1
---

A **workspace** is the top-level container you work inside. It holds a tiling layout of coding agent, terminal, file, and image tabs. Each tab runs on a Worker (machine) in a working directory, usually on its own git branch or worktree. Workspaces persist across restarts and sync live across every browser and desktop app where you sign in.

For how workspaces relate to Hubs, Workers, tiles, and tabs, see [Concepts & Topologies](/docs/getting-started/concepts/). For everything inside a workspace — tabs, splits, grids, and floating windows — see [Tabs & Layout](/docs/using/tabs-and-layout/).

## What a workspace holds

- You view one workspace at a time, while all other non-archived workspaces remain live in the background.
- Workspaces are strictly owner-only: only your account can view, edit, or delete them.
- Agent transcripts, terminal sessions, and file contents stay on the Worker and travel over an end-to-end encrypted (E2EE) channel. The Hub stores only workspace metadata (titles, sections, tab positions, and layout geometry) so it can sync your layout without reading your code or conversations. See [Security & Authentication](/docs/admin/security-and-auth/).

## Creating a workspace

{{< screenshot src="new-workspace-dialog" alt="LeapMux New workspace dialog showing Worker, Agent Provider, Title, Working Directory tree, Resume session, and Git options" caption="The New workspace dialog selects the Worker, coding-agent provider, working directory, optional session to resume, and git mode." width="996" height="792" >}}

Open the **New workspace** dialog in one of three ways:

1. Open the **`⋯`** actions menu on any non-archived workspace section header (such as **In progress**) and choose **New workspace...**.
2. Open that same **`⋯`** menu and pick a repository under **New workspace in** to pre-fill the Worker, path, and last-used git mode for a recent repository.
3. Press `⌥⌘N` / `Ctrl+Alt+N` when no dialog is open (or click **Create a new workspace...** on the empty canvas).

| Field | What it does |
| --- | --- |
| **Worker** | Picks the machine that hosts the workspace's first agent. See [Managing Workers](/docs/admin/managing-workers/). |
| **Agent provider** | Picks the coding-agent CLI (for example, Claude Code or Codex). Use the refresh button to re-query available providers on the Worker. |
| **Title** | Sets the workspace title. Pre-filled with a random three-word title; click **Generate random name** to pick another, or type your own. |
| **Working Directory** | Browses the Worker's filesystem or accepts a typed path. Includes a **Go to home directory** button, a hidden-files toggle, a refresh button, and a drive selector on Windows Workers. |
| **Resume an existing session** | Optional. Reopens a recent conversation for the chosen provider and directory instead of starting fresh. See [Coding Agents](/docs/using/coding-agents/#resuming-sessions). |
| **Git options** | Appears once you select a git repository on a Worker. Lets you stay on the current branch, switch branches, or create a new branch or linked worktree. See [Worktrees & Branches](/docs/using/worktrees-and-branches/). |

Click **Create** to launch the first agent and switch into the new workspace. If the initial agent fails to start, LeapMux rolls back workspace creation automatically so you are not left with an empty workspace.

## Organizing workspaces in the sidebar

{{< screenshot src="sidebar-workspaces" alt="LeapMux left sidebar showing workspaces, git worktree branch groups, Claude Code, Codex, and Antigravity tabs, Archived section, and connected Workers" caption="The left sidebar groups workspaces into collapsible sections and organizes each workspace's tabs by repository and git branch." width="446" height="900" >}}

The left sidebar organizes workspaces into collapsible **sections**. Two default sections always exist:

- **In progress** — expanded by default; holds new workspaces unless you create them in a custom section.
- **Archived** — collapsed by default; holds read-only archived workspaces.

Click any section header to collapse or expand it. Collapse state persists across reloads.

### Section header actions menu (`⋯`)

{{< screenshot src="workspace-section-menu" alt="LeapMux In progress section header actions menu showing New workspace, New workspace in recent repositories, Filter workspaces, Sort by, Collapse all, Expand all, Reveal active workspace, and New section" caption="Click the ⋯ actions button on any workspace section header to create, filter, sort, collapse, or organize workspaces." width="1006" height="588" >}}

Every workspace section header — including **Archived** — has a **`⋯`** actions menu:

| Action | Availability | What it does |
| --- | --- | --- |
| **New workspace...** | Non-archived sections | Opens the **New workspace** dialog targeting this section. |
| **New workspace in** | Non-archived sections with recent repos | Quick-launches **New workspace** pre-filled for a recent repository with its remembered git mode. |
| **Filter workspaces** | All sections | Shows or hides a per-section filter input to narrow visible workspaces by title. |
| **Sort by** | All sections | Sets the sort criterion and direction (ascending or descending) for workspace sections. |
| **Collapse all** / **Expand all** | All non-empty sections | Collapses or expands every workspace tab tree in the section. |
| **Reveal active workspace** | Section holding the active workspace | Expands the section and scrolls the active workspace into view. |
| **Unarchive all** | Non-empty **Archived** section | Moves every archived workspace back to **In progress**. |
| **Empty archive...** | Non-empty **Archived** section | Permanently deletes every archived workspace after confirmation. |
| **New section...** | All sections | Creates a custom section to group related workspaces (for example, by project or sprint). |
| **Rename section...** | Custom sections | Renames a custom section. |
| **Delete section...** | Custom sections | Deletes a custom section after confirmation, moving its workspaces back to **In progress**. |

### Workspace rows and tab trees

Each workspace row displays an expand chevron, the workspace title (falling back to **Untitled**), a repository diff-stats badge summarizing added, deleted, and untracked changes across its tabs, and an accent bar when active.

- **Click** a row to switch to that workspace.
- **Double-click** an active (non-archived) workspace row to rename it inline.
- **Click the chevron** to expand its per-workspace tab tree, grouped as **Repository → Branch → Tabs**. Tabs without git metadata appear ungrouped.
- **Drag** a workspace row to reorder it or move it into another section. Dragging into **Archived** opens the archive confirmation dialog.

Clicking a tab inside an inactive workspace switches to that workspace and activates the tab. Double-click an agent or terminal leaf to rename it inline; middle-click or click **×** to close it (when the workspace is not archived).

## Renaming, moving, archiving, and deleting

{{< screenshot src="workspace-context-menu" alt="LeapMux workspace context menu in the left sidebar showing repository paths and Rename, Archive, and Delete actions" caption="Open a workspace's context menu to rename, move between sections, archive, or delete it." width="1036" height="503" >}}

Open a workspace row's **`⋯`** context menu (or right-click the row) to manage it:

- **Move to** — moves the workspace into **In progress** or any custom section without dragging.
- **Rename** — edits the title inline (`Enter` or click away to save, `Esc` to cancel). Archived workspaces cannot be renamed.
- **Archive** / **Unarchive** — freezes a workspace and moves it to **Archived**, or restores it to **In progress**.
- **Delete** — permanently removes the workspace after confirmation.

### Archiving a workspace

Archiving freezes a workspace as a read-only record without deleting its transcripts or layout:

- **Processes stop cleanly.** Archiving pauses agent input queues, cancels in-flight startups, stops every running agent and terminal process in that workspace, and closes any [Quake terminal](/docs/using/terminals/#quake-mode-terminal) whose working directory has no remaining active tabs in other workspaces.
- **Read-only view.** While archived, you can inspect transcripts and layouts, but you cannot rename the workspace, open or close tabs, change branches, or edit the layout.
- **Unarchiving.** Choose **Unarchive** (or **Unarchive all** from the **Archived** section menu) to return the workspace to **In progress**, re-enable editing, and let its agents resume.

### Deleting a workspace

Confirming **Delete** removes the workspace, stops its agents and terminals, and lets the Worker's housekeeping pass reclaim any linked git worktree that no other workspace uses. Worktrees containing uncommitted changes or unpushed commits are left untouched on disk. If a Worker is offline during deletion, it stops those processes and cleans up unused worktrees the next time it connects.

{{< callout type="warning" >}}
Deletion is permanent and cannot be undone. Use **Archive** when you want to stop running processes while keeping transcripts and layout history intact.
{{< /callout >}}

## Switching workspaces

Click any workspace row in the sidebar to switch immediately. Because every non-archived workspace stays live in the background, tab titles, agent activity, and diff badges are already current when you switch in.

Your browser remembers the last active workspace per account and restores it on reload. If that workspace was deleted from another device, LeapMux falls back to your first non-archived workspace.

## Live sync across devices

LeapMux keeps every open workspace in sync across all browsers and desktop apps signed in to your account. Open the same workspace on a laptop and a phone, or in two browser windows, and structural changes stay aligned automatically with no toggle or manual sync step.

{{< callout type="info" >}}
**Solo Desktop** binds only to a local socket by default. To sync across a second device, enable **Network access** in [Configuration Guide](/docs/admin/configuration/) or connect LeapMux Desktop to a remote Hub in Distributed mode.
{{< /callout >}}

### What syncs and what stays local

| Syncs across your clients | Stays local to each client |
| --- | --- |
| Workspace creation, renaming, sections, archiving, and deletion | Which workspace, tile, or tab is currently focused |
| Tiling splits, grids, divider ratios, and tab order | Draft text typed into a chat composer before sending |
| Floating window position, size, opacity, and inner layout | Floating window stacking order (front-to-back) |
| Tab titles, file paths, and image tab references (over E2EE Worker channels) | Whether a Quake terminal panel is currently slid open |

If two clients edit the same layout property at the same moment, the latest change wins automatically with no merge prompt.

### Active client and turn-end sound

LeapMux tracks one presence signal per workspace: which client you most recently interacted with. When a coding agent finishes a turn, only that active client plays the turn-end sound so alerts do not ring on every open device simultaneously. All tabs and windows of one browser profile share a single login session and count as one client.

To mute a specific device regardless of focus, set **Turn-end sound** to **None** under **Preferences → Notifications** (see [Settings & Preferences](/docs/using/settings/)). LeapMux has no other presence features (no remote cursors, typing indicators, or viewer avatars).

### Sync troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Layout changes do not appear on another device | Different workspaces selected, Solo Desktop has no network listener, or a lost connection | Confirm both clients point to the same Hub and workspace; reload the stale client. |
| Multiple devices play the turn-end sound | Presence has not settled after reconnect | Click inside the client you want active, or set **Turn-end sound** to **None** on secondary devices. |

For deeper diagnostics, see [Troubleshooting](/docs/reference/troubleshooting/).

## Related chapters

- [Coding Agents](/docs/using/coding-agents/) — open, chat with, and steer agents inside your workspaces.
- [Tabs & Layout](/docs/using/tabs-and-layout/) — splits, grids, floating windows, and tab management.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — isolate each agent in its own git checkout.
- [CLI Guide](/docs/using/cli-guide/) — create, archive, and inspect workspaces from scripts.
