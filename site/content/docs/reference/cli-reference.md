---
title: "CLI Reference"
description: "Quick-lookup reference for the leapmux (lmx) CLI: command trees, daemon modes and flags, Hub resolution order, offline recovery, shell completions, and environment variables."
type: docs
weight: 1
---

This is the quick-lookup reference for the `leapmux` command line (also available as the short `lmx` alias). It covers the command trees, daemon lifecycle commands (`leapmux up`, `leapmux down`, and `leapmux daemon <solo|hub|worker|dev>`), Hub resolution order, `version`, `recover`, shell completions, and environment variables.

For task-oriented guides, see [Running LeapMux](/docs/admin/running-leapmux/), [Starting and Stopping](/docs/admin/starting-and-stopping/), [Configuration Reference](/docs/admin/configuration-reference/), [Recovery](/docs/admin/recover/), and [CLI Guide](/docs/using/cli-guide/).

## Which `leapmux` command tree?

`leapmux` ships as a single binary with four distinct command trees:

| Command tree | What it does | Where it runs | Auth required | Full reference |
|---|---|---|---|---|
| `up`, `down`, `daemon <solo\|hub\|worker\|dev>` | Start, stop, register, and manage local LeapMux services | Host machine (manages local OS service or foreground process) | None (`daemon worker register` takes a Hub registration key) | [Starting and Stopping](/docs/admin/starting-and-stopping/) |
| Control commands (`agent`, `terminal`, `workspace`, `file`, `git`, …) | Drive a running Hub and its Workers over RPC | Any machine, scripts, or inside spawned LeapMux tabs | `leapmux auth login`, local solo IPC, or Worker-injected tab token | [Control Command Reference](/docs/reference/commands/) |
| `admin` | Online Hub administration (users, sessions, settings, apps, Workers) | External shell against a running Hub (refuses to run inside a tab) | Administrator login (sensitive writes require step-up elevation) | [Admin CLI](/docs/admin/admin-cli/) |
| `recover` | Offline break-glass repair (first-admin bootstrap, password reset, key rotation, DB inspection) | Hub host with the Hub **stopped** (opens the database directly) | Local filesystem access to Hub data dir and config | [Recovery](/docs/admin/recover/) |

### Disambiguating Worker deregistration commands

Three commands share the words `worker deregister` and act on different layers:

| Command | Target | Where to run it | What it removes |
|---|---|---|---|
| `leapmux daemon worker deregister` | The local Worker in `--data-dir` | On the Worker machine, **with the Worker stopped** | Deletes this Worker on the Hub, then deletes local `state.json` (keeps `worker.db`) |
| `leapmux worker deregister <worker>` | One of **your own** Workers | Anywhere with a user CLI login | Deletes the Worker record on the Hub over RPC |
| `leapmux admin worker deregister <worker>` | **Any user's** Worker | External shell with an **admin** login | Deletes the Worker record on the Hub over RPC |

## Hub resolution order

When you run a control command or `leapmux admin`, the CLI resolves which Hub to contact in this exact precedence order:

| Priority | Source | Notes |
|---|---|---|
| 1 | `--hub <url>` flag | Explicit flag always wins, even inside a LeapMux tab. |
| 2 | `LEAPMUX_CONTROL_SOCK` | Worker-injected local inter-process communication (IPC) socket inside a spawned tab (paired with `LEAPMUX_CONTROL_TOKEN`). |
| 3 | `LEAPMUX_HUB` | Environment variable for external shells and scripts. |
| 4 | `context.json` | Persistent default Hub set with `leapmux context use --hub <url>`. |
| 5 | Stored credential default | Uses the sole stored credential when only one exists; otherwise uses the Hub recorded in `default-hub` (`leapmux auth use <url>`). |
| 6 | Local auto-discovery | Connects to a live local `solo` or `dev` Hub IPC socket when no stored credential file exists. |

## Top-level usage

`leapmux --help` prints:

