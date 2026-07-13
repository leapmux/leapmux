---
title: "Workspaces"
description: "The top-level container in LeapMux for tiled agent, terminal, and file tabs: create, rename, delete, organize, and switch between workspaces."
type: docs
weight: 3
---

A **workspace** is the top-level container you work inside. It holds a tiling layout of tabs — coding agents, terminals, and file browsers — each tab tied to a Worker (machine), a working directory, and (usually) a git branch. Every workspace lives in your [personal organization](/docs/using/accounts/#your-personal-organization), has a single owner (the person who created it), and persists across restarts (see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/) for how agent and terminal sessions are restored). This chapter covers creating, renaming, deleting, organizing, and switching between workspaces.

For the bigger picture of how workspaces fit alongside Hubs, Workers, tiles, and tabs, see [Concepts & Architecture](/docs/getting-started/concepts/). For everything you do *inside* a workspace — tabs, splits, grids, and floating windows — see [Tabs & Layout](/docs/using/tabs-and-layout/).

## What a workspace is

- A workspace lives in your personal organization. The URL of an open workspace is `/o/{username}/workspace/{workspaceId}`.
- The user who creates a workspace is its **owner**. Only the owner can rename or delete it.
- Workspace access is strictly owner-only: you see exactly the workspaces you own.
- Agent and terminal state lives only in the Worker's local database and is never uploaded to the Hub. Frontend↔Worker traffic is end-to-end encrypted in transit. The Hub stores the workspace's title, tab positions, and layout geometry, but never the content. See [Security & Threat Model](/docs/operating/security/).

## Creating a workspace

Open the **New workspace** dialog from the left sidebar:

1. Hover over a workspace section header (for example **In progress**) and click the **+** button. Its tooltip reads something like `New workspace in {section name} (⌘⌥N)` — the section name followed by the keyboard-shortcut hint in parentheses. The **+** button appears on every workspace section except **Archived**. (Note that this button creates a *workspace* in that section; it does not create a section.)
2. Alternatively, trigger the keyboard shortcut bound to the *New workspace dialog* action (see [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/)).

The dialog is titled **New workspace** and contains these fields:

| Field | What it does |
| --- | --- |
| **Worker** | Pick which Worker (machine) hosts the workspace's first agent. See [Managing Workers](/docs/operating/managing-workers/). |
| Agent provider | Choose the agent backend (for example Claude Code or Codex). A refresh control re-queries the Worker for available providers. See [Coding Agents](/docs/using/coding-agents/). |
| **Title** | The workspace name. Pre-filled with a random three-word title-cased name; the placeholder is `New Workspace`. The refresh button beside the label (tooltip **Generate random name**) regenerates the suggestion. |
| Directory | The working directory to open on the Worker (left column). |
| Session ID | Optional agent session ID to resume an existing agent session (right column). See [Coding Agents](/docs/using/coding-agents/). |
| Git options | Once a Worker is chosen, choose the git mode for the working directory — for example opening directly or in a worktree. See [Worktrees & Branches](/docs/using/worktrees-and-branches/). |

Click **Create** to confirm. The button shows **Creating…** while the workspace is provisioned. Creating a workspace spins up its first agent automatically, then opens the new workspace.

> **Note:** Workspace titles are sanitized server-side. The characters `"`, `\`, `$`, and `%` (and control characters) are stripped, surrounding whitespace is trimmed, the result must not be empty, and it must be at most 128 characters. If validation fails, the dialog shows the reason inline.

> **Tip:** If anything fails after the workspace row is created — for example the agent cannot start — LeapMux attempts to roll the creation back automatically, so you normally aren't left with an empty orphan workspace.

## The sidebar workspace tree

The left sidebar lists workspaces grouped into collapsible **sections**. Every user starts with these default workspace sections:

| Section | Contains |
| --- | --- |
| **In progress** | Your workspaces that are not assigned to another section. New workspaces land here by default. |
| **Archived** | Workspaces you have archived. Collapsed by default. |

Beyond the defaults, LeapMux also supports your own **custom sections** for organizing workspaces (see below). All workspace sections except **Archived** are expanded by default; click a section header to collapse or expand it.

### Custom sections

In addition to the default sections, your sidebar can hold **custom sections** that you use to group workspaces. A custom section behaves like **In progress**: it is expanded by default, it carries a **+** button to create a workspace directly in it, and it is a valid drag target and a **Move to** target (see [Moving and archiving](#moving-and-archiving) below). Each section is per-user, so creating, renaming, or deleting a section changes only *your* sidebar.

> **Note:** Custom sections may appear if they already exist, but the current UI provides no way to **create**, **rename**, or **delete** them — so the only sections you will normally see are the two defaults.

### Workspace rows

Each workspace row shows:

- A **chevron** on the left that expands or collapses the workspace's tab tree.
- The workspace **title** (falls back to **Untitled** when blank).
- A diff-stats badge summarizing added / deleted / untracked lines across the workspace's tabs.
- The active workspace is highlighted with an accent bar.

Row interactions:

- **Single click** selects the workspace and switches to it.
- **Double click** starts an inline rename.
- **Click the chevron** to expand the tab tree. Expanding a non-active workspace lazy-loads its tabs.
- Drag a workspace to **reorder** it or **move** it between sections. Dragging into **Archived** routes through the archive confirmation.

> **Note:** Expanded/collapsed state is remembered between page reloads. Expansion survives a refresh, so the tree comes back the way you left it.

### The per-workspace tab tree

Expand a workspace to see its tabs organized as a tree:

- **Repo → Branch → tabs.** Tabs are grouped by their git repository, then by branch, then listed individually. The repo label comes from the git origin URL (for example `github.com/org/repo`), or the directory name for a repo with no origin.
- Tabs with no git information appear as flat leaves in an ungrouped bucket.
- A branch with no current branch is labeled **(no branch)** and offers no branch actions.
- Branch rows expose a **Change branch** and **Delete branch** menu when the branch has a real name. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

Each tab leaf shows the tab's type icon and label. For closable tabs, a close **×** button appears (middle-click also closes them). Double-click an agent or terminal leaf to rename it inline; file tabs are not renamable. Clicking a tab leaf in a workspace that is not currently active switches to that workspace and activates the chosen tab. Repo and branch collapse state is remembered per workspace across reloads.

## Renaming a workspace

To rename a workspace, either:

- Double-click the workspace row, or
- Open the row's context menu (the **⋯** button) and choose **Rename**.

The title becomes an inline input pre-filled with the current name. Press **Enter** or click away to commit; press **Escape** to cancel. An empty value cancels the rename. If the rename fails, you will see a **Failed to rename workspace** toast.

## Moving and archiving

The workspace context menu includes:

- **Move to** — a submenu listing your other sections (excluding the current one and **Archived**), including any [custom sections](#custom-sections). Pick one to move the workspace there. The submenu appears only when at least one such target exists, so with just the default sections it is hidden unless the workspace is somewhere other than **In progress**.
- **Archive** / **Unarchive** — moves the workspace into your **Archived** section, or back to **In progress**.

Archiving asks for confirmation:

> **Archive workspace**
> Are you sure you want to archive this workspace? All active agents and terminals will be stopped.

Archiving is a purely per-user organization of your sidebar: it moves the workspace into your **Archived** section. The workspace itself is not deleted; it stays available and can be unarchived at any time. (While a workspace is archived its context menu shows **Unarchive** in place of **Move to**.)

> **Note:** Despite what the confirmation dialog says, archiving does **not** stop the workspace's agents or terminals in the current implementation. They keep running on their Workers. Archiving only relocates the workspace in *your* sidebar (a per-user section move) and, for the active workspace, clears your client's local pending control state for its agent tabs — it does not free Worker-side resources. To actually stop and reclaim resources, delete the workspace, or close its individual agent and terminal tabs.

## Deleting a workspace

To delete a workspace, open the workspace context menu and choose **Delete** (shown in red). You are asked to confirm:

> **Delete workspace**
> Are you sure you want to delete this workspace? This cannot be undone.

On confirm, the Hub deletes the workspace and tells the Frontend which Workers hosted its tabs; the Frontend then cleans up Worker-side resources for the workspace over the encrypted channel. If the deleted workspace was the active one, you are navigated to your first non-archived workspace (or back to the org dashboard if none remain). If deletion fails, you will see a **Failed to delete workspace** toast.

> **Warning:** Deletion is final from your point of view — there is no undelete in the UI.

## Switching workspaces

Click any workspace row in the sidebar to switch to it. On mobile, this also closes the open sidebar overlay before navigating to `/o/{username}/workspace/{workspaceId}`.

LeapMux caches each workspace's state — its tabs, layout, and active-tab selection — as you visit it, so switching back to a workspace you have already opened restores instantly. The first time you open a workspace in a session, its tabs are fetched fresh.

## Live updates across clients

Workspace lifecycle changes — create, rename, delete — are broadcast to all of your connected clients near-real-time over the organization event stream, so the sidebar stays in sync without a manual refresh. The tiling layout *inside* a workspace also syncs live across your devices; see [Device Sync & Presence](/docs/using/collaboration/).

## Related chapters

- [Tabs & Layout](/docs/using/tabs-and-layout/) — working inside a workspace: tabs, splits, grids, floating windows.
- [Coding Agents](/docs/using/coding-agents/) — opening and using agents in a workspace.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — the git side of workspace tabs.
- [Device Sync & Presence](/docs/using/collaboration/) — live layout sync across your devices.
- [Security & Threat Model](/docs/operating/security/) — what the Hub can and cannot see.
