---
title: "Keyboard Shortcuts"
type: docs
weight: 15
---

LeapMux ships with a VS Code-style keyboard shortcut system: every shortcut is a *command* bound to a *key*, and each binding is active only inside a *context* (for example, only when a dialog is open or a terminal is focused). This chapter explains how the system works, lists every default binding, and shows how to customize them.

## How shortcuts work

A keyboard shortcut in LeapMux is the combination of three independent pieces:

- **Command** — a named action with a human title, such as `app.newAgent` ("New Agent") or `app.closeActiveTab` ("Close Active Tab"). The command holds the logic; it does not know which key triggers it.
- **Keybinding** — a key string mapped to a command, optionally gated by a `when`-clause. For example, `$mod+n` runs `app.newAgent` when `!dialogOpen` (no dialog is open).
- **Context** — a set of named boolean/string values evaluated the moment you press a key (for example `dialogOpen`, `terminalFocused`, or `platform == "mac"`). A binding fires only if its `when`-clause evaluates to true against the current context.

Because these are decoupled, the same command can be bound to several keys, and the same key can run different commands depending on what is focused.

### The `$mod` key

Key strings use the `tinykeys` vocabulary. The most important token is `$mod`:

- On **macOS**, `$mod` means **Cmd** (⌘).
- On **Windows and Linux**, `$mod` means **Ctrl**.

So a single binding such as `$mod+n` is shown as `⌘N` on a Mac and `Ctrl+N` everywhere else. The tables below give both forms.

Other modifiers are `Shift`, `Alt` (also written `Option`), `Control`/`Ctrl`, and `Meta`. Named keys used in the defaults include `Escape`, `F5`, `F12`, the arrow keys, `PageUp`/`PageDown`, the bracket and backslash keys, and the digits `0`–`9`.