```text
LeapMux runs coding agents and terminals on your machines and shows them in
your browser.

Start a local instance with "leapmux up" (or "leapmux daemon solo start").
Drive workspaces, agents, terminals, files, and layouts directly with
"leapmux <command>" (or the short "lmx" alias).

Usage:
  leapmux <command>

Daily Work:
  tree          Show a hierarchical overview of sections, workspaces, tiles, agents, terminals and files
  workspace     List, create, organize, export, apply, and delete workspaces
  section       List, create, rename, move, and delete sidebar sections
  tab           List, close, rename, move and reveal tabs of every type, and set their status
  agent         Start, message, watch and configure coding agents
  terminal      Open, type into, read and resize terminals
  events        Print what happens in your workspaces as it happens, one event per line
  notify        Show a notification in every LeapMux window that you have open
  notification  Send, list and clear recent Worker notifications

Files & Git:
  file          List, read, search, glob, and open files on a worker
  git           Read and change the git state of a directory on a worker
  worktree      List, inspect, create, and remove git worktrees on a worker

Layout & Windows:
  tile          Split, close and arrange the tiles of a workspace
  window        List, create, position, resize, and close floating windows
  layout        Read, replace and watch the whole tile tree of a workspace

Fleet & Auth:
  whoami        Show the identity and tab context that this CLI acts as
  doctor        Check local daemons, Hub connectivity, auth, pins, and online Workers
  auth          Sign this CLI in to a hub, and manage its credentials
  context       Show, set, or clear persistent default hub, worker, workspace, and provider outside a tab
  settings      Read and change your account preferences on the hub
  worker        List workers, read what they report, and register new ones
  tunnel        Forward TCP ports or connect stdio to a host on a worker over E2EE

Admin & Recovery:
  admin         Administer the hub; needs an administrator's login (offline repair: "leapmux recover")
  rpc           Call one procedure of the hub or of a Worker with a request in protobuf JSON
  recover       Offline break-glass recovery (opens the database directly)

Meta:
  guide         Print the guide for an agent that drives LeapMux
  skill         Inspect and install LeapMux agent skills locally or on a Worker
  version       Print the version of this CLI, and of the hub and worker when connected
  commands      List commands; with --json, include arguments, flags and examples
  completion    Print the shell completion script
  help          Print the help of a command

Local Daemon:
  daemon        Manage local LeapMux services (solo, hub, worker, dev)
  up            Start the local solo LeapMux daemon (shortcut for daemon solo start)
  down          Stop the local solo LeapMux daemon (shortcut for daemon solo stop)

Flags:
      --color string   when to emit ANSI colors on TTY output (one of: auto, always, never; default: auto)
      --fields string  comma-separated columns or fields to print
      --format string  output format (one of: text, json, ndjson, tsv; default: text)
      --hub string     hub URL (default: tab socket, else $LEAPMUX_HUB, else context/default credential, else local solo/dev Hub)
      --json           print the result as one JSON envelope per line
      --no-header      omit the header row from table and TSV output
  -q, --quiet          print only the ids of the result, one per line
  -v, --version        print the version of this binary
  -w, --wide           do not truncate table columns

Run "leapmux <command> --help" for the help of a command.
Any command name can be shortened as far as it stays unambiguous. A later
release can make an ad-hoc prefix ambiguous, so scripts use full names or
documented aliases (such as ag, term, ws, ls, rm, mv).
```

Dispatch rules:

- The default TCP port for `solo`, `hub`, and `dev` is **4327**.
- `-h` and `--help` work at every command level (exit `0`), and `leapmux help <command>` prints help for a command.
- `-v` and `--version` print the binary build version offline without dialing a Hub. `leapmux version` prints the CLI version and, when connected, the Hub and Worker versions. Subcommands do not accept `--version`.
- Daemon modes require a subcommand, and flags follow the subcommand: `leapmux daemon solo start --listen ...`. Placing a mode flag before the subcommand fails.
- Every command uses two hyphens for long flags (`--listen`) and one hyphen only for one-letter flags (`-q`, `-v`).

{{< callout type="info" >}}
**Duration formats.** Daemon and config flags whose default is a duration (`1h`, `5m`, …) accept unit suffixes (`ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w`) and combined parts; a bare number is a count of **seconds** (see [Configuration Reference](/docs/admin/configuration-reference/)). Control-command duration flags (`--timeout`, `--heartbeat`, `--since`) use Go duration units (`ms`, `s`, `m`, `h`).
{{< /callout >}}

## Mode commands

Every daemon mode (`leapmux daemon <mode>`) requires a subcommand. The eight lifecycle commands work in every mode, and `daemon worker` adds three Worker-specific commands. Start and stop an instance with `start` and `stop` (or `leapmux up` and `leapmux down` for `solo`); use `run` only in a container. See [Starting and Stopping](/docs/admin/starting-and-stopping/).

