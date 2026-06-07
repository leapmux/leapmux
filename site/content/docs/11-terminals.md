---
title: "Terminals"
type: docs
weight: 11
---

LeapMux gives you full shell terminals that run on a Worker and stream into your Frontend (browser or desktop app) over the same end-to-end-encrypted channel as your agents. A terminal is a tab, just like an agent or a file viewer — you can tile it, float it, move it between workspaces, and it survives page refreshes and reconnects. A Worker restart is the one thing its live shell can't outlive, but LeapMux preserves the terminal's last screen, so the tab comes back exactly where it left off and is one keystroke from restarting. This chapter covers how to open a terminal, how the shell list is built, how to use the terminal view, how persistence works, and how every terminal is automatically wired for remote control.

For the bigger picture of tabs, tiling, and layout, see [Tabs & Layout](/docs/08-tabs-and-layout/). For the git side of opening a terminal in a worktree or branch, see [Worktrees & Branches](/docs/10-worktrees-and-branches/).

## What a LeapMux terminal is

When you open a terminal, the Worker spawns your chosen shell as an interactive login shell (for example `bash -i -l` or `zsh -i -l`), connects it to a real pseudo-terminal (PTY), and streams its output to an [xterm.js](https://xtermjs.org/) view in your tab. The shell and all of its child processes run on the Worker machine — the same machine where your code and git repositories live — not in your browser.

Every spawned shell gets:

- `TERM=xterm-256color` so colour-aware programs render correctly.
- A set of `LEAPMUX_REMOTE_*` environment variables (see [Driving LeapMux from inside a terminal](#driving-leapmux-from-inside-a-terminal-remote-control)).
- A process kill group, so closing the tab reaps the whole process tree (the shell and everything it started) rather than leaking orphans.

Each terminal is given an auto-generated title of the form **`Terminal <Name>`** (for example `Terminal Aaliyah` or `Terminal Zoe`), drawn from a fixed pool of names. You can rename it at any time (see [Renaming a terminal](#renaming-a-terminal)).

## Opening a terminal

There are three ways to open a terminal from the UI, plus a CLI path.

### Quick action: the New terminal button

The tab bar has a terminal icon button (the Lucide `Terminal` glyph). Its tooltip depends on context:

- **"New terminal at the current working directory"** when there is an active tab to inherit a Worker and working directory from.
- **"New terminal..."** otherwise.

The default keyboard shortcut is **`Cmd/Ctrl+T`** (command `app.newTerminal`, active only when no dialog is open).

The button is context-aware:

- If you already have a Worker and working directory in scope (because another tab is active), it opens a terminal **immediately** using that Worker's **default shell**, anchored to that working directory. A later restart returns to the same directory.
- If there is no Worker or working directory in scope, it instead opens the full **New terminal** dialog so you can pick one.

### Shell picker: the overflow menu

The tab bar's overflow ("More options") menu has a **"Terminals"** section. It contains:

- **"New terminal..."** — opens the full dialog.
- One entry per shell that the current Worker reports as available, each shown as a `<code>` path. The Worker's default shell is annotated with **"(default)"**.

Clicking a specific shell opens a terminal with exactly that shell, skipping the dialog.

### The full New terminal dialog

Open the dialog with **`Cmd/Ctrl+Shift+T`** (command `app.newTerminalDialog`, active only when no dialog is open) or via either **"New terminal..."** menu item. Its title is **"New terminal"**.

The dialog has these fields:

| Field | What it does |
| --- | --- |
| **"Worker"** | Selects which Worker spawns the shell. Options show `name (version, os, arch)`. A **"Refresh workers"** button re-queries online Workers. When none are connected: **"No workers online"**. |
| **"Shell"** | Picks the shell binary. See [Shell selection](#shell-selection). |
| **"Working Directory"** | Browses the Worker's filesystem (tree root is `~`). Includes a show/hide-hidden-files toggle and a **"Refresh directory tree"** button. When no Worker is selected: **"No workers online. Connect a worker to browse directories."** |
| **"Git options"** | Appears in the right column when the selected path is (or becomes) a git repository. Lets you open the terminal in a branch or worktree. See [Git options](#git-options-open-a-terminal-in-a-branch-or-worktree). |

Submit with the **"Create"** button (it reads **"Creating..."** while in flight); cancel with **"Cancel"**. The Create button stays disabled until you have a Worker, a non-blank working directory, a selected shell, a valid git-mode choice, and a workspace. If creation fails you'll see **"Failed to create terminal"**.

> **Note:** The dialog sends placeholder terminal dimensions of 80 columns by 25 rows. The real size is sent to the Worker the moment the terminal view mounts and measures itself, so you never see an 80x25 terminal in practice.

### Opening a terminal from the CLI

You can create a terminal from a script or another agent with the [Remote Control CLI](/docs/16-remote-control-cli/):

```bash
leapmux remote tab open --type terminal \
  --worker-id <worker> \
  --working-dir /home/me/project \
  --shell /bin/zsh
```

`--shell` is optional — leaving it empty uses the Worker's default shell. `--shell-start-dir` defaults to the working directory. See [Remote Control CLI](/docs/16-remote-control-cli/) for the full flag set, entity-ID resolution, and placement flags.

> **Note:** There is **no `--remote-enabled` flag**. Remote control is wired into every terminal automatically — see [Driving LeapMux from inside a terminal](#driving-leapmux-from-inside-a-terminal-remote-control).

## Shell selection

The **Shell** dropdown is populated per-Worker by querying the Worker for the shells it has installed. While the list is loading it shows **"Loading shells..."**; if the Worker reports none it shows **"No shells available"**. Each option shows the shell's path, and the default shell is labelled `<path> (default)`.

The shell list is per-Worker. Switching organization or workspace while staying on the same Worker does **not** re-fetch it; switching to a different Worker does, and resets any shell override you had selected.

### How the Worker builds the list

1. The Worker resolves its **default shell** (see below) and places it **first** in the list.
2. It then probes a fixed set of well-known shells — `sh`, `bash`, `zsh`, `fish`, `pwsh`, `powershell` — resolving each against `PATH`. Any that are installed are added, skipping the one that is already the default.

Two names that resolve to the same binary (for example `sh` and `bash` on many systems) are kept as **separate** entries, because invoking a shell as `sh` activates its POSIX mode — the distinction is intentional.

### Default shell resolution

When you don't choose a shell explicitly, the Worker uses its default, resolved in this order:

1. The `LEAPMUX_DEFAULT_SHELL` environment variable (accepts a bare name like `zsh` resolved via `PATH`, or an absolute path like `/bin/zsh`).
2. The `SHELL` environment variable.
3. Platform detection:
   - **macOS:** the user's login shell from `dscl`, falling back to `/bin/zsh`.
   - **Linux:** the user's shell from `/etc/passwd`, falling back to `/bin/sh`.
   - **Windows:** `pwsh`, then `powershell`, falling back to the bundled Windows PowerShell.
   - **Other platforms:** `/bin/sh`.

> **Tip:** To force a specific default shell for every terminal a Worker spawns, set `LEAPMUX_DEFAULT_SHELL` in the Worker's environment. See [Configuration](/docs/18-configuration/).

### Login-shell flags

The Worker invokes each shell with interactive-login flags appropriate to that shell. Most POSIX shells get `-i -l` (interactive login). PowerShell Core (`pwsh`) gets `-Login`; classic Windows PowerShell 5.1 gets none (it has no `-Login`); `cmd` gets `/D`; `tcsh`/`csh` get `-l`.

## Git options: open a terminal in a branch or worktree

When the working directory is inside a git repository, the **Git options** panel offers the same five modes used when opening an agent or a workspace — use current state, switch to branch, create new branch, create new worktree, or use existing worktree. The modes, their fields, branch-name validation, the worktree path formula, and the dirty-tree warnings are all covered in depth in [Worktrees & Branches](/docs/10-worktrees-and-branches/).

The terminal's tab is grouped in the sidebar under its repository and branch. If a terminal owns a worktree it created, closing its last tab can offer to remove that worktree (see [Closing a terminal](#closing-a-terminal)).

## Using the terminal

The terminal view is a real xterm.js terminal: it renders 256-colour output, supports full-screen ("alt screen") TUIs like `vim`, `htop`, and `tmux`, and accepts mouse interaction where the running program supports it.

### Copy and paste

Selection uses **copy-on-select**: highlighting text in the terminal automatically copies it to the clipboard (the same behaviour as iTerm2's "Copy on Select"). Empty selections are ignored. You don't need a dedicated copy shortcut. Paste using your platform's standard paste gesture.

### Scrollback

The terminal keeps scrollback you can scroll through with your mouse or trackpad. Two shortcuts page the **active** terminal:

| Shortcut | Command | Action |
| --- | --- | --- |
| **Alt+PageUp** | `app.scrollActiveTabPageUp` | Scroll up one page |
| **Alt+PageDown** | `app.scrollActiveTabPageDown` | Scroll down one page |

### macOS line/word navigation

On macOS, when a terminal is focused, these shortcuts send the correct escape sequences to the shell:

| Shortcut | Action |
| --- | --- |
| `Cmd+Left` / `Cmd+Right` | Move to start / end of line |
| `Alt+Left` / `Alt+Right` | Move one word left / right |

### Resizing

The terminal automatically fits its tile. When you resize the tile or window, LeapMux measures the new dimensions and tells the Worker, which resizes the PTY so the running program re-flows correctly. A resize is only sent when the column or row count actually changes, so you won't see spurious prompt redraws from minor pixel shifts. The Worker-side resize is skipped for terminals that have exited, disconnected, or failed to start — there is no live PTY to notify — but the view still re-flows the existing buffer locally so dead output stays readable.

### Appearance

The terminal follows your appearance preferences. The default monospace font is `"Hack NF", Hack, "SF Mono", Consolas, monospace` at size 13, and follows your monospace-font preference. The terminal theme has three pill options under the **"Terminal Theme"** heading in Appearance settings:

| Option | Value | Behaviour |
| --- | --- | --- |
| **"Match UI"** | `match-ui` (default) | Follows the UI theme and your OS light/dark preference |
| **"Dark"** | `dark` | Always dark (the "Dimidium" scheme) |
| **"Light"** | `light` | Always light (the "Dimidium Light" scheme) |

See [Settings & Preferences](/docs/14-settings-and-preferences/) for fonts, themes, and other appearance options.

## Terminal status indicators

A terminal moves through several states, reflected both in the terminal pane and in the tab label.

| Status | Meaning |
| --- | --- |
| Starting | The PTY is being spawned. |
| Ready | The PTY has spawned and the view can mount. |
| Startup failed | The shell could not be spawned. |
| Disconnected | The connection to the terminal's Worker was lost. |
| Exited | The shell process exited. |

How each state appears:

- **Starting:** a centered spinner with a per-shell label like **"Starting zsh…"** (falling back to **"Starting terminal…"**). When you open a terminal with git options, the label may instead describe the git work, for example `Creating worktree "feature/x"…`. The spinner stays until the terminal has actually painted visible content, not merely until the PTY spawns.
- **Startup failed:** a full-pane error titled **"Terminal failed to start"** with the Worker's error message.
- **Disconnected:** the tab label is faded.
- **Exited:** the tab label is faded **and** struck through.

If a program in a background (non-active) terminal rings the terminal bell, that tab gets a notification indicator so you notice it.

## Persistence and reattachment

Terminals are durable. Refresh the page, switch workspaces, or lose and regain your connection, and the live shell keeps running on the Worker — the Frontend simply reattaches. A Worker restart is different: the shell process can't survive it, but the terminal's last screen is preserved, so the tab comes back showing where it left off and can be restarted.

This works because the Worker keeps a rolling **100 KB screen buffer** for each terminal and also persists the terminal (its working directory, shell, title, dimensions, and last-seen screen) to its database:

- **Page refresh / tab re-mount:** the Frontend re-fetches the saved screen and resumes streaming from where it left off, so a full-screen TUI redraws correctly rather than showing a blank pane.
- **Workspace switch:** the on-screen contents (viewport plus scrollback) are captured when you switch away, so switching back restores exactly what was showing.
- **Worker restart:** the running shell cannot survive the Worker going down, but because the terminal and its last screen are persisted to the database, the terminal is still listed when the Worker returns — showing its final screen — and pressing **Enter** restarts the shell.

> **Note:** Byte-for-byte replay restores text, full-screen mode, cursor visibility, autowrap, bracketed paste, mouse tracking, and the window title. A few transient attributes — current text colour/bold/italic, scrolling regions, and a saved cursor position — are not replayed; they self-correct as soon as the program next sets them, and content older than the 100 KB window scrolls off.

### When a shell exits

When the shell process exits, the Worker writes a notice into the screen so you can see it and so it persists:

```text
[Terminal process exited (0) - Press Enter to restart]
```

If the Worker was disconnected or forcibly shut down (so the exit code is unknown) the notice instead reads:

```text
[Worker disconnected - Press Enter to restart]
```

On an exited terminal, **Enter** is the only key that does anything — it restarts the shell. All other input is ignored. A restart reuses the terminal's saved working directory, shell, and start directory, mints fresh remote-control credentials, and preserves the existing screen so the new prompt appears below the exit notice. If a restart can't proceed you'll see **"Failed to restart terminal"** (for example, the Worker reports the terminal is still running).

## Driving LeapMux from inside a terminal (remote control)

> **Note:** There is no "remote-enabled" checkbox or toggle in the New terminal dialog, the CLI, or anywhere else. **Every** terminal LeapMux spawns is remote-enabled automatically (as long as the Worker has remote control configured). This is a frequent point of confusion — there is nothing to turn on.

When the Worker spawns your shell, it injects a set of `LEAPMUX_REMOTE_*` environment variables that let any script or program running inside the terminal drive LeapMux through the [`leapmux remote`](/docs/16-remote-control-cli/) CLI — without needing to log in separately. The CLI detects these variables and routes its calls over a local socket the Worker provides, scoped to the terminal's own identity.

The variables injected into a terminal are:

| Variable | When set | Meaning |
| --- | --- | --- |
| `LEAPMUX_REMOTE_SOCK` | Always | Local IPC socket the CLI connects to |
| `LEAPMUX_REMOTE_TOKEN` | Always | Per-spawn bearer token for that socket |
| `LEAPMUX_REMOTE_USER_ID` | Always | The authenticated user |
| `LEAPMUX_REMOTE_WORKER_ID` | Always | The host Worker |
| `LEAPMUX_REMOTE_ORG_ID` | When known | The organization |
| `LEAPMUX_REMOTE_TAB_ID` | When known | This terminal's tab id |
| `LEAPMUX_REMOTE_TAB_TYPE` | When known | `terminal` |
| `LEAPMUX_REMOTE_WORKING_DIR` | When known | The working directory at spawn |

Because these are set, `leapmux remote` commands run inside the terminal default their entity IDs from the environment. For example, this works with no flags from inside the terminal:

```bash
# Who am I, and where?
leapmux remote whoami

# Open a sibling terminal next to this one
leapmux remote tab open --type terminal --last
```

> **Note:** Workspace id and tile id are deliberately **not** injected. The CLI derives them from `LEAPMUX_REMOTE_TAB_ID` at call time, which keeps them correct even if you move the tab. Terminals also do **not** get `LEAPMUX_REMOTE_AGENT_PROVIDER` (that is agents-only).

Any pre-existing `LEAPMUX_REMOTE_*` values are stripped before the Worker re-injects its own, so a terminal opened from inside another agent or terminal targets itself, not its parent. The per-spawn token is retired when the terminal is closed and re-minted on restart.

### Controlling a terminal from outside

The reverse also works: from any authenticated `leapmux remote` session (or from another agent), you can write to and inspect a terminal:

```bash
# Type a command into a terminal's PTY (note the trailing newline to run it)
leapmux remote terminal send --tab-id <tab> --data $'ls -la\n'

# Pipe binary or escape sequences in via stdin
printf '\x03' | leapmux remote terminal send --tab-id <tab> --stdin

# Inspect a terminal's metadata, or dump its current screen with ANSI intact
leapmux remote terminal get --tab-id <tab>
leapmux remote terminal get --tab-id <tab> --screen

# List a worker's available shells (and its default)
leapmux remote terminal shells --worker-id <worker>
```

See [Remote Control CLI](/docs/16-remote-control-cli/) for the complete terminal subcommand reference, authentication, and the JSON output contract.

## Renaming a terminal

A terminal's title updates automatically when a program sets the terminal window title (the standard OSC title escape sequence) — for example, many shells set it to the current directory or running command. You can also rename a terminal tab through its tab menu, or from a script:

```bash
leapmux remote tab rename --tab-id <tab> --title "Build watcher"
```

## Closing a terminal

Closing a terminal tab removes it from your layout immediately and tells the Worker to tear down the PTY and reap the shell's whole process tree. If the close fails on the Worker side you'll see **"Failed to close terminal"**, but the tab is already gone from your view.

If the terminal you're closing is the **last** tab for a worktree, or the last non-worktree tab on a branch with unsaved work — uncommitted changes, unpushed commits, or a branch that was never pushed to a remote — LeapMux shows the **"Close last tab"** confirmation so you don't lose work. From there you can push, close anyway, or — for a worktree — schedule the worktree for removal. This flow is described in full in [Worktrees & Branches](/docs/10-worktrees-and-branches/).

## Keyboard shortcuts

| Shortcut | Command | Action |
| --- | --- | --- |
| `Cmd/Ctrl+T` | `app.newTerminal` | Open a terminal at the current working directory (or the dialog if there's no context) |
| `Cmd/Ctrl+Shift+T` | `app.newTerminalDialog` | Open the New terminal dialog |
| `Cmd/Ctrl+W` | `app.closeActiveTab` | Close the active tab |
| `Alt+PageUp` / `Alt+PageDown` | `app.scrollActiveTabPageUp` / `...PageDown` | Page the active terminal's scrollback |
| `Cmd+Left` / `Cmd+Right` (macOS) | `terminal.lineStart` / `terminal.lineEnd` | Jump to line start / end |
| `Alt+Left` / `Alt+Right` (macOS) | `terminal.wordLeft` / `terminal.wordRight` | Move one word left / right |

All of these are customizable. See [Keyboard Shortcuts](/docs/15-keyboard-shortcuts/) for the full keybinding system and how to remap commands.

## See also

- [Tabs & Layout](/docs/08-tabs-and-layout/) — tiling, floating, and moving terminal tabs.
- [Worktrees & Branches](/docs/10-worktrees-and-branches/) — git options, worktree creation, and the close-last-tab flow.
- [Coding Agents](/docs/09-coding-agents/) — agents share the same tab, Worker, and git-options model.
- [Remote Control CLI](/docs/16-remote-control-cli/) — the full `leapmux remote terminal` and `tab` command surface.
- [Settings & Preferences](/docs/14-settings-and-preferences/) — terminal theme and fonts.
- [Keyboard Shortcuts](/docs/15-keyboard-shortcuts/) — remap any of the shortcuts above.
