---
title: "Workspaces"
description: "The top-level container in LeapMux for tiled agent, terminal, and file tabs: create, rename, delete, organize, and switch between workspaces."
type: docs
weight: 1
---

A **workspace** is the top-level container you work inside. It holds a tiling layout of tabs — coding agents, terminals, and file browsers — each tab tied to a Worker (machine), a working directory, and (usually) a git branch. Workspaces persist across restarts (see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/) for how agent and terminal sessions are restored). This chapter covers creating, renaming, deleting, organizing, and switching between workspaces.

For the bigger picture of how workspaces fit alongside Hubs, Workers, tiles, and tabs, see [Concepts & Architecture](/docs/getting-started/concepts/). For everything you do *inside* a workspace — tabs, splits, grids, and floating windows — see [Tabs & Layout](/docs/using/tabs-and-layout/).

## What a workspace is

- One workspace is open at a time.
- Workspaces are owner-only: only the owner can see, rename, or delete a workspace. There is no sharing.
- Agent and terminal state lives only in the Worker's local database and is never uploaded to the Hub. Frontend↔Worker traffic is end-to-end encrypted in transit. The Hub stores the workspace's title, tab positions, and layout geometry, but never the content. See [Security & Threat Model](/docs/admin/security/).

## Creating a workspace

Open the **New workspace** dialog from the left sidebar:

1. Hover over a workspace section header (for example **In progress**) and click the **+** button. The button creates a workspace in that section. It appears on every workspace section except **Archived**, and its tooltip shows the section name and the shortcut (for example `New workspace in In progress (⌥⌘N)`).
2. Alternatively, trigger the keyboard shortcut bound to the *New workspace dialog* action (see [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/)).

The dialog is titled **New workspace** and contains these fields:

| Field | What it does |
| --- | --- |
| **Worker** | Pick which Worker (machine) hosts the workspace's first agent. See [Managing Workers](/docs/admin/managing-workers/). |
| Agent provider | Choose the agent backend (for example Claude Code or Codex). A refresh control re-queries the Worker for available providers. See [Coding Agents](/docs/using/coding-agents/). |
| **Title** | The workspace name. Pre-filled with a random three-word title-cased name; the placeholder is `New Workspace`. The refresh button beside the label (tooltip **Generate random name**) regenerates the suggestion. |
| Directory | The working directory to open on the Worker. The same picker the other dialogs use: browse the tree, or type a path in the box above it. |
| Session ID | Optional agent session ID to resume an existing agent session. See [Coding Agents](/docs/using/coding-agents/). |
| Git options | Once a Worker is chosen, choose the git mode for the working directory — for example opening directly or in a worktree. See [Worktrees & Branches](/docs/using/worktrees-and-branches/). |

Click **Create** to confirm. Creating a workspace starts its first agent automatically, then opens the new workspace.

{{< callout type="info" >}}
Workspace titles are checked and tidied server-side. If a title cannot be used, the dialog shows the reason inline.
{{< /callout >}}

{{< callout >}}
If anything fails after the workspace row is created — for example the agent cannot start — LeapMux rolls the creation back automatically, so you are not left with an empty workspace.
{{< /callout >}}

## The sidebar workspace tree

The left sidebar lists workspaces grouped into collapsible **sections**. Every user starts with these default workspace sections:

| Section | Contains |
| --- | --- |
| **In progress** | Your workspaces that are not assigned to another section. New workspaces land here by default. |
| **Archived** | Workspaces you have archived. Collapsed by default. |

All workspace sections except **Archived** are expanded by default; click a section header to collapse or expand it.

### Custom sections