| Command | What it does |
|---|---|
| `daemon <mode> start` | Creates the OS service when needed, starts it, waits until ready, and prints the URL to open. |
| `daemon <mode> stop` | Stops the instance (service or foreground) and waits for shutdown to complete. |
| `daemon <mode> restart` | Stops the instance, then starts it as a service. |
| `daemon <mode> status` | Reports process state, readiness, browser URL, registration, and the last failure. |
| `daemon <mode> logs` | Prints or follows (`--follow`, `--lines`) the latest attempt log of the service. |
| `daemon <mode> enable` | Registers the OS service for startup at login or boot without starting it. |
| `daemon <mode> disable` | Removes the OS service. Requires a stopped instance. |
| `daemon <mode> run` | Runs in the foreground without creating a service. Used by the container image. |
| `daemon worker register` | Registers the Worker with a Hub and saves its credentials without starting it. See [below](#worker-register). |
| `daemon worker deregister` | Deletes the Worker on the Hub and removes local credentials. Requires a stopped Worker. See [below](#worker-deregister). |
| `daemon worker cross-worker-pins` | Manages the Worker's local trust-on-first-use (TOFU) pin store. See [below](#worker-cross-worker-pins). |

`start`, `restart`, `enable`, and `run` accept the runtime flags of the mode, and the service persists the flags you pass. `stop`, `status`, `logs`, and `disable` take only `--data-dir` and `--config` from the mode, plus their own options below.

**Options of the mode commands**

| Flag | Commands | Default | Meaning |
|---|---|---|---|
| `--data-dir`, `--config` | all | Mode defaults | Select the instance. Pass the same values to every command for one instance. |
| `--timeout` | `start`, `restart`, `stop`, `status`, `up`, `down`, `worker register`, `worker deregister` | `60s` (`start`/`up`), `4m` (`restart`), `3m` (`stop`/`down`), `30s` (`deregister`), `3s` (`status`), `5m` (`register`) | How long to wait: a positive duration or bare number of seconds. The operation can continue after the wait expires. |
| `--startup` | `start`, `restart`, `enable`, `up` | Saved choice, else prompt | `login` or `boot` |
| `--yes` | `start`, `restart`, `enable`, `up` | off | Accept the proposed configuration without the consent prompt. Does not choose `--startup`. |
| `--environment-file` | `start`, `restart`, `enable`, `up` | none | Path to a YAML map of environment variables for the service. `--environment-file=` clears the saved file. |
| `-f`, `--follow` | `logs` | off | Follow the log file as new lines appear. |
| `-n`, `--lines` | `logs` | `100` | Number of trailing lines to print (`0` prints the entire log). |
| `--json` | `status`, `logs`, `start`, `stop`, `restart`, `enable`, `disable`, `up`, `down`, `worker cross-worker-pins`, `recover *` | off | Print the result as one `{"data": ...}` line (or one JSON line per log entry with `logs --follow --json`). `daemon worker register` and `daemon worker deregister` accept `--json` syntactically from the root flag set but print plain text. |

`status` exits with `0` when the instance is running, `8` when stopped, `1` on failure or when unresponsive, and `2` on invalid usage. A Worker without a Hub connection and a `dev` instance waiting for `/setup` both count as running.

Every other mode command and every `recover` command reports failures as `error: <code>: <message>` on stderr (or `{"error": ...}` with `--json`) and maps the error code to the exit status table in [CLI Guide](/docs/using/cli-guide/). Interrupted commands (`Ctrl+C`) exit with `130`.

### `status --json`

`status --json` prints one `{"data": <report>}` line. Unavailable values are `null`.

| Field | Shape |
|---|---|
| `instance_id`, `mode`, `startup` | String or `null` |
| `execution` | `foreground`, `service`, or `null` |
| `configured` | Boolean: the service matches the saved configuration |
| `process` | `state` (`starting`, `running`, `stopping`, `stopped`, `failed`, `unresponsive`), `pid`, `started_at` |
| `readiness` | `starting`, `ready`, `ready_for_setup`, `failed`, or `null` |
| `url` | Browser URL resolved at startup (`public_url`, or `http://localhost:<port>`). `null` for Workers, IPC-only instances, and stopped instances. |
| `registration` | `state` (`not_applicable`, `unregistered`, `pending`, `registered`, `invalid`), `worker_id`, `hub_url` |
| `connection` | `state` (`not_applicable`, `pending_setup`, `connecting`, `connected`, `disconnected`), `last_error` |
| `attempt` | Latest start: `id`, `started_at`, `ended_at`, `exit_code`, `error`, `log_path` (each nullable) |
| `diagnostics` | Array of `{kind, title, body, source}` objects |

## `solo`

Run a Hub and a Worker in one process on loopback. Local IPC needs no credential; TCP browsers complete first-password setup for the `solo` account, after which every TCP address requires login. See [Running LeapMux](/docs/admin/running-leapmux/).

```bash
leapmux up [flags]
# or equivalently:
leapmux daemon solo start [flags]
```

`restart`, `enable`, and `run` take the same flags.

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:4327` plus platform local IPC URL | Repeatable listen address: TCP address or local IPC URL (`unix:<path>`, `npipe:<name>`). The platform local IPC URL is added automatically if the list specifies none. |
| `--data-dir` | `~/.config/leapmux/solo` | Data directory (split into `<data-dir>/hub` and `<data-dir>/worker`) |
| `--dev-frontend` | empty | Frontend dev-server URL for the local reverse proxy |
| `--storage-sqlite-max-conns` | `4` | SQLite maximum open connections |
| `--storage-sqlite-cache-size` | `0` | SQLite page cache: positive = pages, negative = KiB (for example `-65536` = 64 MiB) |
| `--storage-sqlite-mmap-size` | `0` | SQLite memory-mapped I/O size in bytes (`0` = disabled) |
| `--agent-startup-concurrency` | `0` (`min(4, CPU count)`) | Maximum agents starting concurrently on the bundled Worker |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` (for the bundled Worker) |
| `--use-login-shell` | `true` | Wrap agent launches in the user's login shell (for the bundled Worker) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |

Binding `solo` to a non-loopback address logs a warning: before the `solo` account has a password, the first TCP caller can claim it. Set the password first, or use `hub` or `dev` for multi-user deployments. The `public_url` setting also applies in `solo` mode for the startup URL and the **Register worker** command.

## `hub`

Run only the Hub: authentication, workspace management, Worker registration, and the encrypted relay. Binds all interfaces (`:4327`) by default and requires login. See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration Reference](/docs/admin/configuration-reference/).

```bash
leapmux daemon hub start [flags]
```

`restart`, `enable`, and `run` take the same flags. Runtime policies (sign-up, verification, sessions, SMTP, `secure_cookies`, `public_url`, timeouts, limits) are Hub database settings managed with `leapmux admin settings` (see [Admin CLI](/docs/admin/admin-cli/)).

**Server and config options**

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `:4327` plus platform local IPC URL | Repeatable listen address (`:4327`, `127.0.0.1:4327`, `unix:<path>`, `npipe:<name>`). The platform local IPC URL is added automatically if the list specifies none. |
| `--data-dir` | `~/.config/leapmux/hub` | Data directory |
| `--dev-frontend` | empty | Frontend dev-server URL for the reverse proxy |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |

**Storage options**

| Flag | Default | Meaning |
|---|---|---|
| `--storage-type` | `sqlite` | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `--storage-sqlite-path` | `<data-dir>/hub.db` | SQLite database file path |
| `--storage-sqlite-max-conns` | `4` | SQLite maximum open connections |
| `--storage-sqlite-cache-size` | `0` | Page cache: positive = pages, negative = KiB (for example `-65536` = 64 MiB) |
| `--storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (`0` = disabled) |
| `--storage-postgres-dsn` | empty | PostgreSQL connection string (required for `postgres`) |
| `--storage-mysql-dsn` | empty | MySQL connection string (required for `mysql`) |

The Postgres family (`postgres`, `cockroachdb`, `yugabytedb`) and MySQL family (`mysql`, `tidb`) share these pool defaults (see [Configuration Reference](/docs/admin/configuration-reference/) for every flag and DSN format):

| Family | `max-conns` | `min-conns` | `max-idle-conns` | `conn-max-lifetime` | Idle-time flag & default | `health-check-period` |
|---|---|---|---|---|---|---|
| Postgres (`postgres`, `cockroachdb`, `yugabytedb`) | 25 | 5 | — | `1h` | `max-conn-idle-time` (`5m`) | `30s` |
| MySQL (`mysql`, `tidb`) | 25 | — | 5 | `1h` | `conn-max-idle-time` (`5m`) | — |

{{< callout type="info" >}}
One Hub config key has no CLI flag and is set only through YAML or environment variable: `encryption_key_path` (`LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `<data-dir>/encryption.key`). See [Encryption & Data](/docs/admin/encryption-and-data/).
{{< /callout >}}

