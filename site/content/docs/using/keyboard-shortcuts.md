---
title: "Keyboard Shortcuts"
description: "Default keyboard shortcuts for workspaces, tabs, splits, terminals, and coding agents in LeapMux, plus account-synced customization in Preferences."
type: docs
weight: 9
---

You can open agents and terminals, switch tabs, split panes, toggle the Quake terminal, and steer the agent input queue without leaving the keyboard. Every workspace shortcut can be remapped in **Preferences → Keyboard Shortcuts**, and custom bindings sync automatically across your devices.

{{< callout >}}
Tooltips and dropdown menus always display your active binding automatically (for example, `New Agent (⌘N)` on macOS or `New Agent (Ctrl+N)` on Windows and Linux).
{{< /callout >}}

## Default keyboard shortcuts

On macOS, modifiers use Apple symbols: `⌘` (Command), `⌃` (Control), `⌥` (Option), `⇧` (Shift), `⏎` (Enter), and `Esc` (Escape). On Windows and Linux, modifiers use `Ctrl`, `Alt`, and `Shift` joined with `+`.

### App & workspaces

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Open Preferences | `⌘,` | `Ctrl+,` | Works anywhere in a workspace; also in the macOS desktop menu bar. |
| New Workspace Dialog | `⌥⌘N` | `Ctrl+Alt+N` | Active when no dialog is open. |
| Close menu or dialog | `Esc` | `Esc` | Closes one layer at a time: clears search or closes an open menu before closing the dialog underneath. |

### Tabs

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| New Agent | `⌘N` | `Ctrl+N` | Opens an agent in the active tab's directory, or opens **New workspace** when no workspace is active. |
| New Terminal | `⌘T` | `Ctrl+T` | Opens a terminal in the active tab's directory (or opens **New terminal**). Does nothing when no workspace is active. |
| Close Active Tab | `⌘W` | `Ctrl+W` | Active when no dialog is open. |
| New Agent Dialog | `⇧⌘N` | `Ctrl+Shift+N` | Opens **New agent**, or opens **New workspace** when no workspace is active. |
| New Terminal Dialog | `⇧⌘T` | `Ctrl+Shift+T` | Opens **New terminal**. Does nothing when no workspace is active. |
| Toggle Floating Tab | `⇧⌘O` | `Ctrl+Shift+O` | Pops the active tab into a floating window, or docks a focused floating tab back into the main canvas. |
| Switch to Tab 1–9 | `⌘1`–`⌘9` or `⌥1`–`⌥9` | `Ctrl+1`–`Ctrl+9` or `Alt+1`–`Alt+9` | Selects the Nth visible tab in the focused tile. |
| Previous Tab | `⌘[` or `⌘PageUp` | `Ctrl+[` or `Ctrl+PageUp` | Wraps through tabs in the focused tile. |
| Next Tab | `⌘]` or `⌘PageDown` | `Ctrl+]` or `Ctrl+PageDown` | Wraps through tabs in the focused tile. |

### Layout & view

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Split Tile Horizontally | `⌘\` | `Ctrl+\` | Inserts a horizontal divider (stacked panes) in the focused tile. |
| Split Tile Vertically | `⇧⌘\` | `Ctrl+Shift+\` | Inserts a vertical divider (side-by-side panes) in the focused tile. |
| Toggle Left Sidebar | `⇧⌘[` | `Ctrl+Shift+[` | Collapses or expands the Workspaces and Workers sidebar. |
| Toggle Right Sidebar | `⇧⌘]` | `Ctrl+Shift+]` | Collapses or expands the Files sidebar. |
| Scroll Active Tab Up One Page | `⌥PageUp` | `Alt+PageUp` | Pages up in the active agent chat or terminal. |
| Scroll Active Tab Down One Page | `⌥PageDown` | `Alt+PageDown` | Pages down in the active agent chat or terminal. |

### Files

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Refresh Directory Tree | `⌘R` or `F5` | `Ctrl+R` or `F5` | Reloads git status and directory listings in the Files sidebar. |
| Toggle Hidden Files | `⇧⌘H` | `Ctrl+Shift+H` | Shows or hides dotfiles in the Files sidebar. |

### Chat & input queue

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Resume Queue and Steer or Preempt | `⌘⏎` | `Ctrl+⏎` | Active in an agent tab when the composer is empty and no dialog or terminal is focused. |
| Send Message | Unbound | Unbound | Active only while the chat composer input is focused; bind a custom chord in Preferences if desired. |

When a turn is running and the composer is empty, `⌘⏎` / `Ctrl+⏎` acts on the input queue in one step: it resumes a paused queue (or opens **Recover login** when paused for authentication recovery) and triggers **Steer** or **Preempt** on the first waiting item. When the composer has text, an attachment, or a pending prompt, `⌘⏎` / `Ctrl+⏎` submits the composer instead. See [Coding Agents](/docs/using/coding-agents/#input-queue-scheduling-and-steering).

### Terminal

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Toggle Quake Terminal | ``⌃` `` | ``Ctrl+` `` | Slides the per-directory Quake terminal open or shut from any tab. |

The Quake binding uses `Ctrl` on macOS as well because macOS reserves ``⌘` `` for switching app windows. Two unbound actions — **Open Quake Terminal** and **Close Quake Terminal** — let you assign separate open and close keys in Preferences.

