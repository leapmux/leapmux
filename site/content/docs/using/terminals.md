---
title: "Terminals"
description: "Run interactive shell terminals and per-directory Quake panels on any Worker in LeapMux, with automatic remote control and reconnect persistence."
type: docs
weight: 5
---

LeapMux gives you full interactive shell terminals running on any Worker and streamed into your browser or desktop app over the same end-to-end encrypted (E2EE) channel as your coding agents. Terminal tabs tile, float, move across workspaces, survive page refreshes, and automatically wire up the `leapmux` control CLI inside every shell.

For tiling and tab management, see [Tabs & Layout](/docs/using/tabs-and-layout/). For git worktree creation, see [Worktrees & Branches](/docs/using/worktrees-and-branches/).

## How LeapMux terminals work

{{< screenshot src="terminal-pane" alt="LeapMux integrated PTY terminal running git status and go test with colored output" caption="Integrated PTY terminals run full interactive shells on the Worker alongside your agents." width="496" height="450" >}}

When you open a terminal, the Worker spawns your chosen shell inside a real pseudo-terminal (PTY) on the Worker machine — right beside your code and repositories — and streams it to an xterm.js view supporting 256-color output, mouse events, and full-screen terminal user interfaces (TUIs) like `vim`, `htop`, and `tmux`.

Every spawned shell receives:

