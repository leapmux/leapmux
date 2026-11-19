---
title: "Control CLI"
description: "Drive LeapMux from a script, a CI job, or another agent with leapmux control: open tabs, message agents, type into terminals, arrange layouts, and stream events."
type: docs
weight: 8
---

`leapmux control` drives a running LeapMux from outside the browser. Use it from a script, a CI job, or another agent. It opens and closes tabs, sends messages to agents, types into terminals, arranges the tile layout, reads files and git state on a Worker, and streams live workspace events.

This chapter explains how the commands work together: how a command selects its target, what it prints, how it fails, and how it signs in. The [Control Command Reference](/docs/reference/control-commands/) lists every command with its arguments, flags and examples. The online administration commands (`leapmux control admin ...`) have their own chapter, [Admin CLI](/docs/admin/admin-cli/). For the offline break-glass tree (`bootstrap`, `password`, `encryption-key`, `db`), see [Recovery](/docs/admin/recover/). For the agent and terminal features these commands drive, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## Two callers, one CLI

The same `leapmux control ...` command works in two places:

1. **On your own machine.** You sign the CLI in to a Hub once with `leapmux control auth login --hub ...`, which stores a credential on disk. Every later command presents that credential to the Hub.
2. **Inside an agent or terminal that a Worker started.** The Worker gives the process a private local socket and a token through `LEAPMUX_CONTROL_*` environment variables. A command run there needs no login and no `--hub`. The word `self` selects the tab, tile, workspace or Worker that runs the command.

Both transports reach the same RPCs, so a script written for one place also runs in the other.

**The two transports converging on the Worker(s):**

```text
  ┌────────────────┐                  ┌────────────────────┐
  │  External CLI  │                  │  Worker-spawned    │
  │  (your laptop) │                  │  agent / terminal  │
  └───────┬────────┘                  └─────────┬──────────┘
          │ --hub, LEAPMUX_HUB or               │ LEAPMUX_CONTROL_SOCK
          │ the default hub + bearer            │ + X-LeapMux-Token
          ▼                                     ▼ (local IPC)
  ┌────────────────┐                  ┌────────────────────┐
  │      Hub       │                  │   Host Worker      │
  │  (relays the   │                  │  (delegates the    │
  │   channel)     │                  │   inner RPC)       │
  └───────┬────────┘                  └─────────┬──────────┘
          │ relayed E2EE                        │ Noise channel
          │ (Noise) channel                     │ (cross-worker)
          ▼                                     ▼
  ┌────────────────┐                  ┌────────────────────┐
  │  Target Worker │                  │  Sibling Worker    │
  └────────────────┘                  └────────────────────┘
```

## A first session

```text
$ leapmux control tab list
ID        TYPE      TITLE           STATE    WORKER     WORKSPACE
87a19d5f  agent     CLI Design      working  build-box  4f1ac2b0
4cbb3f34  agent     Fix flaky spec  idle     build-box  4f1ac2b0
9c02e7aa  terminal  zsh             ready    build-box  4f1ac2b0

$ leapmux control agent send 4cbb "Run the tests again"
$ leapmux control agent messages 4cbb --follow
```