When a terminal is focused on **macOS**, four extra bindings send standard shell navigation sequences:

| Action | macOS |
| --- | --- |
| Go to Line Start | `⌘←` |
| Go to Line End | `⌘→` |
| Go to Previous Word | `⌥←` |
| Go to Next Word | `⌥→` |

### Desktop app only

These bindings apply only in **LeapMux Desktop**:

| Action | macOS | Windows / Linux | Notes |
| --- | --- | --- | --- |
| Open in External App | `⇧⌘E` | `Ctrl+Shift+E` | Opens the active tab's local directory in your chosen editor or file manager (solo mode only). |
| Open Web Inspector | `⌥⌘I` or `F12` | `Ctrl+Alt+I` or `F12` | Works on every desktop screen. |
| Zoom In | `⌘=` or `⌘Num+` | `Ctrl+=` or `Ctrl+Num+` | Works on every desktop screen. |
| Zoom Out | `⌘-` or `⌘Num-` | `Ctrl+-` or `Ctrl+Num-` | Works on every desktop screen. |
| Actual Size | `⌘0` or `⌘Num0` | `Ctrl+0` or `Ctrl+Num0` | Resets desktop zoom to 100%. |
| Quit Application | `⌘Q` | `Ctrl+Q` | Works on every desktop screen. |

In **LeapMux Desktop** solo mode, `⇧⌘E` / `Ctrl+Shift+E` (or the **Open in …** title-bar button) launches your preferred local editor or OS file manager for the active tab's directory. Use the chevron beside the button to switch detected editors or click **Refresh app list**. Workspace, repository, and branch menus in the left sidebar expose the same **Reveal in file manager** and **Open in …** actions.

## Chat editor keys

Inside the chat composer, Enter behavior follows your **Enter key behavior** setting (toggle from the composer **[+]** menu or **Preferences → Chat & Composer**):

| Mode | Send message | Insert newline |
| --- | --- | --- |
| **`⌘⏎` / `Ctrl+⏎` sends** (default) | `⌘⏎` / `Ctrl+⏎` | `Enter` |
| **Enter sends** | `Enter` | `Shift+Enter` |

Built-in Markdown editor keys stay fixed: `Tab` and `Shift+Tab` indent or outdent lists and headings, `Backspace` lifts blockquotes and code blocks, `⌘E` / `Ctrl+E` toggles inline code, and `Shift+Tab` toggles plan mode on supported providers.

## Customizing shortcuts

{{< screenshot src="preferences-keyboard-shortcuts" alt="LeapMux Preferences Keyboard Shortcuts panel listing commands, active key bindings, and Default or Custom sources" caption="Remap any workspace command in Preferences → Keyboard Shortcuts with conflict checking and account sync." width="1280" height="856" >}}

Open **Preferences → Keyboard Shortcuts** (`⌘,` / `Ctrl+,`) to remap any workspace command:

- **Record a binding:** Click a shortcut cell and press your key combination, or press two combinations in sequence for a two-step chord.
- **Conflict checking:** LeapMux refuses bindings that conflict in the same context and identifies the conflicting command.
- **Unbind or reset:** Clear a binding to unbind it, or click **Reset** on any **Custom** row to restore its default.
- **Sync:** Up to 200 custom bindings sync immediately across all of your signed-in browsers and desktop apps.

`Esc` stays reserved for dismissing menus and dialogs, and core desktop shortcuts (Zoom, Web Inspector, Quit) stay fixed. On macOS Desktop, remapping **Open Preferences** to a single keystroke updates the native menu bar automatically.

## Keyboard layouts and IME

- **Layout-independent:** Shortcuts match physical key positions rather than printed characters, so bindings like ``⌃` `` / ``Ctrl+` `` and `⌥⌘N` work consistently across non-US layouts.
- **Safe while typing:** Unmodified single-letter bindings never fire inside text inputs, while modifier shortcuts and function keys (`F1`–`F12`) remain active.
- **Input Method Editor (IME) safe:** Shortcuts pause automatically while composing CJK or other IME text.

## Related chapters

- [Settings & Preferences](/docs/using/settings/) — full Preferences dialog and account-vs-device sync.
- [Tabs & Layout](/docs/using/tabs-and-layout/) — tabs, splits, grids, floating windows, and sidebars.
- [Coding Agents](/docs/using/coding-agents/) — input queue steering, preemption, and plan mode.
- [Terminals](/docs/using/terminals/) — terminal tabs and the Quake-mode terminal.
