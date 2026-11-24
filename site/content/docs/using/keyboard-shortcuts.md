---
title: "Keyboard Shortcuts"
description: "Default keyboard shortcuts for workspaces, tabs, splits, terminals, and coding agents in LeapMux, plus how to customize them in Preferences."
type: docs
weight: 11
---

You can open agents and terminals, switch tabs, split panes, toggle the Quake terminal, and steer the agent input queue without leaving the keyboard. Every workspace shortcut can be remapped in **Preferences → Keyboard Shortcuts**, and your custom bindings sync automatically across your devices.

{{< callout >}}
You do not need to memorize every binding. LeapMux shows your active shortcut inside tooltips and dropdown menus automatically (for example, `New Agent (⌘N)` on macOS or `New Agent (Ctrl+N)` on Windows and Linux).
{{< /callout >}}

## Default keyboard shortcuts

On macOS, tooltips and menus use standard Apple modifier symbols: `⌘` (Command), `⌃` (Control), `⌥` (Option), `⇧` (Shift), `⏎` (Enter), and `Esc` (Escape). On Windows and Linux, modifiers appear as `Ctrl`, `Alt`, and `Shift` joined with `+`.

### App & workspaces

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| Open Preferences | `⌘,` | `Ctrl+,` | Works anywhere in a workspace; also appears in the macOS desktop menu bar. |
| New Workspace Dialog | `⌥⌘N` | `Ctrl+Alt+N` | Active when no dialog is open. |
| Close menu or dialog | `Esc` | `Esc` | Closes one layer at a time: clears search or closes an open menu first, then closes the dialog underneath. |

### Tabs

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| New Agent | `⌘N` | `Ctrl+N` | Opens an agent in the active tab's directory (or opens the dialog if none is active). |
| New Terminal | `⌘T` | `Ctrl+T` | Opens a terminal in the active tab's directory (or opens the dialog if none is active). |
| Close Active Tab | `⌘W` | `Ctrl+W` | Active when no dialog is open. |
| New Agent Dialog | `⇧⌘N` | `Ctrl+Shift+N` | Always opens the full New Agent dialog. |
| New Terminal Dialog | `⇧⌘T` | `Ctrl+Shift+T` | Always opens the full New Terminal dialog. |
| Toggle Floating Tab | `⇧⌘O` | `Ctrl+Shift+O` | Pops the active tab out into a floating window, or docks a focused floating tab back into the main layout. |
| Switch to Tab 1–9 | `⌘1`–`⌘9` or `⌥1`–`⌥9` | `Ctrl+1`–`Ctrl+9` or `Alt+1`–`Alt+9` | Selects the Nth visible tab in the focused tile. |
| Previous Tab | `⌘[` or `⌘PageUp` | `Ctrl+[` or `Ctrl+PageUp` | Wraps around tabs in the focused tile. |
| Next Tab | `⌘]` or `⌘PageDown` | `Ctrl+]` or `Ctrl+PageDown` | Wraps around tabs in the focused tile. |