## `worker`

Run a standalone Worker that connects outbound to a Hub. Workers do not listen on an inbound HTTP port. Register the Worker once with `daemon worker register` before starting it. See [Managing Workers](/docs/admin/managing-workers/).

```bash
# Once: register with a key from the Hub's Register worker dialog
leapmux daemon worker register --hub https://hub.example.com --registration-key <key>

# Then start it as a background service
leapmux daemon worker start [flags]
```

`restart`, `enable`, and `run` take the same flags. Runtime commands read the Hub URL saved during registration and reject `--hub` and `--registration-key`.

**Worker options**

| Flag | Default | Meaning |
|---|---|---|
| `--name` | hostname | Worker display name |
| `--data-dir` | `~/.config/leapmux/worker` | Data directory (holds `state.json` and `worker.db`) |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `--use-login-shell` | `true` | Wrap each agent launch in the user's login shell |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path |

**Timeout and limit options**

| Flag | Default | Meaning |
|---|---|---|
| `--max-message-size` | `0` (`16 MiB`) | Maximum application payload size in bytes |
| `--agent-startup-timeout` | `5m` | Agent startup timeout |
| `--api-timeout` | `10s` | JSON-RPC request timeout |
| `--agent-startup-concurrency` | `0` (`min(4, CPU count)`) | Maximum agents restored concurrently after a Worker restart |