In addition to the default sections, your sidebar can hold **custom sections** that you use to group workspaces. A custom section behaves like **In progress**: it is expanded by default, it carries a **+** button to create a workspace directly in it, and it is a valid drag target and a **Move to** target (see [Moving and archiving](#moving-and-archiving) below). Each section is per-user, so creating, renaming, or deleting a section changes only *your* sidebar.

{{< callout type="info" >}}
The current UI provides no way to **create**, **rename**, or **delete** custom sections, so the only sections you will see are the two defaults.
{{< /callout >}}

### Workspace rows

Each workspace row shows:

- A **chevron** on the left that expands or collapses the workspace's tab tree.
- The workspace **title** (falls back to **Untitled** when blank).
- A diff-stats badge summarizing added / deleted / untracked lines across the workspace's tabs.
- The active workspace is highlighted with an accent bar.

Row interactions:

- **Single click** selects the workspace and switches to it.
- **Double click** starts an inline rename.
- **Click the chevron** to expand the tab tree.
- Drag a workspace to **reorder** it or **move** it between sections. Dragging into **Archived** routes through the archive confirmation.

{{< callout type="info" >}}
Expanded/collapsed state is remembered between page reloads, so the tree comes back the way you left it.
{{< /callout >}}

### The per-workspace tab tree

Expand a workspace to see its tabs organized as a tree:

- **Repo → Branch → tabs.** Tabs are grouped by their git repository, then by branch, then listed individually. The repo label comes from the git origin URL (for example `github.com/org/repo`), or the directory name for a repo with no origin.
- Tabs with no git information appear as flat leaves in an ungrouped bucket.
- A group whose worktree has no current branch (for example, a detached HEAD) is labeled **(no branch)** and offers no branch actions.
- Branch rows expose a **Change branch** and **Delete branch** menu when the branch has a real name. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

Each tab leaf shows the tab's type icon and label. For closable tabs, a close **×** button appears (middle-click also closes them). Double-click an agent or terminal leaf to rename it inline; file tabs are not renamable.

Clicking a tab leaf in a workspace that is not currently active switches to that workspace and activates the chosen tab. Repo and branch collapse state is remembered per workspace across reloads.

## Renaming a workspace

To rename a workspace, either:

- Double-click the workspace row, or
- Open the row's context menu (the **⋯** button) and choose **Rename**.

The title becomes an inline input pre-filled with the current name. Press **Enter** or click away to commit; press **Escape** to cancel. An empty value cancels the rename. If the rename fails, the workspace keeps its previous name.

## Moving and archiving

The workspace context menu includes:

- **Move to** — a submenu listing your other sections (excluding the current one and **Archived**), including any [custom sections](#custom-sections). Pick one to move the workspace there. The submenu appears only when at least one such target exists, so with just the default sections the submenu is always hidden.
- **Archive** / **Unarchive** — moves the workspace into your **Archived** section, or back to **In progress**.

Archiving asks for confirmation:

> **Archive workspace**
> Are you sure you want to archive this workspace? All active agents and terminals will be stopped.

Archiving is a purely per-user organization of your sidebar: it moves the workspace into your **Archived** section. The workspace itself is not deleted, and you can unarchive it at any time. (While a workspace is archived its context menu shows **Unarchive** in place of **Move to**.)

Despite the dialog's wording, archiving does not stop the workspace's agents or terminals; they keep running on their Workers. To stop an agent or terminal, close its tab — an archived workspace is read-only, so unarchive it first. To stop everything a workspace holds, delete the workspace.

## Deleting a workspace

To delete a workspace, open the workspace context menu and choose **Delete** (shown in red). You are asked to confirm:

> **Delete workspace**
> Are you sure you want to delete this workspace? This cannot be undone.

On confirm, the workspace is deleted and everything it held is cleaned up. Its agents and terminals are stopped. A worktree that no other workspace points at is reclaimed shortly afterwards by the Worker's housekeeping pass. A worktree with uncommitted changes or unpushed commits is left on disk for you.

A Worker that is unreachable at that moment catches up the next time it connects. If the deleted workspace was the active one, LeapMux switches you to your first non-archived workspace (or shows the empty "Create a new workspace…" state if none remain). If deletion fails, the workspace is left in place.

{{< callout type="warning" >}}
Deletion is final from your point of view — there is no undelete in the UI.
{{< /callout >}}

## Switching workspaces

Click any workspace row in the sidebar to switch to it. On mobile, this also closes the open sidebar overlay.

Every workspace is live at all times, not just the one on screen — switching is instant because there is nothing to load. A change made on another device to a workspace you are not currently looking at lands right away, so its sidebar row, tab titles and diff badges are already correct when you switch in.

Dragging a tab to another workspace, or closing one, applies immediately and syncs to your other clients. The layout is stored on the Hub, so a tab whose Worker cannot be reached moves and closes just the same. That Worker catches up on its side — stopping processes and releasing unreferenced worktrees — the next time it connects.

Your active workspace is remembered in the browser, per account, so a reload or a fresh visit reopens the one you were last on. If that workspace is gone — deleted from another device, say — LeapMux falls back to the first one in your list rather than showing an error.

## Live updates across clients

Workspace lifecycle changes — create, rename, delete — are broadcast to all of your connected clients near-real-time over the user event stream, so the sidebar stays in sync without a manual refresh. The tiling layout *inside* a workspace also syncs live across your devices; see [Device Sync](/docs/using/device-sync/).

## Related chapters

- [Tabs & Layout](/docs/using/tabs-and-layout/) — working inside a workspace: tabs, splits, grids, floating windows.
- [Coding Agents](/docs/using/coding-agents/) — opening and using agents in a workspace.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — the git side of workspace tabs.
- [Device Sync](/docs/using/device-sync/) — live layout sync across your devices.
- [Security & Threat Model](/docs/admin/security/) — what the Hub can and cannot see.