`4cbb` is a prefix of the tab id `4cbb3f34`; any unique prefix of four or more characters selects its tab. [Targets](#targets) lists every form.

## Command shape

Every command is `leapmux control <group> <command>`, followed by its arguments and flags:

```text
leapmux control agent send 87a19d5f "Run the tests" --json
                ^^^^^ ^^^^ ^^^^^^^^ ^^^^^^^^^^^^^^^ ^^^^^^
                group  |   target   argument        flag
                    command
```

- **Flags stand anywhere** after the command name. A value that starts with a dash goes after `--`: `leapmux control agent send 87a19d5f -- "-v is the verbose flag"`.
- **A command name can be shortened** as far as it stays unambiguous: `leapmux control ag se` runs `agent send`. A later release can add a command that makes a short form ambiguous, so a script spells every name in full.
- **Every command prints its help** with `--help`: the usage line, what each argument accepts, every flag, and examples.
- **An agent starts with `leapmux control guide`**, which prints a short guide to driving LeapMux from inside a tab.
- **A program reads the whole grammar** from `leapmux commands --json`: every command with its arguments, its flags, their types and accepted values, and its examples.
- **Shell completion** completes the command names, the flags, and the ids of your tabs, agents, workspaces and Workers with their titles. Print the script with `leapmux completion bash`, `zsh`, `fish` or `powershell`.

## Targets

The first argument of most commands selects the object it acts on. A target takes one of these forms:

| Form | Example | Selects |
| --- | --- | --- |
| A full id | `87a19d5f-4838-4e34-9f0f-56130f0a62b1` | That object. It costs no extra request. |
| A unique id prefix of 4 or more characters | `87a1` | The one object whose id starts with it. |
| `title:<title>` | `"title:Release 1.4"` | The tab or workspace with exactly that title. |
| `name:<name>` | `name:build-box` | The Worker with exactly that name. |
| `self` | `self` | The tab, tile, workspace or Worker of the LeapMux tab that runs the command. |

Each form other than a full id costs one request that lists the objects of its kind. A form that matches nothing fails with `not_found`. A form that matches more than one object fails with `ambiguous_target`, and the error lists every candidate with its full id and its title:

```text
$ leapmux control tab get 87
error: usage: "87" is too short for a tab id; type at least 4 characters
hint: use "self" for your own tab, or "title:<title>"
$ leapmux control agent get title:Review
error: ambiguous_target: 2 agents match the title "Review"
candidates:
  ID                                    KIND   TITLE
  87a19d5f-4838-4e34-9f0f-56130f0a62b1  agent  Review
  c41e09b2-1d7e-4c51-a0d2-8b6f2e3c9a11  agent  Review
hint: type more characters of the id, or the full id
```

**A command that only reads defaults to `self`.** Inside an agent tab, `leapmux control agent get` shows that agent. **A command that changes or removes an object needs its target**, so a bare command never acts on the tab that runs it: `leapmux control tab close` fails, and `leapmux control tab close self` closes the calling tab.

`self` follows the kind of the command. `agent ...` commands see the calling tab only when it is an agent tab, and `terminal ...` commands only when it is a terminal. Inside a [Quake terminal](/docs/using/terminals/#quake-mode-terminal), `self` on a `terminal` command is the panel's terminal.

The commands that open a tab (`agent open`, `terminal open`, `file open`) place it with `--workspace`, `--tile`, `--worker` and the position flags. Each placement flag defaults to the placement of the calling tab.

## Output

**Text is the default.** A list prints as a table and one object as aligned `key: value` lines. A table shows the first eight characters of each id; a `key: value` view prints the whole id.

**`--json` prints one JSON envelope per line** on stdout:

- **Success:** `{"data": ...}`. `data` is always an object, and a list lives under its plural noun: `{"data": {"tabs": [...]}}`.
- **Failure:** `{"error": {...}}`, described in [Errors and exit codes](#errors-and-exit-codes).

**`-q` prints only the ids** of the result, one per line, for `$(...)`:

```bash
A=$(leapmux control agent open --provider claude --message "Run the tests" -q)
leapmux control agent get "$A" --json | jq -r .data.state
```

**A stream prints one line per event**: `agent messages --follow` and `layout watch`. With `--json`, each line is a `{"data": ...}` envelope, so a consumer reads every line alike.

Prose for a person, such as the login prompts, goes to stderr, so it never corrupts a result that a script parses.

## Errors and exit codes

A failure prints `error: <code>: <message>` on stderr, followed by one `hint:` line for each next step that can fix it. With `--json`, the envelope also reaches stdout:

```json
{"error":{"code":"self_target_refused","message":"refusing to close the calling tab 87a19d5f-4838-4e34-9f0f-56130f0a62b1","hints":["pass --allow-self to do it anyway"],"retryable":false}}
```

| Field | Meaning |
| --- | --- |
| `code` | One of the codes below. A script branches on it. |
| `message` | What failed, for a person. |
| `hints` | The next steps that can fix it, when the CLI knows them. |
| `retryable` | `true` when the same command can succeed later without a change: the Hub or the Worker could not answer now. |
| `candidates` | For `ambiguous_target`, every object that the target matched. |

The exit status states the class of the failure, so a script can branch without reading the envelope:

| Status | Codes | Meaning |
| --- | --- | --- |
| `0` | | Success |
| `1` | `invalid_argument`, `conflict`, `internal` | The Hub or the Worker refused the request, or the CLI failed |
| `2` | `usage` | The command line cannot run: an unknown command or flag, a missing argument, or a value the CLI refuses before it sends anything |
| `3` | `not_logged_in`, `permission_denied` | No usable credential, or a credential that does not allow the call |
| `4` | `not_found`, `ambiguous_target` | The target does not exist, or matches more than one object |
| `5` | `self_target_refused`, `tab_busy_refused`, `agent_waiting` | A safety guard refused the command; see [Safety guards](#safety-guards). `agent_waiting` is `agent send --wait` to an agent that waits for an answer to a permission prompt. |
| `6` | `timeout` | A wait ended before its condition held; see [Waiting for agents](#waiting-for-agents) |
| `7` | `worker_offline`, `unavailable` | The Worker or the Hub cannot answer now; a retry can succeed |
| `130` | | You interrupted the command |

`version_mismatch` (status `1`) means that the Hub or the Worker does not know the call, because its version differs from the CLI's. Compare them with `leapmux control version`.

## Choosing the Hub

Each command reaches the first of these:

1. The Hub that `--hub <url>` gives.
2. Inside a LeapMux tab, the tab's own socket, `LEAPMUX_CONTROL_SOCK`.
3. The Hub that `LEAPMUX_HUB` gives.
4. The default Hub: the Hub of the only stored credential, or the Hub that `leapmux control auth use <hub>` chose when more than one is stored.

With none of them, the command fails with `not_logged_in` and states how to select a Hub. `--hub` accepts an http(s) URL, or a Hub's local socket (`unix:...` or `npipe:...`); see [Sockets and login](#sockets-and-login).

## Authentication

### `leapmux control auth login`

Signs the CLI in to a Hub and writes a credential file to disk. Give the Hub with `--hub`.

The CLI is a registered app like any other, so a login is an ordinary OAuth 2.1 authorization. See [App Authorization](/docs/admin/app-authorization/) for how apps are registered and what they may ask for, and [OAuth API](/docs/reference/oauth-api/) for the wire contract.

**You verify your identity in the browser, not in the terminal.** Authorizing a credential needs an elevated session, so the consent page sends you through a verification prompt (password, passkey, or your identity provider) before it hands anything back. That elevation lasts {{< duration elevation-window >}}, so a second login in the same sitting does not ask again. See [Session elevation](/docs/admin/security/#session-elevation).

**Default flow (PKCE local redirect).** The CLI opens a loopback listener on `127.0.0.1`, prints the authorization URL on stderr, and tries to open your browser (`open` on macOS, `xdg-open` on Linux, the shell handler on Windows). You sign in on the Hub's web page, and the Hub redirects back to the loopback listener to complete the exchange. The CLI waits up to **10 minutes** for the callback before it fails with `timeout`.

```bash
leapmux control auth login --hub https://leapmux.example.com
```

**Device-code flow (`--device-code`).** Use this on a machine without a browser, over SSH, or inside a container. The CLI prints a verification URL and a short user code on stderr:

```bash
leapmux control auth login --hub https://leapmux.example.com --device-code
```

The user code is six characters from an alphabet with no look-alike characters (no `0`, `1`, `I`, `O` or `L`), shown as `XXX-XXX`. Open the verification URL on any device and enter the code. The CLI polls in the background and completes once you approve. A second link carries the code in its query string, so you can skip the typing.

`--device-name` labels the credential in the account's list of connected apps. It defaults to `<user>@<host>` of the machine that signs in.

The flow you pick also decides how a later step-up runs. A credential from the default flow verifies through the loopback ceremony: a refused command opens a browser on the CLI's own machine, and the command runs once you prove the factor there. A credential from `--device-code` keeps the device-code ceremony, printing an address and a short code instead. See [Verifying a command-line credential](/docs/admin/security/#verifying-a-command-line-credential).

### `--scope`: what the credential may do

An omitted `--scope` asks for everything you can do **except** administer the Hub. A routine login therefore leaves no administration credential on disk, and `leapmux control admin ...` refuses it even when your account is an administrator.

Name the permissions to ask for less, or to ask for administration:

```bash
# A credential that reads files and git state, and nothing else.
leapmux control auth login --hub https://leapmux.example.com --scope "file:read git:read"

# A credential that administers the Hub. Only an administrator may grant it.
leapmux control auth login --hub https://leapmux.example.com --scope "admin:read admin:users admin:settings admin:workers admin:apps"
```

Both separators work. The wire format is space-delimited, so a shell needs quotes around it; a comma-separated list also works.

The **browser** decides. The consent page states in sentences what the credential will be able to do, and you approve or refuse there. The login result lists the permissions you actually granted, which is what the CLI records. That list can be wider than the request, because some permissions imply others: `file:read` implies `worker:read`, since reading a file means reaching the machine that holds it. It can also be narrower, and the result then carries a `warning` that lists each permission the Hub did not grant.

The whole vocabulary is in [App Authorization](/docs/admin/app-authorization/#permissions).

### Renewal and lifetime

The access token lives for {{< duration access-token >}}. The CLI renews it silently: before a call whose stored expiry is close, and once more if the Hub refuses a call it expected to succeed. You run nothing to renew it.

Each renewal moves the {{< duration refresh-token >}} refresh window forward, but never past **{{< duration absolute-cap >}}** from the day you authorized the credential. After that, the device signs in again. `auth status` reports both deadlines.

Signing in again on the same machine **revokes the credential it replaces**, so a new login leaves no live secret behind.

### The other `auth` commands

| Command | What it does |
| --- | --- |
| `auth status` | Shows the user, the permissions and both deadlines of the credential for the Hub. One request to the Hub confirms them: `hub_checked` says whether it did, and a `warning` states what it could not confirm. `expires` is the access token, which renews itself; `refresh_expires` is when the device must sign in again. |
| `auth list` | Lists the Hubs that this machine holds a credential for, and marks the default Hub. |
| `auth use <hub>` | Makes `<hub>` the default Hub, for a machine that holds credentials for more than one. |
| `auth credentials` | Lists every credential that can reach your account on the Hub, from any machine. `current` marks the one this command uses. |
| `auth logout` | Revokes the credential on the Hub, then deletes the local file. A revoke that fails still deletes the file, and the result carries a `warning`. |

**`list` and `credentials` answer different questions.** `list` reads this machine's credential files: which Hubs this machine can reach. `credentials` asks the Hub what the whole account holds: what else can reach your account. It is the same list the browser shows under **Preferences → Apps → Connected apps**, where you can also disconnect a credential. See [Connected Apps](/docs/using/connected-apps/).

### Credential file location

The CLI writes one file per Hub:

```text
<ConfigDir>/<hub-host>.json
```

`<ConfigDir>` resolves in this order:

1. `LEAPMUX_CONTROL_CONFIG_DIR` (used verbatim if set)
2. `$XDG_CONFIG_HOME/leapmux/control`
3. `~/.config/leapmux/control`

`<hub-host>` is the Hub's host name, with `_<port>` appended when the URL carries a port (for example `leapmux.example.com_8443`). The CLI writes the file atomically with mode `0600`, in a directory with mode `0700`. It holds the access token, the refresh token, both expiries, your user identity, the token id, and the permissions the credential holds. `auth use` records the default Hub in `<ConfigDir>/default-hub`.

{{< callout >}}
Point `LEAPMUX_CONTROL_CONFIG_DIR` at a directory for each CI job to keep its credentials isolated and easy to discard.
{{< /callout >}}

## Inside a LeapMux tab

When a Worker starts an agent or a terminal, it gives the process these environment variables:

| Variable | When present | Meaning |
| --- | --- | --- |
| `LEAPMUX_CONTROL_SOCK` | always | The local socket that the CLI talks to |
| `LEAPMUX_CONTROL_TOKEN` | always | The token of this process |
| `LEAPMUX_CONTROL_USER_ID` | always | The user the process acts for (informational) |
| `LEAPMUX_CONTROL_WORKER_ID` | always | The host Worker: `self` for a Worker, and the default of `--worker` |
| `LEAPMUX_CONTROL_TAB_ID` | when non-empty | The tab: `self` for a tab, an agent or a terminal |
| `LEAPMUX_CONTROL_TAB_TYPE` | when non-empty | `agent` or `terminal` |
| `LEAPMUX_CONTROL_TERMINAL_ID` | terminals only | The terminal you run **inside**. It equals `TAB_ID` in a terminal tab. In a [Quake panel](/docs/using/terminals/#quake-mode-terminal) it is the only id, because a panel has no tab. |
| `LEAPMUX_CONTROL_WORKING_DIR` | when non-empty | The working directory at start: the default of `--dir` |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | agents only | The agent's provider: the default of `--provider` |
| `LEAPMUX_CONTROL_CLI` | when the Worker has a CLI to link | The `leapmux` executable of the Worker that started the process. Its directory comes first on `PATH`. |

The tab carries no workspace id and no tile id. A command finds both from the tab when it runs, so a script never acts on a tile that somebody moved the tab away from.

The Worker also puts one directory first on the `PATH` of each agent and terminal that it starts. The directory holds only `leapmux` (`leapmux.exe` on Windows), which is the Worker's own CLI. A bare `leapmux` therefore runs a CLI of the same version as the Worker, even when you installed another version. `LEAPMUX_CONTROL_CLI` gives the full path of that executable. The desktop app links the CLI that it ships; an AppImage ships none, so it sets neither.

{{< callout type="warning" >}}
A login shell runs its profile after it starts, and the profile can move the entry or remove it. macOS moves its system directories, such as `/usr/local/bin`, before it. Debian's `/etc/profile` replaces `PATH` for bash and sh. A `leapmux` in such a directory then runs instead of the Worker's. To run the Worker's own CLI in every case, call it through the variable:

```sh
"$LEAPMUX_CONTROL_CLI" control tab list
```
{{< /callout >}}

{{< callout type="info" >}}
Terminals and agents receive `LEAPMUX_CONTROL_*` automatically whenever the Worker has remote control enabled. The Worker removes inherited `LEAPMUX_CONTROL_*` values, and the `PATH` entry of a parent Worker, before it sets its own, so an agent started from inside another agent gets its own context rather than its parent's. See [Terminals](/docs/using/terminals/) for the terminal side.
{{< /callout >}}

Run `printenv | grep LEAPMUX_CONTROL_` inside a tab to see the context that the tab received.

## Safety guards

Three guards refuse a command that would lose work the caller may not mean to lose. Each refusal states the flag that overrides it.

| Guard | Code | Override | Refuses |
| --- | --- | --- | --- |
| Self-target | `self_target_refused` | `--allow-self` | `workspace delete`, `tab close`, `tile close` and `tile remove-grid` when the calling tab is the target or sits inside it |
| Busy tab | `tab_busy_refused` | `--allow-busy` | `tab close` of an agent in the middle of a turn, or of a terminal with a process running |
| Last tab of a worktree | `usage` | `--worktree keep\|push\|discard` | `tab close` of the last tab of a git worktree or branch, until you say what happens to it |

The self-target guard does not fire for `--with-tabs move`, because the tabs survive the move. It also does not fire inside a [Quake terminal](/docs/using/terminals/#quake-mode-terminal), which has no tab to protect. Closing the *last* tab in the panel's working directory still ends the panel's shell, including one you type the command into.

## Worker key pins

LeapMux pins each Worker's key on the first verified connection (trust on first use). `leapmux control worker pins` manages the pins of this machine:

| Command | What it does |
| --- | --- |
| `worker pins list` | Lists every pinned Worker of the Hub |
| `worker pins get <worker>` | Shows one pin |
| `worker pins remove <worker>` | Removes the pin. The next verified channel records a replacement. |

A key mismatch aborts the connection. Before you remove a pin, compare the presented fingerprint in the error with the Worker's `key_fingerprint` in its `worker initialized` startup log, through a trusted, independent channel. Remove the pin only after they match. The CLI records a replacement key after the channel completes an encrypted round trip. See [Managing Workers](/docs/admin/managing-workers/) for fingerprint verification.

Pins are stored at `<ConfigDir>/<hub-host>/pins.json` with mode `0644`, because they are not secrets.

## What each group does

The [Control Command Reference](/docs/reference/control-commands/) gives the flags of each command. This section explains the behavior that a flag table cannot.

### Agents

- `agent list` and `agent get` print each agent's state (`idle`, `working`, `waiting_for_user`), model, effort and git state. `agent get --options` also prints every option group with its choices.
- `agent send <agent> [text]` takes the text as an argument, `-` to read standard input, or `--file`. `--wait` waits for the reply; see [Waiting for agents](#waiting-for-agents).
- `agent wait <agent>...` waits until each agent reaches a state; see [Waiting for agents](#waiting-for-agents).
- `agent requests`, `agent approve`, `agent deny` and `agent answer` list and answer the permission prompts, plans and questions that agents wait on; see [Answering prompts](#answering-prompts).
- `agent messages` prints the most recent page by default (`--anchor latest`). `--anchor oldest` prints the first messages in history. `--anchor before --cursor-seq N` prints the page older than seq N, and `--anchor after --cursor-seq N` the page newer than it. Messages always come back in ascending seq. `--follow` prints the page and then each new message as it arrives, and reconnects after a transient drop. It accepts only `--anchor latest` and `--anchor after`.
- `agent set` changes the model, the effort, the permission mode, and any provider option with `--option key=value`. A provider that can apply a change to the running process applies it at once (Claude Code and Codex do for most settings). Another change restarts the agent. See [Coding Agents](/docs/using/coding-agents/) for the settings of each provider.

### Waiting for agents

`agent wait` and `agent send --wait` watch the agents through the Worker. They never poll, and they never read a screen.

- **`agent wait <agent>... --until <state>`** ends once each agent was in the state at some moment after the wait began. An agent that is in the state already ends the wait at once. `--until` takes a state that `agent list` prints (`idle`, `working`, `waiting_for_user`), or `settled`, the default: `idle` or `waiting_for_user`, so the agent works no more. `--any` ends the wait with the first agent instead of the last.
  - An agent whose input queue holds a message that it will read is neither `idle` nor `settled` yet. So a wait right after `agent open --message` waits for the turn of that first message, although the starting agent reports `idle`. A message scheduled for later does not hold the wait.
- **`agent send <agent> [text] --wait`** sends the message, waits until the agent settles after it, and prints the agent's last reply as the transcript shows it. `--all` prints every reply after the message. An agent that runs a background task settles when the task ends too.
  - It sends nothing to an agent that waits for an answer to a permission prompt, because the message would wait behind the prompt. The command fails with `agent_waiting` (status `5`). Answer the prompt first; see [Answering prompts](#answering-prompts).
  - A paused input queue keeps the message, and the command says so on stderr and keeps waiting. A planned restart pauses the queue too, and that pause ends by itself.
- **Both print their result even when the condition did not hold, and exit `6`**: `--timeout` passed, an agent closed, or the agent stopped at a prompt after the message. The result states where things stand. For `agent wait`, `reached` marks each agent that reached the state. For `agent send --wait`, `message` is `queued` or `delivered`, and `state` is the agent's state, such as `waiting_for_user`.
- **A wait refuses the calling agent** with `usage`, because an agent cannot settle while its own turn runs the wait.

### Answering prompts

An agent asks the user through a control request: a permission prompt, a plan to approve, one or more questions, an MCP elicitation form, or an extension's dialog. The browser shows it as a banner. The CLI answers the same request.

- **`agent requests [agent]...`** lists the pending requests of the agents, or of every agent when you give none (`--workspace` narrows it). The text form prints each request in full, so you read a whole command before you approve it, and it numbers the options that `--option` takes. `--json` prints each prompt as data.
- **Each request has a handle.** The three answer commands take the agent and the handle, or a unique prefix of the handle. A request that the agent replaces gets a new handle, so an answer never reaches a request that you did not read.
- **`agent approve <agent> <request>`** approves:
  - A permission prompt: its one-time approval
  - A plan: the approval, with `--permission-mode` and `--clear-context`
  - An MCP elicitation: the accept, with the default values of the fields that the form asks
  - A confirm dialog: the confirmation
- **`agent deny <agent> <request> [--message <text>]`** refuses a permission prompt, a plan or a question, with the message as the reason that the agent reads. It also declines an MCP elicitation and refuses or cancels a dialog.
- **`agent answer <agent> <request>`** gives any answer that the request offers:
  - `--option <n>` chooses an option of a question or a permission prompt, a choice of a plan, or an accept choice of an MCP elicitation. Use it for an approval for the whole session, for example.
  - A text argument is a typed answer to a question, or the text of an input dialog.
  - A multiple-choice question takes options and a text together: `--option 1 --option 3 "Metrics"` answers with both options and the text.
  - With several questions, each value says which question it answers: `--option 2:1` and `"1:Use the cache"`.
  - A question that no `--option` answers keeps the options that the agent selected for it, which `agent requests` shows as its `default`. With no value at all, the command sends those defaults.
  - `--field <name>=<value>` fills an MCP form, once for each field. A field that `agent requests` lists with a `when` condition takes a value only while the condition holds; otherwise the form neither sends nor requires it. A field that the list leaves out is hidden, and it keeps its default.
- **The Worker checks each answer against the prompt**, as it does for the browser, and fails a wrong one with `invalid_argument`. A request that the agent withdrew, or that another client answered first, fails with `conflict`.
- **An agent cannot answer its own requests.** The command fails with `usage`: the user answers them.

### Terminals

- `terminal send <terminal> [text]` types the text as it is, with no Enter added. `--file` sends the bytes of a file, such as escape sequences.
- `--key <name>` presses a key after the text. Repeat it to press several keys in order: `leapmux control terminal send 9c02 --key down --key enter`. The Worker encodes each key for the modes that the program in the terminal enabled, as the browser does, so `up` works in a shell and in a full-screen program alike.
  - The names are `enter`, `tab`, `escape` (or `esc`), `backspace`, `space`, `up`, `down`, `left`, `right`, `home`, `end`, `insert`, `delete`, `pageup`, `pagedown`, `f1` to `f12`, and one printable character.
  - Each name may follow any of `ctrl-`, `alt-` and `shift-`, such as `ctrl-c`, `alt-b`, `shift-tab` or `ctrl-left`. The names are case-insensitive.
  - An unknown name, or a modifier that a terminal cannot send with the key, such as `ctrl-enter`, fails with `usage`, and nothing is sent.
- `--paste` sends the text as the browser sends a paste. Each line ending becomes a carriage return. When the program enabled bracketed paste, the text arrives between the paste markers, so the program inserts it instead of running each line. A paste end marker inside the text is removed, so the text cannot end its own paste.
- `terminal run <terminal> <command>` pastes the command line and presses Enter in one request, so no other input can arrive between them: `leapmux control terminal run 9c02 'make test'`. It does not wait for the command to finish.
- `terminal read [terminal]` prints what the terminal shows, as plain text. It replays the bytes that the Worker retains through a Go port of xterm.js at the terminal's size, so the text matches the browser.
  - Each row loses its trailing blanks, and the empty rows at the end of the screen do not print.
  - `--scrollback` adds the lines above the screen: at most 1000, and only those that the retained bytes still hold. A full-screen program on the alternate screen has none.
  - `--lines N` keeps the last N lines.
  - `--json` adds `cursor`, whose row and column count from 0 at the top left of the screen, and `alternate_screen`.
  - `--raw` prints the retained bytes with their escape sequences, for `less -R`.
- `terminal quake open|close|toggle` show and hide a working directory's [Quake-mode terminal](/docs/using/terminals/#quake-mode-terminal) in every browser you have open. They store nothing: a browser that is not open never hears the request. A browser with no tab in that directory ignores it, because it has no workspace to open the panel in. Opening a panel that has no shell yet creates one. Hiding it never ends the shell. `--dir` and `--worker` default to the calling tab's, so inside a panel, `leapmux control terminal quake close` hides the panel you typed it into.

### Files and git

- `file read` and `git read` read at most `--limit` bytes. `--limit 0`, the default, reads 256 KiB. The Worker caps a larger `--limit` at the message size of the channel (`max_message_size`, 16 MiB unless your administrator changed it). `total_size` gives the size of the whole file, so a `content` shorter than `total_size` shows where the read stopped. Read the rest of a large file with `file read --offset`; `git read` has no offset.
- `file list` prints one listing per directory. With `--from-root <dir>`, it prints each directory from that ancestor down to the path, outermost first, so one request reads a whole branch. The Worker may answer with fewer levels than the depth implies, because a cap on the count of listings and the message size both drop the deepest levels.
- `file roots` reports the Worker's filesystem roots: `/` on a POSIX host, and each drive that carries a volume (`C:\`, `D:\`) on Windows. A WSL or Docker Worker runs a Linux binary, so it reports `/`.
- `git status`, `git branches` and `git worktrees` take the directory with `--dir`, which defaults to the calling tab's working directory. `git read` prints a file as the last commit (`--ref head`) or the index (`--ref staged`) holds it; `exists` is `false` when the file is absent at that ref, which tells it apart from an empty file.

### Tiles and layouts

- `tile split`, `tile close`, `tile make-grid` and `tile remove-grid` change one tile. `tile close` and `tile remove-grid` refuse a tile that holds tabs until `--with-tabs close` or `--with-tabs move` says what happens to them.
- `layout get` prints the whole tile tree of a workspace with its tabs. `layout set <workspace> [file]` replaces it from a JSON tree, and accepts the output of `layout get --json` as it is. A `split` needs at least two children and a `direction`; a `grid` needs `rows` and `cols` from 1 to 20 and exactly `rows * cols` children. An error identifies the node that fails, such as `root.children[1].children[0]`. If a tab arrives during the rewrite, `layout set` retries once; contention that persists fails with `conflict`.
- `layout watch [workspace]` prints every change of your layouts as it happens, one event per line. The first event is the snapshot. Each event carries a `kind`:

| `kind` | Meaning |
| --- | --- |
| `materialized` | The snapshot (always first) |
| `batch` | A batch of layout and tab operations |
| `batch_end` | The boundary after a committed batch |
| `resume_delta` | Frames of a resumed stream |
| `entity_materialized` | A tab, a tile or a floating window became visible |
| `entity_removed` | An entity was removed |
| `presence` | The clients that view a workspace changed |
| `workspace_renamed` | A workspace title changed |
| `workspace_created` | A workspace was created |
| `workspace_deleted` | A workspace was deleted |
| `workers_changed` | Your Worker list changed: a Worker registered, deregistered, came online, or went offline. It arrives for every workspace, but only when your credential can list Workers (`worker:read`). |
| `unknown` | An event that this CLI does not know |

To follow an agent's chat, use `agent messages --follow` instead.

### Watching events

`events` prints what happens in your workspaces as it happens, one event per line. It merges the Hub's layout changes with a watch of each Worker that hosts a tab, so one command sees every agent and every terminal.

- **Each line is one event**: the local time, the kind, the ids, and a summary.

  ```text
  2026-11-19T19:00:01.123+09:00 agent.activity workspace=9f3kQ2aB tab=87a19d5f worker=1a2b3c4d idle (3 tool calls)
  ```

  `--json` prints one `{"data": …}` envelope per line instead, with the full ids and a field for each fact.

| Kind | Meaning |
| --- | --- |
| `workspace.created`, `workspace.renamed`, `workspace.deleted` | A workspace was created, renamed or deleted. |
| `tab.opened`, `tab.closed`, `tab.moved` | A tab opened, closed, or moved to another workspace. |
| `agent.activity` | An agent became `idle`, `working` or `waiting_for_user`. |
| `agent.status` | The status of an agent's process changed. |
| `agent.turn_end` | An agent ended a turn. |
| `agent.request`, `agent.request_ended` | An agent asks for a permission or an answer, and the request ended. `request` is the handle that `agent approve`, `agent deny` and `agent answer` take. |
| `agent.todos`, `agent.background_tasks` | An agent's to-do list or background tasks changed. |
| `terminal.status`, `terminal.exit` | A terminal's shell changed its status, or exited with its exit code. |
| `terminal.title`, `terminal.bell`, `terminal.notification`, `terminal.progress` | A program in a terminal set the title, rang the bell, asked for a desktop notification, or reported its progress. |
| `stream.ready`, `stream.unavailable`, `stream.gap`, `stream.heartbeat` | The health of the stream itself. |

- **`--kind` selects what prints**: a kind such as `agent.request`, or a family such as `agent`. Repeat it, or separate the values with commas. An unknown value fails with `usage` and lists the known ones.
- **`--workspace` narrows the scope** to one workspace. Without it, the stream watches every workspace. A tab that opens while the stream runs joins it, and a tab that closes leaves it. The stream ends with status `0` when the watched workspace is deleted.
- **Read the state after `stream.ready`.** The stream has no cursor, because a Worker keeps no record of these events, so each run starts from the present. A state that you read after `stream.ready`, such as `agent list`, misses no later change.
- **The stream survives drops.**
  - When the Hub or a Worker cannot deliver, the stream prints `stream.unavailable` and retries. It watches an offline Worker again as soon as the Hub reports that a Worker came online.
  - A Worker that refuses for good, such as for a missing permission, prints `retrying: false`, and the stream goes on without it.
  - When a source delivers again, the stream prints `stream.gap` with the time that it stopped (`since`) and the kinds that it may have lost (`lost`). Then it prints each change of an agent's activity and status, and of the tabs and workspaces, that happened meanwhile, with `reconciled: true`.
- **`--kind` never hides the health of the stream**: a `stream.*` event that `--kind` does not select prints on stderr.
- **`--limit N`** ends the stream after N events, so `--limit 1` waits for the next one. **`--heartbeat 30s`** prints `stream.heartbeat` every 30 seconds, so a script can tell a quiet stream from a stuck one; `--limit` does not count it.
- Ctrl-C ends the stream with status `0`.

`layout watch` prints the raw changes of the layout instead, and `agent messages --follow` prints an agent's chat.

## End-to-end examples

### Drive an agent from your laptop

```bash
leapmux control auth login --hub https://leapmux.example.com

WS=$(leapmux control workspace create "Bugfix" -q)
A=$(leapmux control agent open --workspace "$WS" --worker name:build-box \
      --provider claude --dir ~/src/app \
      --message "Find and fix the failing test in ./pkg/auth." -q)

leapmux control agent messages "$A" --follow
```

### Run two agents and collect their answers

```bash
A=$(leapmux control agent open --provider claude --message "Fix the flaky test in ./pkg/auth." -q)
B=$(leapmux control agent open --provider codex --message "Update CHANGELOG.md for the release." -q)

# Wait until both agents settle, for half an hour at most.
leapmux control agent wait "$A" "$B" --timeout 30m

# Ask one of them a follow-up question and keep the answer.
summary=$(leapmux control agent send "$A" "Summarize your change in one line." --wait)
```

### Approve an agent's prompts

```bash
# Wait until the agent stops at a prompt, then read what it asks.
leapmux control agent wait "$A" --until waiting_for_user
leapmux control agent requests "$A"

# Approve the first request, or refuse it with a reason.
R=$(leapmux control agent requests "$A" -q | head -1)
leapmux control agent approve "$A" "$R"
leapmux control agent deny "$A" "$R" --message "Run the unit tests only."
```

### React to what agents do

```bash
# Ring the bell and show each prompt that an agent stops at.
leapmux control events --kind agent.request --json |
  while read -r line; do
    printf '\a'
    jq -r '.data | "\(.tab_id): \(.summary)"' <<<"$line"
  done

# Wait for the next prompt of any agent, then read it in full.
ev=$(leapmux control events --kind agent.request --limit 1 --json)
leapmux control agent requests "$(jq -r .data.tab_id <<<"$ev")"
```

### A script that runs inside an agent

No login and no ids: the tab supplies them.

```bash
#!/usr/bin/env bash
set -euo pipefail

# Inspect the repository that this agent works in.
leapmux control git status

# Open a terminal beside this tab and run the build in it.
T=$(leapmux control terminal open -q)
leapmux control terminal run "$T" 'make build'
```

### Snapshot and restore a layout

```bash
leapmux control layout get "$WS" --json > before.json
leapmux control tile split "$TILE" --direction vertical
# ... experiment ...
leapmux control layout set "$WS" before.json
```

### Branch on the result in a script

```bash
if ! out=$(leapmux control tab close "$T" --json 2>/dev/null); then
  case $(jq -r .error.code <<<"$out") in
    tab_busy_refused) echo "the tab is busy; try again later" ;;
    not_found)        echo "the tab is already gone" ;;
    *)                jq -r .error.message <<<"$out"; exit 1 ;;
  esac
fi
```

## Sockets and login

A `--hub` value may be a Hub's local IPC listener (`unix:$HOME/.config/leapmux/hub/hub.sock` on Unix, `npipe:...` on Windows) as well as an http(s) URL. A socket URL still reaches the Hub: the CLI presents the same `Authorization: Bearer` credential as over http(s). Only the Worker's socket inside a tab uses the internal `X-LeapMux-Token` header.

Login rules:

- **Solo needs no login over its local socket.** Only the machine that runs the Hub can reach that socket, so ordinary commands need no credential there. TCP never receives this exemption. Before the `solo` password exists, a TCP caller can only set the first password. After that, TCP requires a normal login. See [Solo mode: a reduced threat model](/docs/admin/security/#solo-mode-a-reduced-threat-model). Solo still **authorizes apps**: a login mints a scoped credential, and the Hub enforces that credential's permissions rather than the solo account's. Both flows complete, because the consent pages accept the solo account. See [App Authorization](/docs/admin/app-authorization/#solo-mode).
- **A Hub that is not solo, over a socket,** uses `--device-code`. `leapmux control auth login --hub unix:...hub.sock --device-code` dials the socket for the token exchange while you approve in a browser against the Hub's **public** origin, which the Hub derives from its own settings. The local-redirect flow refuses a socket URL and points at `--device-code`, because a browser cannot reach a socket.

```bash
# With a Hub that runs on this machine (leapmux hub start):
leapmux control auth login --hub unix:$HOME/.config/leapmux/hub/hub.sock --device-code
leapmux control admin settings list --hub unix:$HOME/.config/leapmux/hub/hub.sock
```

## Security model

The two transports carry different credentials and trust boundaries:

- **External CLI.** Each Hub credential is a bearer token (an `api_tokens` row, stored only as a peppered HMAC-SHA256 hash). End-to-end channels to Workers use the same Noise_NK handshake the browser uses, with each Worker's static key pinned for each Hub on first use (see [Worker key pins](#worker-key-pins)).
- **An agent or terminal that a Worker started.** The Worker hands the process a private local interprocess communication (IPC) endpoint and a token for that process, scoped to the user and the tab. On Unix, the endpoint is a socket with mode `0600`. It lives in `<data-dir>/run/control` when the full socket path fits the platform limit. Otherwise, the Worker uses a private namespace under the canonical system `/tmp` directory. That namespace derives from the operating-system account and the canonical Worker data directory, independently of the Worker ID or the session environment. Only that account can access the directory. The Worker holds its namespace lock until every control listener closes. On Windows, the endpoint is a named pipe with an explicit access-control list that grants access only to that account's security identifier (SID). When the agent or terminal closes, the endpoint closes and the token is invalidated.
- **A call to a sibling Worker from inside a tab.** Reaching another Worker (an `agent`, `terminal`, `file` or `git` command whose target lives on another Worker) uses a delegation token that the host Worker mints: it carries your identity and lists the machines it may reach. The Worker mints it on the first such call and revokes it when the agent closes, so an agent that never reaches another Worker never holds one.
- **Administration stays out of a tab.** `leapmux control admin ...` refuses to run inside a LeapMux tab, even with `--hub`, so an agent cannot act as an administrator through your credential.

For the full trust model, what the Hub can and cannot see, and the encryption primitives, see [Security & Threat Model](/docs/admin/security/).

## See also

- [Control Command Reference](/docs/reference/control-commands/) — every command with its arguments, flags and examples.
- [Coding Agents](/docs/using/coding-agents/) — providers, models, effort, control prompts, and resume that `agent` commands drive.
- [Terminals](/docs/using/terminals/) — PTY sessions, shells, and the automatic `LEAPMUX_CONTROL_*` variables.
- [Managing Workers](/docs/admin/managing-workers/) — Worker registration, approval, and key pinning.
- [Admin CLI](/docs/admin/admin-cli/) — the `control admin` commands for Hub administration.
- [Recovery](/docs/admin/recover/) — the offline break-glass tree.
- [Tabs and Layout](/docs/using/tabs-and-layout/) — the tile, split and grid model that `tile` and `layout` change.
- [Worktrees and Branches](/docs/using/worktrees-and-branches/) — the worktree dispositions that `tab close --worktree` uses.