**SQLite database options**

| Flag | Default | Meaning |
|---|---|---|
| `--db-max-conns` | `4` | Maximum open database connections |
| `--db-cache-size` | `0` | Page cache: positive = pages, negative = KiB (for example `-65536` = 64 MiB) |
| `--db-mmap-size` | `0` | Memory-mapped I/O size in bytes (`0` = disabled) |

### `worker register`

Register the Worker with a Hub, save its credentials in `--data-dir`, and exit without starting the Worker.

```bash
leapmux daemon worker register --hub <url> --registration-key <key> [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `--hub` | required | Hub URL (`http[s]://...`, `unix:<socket>`, or `npipe:<name>`). Saved for all future runs. |
| `--registration-key` | required | Single-use key from the Hub's **Register worker** dialog. |
| `--timeout` | `5m` | Total wait time for the Hub, including retries. |
| `--data-dir` | `~/.config/leapmux/worker` | Data directory. |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path (supplies `data_dir`). |

`--hub` and `--registration-key` are flags only (no config key or environment variable sets them). Re-running the same command completes an interrupted registration without spending a new key. If credentials already exist in `--data-dir`, the command refuses; see [Managing Workers](/docs/admin/managing-workers/).

### `worker deregister`

Delete this Worker on its Hub, then remove its credentials from `--data-dir`. Stop the Worker first.

```bash
leapmux daemon worker deregister [flags]
```

| Flag | Default | Meaning |
|---|---|---|
| `--timeout` | `30s` | How long to wait for the Hub. |
| `--data-dir` | `~/.config/leapmux/worker` | Data directory. |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path (supplies `data_dir`). |

`worker deregister` preserves `worker.db`. If the Hub cannot confirm deletion, local credentials are preserved so you can retry.

### `worker cross-worker-pins`

Local-only management of the Worker's trust-on-first-use (TOFU) pin store for sibling Workers. Operates on local files without starting a Worker. See [Security & Authentication](/docs/admin/security-and-auth/).

```bash
leapmux daemon worker cross-worker-pins list [flags]
leapmux daemon worker cross-worker-pins get <worker-id> [flags]
leapmux daemon worker cross-worker-pins remove <worker-id> [flags]
```

| Command | Action |
|---|---|
| `list` | Print all pinned sibling Worker IDs and timestamps (with `--json`, `{"data": {"pins": [...]}}` including keys). |
| `get <worker-id>` | Print the pin and keys for one sibling Worker. |
| `remove <worker-id>` | Delete the pin for one sibling Worker (with `--json`, `{"data": {"removed": "<worker-id>"}}`). |

`get` and `remove` fail with `not_found` (exit status `4`) if no pin exists for `<worker-id>`. All three commands accept `--data-dir` and `--config`.

## `dev`

