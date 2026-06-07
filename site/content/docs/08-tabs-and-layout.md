---
title: "Tabs & Layout"
type: docs
weight: 8
---

The center of every workspace is a tiling canvas. You divide it into **tiles** (rectangular panes), fill each tile with **tabs** (agents, terminals, or files), arrange those tiles as splits or grids, and — when you need a pane to float above everything else — pop a tab out into a movable, resizable **floating window**. This chapter explains every part of that workflow.

The layout you build is part of the workspace, not just your local view: the tile tree is synced across reloads and across everyone in the workspace. (Focus and floating-window stacking order stay local to your client.) See [Collaboration & Presence](/docs/13-collaboration-and-presence/) for what does and doesn't sync.

For the concept-level model of organizations, workspaces, tiles, and tabs, see [Concepts & Architecture](/docs/02-concepts/). For the content that lives *inside* tabs, see [Coding Agents](/docs/09-coding-agents/), [Terminals](/docs/11-terminals/), and [File Browser](/docs/12-file-browser/).

## Tabs

A tab holds one piece of content. There are three kinds:

| Tab kind | What it is | Chapter |
|---|---|---|
| **Agent** | A coding-agent chat (Claude Code, Codex, and others) | [Coding Agents](/docs/09-coding-agents/) |
| **Terminal** | A live shell / PTY session | [Terminals](/docs/11-terminals/) |
| **File** | A file viewer or diff | [File Browser](/docs/12-file-browser/) |

Each tile shows a **tab bar** along its top and the active tab's content below. Exactly one tab is active per tile.

### Opening tabs

Open new tabs from the tab bar's new-tab controls (shown only in a workspace you can edit):

- **New agent** — Up to two buttons for your most-recently-used agent providers appear directly in the tab bar (tooltip "New *Provider* agent"). If no agent providers are configured, a disabled robot button shows the tooltip "No agents available".
- **New terminal** — A terminal button (tooltip "New terminal at the current working directory" when the tile already has a tab to inherit a working directory from, otherwise "New terminal..."). The keyboard shortcut is `Cmd/Ctrl + T`.
- **More options** (the **+** button) — Opens a menu with everything else.

The keyboard shortcut for a new agent tab is `Cmd/Ctrl + N`. If there is no active workspace when you press it, LeapMux opens the New Workspace dialog instead so you can keep moving.

The **More options** menu groups its actions:

- **Agents** — one button per available provider, plus **New agent...** (`Cmd/Ctrl + Shift + N`).
- **Terminals** — **New terminal...** (`Cmd/Ctrl + Shift + T`), then one entry per available shell. The configured default shell is marked **(default)**. See [Terminals](/docs/11-terminals/) for shell selection.
- **Advanced** — toggles for **Expand agent thoughts** and **Show hidden messages** (a checkmark indicates the toggle is on).

> **Tip:** Double-click empty space in the tab list to jump straight to the New Agent dialog.

