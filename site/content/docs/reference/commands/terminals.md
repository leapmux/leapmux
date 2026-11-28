---
title: "Terminals"
description: "Reference for leapmux terminal commands: open, type into, read, resize, and manage terminals."
type: docs
weight: 2
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [CLI Guide](/docs/using/cli-guide/) for target selection, output formatting, and authentication.

## Common arguments and flags

These selectors and flags repeat across commands on this page.

| Selector / Flag | Description |
|---|---|
| `<terminal>` | A terminal id, a unique prefix of 4 or more characters of one, "id:&lt;id&gt;" for an id or id prefix, "title:&lt;title&gt;" or a bare case-insensitive title, or "self" for the terminal that runs this command. |
| `--workspace string` | narrow target lookup to this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--exit` | wait until the shell of the terminal ends, and print its exit code |
| `--regex string` | wait until a line of the terminal matches this regular expression, in RE2 syntax |
| `--change` | wait until the visible screen text changes, and print the updated screen |
| `--timeout duration` | stop waiting after this long, such as 90s or 10m; 0 waits without a limit (default: 0s) |
| `--worker string` | worker: an id, a unique id prefix, "name:&lt;name&gt;" or "self" (default: $LEAPMUX_CONTROL_WORKER_ID) |
| `--dir string` | the directory whose quake terminal to act on (default: $LEAPMUX_CONTROL_WORKING_DIR) |

## `leapmux terminal`

Open, type into, read and resize terminals.

Aliases: `term`.

### `leapmux terminal list`

List your terminals.

_Aliases: `ls`._

```text
leapmux terminal list [flags]
```

| Flag | Description |
|---|---|
| `--workspace string` | list only the terminals of this workspace (an id, a prefix, "title:&lt;title&gt;" or "self") |
| `--worker string` | list only the terminals hosted by this worker (an id, a prefix, "name:&lt;name&gt;" or "self") |
| `--state string` | list only the terminals in this status (one of: starting, ready, startup_failed, exited) |

```sh
leapmux terminal list
```

### `leapmux terminal get`

Show a terminal's status, size and directory (default: this terminal).

```text
leapmux terminal get [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal get
leapmux terminal get 4cbb3f34
```

### `leapmux terminal open`

Open a new terminal in a new tab. Without --workspace and --tile, the new tab opens beside the tab that runs leapmux.

--command runs a command in the original interactive shell after its startup files run. On Unix, use Bash, zsh, fish, sh, Dash, Elvish, Nushell, Xonsh, or Yash. PowerShell also works. If Elvish has a startup file, its directory must allow the Worker to create a temporary link. The Worker reports a startup failure if the shell cannot acknowledge the handoff. A restart of the terminal does not run it again. If the Worker stops after claiming the command, recovery reports an unverified outcome without running it again. The command is the value of --command, or "-" to read it from stdin. Read its output with "terminal read", or wait for it with "terminal wait".

_Mutates state._

```text
leapmux terminal open [flags]
```

| Flag | Description |
|---|---|
| `--command string` | evaluate this command in the original shell after startup ("-" reads stdin) |
| `--shell string` | shell to start (default: the worker's default shell) |
| `--start-dir string` | directory the shell starts in (default: --dir) |
| `--title string` | tab title (default: the worker picks one) |
| `--wait` | wait after opening until --exit, --text, --regex, or --change holds |
| `--exit` | See [common arguments and flags](#common-arguments-and-flags). |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--change` | See [common arguments and flags](#common-arguments-and-flags). |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--dir string` | working directory of the terminal (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worktree string` | create a git worktree on this new branch and start in it |
| `--use-worktree string` | start in an existing git worktree by path, path:&lt;path&gt;, or branch:&lt;branch&gt; |
| `--checkout string` | check out this existing branch first |
| `--new-branch string` | create this branch and check it out first |
| `--base string` | the branch that --worktree or --new-branch starts from (default: the current branch) |
| `--workspace string` | workspace of the new tab (default: the workspace of this tab); the tab lands on its first tile unless --tile says otherwise |
| `--tile string` | tile of the new tab (default: the tile of this tab) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--first` | put the new tab first on the destination tile |
| `--last` | put the new tab last on the destination tile (default) |
| `--before string` | put the new tab right before this tab |
| `--after string` | put the new tab right after this tab |
| `--focus` | reveal and focus the new tab in open LeapMux windows |
| `--no-focus` | keep the new tab in the background without focusing it |

```sh
# Open a terminal beside this tab and keep its id.
T=$(leapmux terminal open -q)

# Open a terminal in a new git worktree.
leapmux terminal open --worktree try-fix --dir ~/src/app

# Run the tests in a new terminal and wait until its shell exits.
leapmux terminal open --command 'make test; exit' --wait --exit
```

### `leapmux terminal send`

Type text into a terminal, then press keys. The text reaches the shell as it is, with no Enter added: add --key enter, or use "terminal run". The text is the second argument, "-" to read it from stdin, or --file. A text that starts with "-" needs "--" before it.

--key presses a key after the text. Repeat it to press several keys in order. The Worker encodes each key for the modes that the program in the terminal enabled, as the browser does, so "up" moves the cursor in a shell and in a full-screen program alike. A modifier on a cursor, editing or function key takes xterm's encoding, such as ESC [1;5D for ctrl-left. Key names: enter, tab, escape or esc, backspace, space, up, down, right, left, home, end, insert, delete, pageup, pagedown, f1 to f12, or one printable ASCII character, after any of ctrl-, alt- and shift-, such as ctrl-c, alt-b, shift-tab or ctrl-left; the names are case-insensitive, and a character keeps its case.

--paste sends the text as the browser sends a paste: each line ending becomes a carriage return, and when the program enabled bracketed paste, the text arrives between the paste markers, so the program inserts it instead of running each line. A paste end marker inside the text is removed, so the text cannot end its own paste.

The text and the keys travel in one request, so no other input can arrive between them.

_Mutates state._

```text
leapmux terminal send <terminal> [text] [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the bytes from this file ("-" reads stdin) |
| `--key string` | press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order (repeatable) |
| `--paste` | send the text as a paste, between bracketed-paste markers when the program enabled them |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal send 4cbb3f34 $'make test\n'

# Stop the program in the foreground.
leapmux terminal send 4cbb3f34 --key ctrl-c

# Choose the second entry of a menu.
leapmux terminal send 4cbb3f34 --key down --key enter

# Paste a file into the program, as the browser pastes it.
leapmux terminal send 4cbb3f34 --paste --file snippet.py

# Type the bytes of a file, such as escape sequences.
leapmux terminal send 4cbb3f34 --file keys.bin
```

### `leapmux terminal run`

Run a command in a terminal: send the command line and press Enter, in one request, so no other input can arrive between them. The command is the second argument, "-" to read it from stdin, or --file. The line endings at its end are dropped, because the command presses Enter itself.

The command line arrives as a paste, as "terminal send --paste" sends it. A line editor that enabled bracketed paste therefore inserts it as it is: an abbreviation, an automatic closing bracket or a vi key binding cannot change it. A program without bracketed paste reads it as typed text. To type the command key by key in any case, run "terminal send &lt;terminal&gt; &lt;command&gt; --key enter".

By default the command returns as soon as the input is written. Pass --wait with --exit, --text, --regex, or --change to wait until that condition holds, or read output later with "terminal read".

_Mutates state._

```text
leapmux terminal run <terminal> [command] [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |
| `[command]` | The command line, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the command line from this file ("-" reads stdin) |
| `--request-id string` | an id of your choice that makes a retry safe: a second run with the same id and the same arguments creates nothing new |
| `--wait` | wait after running until --exit, --text, --regex, or --change holds |
| `--exit` | See [common arguments and flags](#common-arguments-and-flags). |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--change` | See [common arguments and flags](#common-arguments-and-flags). |
| `--after-offset int` | with --change, return immediately if screen_end_offset already exceeds this byte offset |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal run 4cbb3f34 'make test'

# Run the command line that stdin holds.
echo 'go test ./...' | leapmux terminal run 4cbb3f34 -

# Run a command and wait for a sentinel line.
leapmux terminal run 4cbb3f34 'make test; echo DONE' --wait --regex '^DONE$' --timeout 10m
```

### `leapmux terminal read`

Print what a terminal shows, as plain text. The CLI replays the bytes that the Worker retains through a terminal emulator and applies recorded size changes in order. The emulator is a Go port of xterm.js, which the browser uses. Colors and other attributes do not print.

Each row loses its trailing blanks, and the empty rows at the end of the screen do not print, so a short output does not end in empty lines.

--scrollback also prints the lines above the screen: at most 1000, as in the browser, and only those that the bytes the Worker retains still hold. A full-screen program on the alternate screen has no scrollback, as in the browser, so --scrollback adds nothing there. --lines N keeps the last N lines of what the command prints otherwise.

--json adds the cursor, whose row and column count from 0 at the top left of the screen, and alternate_screen, which is true while a full-screen program uses the alternate screen. --raw prints the retained bytes as they are, with their escape sequences, so that "less -R" shows them.

_Aliases: `cat`._

```text
leapmux terminal read [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `-n, --lines int` | print only the last N lines |
| `--scrollback` | also print the lines above the screen |
| `--raw` | print the retained bytes as they are, with their escape sequences |
| `-f, --follow` | stream live output until Ctrl-C or shell exit |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal read 4cbb3f34

# Print the last 200 lines, the scrollback included.
leapmux terminal read 4cbb3f34 --scrollback --lines 200

# Page through the retained bytes with their colors.
leapmux terminal read 4cbb3f34 --raw | less -R

# Follow live terminal output until Ctrl-C or shell exit.
leapmux terminal read 4cbb3f34 --follow
```

### `leapmux terminal attach`

Attach this terminal interactively to a LeapMux terminal over the encrypted Worker channel. Keystrokes travel straight to the remote PTY, output streams live to stdout, and local window resizes forward automatically.

Press Ctrl-] (ASCII 0x1d, or the key set by --detach) to detach cleanly without closing the remote shell.

_Does not support `-q` / `--quiet`._

```text
leapmux terminal attach [terminal] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--detach string` | key sequence that detaches without closing the remote shell (e.g. ctrl-], ctrl-b, ctrl-a) (default: ctrl-]) |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal attach 4cbb3f34

# Attach with Ctrl-b as the detach key.
leapmux terminal attach 4cbb3f34 --detach ctrl-b
```

### `leapmux terminal ps`

List foreground and background processes running inside one or more terminals.

```text
leapmux terminal ps [terminal...] [flags]
```

| Argument | Description |
|---|---|
| `[terminal]...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--all` | inspect every open terminal instead of a single target |
| `--workspace string` | inspect terminals in this workspace (implies --all when no targets are given) |

```sh
leapmux terminal ps
leapmux terminal ps 4cbb3f34

# Inspect every terminal in the workspace.
leapmux terminal ps --all
```

### `leapmux terminal wait`

Wait until a terminal meets one condition, then print what met it:

- --exit waits until the shell of the terminal ends, and prints its exit code.
- --text waits until a line of the terminal holds the text.
- --regex waits until a line of the terminal matches the regular expression, in RE2 syntax.
- --change waits until the visible screen text changes (event-driven over the live Worker output stream, with no polling), and prints the updated screen. Pass --after-offset with screen_end_offset from a prior "terminal read --json" or "terminal wait --change --json" to return immediately if the terminal already produced output since that offset.

The CLI replays the output of the terminal through the emulator of "terminal read", so a line is the text that the terminal shows: an escape sequence, or a text that arrives in two parts, cannot break a match. The match is per line. A line that the terminal wraps at its width counts as one line, and it loses its trailing blanks, as "terminal read" prints it, so ^ and $ match at its ends. The command prints the line that matched. When several lines match at once, it prints the lowest one.

The text that the screen shows when the wait begins counts, so a wait for a text that the terminal already shows ends at once. Each line that appears later counts too, even one that scrolls off the screen before the CLI reads it. The wait follows size changes and applies them in order after reconnect. Use --scrollback to include the retained history from before the wait. To wait for the output of a new command only, use the default screen scope and clear the screen first, or wait for a line that the command text does not show. In the second example, the echoed command line holds "echo DONE". The line of output matches '^DONE$'.

The command exits 0 when the condition holds. It exits 6 when the wait ended without it: --timeout passed, the shell ended before the terminal showed the text, or the shell failed to start. --json prints the status of the terminal in each case, and the exit code of the shell once it ended.

--exit ends at once for a shell that ended before the wait began, and prints its exit code, which the Worker keeps. When the Worker holds no record of the code, because it was killed while the shell ran, the command prints no exit code, says so on stderr, and still exits 0. --exit refuses the terminal that runs the command, because its shell cannot end while the wait runs in it.

"leapmux events --kind terminal" prints the bells, the titles, the notifications, the progress reports and the exits of terminals as they happen.

```text
leapmux terminal wait <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--exit` | See [common arguments and flags](#common-arguments-and-flags). |
| `--text string` | wait until a line of the terminal holds this text |
| `--regex string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--change` | See [common arguments and flags](#common-arguments-and-flags). |
| `--after-offset int` | with --change, return immediately if screen_end_offset already exceeds this byte offset |
| `--scrollback` | also search the retained scrollback from before the wait |
| `--timeout duration` | See [common arguments and flags](#common-arguments-and-flags). |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
# Wait until the shell ends, and keep its exit code.
CODE=$(leapmux terminal wait 4cbb3f34 --exit)

# Run the tests, then wait for the line that the last command prints.
leapmux terminal run 4cbb3f34 'make test; echo DONE' && leapmux terminal wait 4cbb3f34 --regex '^DONE$' --timeout 10m

# Wait until a server reports that it listens, for 2 minutes at most.
leapmux terminal wait 4cbb3f34 --text 'Listening on' --timeout 2m

# Look for a completed build in the terminal's retained output.
leapmux terminal wait 4cbb3f34 --text 'Build complete' --scrollback
```

### `leapmux terminal resize`

Resize a terminal.

_Mutates state._

```text
leapmux terminal resize <terminal> <cols> <rows> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal resize 4cbb3f34 120 40
```

### `leapmux terminal restart`

Restart a terminal's shell.

_Mutates state._

```text
leapmux terminal restart <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--replace` | stop the running shell before starting a new one in the same tab |
| `--command string` | run this command in the new shell after its startup files, or read it from stdin with - |
| `--allow-self` | replace this tab's shell even though it ends this command's own process |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal restart 4cbb3f34
leapmux terminal restart 4cbb3f34 --replace --command 'npm run dev'
```

### `leapmux terminal shells`

List the shells that a worker offers.

```text
leapmux terminal shells [flags]
```

| Flag | Description |
|---|---|
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal shells
```

### `leapmux terminal quake`

Show, hide, inspect, read, and drive the quake terminal of a directory.

#### `leapmux terminal quake open`

Show the quake terminal.

_Mutates state._

```text
leapmux terminal quake open [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake open
leapmux terminal quake open --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake close`

Hide the quake terminal.

_Mutates state._

```text
leapmux terminal quake close [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake close
leapmux terminal quake close --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake toggle`

Show the quake terminal if it is hidden, and hide it otherwise.

_Mutates state._

```text
leapmux terminal quake toggle [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | See [common arguments and flags](#common-arguments-and-flags). |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake toggle
leapmux terminal quake toggle --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake get`

Show the status and details of the quake terminal for a directory.

```text
leapmux terminal quake get [flags]
```

| Flag | Description |
|---|---|
| `--dir string` | the directory whose quake terminal to inspect (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake get
leapmux terminal quake get --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake read`

Print what the quake terminal of a directory shows.

_Aliases: `cat`._

```text
leapmux terminal quake read [flags]
```

| Flag | Description |
|---|---|
| `-n, --lines int` | print only the last N lines |
| `--scrollback` | also print the lines above the screen |
| `--raw` | print the retained bytes as they are, with their escape sequences |
| `-f, --follow` | stream live output until Ctrl-C or shell exit |
| `--dir string` | the directory whose quake terminal to read (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake read
leapmux terminal quake read --lines 50
```

#### `leapmux terminal quake run`

Run a command in the quake terminal of a directory (starts the quake shell if needed).

_Mutates state._

```text
leapmux terminal quake run [command] [flags]
```

| Argument | Description |
|---|---|
| `[command]` | The command line, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the command line from this file ("-" reads stdin) |
| `--dir string` | the directory whose quake terminal to run in (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake run 'go test ./...'
leapmux terminal quake run 'make build' --dir ~/src/app --worker name:build-box
```

#### `leapmux terminal quake send`

Type text or press keys in the quake terminal of a directory.

_Mutates state._

```text
leapmux terminal quake send [text] [flags]
```

| Argument | Description |
|---|---|
| `[text]` | The text, or "-" to read it from standard input. |

| Flag | Description |
|---|---|
| `--file string` | read the bytes from this file ("-" reads stdin) |
| `--key string` | press this key after the text, such as enter, ctrl-c or up; repeat it to press several keys in order (repeatable) |
| `--paste` | send the text as a paste, between bracketed-paste markers when the program enabled them |
| `--dir string` | the directory whose quake terminal to type into (default: $LEAPMUX_CONTROL_WORKING_DIR) |
| `--worker string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal quake send --key ctrl-c
leapmux terminal quake send "ls -la" --key enter
```

### `leapmux terminal close`

Close one or more terminal tabs and stop their shells.

_Aliases: `rm` · Mutates state._

```text
leapmux terminal close <terminal>... [flags]
```

| Argument | Description |
|---|---|
| `<terminal>...` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--worktree string` | what happens to the git worktree when this is its last tab; required in that case (one of: keep, push, discard) |
| `--allow-busy` | close even if work is in progress or the Worker cannot verify the tab's activity |
| `--allow-self` | close the tab even when it is this tab, which ends this command's own process |
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal close 87a19d5f

# Close the last tab of a worktree and keep the worktree.
leapmux terminal close 87a19d5f --worktree keep
```

### `leapmux terminal rename`

Rename a terminal tab.

_Mutates state._

```text
leapmux terminal rename <terminal> <title> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal rename self "Release notes"
```

### `leapmux terminal reveal`

Show a tab in every LeapMux window that you have open: each window switches to the tab's workspace, and the tab becomes the active tab of its tile, as a click on the tab in the sidebar does.

Nothing stores the reveal. Which tab a window shows stays the window's own state, so a window that you open later shows what it showed before, and a window that does not know the tab yet ignores the command.

_Mutates state._

```text
leapmux terminal reveal <terminal> [flags]
```

| Argument | Description |
|---|---|
| `<terminal>` | See [common arguments and flags](#common-arguments-and-flags). |

| Flag | Description |
|---|---|
| `--workspace string` | See [common arguments and flags](#common-arguments-and-flags). |

```sh
leapmux terminal reveal 87a19d5f

# Bring the tab that runs this command into view.
leapmux terminal reveal self
```