Run a Hub and a Worker in one process with real authentication, binding `:4327` (all interfaces), storing data in `~/.config/leapmux/dev/`, and bootstrapping the first administrator through `/setup`. `dev` accepts the same flags as `solo` and enables `signup_enabled=true` by default; the bundled Worker registers automatically after `/setup` completes. See [Running LeapMux](/docs/admin/running-leapmux/).

```bash
leapmux daemon dev start [flags]
```

## `version`

Print the CLI build version, plus the Hub version (and host Worker version inside a tab) when connected. With no selected Hub, `leapmux version` prints only the CLI version; if the selected Hub is unreachable, it prints the CLI version and the connection error.

```bash
$ leapmux --version
0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST
```

Top-level `-v` and `--version` run offline without contacting a Hub.

## `recover`

`leapmux recover` is the offline break-glass tree: it operates directly on the Hub database and encryption key file while the Hub is stopped, with no network calls and no login. See [Recovery](/docs/admin/recover/) for full flags and procedures.

```bash
leapmux recover <command> [arguments] [flags]
```

| Command / Group | Subcommands |
|---|---|
| `create-admin <username>` | Bootstrap the first administrator (refuses once any admin exists) |
| `reset-password <user>` | Reset a password (`<username>`, `username:<name>`, or `id:<id>`) |
| `encryption-key` | `rotate`, `remove <version>`, `reencrypt`, `rotate-pepper` |
| `db` | `path`, `migrate`, `version` |

Every `recover` command takes `--data-dir` and, for external PostgreSQL or MySQL databases, `--config`. Without `--config`, `recover` opens `<data-dir>/hub.db` directly.

## Control commands

Top-level `leapmux <group> <command>` drives a running Hub and its Workers over RPC. Use it interactively, in scripts (`leapmux auth login`), or inside spawned agents and terminals. All control commands support `--format text|json|ndjson|tsv`, `--json`, `-q`, `--fields`, `--no-header`, `-w`, and `--color`.

For every control command, argument, flag, and example, see the generated [Control Command Reference](/docs/reference/commands/). For target selection, output formats, exit codes, and scripting patterns, see [CLI Guide](/docs/using/cli-guide/). For online Hub administration (`leapmux admin`), see [Admin CLI](/docs/admin/admin-cli/).

## `commands` and `completion`

### `commands [prefix...]`

Print the command tree as a text summary or machine-readable JSON schema (`--json`):

```bash
leapmux commands
leapmux commands agent --depth 1 --compact --json
```

| Flag | Default | Meaning |
|---|---|---|
| `--depth <n>` | `0` (unlimited) | Maximum command depth relative to `[prefix...]` |
| `--compact` | off | Omit flag tables, argument help, and examples in JSON output |

### `completion <shell>`

Generate dynamic shell completion scripts for `leapmux` and `lmx`. Supported shells: `bash`, `zsh`, `fish`, `powershell`, `elvish`, `nushell`.

```bash
leapmux completion zsh > "${fpath[1]}/_leapmux"
leapmux completion bash > /etc/bash_completion.d/leapmux
```

## Environment variables

### Daemon configuration (`hub` / `solo` / `dev` and `worker`)

Hub-family modes (`hub`, `solo`, `dev`) read `LEAPMUX_HUB_*` variables; standalone Workers read `LEAPMUX_WORKER_*`. The suffix is lowercased to form the top-level config key. OS services do not inherit variables from the shell that starts them; pass `--environment-file` instead (see [Starting and Stopping](/docs/admin/starting-and-stopping/)).

| Variable | Config key | Example |
|---|---|---|
| `LEAPMUX_HUB_LISTEN` | Hub `listen` (comma-delimited) | `:4327,unix:/run/leapmux/hub.sock` |
| `LEAPMUX_HUB_DATA_DIR` | Hub `data_dir` | `/var/lib/leapmux/hub` |
| `LEAPMUX_HUB_LOG_LEVEL` | Hub `log_level` | `info` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | Hub `encryption_key_path` (no CLI flag) | `/etc/leapmux/encryption.key` |
| `LEAPMUX_WORKER_NAME` | Worker `name` | `build-box-1` |
| `LEAPMUX_WORKER_DATA_DIR` | Worker `data_dir` | `/var/lib/leapmux/worker` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | Worker `encryption_mode` | `post-quantum` |
| `LEAPMUX_WORKER_LOG_LEVEL` | Worker `log_level` | `info` |
| `LEAPMUX_WORKER_AGENT_STARTUP_CONCURRENCY` | Worker `agent_startup_concurrency` | `4` |