### Layout & view

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| Split Tile Horizontally | `⌘\` | `Ctrl+\` | Inserts a horizontal divider (stacked panes) in the focused tile. |
| Split Tile Vertically | `⇧⌘\` | `Ctrl+Shift+\` | Inserts a vertical divider (side-by-side panes) in the focused tile. |
| Toggle Left Sidebar | `⇧⌘[` | `Ctrl+Shift+[` | Collapses or expands the Workspaces and Workers sidebar. |
| Toggle Right Sidebar | `⇧⌘]` | `Ctrl+Shift+]` | Collapses or expands the Files sidebar. |
| Scroll Active Tab Up One Page | `⌥PageUp` | `Alt+PageUp` | Pages up in the active agent chat or terminal. |
| Scroll Active Tab Down One Page | `⌥PageDown` | `Alt+PageDown` | Pages down in the active agent chat or terminal. |

### Files

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| Refresh Directory Tree | `⌘R` or `F5` | `Ctrl+R` or `F5` | Reloads git status and directory listings in the Files sidebar. |
| Toggle Hidden Files | `⇧⌘H` | `Ctrl+Shift+H` | Shows or hides dotfiles in the Files sidebar. |

### Chat & input queue

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| Resume Queue and Steer or Preempt | `⌘⏎` | `Ctrl+⏎` | Active in an agent tab when the composer is empty and no dialog or terminal is focused. |

When a turn is running and the composer is empty, pressing `⌘⏎` / `Ctrl+⏎` handles the input queue in one step:

- If the queue is paused, it resumes delivery (or opens the login-recovery dialog if the queue waits for sign-in).
- If a waiting item sits at the head of the queue, it steers that item into the running turn — or cancels the running turn and sends the item next when the provider supports preemption instead of steering.
- If the composer holds text, an attachment, or a pending permission prompt, `⌘⏎` / `Ctrl+⏎` sends the composer instead. Scheduled items stay waiting until their scheduled time arrives.

See [The input queue](/docs/using/coding-agents/#the-input-queue) for the full queueing and steering workflow.

{{< callout type="info" >}}
An additional **Send Message** command starts unbound because the chat editor already sends with `Enter` or `Cmd/Ctrl+Enter`. You can assign it a shortcut in **Preferences → Keyboard Shortcuts** if you want a global chord that submits from anywhere in the chat pane.
{{< /callout >}}

### Terminal

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| Toggle Quake Terminal | ``⌃` `` | ``Ctrl+` `` | Slides the per-directory Quake terminal open or shut from any tab. |

The Quake shortcut uses `Ctrl` on macOS too, because macOS reserves ``⌘` `` for switching between app windows. Two extra commands — **Open Quake Terminal** and **Close Quake Terminal** — start unbound so you can bind separate open and close keys in Preferences if you prefer not to toggle. See [Quake-mode terminal](/docs/using/terminals/#quake-mode-terminal).

When a terminal is focused on **macOS**, four extra bindings send standard shell line and word navigation sequences:

| Action | macOS |
|---|---|
| Go to Line Start | `⌘←` |
| Go to Line End | `⌘→` |
| Go to Previous Word | `⌥←` |
| Go to Next Word | `⌥→` |

### Desktop app only

These shortcuts work only inside **LeapMux Desktop** (they have no effect in a web browser):

| Action | macOS | Windows / Linux | Notes |
|---|---|---|---|
| [Open in External App](#open-in-external-app-desktop-solo-mode) | `⇧⌘E` | `Ctrl+Shift+E` | Opens the active tab's directory in your chosen editor or file manager (solo mode only). |
| Open Web Inspector | `⌥⌘I` or `F12` | `Ctrl+Alt+I` or `F12` | Works on every desktop screen. |
| Zoom In | `⌘=` or `⌘Num+` | `Ctrl+=` or `Ctrl+Num+` | Works on every desktop screen. |
| Zoom Out | `⌘-` or `⌘Num-` | `Ctrl+-` or `Ctrl+Num-` | Works on every desktop screen. |
| Actual Size | `⌘0` or `⌘Num0` | `Ctrl+0` or `Ctrl+Num0` | Resets desktop zoom to 100%. |
| Quit Application | `⌘Q` | `Ctrl+Q` | Works on every desktop screen. |

#### Open in external app (desktop, solo mode)

Press `⇧⌘E` (macOS) or `Ctrl+Shift+E` (Windows / Linux), or click the **Open in …** split button in the desktop title bar, to open the active tab's working directory in your local editor or file manager.

- **Availability:** Shown only in **LeapMux Desktop** running in **solo mode** when the active tab has a local working directory. It is hidden in the browser and in distributed mode because remote Workers run on another machine.
- **Remembered editor:** Clicking the main face or pressing the shortcut launches your preferred application (or the first detected editor such as VS Code, Cursor, Zed, JetBrains IDEs, or Sublime Text if you have not chosen one yet).
- **Switching applications:** Click the chevron beside the button to pick any detected editor or your OS file manager (Finder, File Explorer, or Linux file manager), or click **Refresh app list** after installing a new editor.
- **Sidebar menus:** Workspace, repository, and branch menus in the left sidebar include the same **Reveal in file manager**, **Open in {AppName}**, and **Open in…** actions for each checkout.

## Chat editor keys

Inside the chat message box, key handling depends on your **Enter key behavior** preference (switch it from the composer's **[+]** menu or **Preferences → Chat & Composer**):

| Mode | Send message | Insert newline |
|---|---|---|
| **Cmd/Ctrl+Enter sends** (default) | `⌘⏎` / `Ctrl+⏎` | `Enter` |
| **Enter sends** | `Enter` | `Shift+Enter` |

The chat editor also includes built-in Markdown keys that stay fixed: `Tab` and `Shift+Tab` indent or outdent lists and headings, `Backspace` lifts blockquotes and code blocks, `Cmd/Ctrl+E` toggles inline code, and `Shift+Tab` toggles plan mode on providers that support it.

## Customizing shortcuts

{{< screenshot src="preferences-keyboard-shortcuts" alt="LeapMux Preferences Keyboard Shortcuts panel listing commands, active key bindings, and Default or Custom sources" caption="Remap any workspace command in Preferences → Keyboard Shortcuts with live conflict checking and account-level sync." width="1280" height="856" >}}

Open **Preferences → Keyboard Shortcuts** (`⌘,` on macOS, `Ctrl+,` on Windows and Linux) to view and remap any workspace shortcut.

- **Record a new binding:** Click a command's shortcut, then press the key combination you want. You can also record a two-step **chord** (press one combination, then a second).
- **Conflict checks:** If another command in the same context already uses that shortcut, LeapMux refuses it and tells you which command conflicts.
- **Unbind or reset:** Clear a binding to unbind the command, or click **Reset** on any row marked **Custom** to restore its default.
- **Automatic sync:** Customizations take effect immediately with no reload and sync to your account across every browser and desktop device where you sign in (up to 200 custom bindings).

A few rules always apply:

- **`Esc` stays reserved.** You cannot rebind `Esc` to another command, so it always closes the topmost menu, search box, or dialog reliably.
- **Core desktop shortcuts stay fixed.** Zoom, Web Inspector, and Quit keep their platform defaults.
- **macOS menu bar accelerators.** On macOS Desktop, remapping **Open Preferences** updates the native menu bar automatically as long as the new binding is a single keystroke (two-step chords still work inside the app, but cannot appear as native macOS menu accelerators).

## Keyboard layouts and typing

- **Consistent across keyboard layouts:** Shortcuts follow physical key positions rather than typed characters, so bindings like ``Ctrl+` `` and `⌥⌘N` keep working on non-US keyboard layouts and macOS Option-key combinations.
- **Safe while typing:** Typing inside a text box, editor, or search field never accidentally fires plain unmodified letter shortcuts. Shortcuts with modifiers (`⌘`, `Ctrl`, `Alt`, `Shift`) and function keys (`F1`–`F12`) still work everywhere.
- **IME friendly:** While you compose text with an Input Method Editor (for example Chinese, Japanese, or Korean), LeapMux holds shortcuts back until composition finishes.

## See also

- [Tabs & Layout](/docs/using/tabs-and-layout/) — tabs, splits, grids, floating windows, and sidebars.
- [Coding Agents](/docs/using/coding-agents/) — composer keys, plan mode, and the input queue.
- [Terminals](/docs/using/terminals/) — terminal tabs, scrollback paging, and the Quake-mode terminal.
- [File Browser](/docs/using/file-browser/) — directory refresh and hidden-file toggles.
- [Settings & Preferences](/docs/using/settings/) — the full Preferences dialog and account-vs-device sync.