> **Note:** Single-letter keys are matched by physical key position (the keyboard's `event.code`), not by the produced character. This is deliberate: on macOS WebKit, holding Option transforms the character (for example, `Cmd+Alt+N` would otherwise produce `˜`), so position-based matching keeps shortcuts working across keyboard layouts and modifier combinations.

### Chords

The engine supports **chords** — space-separated key sequences such as `$mod+k $mod+s` (press the first combination, then the second). Chords render correctly in tooltips and menus, but no default binding uses one. You can introduce chords through custom keybindings (see [Customizing keybindings](#customizing-keybindings)), with one caveat on the desktop app noted later.

### When shortcuts are suppressed

LeapMux binds shortcuts at the window level so they work no matter where focus sits, but it suppresses some of them to avoid hijacking your typing:

- **Input focus.** When the cursor is in a text input, textarea, select, or any `contenteditable` element, a shortcut that has **no modifier and is not a function key** is suppressed — unless its `when`-clause explicitly references `inputFocused`. Shortcuts that use a modifier (`$mod`, `Ctrl`, `Alt`, `Meta`, `Shift`) or a plain function key (`F1`–`F12`) always fire, regardless of focus.
- **IME composition.** While an Input Method Editor composition is active (for example, typing Chinese, Japanese, or Korean), shortcut dispatch is skipped entirely so it cannot interfere with your text entry.

When a key matches, the first binding in registration order whose `when`-clause is true wins; LeapMux then prevents the browser default and runs the command.

## Contexts (the `when` clause)

A binding's `when`-clause is a small boolean expression evaluated against the active context. The available context values are:

| Context | Type | Meaning |
|---|---|---|
| `isDesktop` | boolean | Running inside the Tauri desktop app. |
| `platform` | string | `"mac"`, `"windows"`, or `"linux"`. |
| `dialogOpen` | boolean | A modal dialog is currently open. |
| `activeTabType` | string | `"agent"`, `"terminal"`, `"file"`, or empty when no tab is active. |
| `inputFocused` | boolean | Focus is in an input, textarea, select, or `contenteditable` element. |
| `editorFocused` | boolean | Focus is inside a rich-text editor surface. |
| `chatInputFocused` | boolean | Focus is inside a chat message input. |
| `terminalFocused` | boolean | Focus is inside a terminal. |

The `when` expression grammar supports `||` (or), `&&` (and), `!` (not), parentheses, `==`/`!=` comparisons, identifiers, quoted strings, and the literals `true`/`false`. An empty or absent `when` means "always active." Examples drawn from the defaults: `!dialogOpen`, `dialogOpen`, `isDesktop`, `!dialogOpen && isDesktop`, `chatInputFocused`, and `terminalFocused && platform == "mac"`.

## Default keyboard shortcuts

The tables below list every default binding. The **macOS** column uses Apple's symbol glyphs in HIG order (⌃⌥⇧⌘, run together with no separators); the **Windows / Linux** column uses `Ctrl`/`Alt`/`Shift` joined with `+`. Where a command has more than one binding, both are shown separated by `/` — exactly as LeapMux renders them in tooltips and menus.

### App

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Open Preferences | `⌘,` | `Ctrl+,` | always |

Open Preferences opens the Preferences dialog. It is a workspace binding (it works everywhere inside a workspace), not desktop-only. On the macOS desktop app it is also mirrored onto the native menu bar — see [Desktop (Tauri) accelerator differences](#desktop-tauri-accelerator-differences). For the dialog's contents, see [Settings & Preferences](/docs/14-settings-and-preferences/).

### Tabs

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| New Agent | `⌘N` | `Ctrl+N` | no dialog open |
| New Terminal | `⌘T` | `Ctrl+T` | no dialog open |
| Close Active Tab | `⌘W` | `Ctrl+W` | no dialog open |
| New Agent Dialog | `⌘⇧N` | `Ctrl+Shift+N` | no dialog open |
| New Terminal Dialog | `⌘⇧T` | `Ctrl+Shift+T` | no dialog open |
| Toggle Floating Tab | `⌘⇧O` | `Ctrl+Shift+O` | no dialog open |
| Switch to Tab 1–9 | `⌘1`–`⌘9` / `⌥1`–`⌥9` | `Ctrl+1`–`Ctrl+9` / `Alt+1`–`Alt+9` | always |
| Previous Tab | `⌘[` / `⌘PageUp` | `Ctrl+[` / `Ctrl+PageUp` | always |
| Next Tab | `⌘]` / `⌘PageDown` | `Ctrl+]` / `Ctrl+PageDown` | always |

Tab switching and navigation operate on the **visible** tabs of the focused tile (or on all tabs when no tile is focused). Previous/Next wrap around and do nothing if a tile has fewer than two tabs. See [Tabs & Layout](/docs/08-tabs-and-layout/).

### Layout

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Split Tile Horizontally | `⌘\` | `Ctrl+\` | always |
| Split Tile Vertically | `⌘⇧\` | `Ctrl+Shift+\` | always |
| Toggle Left Sidebar | `⌘⇧[` | `Ctrl+Shift+[` | always |
| Toggle Right Sidebar | `⌘⇧]` | `Ctrl+Shift+]` | always |

Splitting acts on the focused tile. For more on tiling and sidebars, see [Tabs & Layout](/docs/08-tabs-and-layout/).

### View

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Scroll Active Tab Up One Page | `⌥PageUp` | `Alt+PageUp` | no dialog open |
| Scroll Active Tab Down One Page | `⌥PageDown` | `Alt+PageDown` | no dialog open |

Page scrolling only acts when the active tab is an agent or a terminal.

### Files

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Refresh Directory Tree | `⌘R` / `F5` | `Ctrl+R` / `F5` | always |
| Toggle Hidden Files | `⌘⇧H` | `Ctrl+Shift+H` | always |

These act on the file browser. See [File Browser](/docs/12-file-browser/).

### Chat

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Send Message | `⌘J` | `Ctrl+J` | chat input focused |

> **Note:** `Send Message` (`⌘J` / `Ctrl+J`) is a *global* way to submit the focused chat input, independent of the chat editor's own Enter behavior. The Enter-to-send vs. Cmd/Ctrl+Enter-to-send choice is a separate, editor-level setting described in [Chat editor keys](#chat-editor-keys-not-part-of-the-global-system) below.

### Terminal (macOS only)

These give Mac users emacs-style line and word motion under Cmd/Option + arrow keys. They are active only when a terminal is focused **and** the platform is macOS.

| Command | macOS | Active when |
|---|---|---|
| Go to Line Start | `⌘←` | terminal focused, macOS |
| Go to Line End | `⌘→` | terminal focused, macOS |
| Go to Previous Word | `⌥←` | terminal focused, macOS |
| Go to Next Word | `⌥→` | terminal focused, macOS |

Under the hood these send terminal control sequences (Ctrl-A, Ctrl-E, Esc-b, Esc-f) to the focused terminal. See [Terminals](/docs/11-terminals/).

### Dialogs

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| Close Dialog | `Esc` | `Esc` | a dialog is open |

`Esc` closes the most recently opened dialog — unless that dialog is busy (for example, mid-operation), in which case it resists closing until the operation finishes.

### New Workspace

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| New Workspace Dialog | `⌘⌥N` | `Ctrl+Alt+N` | no dialog open |

See [Workspaces](/docs/07-workspaces/).

### Desktop app only

These are gated to the desktop app via `isDesktop` and have no effect in the browser.

| Command | macOS | Windows / Linux | Active when |
|---|---|---|---|
| [Open in External Editor](#open-in-external-editor-desktop-solo-mode) | `⌘⇧E` | `Ctrl+Shift+E` | no dialog open, desktop |
| Open Web Inspector | `⌘⌥I` / `F12` | `Ctrl+Alt+I` / `F12` | desktop |
| Zoom In | `⌘=` / `⌘Num+` | `Ctrl+=` / `Ctrl+Num+` | desktop |
| Zoom Out | `⌘-` / `⌘Num-` | `Ctrl+-` / `Ctrl+Num-` | desktop |
| Actual Size | `⌘0` / `⌘Num0` | `Ctrl+0` / `Ctrl+Num0` | desktop |
| Quit Application | `⌘Q` | `Ctrl+Q` | desktop |

> **Note:** "Open in External Editor" works only in **solo mode**. In a distributed Hub/Worker setup, the active tab's working directory lives on the Worker machine, not your local filesystem, so there is no local editor to launch and the command does nothing. See [Running LeapMux](/docs/17-running-leapmux/) for run modes, and [Open in external editor (desktop, solo mode)](#open-in-external-editor-desktop-solo-mode) below for the full feature.

The zoom, web-inspector, and quit shortcuts ("core" bindings) are mounted at the application root, so they work on every screen — including the launcher and the sign-in/setup pages — not just inside a workspace.

### Open in external editor (desktop, solo mode)

The `⌘⇧E` / `Ctrl+Shift+E` shortcut launches an external code editor against the active tab's working directory. It is one face of a feature that also lives in the desktop workspace title bar as a **split button**.

**Where it appears.** The split button shows in the title bar only when all three conditions hold: you are on the **desktop app in solo mode**, the active tab has a working directory, and at least one editor was auto-detected on your machine. It is hidden in the browser, in distributed mode, and when no editor is found.

**The two faces of the split button.**

- The **main face** reads "Open in {EditorName}" (with the editor's icon) when you have a preferred editor, or "Open in …" with a generic icon when you have not yet picked one. Clicking it — or pressing `⌘⇧E` / `Ctrl+Shift+E` — launches your preferred editor against the active tab's working directory. If no editor is preferred yet, clicking the main face opens the dropdown instead of launching.
- The **chevron** opens a dropdown that lists every detected editor alphabetically (with a checkmark on the current preferred one), followed by a separator and **"Refresh editor list"**.

> **Note:** Picking an editor from the dropdown only **sets it as your preferred editor** — it does not launch it. To actually open the editor, use the main face or the keyboard shortcut after selecting. "Refresh editor list" re-probes your machine for installed editors, useful after you install or remove one.

**What gets opened.** The editor is launched against the **active tab's working directory** and only against an absolute, existing directory — relative paths, missing paths, and plain files are rejected, so nothing launches if the active tab has no real directory.

**Detected editors.** LeapMux probes your `PATH`, known install locations, macOS `.app` bundles, and JetBrains Toolbox scripts. Detected editors include Visual Studio Code (and Insiders), VSCodium, Cursor, Windsurf, Sublime Text, Zed, the JetBrains IDEs (IntelliJ IDEA, WebStorm, GoLand, RustRover, PyCharm, PhpStorm, RubyMine, CLion, Rider, DataGrip, Android Studio, Fleet), and platform extras — Xcode on macOS, Notepad++ on Windows.

## Reading the macOS glyphs

| Glyph | Modifier |
|---|---|
| ⌘ | Cmd (`$mod` on macOS) |
| ⌃ | Control |
| ⌥ | Option / Alt |
| ⇧ | Shift |

A few key names also render specially: `Escape` shows as `Esc`, arrows as `← → ↑ ↓`, `NumpadAdd` as `Num+`, `NumpadSubtract` as `Num-`, and `Numpad0` as `Num0`. On macOS, modifiers always appear in the order ⌃⌥⇧⌘ with no separators (for example, `⌘⇧N`); on Windows and Linux they are joined with `+` (for example, `Ctrl+Shift+N`).

> **Tip:** You do not have to memorize these. LeapMux appends the active shortcut to tooltips and dropdown menu items automatically — for example, "New Agent (⌘N)" — formatted for your platform.

## Customizing keybindings

You can rebind, add, or remove shortcuts. There is currently **no graphical keybinding editor** in the Preferences dialog — its sections cover appearance (theme, terminal theme, diff view, turn-end sound), fonts, and debug logging, but there is no keybinding panel (see [Settings & Preferences](/docs/14-settings-and-preferences/)). Customization is done through **account-level overrides** stored as JSON.

### Where overrides live

Custom keybindings are stored on your user-preferences record in the Hub database (field `custom_keybindings_json`), not in browser storage. Because they are account-level, they follow you across browsers and devices when you sign in to the same account. The value is a JSON-encoded array of override objects:

```json
[
  { "command": "app.newAgent", "key": "$mod+Shift+a" },
  { "command": "app.closeActiveTab", "key": "" },
  { "command": "app.toggleHiddenFiles", "key": "$mod+k $mod+h" }
]
```

Each override has the shape:

| Field | Required | Meaning |
|---|---|---|
| `command` | yes | The command id to bind (for example `app.newAgent`). |
| `key` | yes | A `tinykeys` key string, or `""` (empty) to unbind. |
| `when` | no | A `when`-clause; inherits the default's clause if omitted. |

> **Warning:** Overrides are applied to **workspace** keybindings. The core desktop bindings (quit, zoom, web inspector) are not part of the override-merged set.

### How overrides merge with the defaults

For each command you override, LeapMux merges your overrides with the defaults using these rules:

- The default binding(s) for that command are **replaced** by all of your non-empty-key overrides for it.
- Each override **inherits the default's `when`-clause** unless it specifies its own `when`.
- You may give a command **multiple overrides** — it gets bound to every non-empty key you list.
- An override with `"key": ""` (empty) **unbinds** the command. If every override for a command has an empty key, the command is fully unbound.
- An override for a command **not** in the defaults is appended as a brand-new binding (with no inherited `when`-clause).

Changes take effect immediately: when your overrides change, LeapMux re-merges and re-binds the workspace shortcuts without a reload.

Override the command by its id. Each id corresponds to one of the actions in the [default tables above](#default-keyboard-shortcuts); the bindable workspace command ids are:

| Command id | Action |
|---|---|
| `app.newAgent` | New Agent |
| `app.newTerminal` | New Terminal |
| `app.closeActiveTab` | Close Active Tab |
| `app.newAgentDialog` | New Agent Dialog |
| `app.newTerminalDialog` | New Terminal Dialog |
| `app.newWorkspaceDialog` | New Workspace Dialog |
| `app.toggleFloatingTab` | Toggle Floating Tab |
| `app.refreshDirectoryTree` | Refresh Directory Tree |
| `app.toggleHiddenFiles` | Toggle Hidden Files |
| `app.switchToTab1` … `app.switchToTab9` | Switch to Tab 1–9 |
| `app.previousTab` / `app.nextTab` | Previous / Next Tab |
| `app.scrollActiveTabPageUp` / `app.scrollActiveTabPageDown` | Scroll Active Tab Up / Down One Page |
| `app.splitTileHorizontal` / `app.splitTileVertical` | Split Tile Horizontally / Vertically |
| `app.toggleLeftSidebar` / `app.toggleRightSidebar` | Toggle Left / Right Sidebar |
| `app.openPreferences` | Open Preferences |
| `app.openInExternalEditor` | Open in External Editor |
| `dialog.close` | Close Dialog |
| `chat.sendMessage` | Send Message |
| `terminal.lineStart` / `terminal.lineEnd` | Go to Line Start / End |
| `terminal.wordLeft` / `terminal.wordRight` | Go to Previous / Next Word |

> **Note:** Because there is no in-app keybinding editor yet, editing `custom_keybindings_json` is currently a JSON-only path through the preferences record. Set it carefully — an invalid JSON value is treated as "no overrides" and silently ignored, so your customizations simply will not apply if the JSON is malformed.

## Desktop (Tauri) accelerator differences

On the desktop app, a small number of shortcuts are mirrored onto the **native menu bar** as accelerators, but only on **macOS**. Windows and Linux render their own controls through the app's custom title bar instead.

Two commands get their accelerators synced onto the macOS menu:

- **Open Preferences** (`⌘,`) appears as **Preferences...** in the **LeapMux Desktop** application menu (the leftmost app menu on macOS).
- **Open Web Inspector** appears as **Open Web Inspector** in the Help submenu.

If you rebind either command via a custom keybinding, the macOS menu accelerator updates to match.

> **Warning:** **Chords cannot become macOS menu accelerators.** A custom binding that uses a space-separated chord (such as `$mod+k $mod+s`) still works as a regular shortcut, but it will not appear as an accelerator on the native menu item.

The macOS menu also provides standard system items with their OS-default accelerators — About LeapMux Desktop, Services, Hide, Quit (the **LeapMux Desktop** application menu); Undo/Redo/Cut/Copy/Paste/Select All (Edit menu); Toggle Full Screen (View menu); and Minimize/Zoom/Close (Window menu). These come from macOS, not from the LeapMux shortcut registry, and are not customizable through `custom_keybindings_json`.

## Chat editor keys (not part of the global system)

The chat message editor has its own in-editor key handling that is **separate** from the global shortcut registry described above. The most important of these is how Enter behaves, governed by the **Enter key mode** preference (a per-browser setting; the default is Cmd/Ctrl+Enter sends):

- **Cmd/Ctrl+Enter sends (default):** `⌘Enter` / `Ctrl+Enter` sends the message; a plain `Enter` inserts a newline.
- **Enter sends:** a plain `Enter` sends the message; `Shift+Enter` inserts a newline.

You can flip between the two with the toggle button in the chat editor toolbar. Other editor keys handle Markdown structure inside the message box — for example, `Tab`/`Shift+Tab` adjust list and heading levels, `Backspace` lifts blockquotes and code blocks, and `Cmd/Ctrl+E` toggles inline code. These editor keys are not bindable through `custom_keybindings_json`.

For more on writing and sending messages to agents, see [Coding Agents](/docs/09-coding-agents/).

## See also

- [Tabs & Layout](/docs/08-tabs-and-layout/) — the tabs, tiles, and sidebars these shortcuts drive.
- [Terminals](/docs/11-terminals/) — terminal behavior and the macOS cursor-motion shortcuts.
- [File Browser](/docs/12-file-browser/) — the directory tree refreshed and filtered by the Files shortcuts.
- [Settings & Preferences](/docs/14-settings-and-preferences/) — the Preferences dialog and where account vs. browser settings live.
- [Running LeapMux](/docs/17-running-leapmux/) — solo vs. distributed mode, which affects the "Open in External Editor" shortcut.