- `TERM=xterm-256color` for full color support.
- Automatic `LEAPMUX_CONTROL_*` environment variables for calling `leapmux` without logging in (see [Automatic remote control](#automatic-remote-control)).
- A dedicated process group so closing the tab terminates the shell and all of its child processes cleanly.

New terminals receive a generated title (`Terminal <Name>`) that updates automatically when programs emit standard window-title escape sequences. You can also double-click the tab to rename it manually, or run `leapmux tab rename`.

## Opening a terminal

- **Quick open (`⌘T` / `Ctrl+T` or the tab-bar terminal icon):** When an active tab already establishes a Worker and working directory, opens a terminal immediately in that directory using the Worker's default shell. Otherwise opens the full dialog.
- **Specific shell (`+` tab-bar menu):** Under **Terminals**, pick any shell installed on the current Worker (the default is marked **(default)**) to open it immediately, or choose **New terminal...**.
- **Full dialog (`⇧⌘T` / `Ctrl+Shift+T`):** Opens **New terminal** to choose every option explicitly.

{{< screenshot src="new-terminal-dialog" alt="LeapMux New terminal dialog showing Worker, Shell with default marker, Title, Working Directory tree, and Git options" caption="The New terminal dialog selects a Worker, shell binary, working directory, title, and optional git branch or worktree." width="996" height="792" >}}

| Field | What it does |
| --- | --- |
| **Worker** | Selects which online Worker hosts the shell. |
| **Shell** | Picks any shell discovered on the Worker, with a refresh button to re-scan. |
| **Working Directory** | Browses the Worker's filesystem or accepts a typed path, with **Go to home directory**, hidden-files toggle, and a Windows drive selector. |
| **Title** | Tab title (pre-filled with a random `Terminal <Name>`). |
| **Git options** | Appears inside git repositories to switch branches or launch in a new or existing linked worktree. |

To open terminals from scripts or agents, use `leapmux terminal open` (including `--command` to run an initial command inside the interactive shell after startup files load). See [CLI Guide](/docs/using/cli-guide/) and [Control Command Reference — Terminals](/docs/reference/commands/terminals/).

## Shell discovery and default shell

Each Worker builds its shell list by putting its default shell first, then scanning `PATH` for `sh`, `bash`, `zsh`, `fish`, `nu` (or `nushell`), `elvish`, `pwsh`, and `powershell`. Shells invoked in distinct modes (such as `sh` vs. `bash`) appear as separate entries.

When you do not pick a specific shell, the Worker selects its default in this order:

1. `LEAPMUX_DEFAULT_SHELL` (name on `PATH` or absolute path)
2. `SHELL`
3. Platform default: macOS login shell from `dscl` (fallback `/bin/zsh`), Linux shell from `/etc/passwd` (fallback `/bin/sh`), Windows `pwsh` then `powershell`

The Worker launches each shell with its interactive login flags (`-i -l` for most POSIX shells, `-Login` for `pwsh`, `/D` for `cmd`, `-l` for `csh`/`tcsh`, and no login flag for classic Windows PowerShell 5.1).

## Using the terminal

- **Copy and paste:** Selecting text automatically copies it to your clipboard (copy-on-select); paste with your system shortcut.
- **Scrollback and resizing:** Scroll with the mouse or trackpad, or page with `⌥PageUp` / `Alt+PageUp` and `⌥PageDown` / `Alt+PageDown`. Resizing a tile automatically resizes the PTY on the Worker. On macOS, line and word navigation keys (`⌘←`, `⌘→`, `⌥←`, `⌥→`) send standard shell movement sequences; see [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/#terminal).
- **Themes and fonts:** Terminals use your monospace font stack (defaulting to bundled **Hack NF**) and follow your app theme by default (**Match UI**), or can use an independent palette and renderer. Configure appearance and renderer backend in [Settings & Preferences](/docs/using/settings/#appearance).
- **Status indicators:** Tabs show a spinner while starting (including git worktree setup), a faded label when the Worker disconnects, a faded struck-through label after the shell exits, and a notification dot when a background terminal rings the bell.

## Quake-mode terminal

{{< screenshot src="quake-terminal" alt="LeapMux translucent Quake-mode terminal sliding down from the top over tiled agent and diff panes running git diff --stat" caption="Press ⌃` / Ctrl+` from any tab to slide down a translucent Quake-mode terminal bound to that tab's working directory." width="948" height="900" >}}

A **Quake-mode terminal** slides over the center canvas without changing your tile layout. It is keyed to the active tab's **(Worker, working directory)** pair, so any agent, terminal, file, or image tab in that directory shares the same persistent shell.

- **Toggle:** Press ``⌃` `` / ``Ctrl+` `` (using `Control` on macOS as well, since macOS reserves ``⌘` `` for app windows). The binding follows the physical key below `Esc` across keyboard layouts; on Japanese (JIS) keyboards where that key triggers input-method switching, remap it in **Preferences → Keyboard Shortcuts**. Unbound **Open Quake Terminal** and **Close Quake Terminal** actions are also available to bind separately.
- **Shared shell, per-device visibility:** Every device signed in to your account attaches to the same running shell and scrollback for that directory, while whether the panel is currently slid open stays local to each screen.
- **Lifecycle:** The shell keeps running while hidden and survives page refreshes. It closes only when you exit the shell (`exit` or `Ctrl+D`) or when no active (non-archived) tabs remain in that directory across all workspaces. When it ends, the panel retracts and the next toggle starts a fresh shell.
- **Position, size, animation, and opacity:** Configure all four Quake settings under **Preferences → Terminal** (see [Settings & Preferences](/docs/using/settings/#terminal)).
- **CLI control:** Run `leapmux terminal quake open`, `close`, or `toggle` to control the panel from scripts (`--dir` defaults to `$LEAPMUX_CONTROL_WORKING_DIR`).

## Persistence, exit, and restart

Live shells keep running on the Worker across browser reloads, workspace switches, and temporary disconnects; reconnecting replays the preserved screen so full-screen apps redraw cleanly. If the Worker itself restarts, the running shell cannot survive, but the tab keeps its final preserved screen so you can see where you left off.

When a shell exits, LeapMux appends an exit notice with the exit code (also readable via `leapmux terminal get` and `leapmux terminal wait --exit`). On an exited terminal tab, press **`Enter`** to start a fresh shell in the same working directory with newly minted remote-control credentials while preserving prior output above the new prompt.

## Automatic remote control

{{< callout type="info" >}}
Remote control requires no checkbox or setup. Every terminal LeapMux spawns is automatically wired to the `leapmux` CLI whenever Worker remote control is enabled.
{{< /callout >}}

The Worker injects `LEAPMUX_CONTROL_*` environment variables — including `LEAPMUX_CONTROL_CLI` pointing at the Worker's matching `leapmux` binary — and strips any inherited parent variables so nested terminals always target themselves. For the complete environment variable table and CLI version-matching rules, see [CLI Guide — Inside a managed tab](/docs/using/cli-guide/#inside-a-managed-tab).

One intentional difference applies inside a **Quake-mode terminal**: because a Quake panel belongs to a directory rather than a single tab, it receives `LEAPMUX_CONTROL_TERMINAL_ID`, `LEAPMUX_CONTROL_WORKING_DIR`, and `LEAPMUX_CONTROL_WORKER_ID`, and omits `LEAPMUX_CONTROL_TAB_ID` and `LEAPMUX_CONTROL_TAB_TYPE`. Commands targeting the panel itself (`leapmux terminal get`, `leapmux terminal quake close`, `leapmux git status`) work with no arguments, while commands targeting a specific tab or agent require an explicit target argument instead of `self`.

From outside a terminal, scripts and agents can run commands (`leapmux terminal run`), send keys (`leapmux terminal send --key`), read rendered text (`leapmux terminal read`), and wait without polling (`leapmux terminal wait`). See [CLI Guide](/docs/using/cli-guide/) and [Control Command Reference — Terminals](/docs/reference/commands/terminals/).

## Closing a terminal

Closing a terminal tab removes it from your layout and stops the shell and its child process tree on the Worker. Closing a busy terminal triggers the two-click **Close tab** confirmation (see [Tabs & Layout](/docs/using/tabs-and-layout/#closing-tabs)), and closing the last tab on a worktree or dirty branch opens the **Close last tab** dialog (see [Worktrees & Branches](/docs/using/worktrees-and-branches/#dirty-worktree-protection-when-closing-tabs)).

## Related chapters

- [CLI Guide](/docs/using/cli-guide/) — `LEAPMUX_CONTROL_*` variables, zero-polling terminal waits, and remote control.
- [Settings & Preferences](/docs/using/settings/) — terminal themes, renderer backend, fonts, and Quake settings.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — terminal navigation, scrollback, and Quake bindings.
- [Worktrees & Branches](/docs/using/worktrees-and-branches/) — opening terminals in git worktrees.
