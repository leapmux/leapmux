---
title: "Tabs & Layout"
description: "Arrange your workspace canvas in LeapMux: manage agent, terminal, file, and image tabs, build splits and grids, pop out floating windows, and switch between desktop and mobile layouts."
type: docs
weight: 4
---

The center of every workspace is a tiling canvas. Divide it into **tiles** (rectangular panes), fill each tile with **tabs**, arrange tiles into splits or grids, or pop any tab out into a draggable **floating window**. Your tiling tree, tabs, and floating window geometry sync live across all of your devices (while active focus and window stacking order stay local). See [Workspaces](/docs/using/workspaces/#live-sync-across-devices) for sync details.

{{< screenshot src="tabs-tiling-layout" alt="LeapMux tiled workspace showing a coding agent chat pane, inline file diff viewer, and integrated terminal side by side" caption="Tiled split panes keep a coding agent chat, an inline diff viewer, and a shell terminal visible together." width="948" height="900" >}}

## Tabs

LeapMux supports four tab kinds:

| Tab kind | What it holds | Learn more |
| --- | --- | --- |
| **Agent** | A coding-agent conversation and composer | [Coding Agents](/docs/using/coding-agents/) |
| **Terminal** | A live shell pseudo-terminal (PTY) session | [Terminals](/docs/using/terminals/) |
| **File** | A read-only file viewer or inline git diff | [File Browser](/docs/using/file-browser/) |
| **Image** | A zoomable viewer for inline images opened from agent tool results | [Coding Agents](/docs/using/coding-agents/#images-in-tool-results) |

Each tile displays a **tab bar** along the top and shows one active tab at a time.

### Opening tabs

In any active (non-archived) workspace, open tabs from the tile's tab bar or keyboard:

- **Recent agent buttons** — up to two buttons for your most recently used providers appear directly in the tab bar. Press `⌘N` / `Ctrl+N` to open an agent in the active tab's directory (or open **New workspace** when no workspace is active).
- **New terminal button** — opens a terminal at the active tab's working directory (or opens the dialog when none is in scope). Shortcut: `⌘T` / `Ctrl+T`.
- **More options (`+`)** — opens a menu listing all available providers (**New agent...**, `⇧⌘N` / `Ctrl+Shift+N`), all detected shells (**New terminal...**, `⇧⌘T` / `Ctrl+Shift+T`, with the Worker's default marked **(default)**), and **Advanced** toggles (**Expand agent thoughts** and **Show hidden messages**).
- **Empty tab-bar space** — double-click empty space in the tab bar to open the **New agent** dialog.

{{< screenshot src="tab-bar-more-menu" alt="LeapMux tab bar More options menu showing agent provider icons, New agent, New terminal, detected shells with default marker, and Advanced toggles" caption="The tab bar's + menu launches detected coding agents and shells directly or opens the full New agent and New terminal dialogs." width="480" height="712" >}}

On narrow tiles, the tab bar collapses its controls into a single **`+`** button or a **`…`** overflow menu that also holds the tile's split, grid, and close actions.

### Switching, status, and renaming

- **Switch by number:** `⌘1`–`⌘9` / `Ctrl+1`–`Ctrl+9` (or `⌥1`–`⌥9` / `Alt+1`–`Alt+9`) jumps to the Nth tab in the focused tile.
- **Cycle tabs:** `⌘[` / `Ctrl+[` (or `⌘PageUp` / `Ctrl+PageUp`) and `⌘]` / `Ctrl+]` (or `⌘PageDown` / `Ctrl+PageDown`) wrap through tabs in the focused tile.
- **Scroll overflow:** Scroll the mouse wheel over a crowded tab bar to scroll horizontally.
- **Activity and custom status:** A small dot marks unseen activity and clears when you activate the tab. Scripts or agents can also attach a short status badge with a progress ring and tone using `leapmux tab status set` (see [CLI Guide](/docs/using/cli-guide/)). Hover a truncated status to read it in full.
- **Renaming:** Double-click an **agent** or **terminal** tab to rename it inline (`Enter` commits, `Esc` cancels). File tabs, image tabs, and tabs inside an archived workspace cannot be renamed. An agent tab still using its auto-generated `Agent <Name>` title is renamed automatically from the plan title when the agent enters plan mode; any name you type manually is preserved.

### Closing tabs

Close a tab by clicking its **×** button, middle-clicking it, or pressing `⌘W` / `Ctrl+W` for the active tab.

{{< callout type="info" >}}
In an **archived** workspace, no tab of any kind — agent, terminal, file, or image — can be closed. Unarchive the workspace first to close or rearrange tabs.
{{< /callout >}}

Closing a tab can prompt for confirmation in two situations (only one dialog ever opens per close):

- **Worktree or dirty branch (`Close last tab`):** Closing the last tab tied to a linked worktree, or the last non-worktree tab on a branch with uncommitted or unpushed work, opens the **Close last tab** dialog so you can push, delete the worktree, or close while leaving files on disk. See [Worktrees & Branches](/docs/using/worktrees-and-branches/#dirty-worktree-protection-when-closing-tabs).
- **Busy agent or terminal (`Close tab`):** Closing a running agent turn or a terminal with an active foreground process — when not already covered by the **Close last tab** dialog — opens the **Close tab** confirmation dialog. Click **`Close anyway`** twice to confirm and stop the running process; pressing `Enter` alone does not bypass the two-click guard.

{{< screenshot src="busy-tab-close-dialog" alt="LeapMux Close tab confirmation dialog warning that the Retry jitter agent is still working and offering Cancel and Close anyway buttons" caption="Closing a tab with a running agent turn or foreground terminal command prompts for confirmation before stopping it." width="608" height="294" >}}

If the tab's Worker is offline, LeapMux skips confirmation dialogs and closes the tab immediately; the Worker stops the underlying process the next time it connects.

### Moving and reordering tabs

Drag any tab to reorganize it:

- Drop onto a sibling tab to reorder within a tile.
- Drop onto another tile's tab bar to move it into that pane.
- Drop onto a workspace row in the left sidebar to move it into another workspace.

Moving the active tab promotes another tab in the source tile. Dragging the last tab out of a single-tile floating window closes that empty window automatically.

## Tiling: splits and grids

{{< video-clip src="tiling-floating-layout" poster="floating-window" alt="Animated walkthrough of splitting a single tile side by side, turning the right tile into a 2 by 2 grid, popping the agent tab out into a translucent floating window, and dragging it back into the grid" caption="Split a single tile side by side, turn the right tile into a 2×2 grid, pop the agent tab out into a draggable translucent floating window, and drag it right back into the grid." width="1280" height="800" >}}

Divide any tile into a two-pane **split** or an **R × C grid**. Splits and grids can nest up to a depth of **3** in the main layout (split and grid buttons disappear once a pane reaches depth 3; floating windows have no depth limit).

### Tile action controls

The right edge of each tile's tab bar shows available layout actions (collapsing into a **`…`** tile menu on narrow panes):

| Button | Shortcut | What it does |
| --- | --- | --- |
| **Pop out** / **Pop in** | `⇧⌘O` / `Ctrl+Shift+O` | Lifts the active tab into a floating window, or docks a focused floating tab back into the main canvas. |
| **Split horizontally** | `⌘\` / `Ctrl+\` | Inserts a horizontal divider (top and bottom stacked panes, 50/50). |
| **Split vertically** | `⇧⌘\` / `Ctrl+Shift+\` | Inserts a vertical divider (left and right side-by-side panes, 50/50). |
| **Make grid** | — | Opens the grid-size picker to divide the tile into rows and columns. |
| **Close tile** / **Close grid** | — | Closes the tile (or closes the whole grid from its top-right anchor cell). |

The direction matches the divider line: **Split vertically** draws a vertical divider for side-by-side panes; **Split horizontally** draws a horizontal divider for stacked panes. Your existing tabs stay in the first pane, and the new pane starts empty.

### Making a grid

{{< screenshot src="tile-grid-popover" alt="LeapMux Make grid popover showing an interactive 3 by 4 cell hover picker and manual row and column inputs" caption="Choose grid dimensions with the hover picker or enter custom row and column counts up to 20 × 20." width="382" height="322" >}}

Click **Make grid** to pick dimensions up to **20 × 20** using either the interactive 6 × 9 hover picker (also navigable with arrow keys and `Enter`) or the manual **Rows** and **Columns** inputs. Existing tabs move into the top-left cell while the remaining cells start empty. From the narrowest overflow menu, the grid action creates a fixed **2 × 2** grid directly.

### Resizing and closing panes

Drag any divider between panes to resize them; panes cannot shrink below **5%** of their container.

- Closing an empty tile or grid removes it immediately.
- Closing a tile or grid that contains tabs prompts you to either preserve the tabs (**Move tabs to neighbor** for a tile, **Convert to tile** for a grid) or **Close all tabs** (each tab goes through its normal close checks).
- A workspace always keeps at least one tile; the last remaining tile in the main layout cannot be closed.

## Floating windows

{{< screenshot src="floating-window" alt="LeapMux workspace with a semi-transparent floating terminal window hovering over tiled agent, diff viewer, and terminal panes" caption="Floating windows hover above the tiling canvas with adjustable transparency when you scroll on the title bar." width="1172" height="900" >}}

Press `⇧⌘O` / `Ctrl+Shift+O` or click the pop-out button to lift the active tab out of the main layout into a movable, resizable overlay (and press `⇧⌘O` / `Ctrl+Shift+O` again while focused inside it to dock it back).

- **Cascade placement:** New floating windows open at 40% × 50% of the canvas and cascade down-and-right when you open several in a row.
- **Move and snap:** Drag the title bar; edges snap automatically within 15 px of the canvas borders.
- **Resize:** Drag any edge or corner handle (minimum 5% of canvas width and height).
- **Opacity:** Scroll the mouse wheel over the title bar to adjust transparency between 20% and 100% so you can read panes underneath.
- **Nested layouts:** Floating windows support splits and grids inside them with no nesting limit.
- **Close:** Clicking **×** on a non-empty floating window lets you **Move tabs to main** or **Close all tabs**.

## Empty tiles, sidebars, and mobile layout

An empty focused tile shows quick buttons for **Open a new agent tab...** (`⌘N` / `Ctrl+N`) and **Open a new terminal tab...** (`⌘T` / `Ctrl+T`); you can also drag tabs directly into it. When no workspace is open, the canvas shows **Create a new workspace...** (`⌥⌘N` / `Ctrl+Alt+N`).

On **desktop**, LeapMux renders three columns: the left Workspaces sidebar (`⇧⌘[` / `Ctrl+Shift+[`), the center tiling canvas, and the right Files sidebar (`⇧⌘]` / `Ctrl+Shift+]`). Drag either sidebar border to resize it. When you shrink the browser window, LeapMux auto-collapses open sidebars if they would occupy more than half the viewport and restores auto-collapsed sidebars when space returns.

{{< screenshot src="mobile-layout" alt="LeapMux mobile single-column layout showing an agent chat with inline diff, tool output, composer, and top-bar drawer buttons" caption="On narrow viewports, LeapMux switches to a single-column layout with slide-out Workspaces and Files drawers." width="390" height="844" >}}

On **mobile** and narrow viewports, LeapMux switches to a single-column shell displaying one tile at a time. Splits, grids, and floating windows are desktop-only; top-bar buttons and edge swipes open the Workspaces and Files sidebars as off-canvas drawers.

## Related chapters

- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — complete shortcut table and customization.
- [Workspaces](/docs/using/workspaces/) — archiving, sidebar sections, and live sync across devices.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — the **Close last tab** dialog and branch management.
- [CLI Guide](/docs/using/cli-guide/) — scripting tiles, layouts, floating windows, and tab status.