When the tab bar is too narrow to show the full button set, it collapses: a single **+** button on a minimal-width bar, or a **…** overflow button on a very narrow bar (the **…** menu also includes the tile actions — see [Splitting a tile](#splitting-a-tile)).

### Selecting, scrolling, and switching tabs

- **Select** a tab by clicking it, or with `Enter`/`Space` when it's focused.
- **Switch by number:** `Cmd/Ctrl + 1` through `Cmd/Ctrl + 9` (also `Alt + 1`–`Alt + 9`) select the Nth visible tab in the focused tile.
- **Previous / next tab:** `Cmd/Ctrl + [` / `Cmd/Ctrl + ]` (also `Cmd/Ctrl + PageUp` / `Cmd/Ctrl + PageDown`). These wrap around and do nothing if the tile has fewer than two tabs.
- When the tab list overflows, scroll the mouse wheel over it to scroll the tabs horizontally.

A small dot on a tab (a **notification indicator**) means the tab has unseen activity; activating the tab clears it.

### Renaming a tab

Double-click an **agent** or **terminal** tab to rename it inline. Press `Enter` to commit, `Escape` to cancel; clicking away also commits. Empty or unchanged names are ignored.

> **Note:** File tabs cannot be renamed, and tabs in a read-only (archived) workspace cannot be renamed. Renaming an agent also updates the agent's name on the Worker; if that fails you'll see a "Failed to rename agent" toast and the rename is reverted.

### Closing tabs

Close a tab with the **X** on the tab, by **middle-clicking** it, or with `Cmd/Ctrl + W` for the active tab. In a read-only workspace, only file tabs can be closed.

Closing certain tabs triggers a confirmation:

- Closing the **last tab tied to a git worktree**, or the last non-worktree tab for a **branch with uncommitted or unpushed work**, raises the **Close last tab** dialog (see below).
- Closing a tab is also what happens when you choose "Close all tabs" in a close-tile / close-grid / close-window dialog — each tab is closed one at a time, so you may see a last-tab prompt for one of them.

#### The "Close last tab" dialog

This dialog (titled **Close last tab**) protects you from accidentally abandoning a worktree or losing local git state. Its body explains the situation — either "You are closing the last tab for worktree `<path>`." or "You are closing the last non-worktree tab for branch `<name>`." — and lists the affected agents, terminals, and files (agents and terminals "will be stopped"; other resources "will keep running").

Footer buttons:

| Button | Shown when | Effect |
|---|---|---|
| **Cancel** | always | Keeps the tab open. |
| **Push branch** | there is unpushed work | Pushes the branch before continuing. |
| **Delete** | the tab is on a worktree | Schedules the worktree for deletion (confirmation required). |
| **Close anyway** | always | Closes the tab and leaves git state untouched (confirmation required). |

For everything about worktrees, branches, dirty-state protection, and pushing, see [Worktrees & Branches](/docs/10-worktrees-and-branches/).

### Moving and reordering tabs

Drag a tab to rearrange it:

- **Reorder within a tile** — drop it onto a sibling tab.
- **Move to another tile** — drop it onto that tile's tab bar. The target tab bar highlights as a drop target while you drag a tab from another tile over it.
- **Move to another workspace** — drop it onto a workspace in the sidebar.

Moving the active tab out of a tile automatically promotes another tab in that tile to active. Dragging the **last** tab out of a single-tile floating window removes the now-empty window.

## Tiling: splits and grids

A single tile can be divided into more panes. Each division is either a **split** (two panes) or a **grid** (rows × columns). Splits and grids can nest, building up a layout tree.

> **Note:** In the main layout, splitting and gridding are allowed up to a nesting depth of **3**. Once a region is that deeply nested, its split/grid buttons stop appearing — flatten or use a grid instead. Floating windows have no depth limit.

### The tile actions

Each tile's tab bar has a strip of action buttons on the right (it appears only when at least one action is available):

| Button | Icon | Label | What it does |
|---|---|---|---|
| Pop out / Pop in | Picture-in-picture | "Pop out to floating window" / "Pop in to main window" | Floats the active tab out, or docks it back. |
| Split vertically | Columns | "Split vertically" | Inserts a **vertical divider** → two **side-by-side** panes. |
| Split horizontally | Rows | "Split horizontally" | Inserts a **horizontal divider** → two **stacked** panes. |
| Make grid | Grid | "Make grid" | Opens the grid-size popover. |
| Close | X | "Close tile" / "Close grid" | Closes this tile, or the whole grid if this is the grid's anchor cell (top-right cell). |

> **Tip:** "Split **vertically**" means a vertical divider that puts panes left-and-right. "Split **horizontally**" means a horizontal divider that stacks panes top-and-bottom. Match the word to the divider line, not to the arrangement of panes.

On a very short tile, the whole strip collapses into a single **…** ("Tile menu") button that opens the same actions in a dropdown. In that menu, **Make grid** appears as "Make a grid…".

### Splitting a tile

Split via the action buttons, the tile menu, or the keyboard:

| Action | Shortcut |
|---|---|
| Split tile horizontally | `Cmd/Ctrl + \` |
| Split tile vertically | `Cmd/Ctrl + Shift + \` |

The shortcuts act on the **focused** tile. When you split, the tile becomes a two-pane split at a 50/50 ratio: your existing tabs move into the first pane, and the second pane starts empty (see [Filling an empty tile](#filling-an-empty-tile)).

A **vertical** split inserts a vertical divider, leaving two side-by-side panes:

**Split vertically (side-by-side panes):**

```text
┌────────────────────────┬────────────────────────┐
│ Agent ×                │ Terminal ×             │
├────────────────────────┼────────────────────────┤
│                        │                        │
│                        │                        │
│         Agent          │        Terminal        │
│                        │                        │
│                        │                        │
└────────────────────────┴────────────────────────┘
                          ▲
                vertical divider (drag to resize)
```

A **horizontal** split inserts a horizontal divider instead, stacking the two panes top-and-bottom.

### Making a grid

Click **Make grid** to open the grid-size popover. You pick a size three ways:

- **Hover picker** — A grid of cells (up to 6 rows × 9 columns). Hover to highlight the top-left rectangle; the label reads, for example, "3 × 4" (or "Pick a size" before you hover). Click to apply. Arrow keys move the highlight; `Enter` applies; `Escape` dismisses.
- **Manual entry** — Type into the **Rows** and **Columns** number fields (each 1–20) and click **Create**. **Create** stays disabled until both fields hold whole numbers in range; pressing `Enter` in either field also submits.

The grid can be up to **20 × 20**. When you make a grid, the tile's existing tabs move into the **top-left cell** (cell 0,0); every other cell starts empty.

**A 2×2 grid (existing tabs land in cell 0,0):**

```text
┌────────────────────────┬────────────────────────┐
│ Agent ×                │ anchor cell (X closes  │
│                        │ the whole grid)        │
│      cell 0,0          │      cell 0,1          │
│   (existing tabs)      │      (empty)           │
├────────────────────────┼────────────────────────┤
│                        │                        │
│      cell 1,0          │      cell 1,1          │
│      (empty)           │      (empty)           │
└────────────────────────┴────────────────────────┘
```

> **Note:** From the narrowest tab-bar overflow menu, the make-grid action is a fixed **Make a 2×2 grid** rather than the size popover.

### Adjusting split and grid ratios

Drag the divider between any two panes to resize them. The drag shows a live preview and commits when you release. No pane can be dragged below **5%** of its container, so a pane can never disappear by dragging. (Resizing by keyboard is not supported.)

### Closing a tile or grid

The **X** on a normal tile closes that tile; the **X** on a grid's **anchor cell** (its top-right cell) closes the entire grid.

- A tile or grid with **no tabs** closes immediately.
- A tile or grid that still **has tabs** raises a confirmation dialog: "This *tile/grid* contains *N* tab(s). What would you like to do?" with a preserve option, a danger **Close all tabs** option, and **Cancel**.

| Dialog | Preserve action | What "preserve" does |
|---|---|---|
| **Close tile** | **Move tabs to neighbor** | Merges this tile's tabs into a neighboring tile, then removes the tile. |
| **Close grid** | **Convert to tile** | Replaces the grid with a single tile and moves all the grid's tabs into it. |

**Close all tabs** closes each tab in turn (you may see the Close last tab dialog for one of them), then removes the structure.

> **Note:** A workspace always keeps at least one tile. The last remaining tile in the main layout has no close button and cannot be removed.

## Floating windows

A floating window is a tab (or a whole mini-layout) lifted out of the tiling grid and rendered as a draggable, resizable overlay on top of the workspace. It's useful for keeping a terminal or an agent visible while you rearrange everything underneath it.

**A floating window overlapping the tiled canvas:**

```text
┌────────────────────────────────────────────────────────────┐
│ Agent ×                     │ Terminal ×                   │
├─────────────────────────────┼──────────────────────────────┤
│                             │                              │
│            ┌────────────────────────┐                      │
│            │ Terminal ×  (title bar)│                      │
│            ├────────────────────────┤                      │
│            │                        │                      │
│            │    floating            │                      │
│   tiled    │     window             │      tiled           │
│   canvas   │                        │      canvas          │
│            └────────────────────────┘                      │
│                             │                              │
│                             │                              │
│                             │                              │
└────────────────────────────────────────────────────────────┘
```

### Popping out and docking back

- **Pop out** — Click the pop-out button, use the tile menu, or press `Cmd/Ctrl + Shift + O`. This lifts the **active tab of the focused tile** into a new floating window and focuses it. If that empties the source tile (and other tiles remain), the source tile is removed.
- **Pop in (dock back)** — Click the pop-in button, use the tile menu, or press `Cmd/Ctrl + Shift + O` again while focused inside a floating window. The tab returns to the main layout and the empty window is removed.

`Cmd/Ctrl + Shift + O` (Toggle Floating Tab) toggles based on where focus currently is: out if you're in the main layout, in if you're in a floating window.

A new window opens at 20% from the left, 15% from the top, sized to 40% × 50% of the canvas, fully opaque. Open several in a row and each cascades down-and-right from the last.

### Working with a floating window

- **Move** — Drag the title bar. While dragging, the window comes to the front and **snaps** to a canvas edge when it gets within about 15 pixels of it (each axis snaps independently). The title bar shows the active tab's title (or "Window" when empty).
- **Resize** — Drag any of the 8 edge or corner handles. The minimum size is 5% of the canvas in each dimension.
- **Opacity** — Scroll the mouse wheel over the **title bar** to fade the window in or out, in steps, between 20% and fully opaque. This is handy for peeking at content behind a floating terminal.
- **Bring to front** — Click anywhere in the window to raise it and make it active.
- **Close** — Click the **X** ("Close window") in the title bar. As with tiles, closing a window that still has tabs raises a **Close floating window** dialog whose preserve action is **Move tabs to main** (it merges the window's tabs into the first main-layout tile).

A floating window is a full layout in its own right: you can split it, grid it, and resize panes inside it, with no nesting-depth cap. Its position, size, and opacity persist across reloads; its stacking order and focus are local to your client.

## Filling an empty tile

A tile with no tabs shows a placeholder. What you see depends on context:

- **Focused (or single) tile** — Two buttons: **Open a new agent tab...** (`Cmd/Ctrl + N`) and **Open a new terminal tab...** (`Cmd/Ctrl + T`). You can also drag an existing tab in from another tile or the sidebar.
- **Unfocused empty tile** in a multi-tile layout — A quiet hint: "No tabs in this tile." Focus it (or drag a tab onto it) to act on it.
- **Archived workspace** — "This workspace is archived. Unarchive it to create new agents or terminals." See [Workspaces](/docs/07-workspaces/) for archiving and unarchiving.

When there is no active workspace at all, the center panel shows a **Create a new workspace...** button (`Cmd/Ctrl + Alt + N`) instead. See [Workspaces](/docs/07-workspaces/).

## Desktop vs. mobile layout

LeapMux switches between two shells based on viewport width.

### Desktop layout

Three panes: a left sidebar (workspaces), the center tiling canvas (with the floating-window layer on top), and a right sidebar (files). Drag the handle between a sidebar and the center to resize it.

- Sidebars default to 250 px wide and collapse to a thin 45 px strip.
- Toggle sidebars with `Cmd/Ctrl + Shift + [` (left) and `Cmd/Ctrl + Shift + ]` (right).
- On a shrinking window LeapMux auto-collapses a sidebar when the visible sidebars would take more than half the viewport, and re-expands one it auto-collapsed when there's room again. (Sidebars you collapsed yourself are not auto-expanded.)

### Mobile layout

On a narrow viewport the shell becomes a single column: one focused tile's tab bar and content, with no tiling chrome. Both sidebars become off-canvas overlays:

- The tab bar shows a **menu** button (left, "Toggle workspaces") and a **panel** button (right, "Toggle files").
- Opening one sidebar closes the other; tapping the dimmed background closes both.

Splitting, gridding, and floating windows are desktop features — on mobile you work one tile at a time.

## Keyboard shortcuts

The layout shortcuts referenced above, in one place. `Cmd` is the modifier on macOS; `Ctrl` on Windows and Linux.

| Action | Shortcut |
|---|---|
| New agent tab | `Cmd/Ctrl + N` |
| New terminal tab | `Cmd/Ctrl + T` |
| New agent dialog | `Cmd/Ctrl + Shift + N` |
| New terminal dialog | `Cmd/Ctrl + Shift + T` |
| New workspace dialog | `Cmd/Ctrl + Alt + N` |
| Close active tab | `Cmd/Ctrl + W` |
| Switch to tab 1–9 | `Cmd/Ctrl + 1`–`9` (also `Alt + 1`–`9`) |
| Previous tab | `Cmd/Ctrl + [` (also `Cmd/Ctrl + PageUp`) |
| Next tab | `Cmd/Ctrl + ]` (also `Cmd/Ctrl + PageDown`) |
| Split tile horizontally | `Cmd/Ctrl + \` |
| Split tile vertically | `Cmd/Ctrl + Shift + \` |
| Toggle floating tab (pop out/in) | `Cmd/Ctrl + Shift + O` |
| Toggle left sidebar | `Cmd/Ctrl + Shift + [` |
| Toggle right sidebar | `Cmd/Ctrl + Shift + ]` |

For the complete keybinding reference and how to customize bindings, see [Keyboard Shortcuts](/docs/15-keyboard-shortcuts/). To drive tabs, tiles, and layouts from a script, see [Remote Control CLI](/docs/16-remote-control-cli/).