Nested storage keys (`storage.type`, `storage.postgres.dsn`, …) cannot be set through `LEAPMUX_HUB_*` environment variables because underscores are not translated to dots; use YAML or `--storage-*` flags instead. See [Configuration Reference](/docs/admin/configuration-reference/) for full precedence rules (`defaults < config file < env vars < CLI flags`).

### Control CLI (`leapmux`)

**User-set variables** (set these in your own shell or scripts):

| Variable | Meaning |
|---|---|
| `LEAPMUX_HUB` | Hub URL when neither `--hub` nor `LEAPMUX_CONTROL_SOCK` selects one. |
| `LEAPMUX_CONTROL_CONFIG_DIR` | Override the credential, context, and pin directory (default `$XDG_CONFIG_HOME/leapmux/control`, `%APPDATA%\leapmux\control` on Windows, or `~/.config/leapmux/control`). |
| `XDG_CONFIG_HOME` | Base config directory on POSIX when `LEAPMUX_CONTROL_CONFIG_DIR` is unset. |
| `LEAPMUX_CONTROL_NO_PROMPT` | Disable interactive step-up elevation prompts. |
| `LEAPMUX_FOCUS_NEW` | Override default focus behavior for `agent open`, `terminal open`, and `file open` (`0` = background, `1` = focus). |
| `NO_COLOR`, `CLICOLOR_FORCE` | Disable or force ANSI color output under `--color auto` (`NO_COLOR` also disables TTY wait spinners). |

**Worker-injected tab variables** (set automatically inside spawned agents and terminals; do not set by hand):

| Variable | Meaning |
|---|---|
| `LEAPMUX_CONTROL_CLI` | Full path to the Worker's matching `leapmux` binary for version hand-off. |
| `LEAPMUX_CONTROL_SOCK` | Local IPC socket URL for the tab (`leapmux admin` refuses to run when set). |
| `LEAPMUX_CONTROL_TOKEN` | Per-process delegation token for the local IPC socket. |
| `LEAPMUX_CONTROL_USER_ID` | Authenticated user ID (informational). |
| `LEAPMUX_CONTROL_WORKER_ID` | Host Worker ID (`self` for Worker targets and default `--worker`). |
| `LEAPMUX_CONTROL_TAB_ID` | Spawning tab ID (`self` for tab, agent, or terminal targets). |
| `LEAPMUX_CONTROL_TAB_TYPE` | `agent` or `terminal`. |
| `LEAPMUX_CONTROL_TERMINAL_ID` | Current terminal ID (`self` for `terminal` commands; in a Quake panel, set without `_TAB_ID` or `_TAB_TYPE`). |
| `LEAPMUX_CONTROL_WORKING_DIR` | Working directory at spawn (default `--dir`). |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | Agent provider in agent tabs (default `--provider`). |

`LEAPMUX_CONTROL_CONFIG_DIR` and `LEAPMUX_CONTROL_NO_PROMPT` are user-set CLI variables; all other `LEAPMUX_CONTROL_*` variables are Worker-injected into spawned tabs. See [CLI Guide](/docs/using/cli-guide/) for the full tab environment contract.

## Config and data locations

Each mode reads an optional YAML config file matching the mode and skips missing config files silently.

| Mode | Config file | Default data directory |
|---|---|---|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` (`hub/` and `worker/` subdirectories) |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` (`hub/` and `worker/` subdirectories) |
| `control` | `<ConfigDir>/<hub-key>.json` (credentials, mode `0600`), `default-hub`, `context.json`, `<hub-key>/pins.json` | — |

### Managed files inside a data directory

| Path | Mode | Holds |
|---|---|---|
| `<data-dir>/.lifecycle/` | every mode | Lock files, saved service settings, local control token, and the last ten start logs. Do not edit manually. |
| `<data-dir>/state.json` | `worker` | Hub URL, Worker ID, auth token, and private keys (secret). |
| `<data-dir>/pending-registration.json` | `worker` | Unfinished registration state and private keys (secret; deleted on completion). |
| `<data-dir>/worker.db` | `worker` | Agent transcripts, queues, and terminal state. |

Use `status --json` in scripts; the internal files above have no stable schema. See [Managing Workers](/docs/admin/managing-workers/) for backup guidance.
