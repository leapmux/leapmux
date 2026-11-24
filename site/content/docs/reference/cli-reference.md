---
title: "CLI Reference"
description: "Complete cheat-sheet for the leapmux (lmx) CLI: top-level control and skill commands, daemon modes and flags, recovery, shell completions, and environment variables."
type: docs
weight: 1
---

This is the quick-lookup cheat-sheet for the `leapmux` command line (also available as the short `lmx` alias). It covers the top-level command list, the daemon lifecycle commands (`leapmux up`, `leapmux down`, and `leapmux daemon <solo|hub|worker|dev>`), the unified `version` command, the environment variables LeapMux reads, and outlines of the command trees — `recover` (offline break-glass), top-level control commands (user-facing scripting), and `admin` (online hub administration) — which have their own dedicated chapters.

For task-oriented walkthroughs rather than reference tables, see [Running LeapMux](/docs/admin/running-leapmux/) (run modes, ports, data dirs, Docker), [Starting and Stopping](/docs/admin/starting-and-stopping/), [Configuration](/docs/admin/configuration/) (full config-key reference and storage backends), [Recovery](/docs/admin/recover/), and [Control CLI](/docs/using/control-cli/).

## Top-level usage

`leapmux` is a single binary. `leapmux --help` prints:

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

| Command | What it does | Reference |
|---------|--------------|-----------|
| `tree`, `workspace`, `section`, `tab`, `agent`, `terminal`, `events`, `notify`, `notification`, `file`, `git`, `worktree`, `tile`, `window`, `layout`, `whoami`, `doctor`, `auth`, `context`, `settings`, `worker`, `tunnel`, `rpc`, `guide`, `skill` | Drive a running Hub and Workers over RPC (interactive use, scripts, and spawned agents) | [Control CLI](/docs/using/control-cli/) |
| `admin` | Online hub administration over RPC (requires an admin login; refuses to run inside a tab) | [Admin CLI](/docs/admin/admin-cli/) |
| `up`, `down` | Start or stop the local solo daemon (`daemon solo start` / `daemon solo stop`) | [Starting and Stopping](/docs/admin/starting-and-stopping/) |
| `daemon solo` | Hub + Worker in one process; local IPC is credential-free and TCP starts with password setup | [Running LeapMux](/docs/admin/running-leapmux/) |
| `daemon hub` | Hub service only; Workers connect separately | [Running LeapMux](/docs/admin/running-leapmux/) |
| `daemon worker` | Worker only; connects out to a Hub (`register`, `deregister`, `cross-worker-pins`, lifecycle verbs) | [Managing Workers](/docs/admin/managing-workers/) |
| `daemon dev` | Hub + Worker in one process with real auth (development) | [Running LeapMux](/docs/admin/running-leapmux/) |
| `recover` | Offline break-glass: first-admin bootstrap, password reset, keys, db | [Recovery](/docs/admin/recover/) |
| `version` | Print the CLI build version, and the Hub and Worker versions when connected | [below](#version) |
| `commands` | Print every command; with `--json`, its arguments, flags, accepted values and examples | [Control CLI](/docs/using/control-cli/) |
| `completion` | Print the completion script for Bash, zsh, fish, PowerShell, Elvish, or Nushell | [Control CLI](/docs/using/control-cli/) |

Notes on dispatch:

- The default TCP port for every mode is **4327**.
- `-h` and `--help` are recognized at every level (help prints to stdout, exit 0), and `leapmux help <command>` prints the help of a command.
- `-v` and `--version` print the binary's build version without dialing a Hub. `leapmux version` prints the CLI version and, when connected to a Hub (or inside a tab), the Hub and Worker versions too. A subcommand takes no `--version` of its own.
- A daemon mode needs a command, and flags follow the command: `leapmux daemon solo start --listen ...`. `leapmux daemon solo --listen ...` fails, and the error states that a flag goes after the command that takes it. LeapMux also rejects an unknown leading flag such as `leapmux --listen ...`, because it is not a top-level flag.
- LeapMux rejects an unexpected positional argument and points you at `--help`.

{{< callout >}}
Every command of the binary parses its flags the same way. A long flag takes two hyphens (`--listen`), and a single hyphen goes only before a one-letter flag (`-q`, `-v`). A single hyphen before a long name, as in `-listen`, fails, and the error states the two-hyphen form.
{{< /callout >}}

> **Durations.** Daemon and config flags whose default is shown as a duration (`1h`, `5m`, …) take a unit suffix — `ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w` — and combine parts; a bare number is a count of **seconds** (see [Duration values](/docs/admin/configuration/#duration-values)). Control-command duration flags (`--timeout`, `--heartbeat`, `--since`) use Go duration units (`ms`, `s`, `m`, `h`).

## Mode commands

Every daemon mode (`leapmux daemon <mode>`) requires a command. The eight commands below work in every mode, and `daemon worker` adds three. Start and stop an instance with `start` and `stop` (or `leapmux up` and `leapmux down` for `solo`); use `run` only in a container. [Starting and Stopping](/docs/admin/starting-and-stopping/) explains the service that `start` and `enable` create.

| Command | What it does |
| --- | --- |
| `daemon <mode> start` | Creates the service when necessary, starts it, waits until it is ready, and prints the URL to open. |
| `daemon <mode> stop` | Stops the instance, whether `start` or `run` started it, and waits until its shutdown completes. |
| `daemon <mode> restart` | Stops the instance, then starts it as a service. |
| `daemon <mode> status` | Reports the process state, readiness, the URL to open, registration, and the last failure. |
| `daemon <mode> logs` | Prints or follows (`--follow`, `--lines`) the latest attempt log of the service. |
| `daemon <mode> enable` | Creates the service for startup at login or boot, without starting it. |
| `daemon <mode> disable` | Removes the service. Requires a stopped instance. |
| `daemon <mode> run` | Runs in the foreground until the process stops, and never creates a service. The container image uses it. |
| `daemon worker register` | Registers the Worker with a Hub and saves its credentials. Never starts it. See [below](#worker-register). |
| `daemon worker deregister` | Deletes the Worker on the Hub, then its credentials. Requires a stopped Worker. See [below](#worker-deregister). |
| `daemon worker cross-worker-pins` | Manages the Worker's pin store. See [below](#worker-cross-worker-pins). |

`start`, `restart`, `enable`, and `run` also take the runtime flags of the mode, which its section below lists. A service saves the runtime flags that you give it. `stop`, `status`, `logs`, and `disable` take only `--data-dir` and `--config` from the mode, plus their own options below.

A lifecycle command that succeeds, and `worker register`, end with the commands to run next for the same instance, such as `stop` and `disable` after `start`, or `start` and `enable` after `disable`. Each one selects the instance by its configuration file and its data directory, so it works from any directory. `run`, `logs`, and structured output (`--json`, `--format`) print no such commands.

**Options of the mode commands**

| Flag | Commands | Default | Meaning |
|------|----------|---------|---------|
| `--data-dir`, `--config` | all | the defaults of the mode | Select the instance. Pass the same values to every command for one instance. |
| `--timeout` | `start`, `restart`, `stop`, `status`, `up`, `down`, `worker register`, `worker deregister` | `60s` for `start`/`up`, `4m` for `restart`, `3m` for `stop`/`down`, `30s` for `deregister`, `3s` for `status`, `5m` for `register` | How long to wait: a positive duration, or a bare number of seconds. The operation can continue after the wait ends. |
| `--startup` | `start`, `restart`, `enable`, `up` | the saved choice, else a prompt | `login` or `boot` |
| `--yes` | `start`, `restart`, `enable`, `up` | off | Accept the proposed configuration without the consent prompt. It does not choose `--startup`. |
| `--environment-file` | `start`, `restart`, `enable`, `up` | none | Path to a YAML map of environment variables for the service. `--environment-file=` removes the saved file. |
| `-f`, `--follow` | `logs` | off | Follow the log file as new lines are written. |
| `-n`, `--lines` | `logs` | `100` | Number of trailing lines to print (`0` prints the entire log). |
| `--json` | `status`, `logs`, `start`, `stop`, `restart`, `enable`, `disable`, `up`, `down`, `worker cross-worker-pins`, `recover *` | off | Print the result as one `{"data": ...}` line (or one JSON line per log entry with `logs --follow --json`). |

`status` exits with `0` when the instance runs, `8` when it is stopped, `1` for a failure or an instance that does not answer, and `2` for invalid use. A Worker without a Hub connection and a `dev` instance that waits for `/setup` both count as running.

Every other command of a mode, and every `recover` command, reports a failure as `leapmux` does: an `error: <code>: <message>` line with its hints on stderr, or the `{"error": ...}` envelope with `--json`. The code sets the exit status by the one table in [Errors and exit codes](/docs/using/control-cli/#errors-and-exit-codes). For example, an unknown flag exits with `2`, a registration key that the Hub refuses with `3`, a `cross-worker-pins get` of a Worker without a pin with `4`, and a Hub that cannot answer with `7`. A command that an interrupt such as Ctrl-C ends exits with `130`.

### `status --json`

`status --json` prints one line, `{"data": <report>}`, and nothing else, as every `--json` command prints its result. A command line that fails before the report prints `{"error": ...}` instead. In the report, a value that is not available is `null`. These are the fields under `data`:

| Field | Shape |
| --- | --- |
| `instance_id`, `mode`, `startup` | String or `null` |
| `execution` | `foreground` or `service`, or `null` |
| `configured` | Boolean: the service matches the saved configuration |
| `process` | `state` (string), `pid` (integer or `null`), and `started_at` (timestamp or `null`) |
| `readiness` | `starting`, `ready`, `ready_for_setup`, `failed`, or `null` |
| `url` | The address to open in a browser, as the instance resolved it at startup: the `public_url` setting, or `http://localhost:<port>` from the TCP listen address. `null` for a Worker, for an instance without a TCP address, and for an instance that does not run. |
| `registration` | `state` (string), `worker_id` (string or `null`), and `hub_url` (string or `null`) |
| `connection` | `state` (string) and `last_error` (string or `null`) |
| `attempt` | The latest start: `id`, `started_at`, `ended_at`, `exit_code`, `error`, and `log_path`, each of them nullable |
| `diagnostics` | An array of objects with `kind`, `title`, `body`, and a nullable `source` |

The `state` fields take these values:

- `process.state`: `starting`, `running`, `stopping`, `stopped`, `failed`, or `unresponsive`. An unresponsive instance holds its lock but does not answer on its control endpoint.
- `registration.state`: `not_applicable`, `unregistered`, `pending`, `registered`, or `invalid`
- `connection.state`: `not_applicable`, `pending_setup`, `connecting`, `connected`, or `disconnected`

## solo

Run a Hub and a Worker in one process on loopback. Local IPC needs no credential. A TCP browser sets the first `solo` password, then every TCP address asks for it. See [Running LeapMux](/docs/admin/running-leapmux/#solo-mode) and [Network access](/docs/admin/configuration/#network-access).

```bash
leapmux daemon solo start [flags]
# then open the URL that it prints: http://localhost:4327 by default
```

`restart`, `enable`, and `run` take the same flags.

| Flag | Default | Meaning |
|------|---------|---------|
| `--listen` | `127.0.0.1:4327` plus the platform's local IPC URL | One listen address, repeatable: a TCP address or a local IPC URL (`unix:<path>`, `npipe:<name>`). The list is the bind set; the platform's local IPC URL is added when the list names none. Additional addresses are a setting; see [Network access](/docs/admin/configuration/#network-access). |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory (split into `<data-dir>/hub` and `<data-dir>/worker`) |
| `--dev-frontend` | empty | Frontend dev-server URL for the local reverse proxy |
| `--storage-sqlite-max-conns` | `4` | SQLite max open connections |
| `--storage-sqlite-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `--storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |
| `--max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel (for the bundled Worker) |
| `--agent-startup-concurrency` | `0` (= min(4, CPU count)) | Max agents starting concurrently on the bundled Worker |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` (for the bundled Worker) |
| `--use-login-shell` | `true` | Wrap the agent invocation in the user's login shell (for the bundled Worker) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |

{{< callout type="info" >}}
Binding solo to a non-loopback address logs a warning. Before the account has a password, the first TCP caller can claim it through the restricted setup flow. Set the password first, or use `hub` or `dev` for a multi-user deployment.
{{< /callout >}}

{{< callout type="info" >}}
The `public_url` setting applies in solo: it sets the URL in the startup banner, and the `--hub` address in the command that the **Register worker** dialog prints.
{{< /callout >}}

## hub

Run only the Hub: authentication, workspace management, Worker registration, and the encrypted relay. Binds all interfaces by default and requires a real login. See [Running LeapMux](/docs/admin/running-leapmux/#hub-mode) and [Configuration](/docs/admin/configuration/) for the complete key reference.

```bash
leapmux daemon hub start [flags]
```

`restart`, `enable`, and `run` take the same flags.

This table lists the most common flags. The full set — including all PostgreSQL/MySQL/CockroachDB/YugabyteDB/TiDB pool-tuning flags — is in [Configuration](/docs/admin/configuration/).

**Server options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--listen` | platform default | One listen address, repeatable. A TCP address (`:4327`, `127.0.0.1:4327`) or a local IPC URL (`unix:<path>`, `npipe:<name>`). The list is the bind set, and at least one local IPC address is always bound: the platform's local IPC URL is added when the list names none. |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `--dev-frontend` | empty | Frontend dev-server URL for the reverse proxy |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Behavioral settings.** Auth policy (sign-up, verification, sessions), SMTP, timeouts, and per-user limits are not flags: they are instance settings in the Hub's database, managed by `leapmux admin settings` (see [Configuration](/docs/admin/configuration/) and [Admin CLI](/docs/admin/admin-cli/)).

**Storage options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--storage-type` | empty (= `sqlite`) | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `--storage-sqlite-path` | empty (= `<data-dir>/hub.db`) | SQLite database file path |
| `--storage-sqlite-max-conns` | `4` | SQLite max open connections |
| `--storage-sqlite-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `--storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |
| `--storage-postgres-dsn` | empty | PostgreSQL connection string (required when `storage.type` is `postgres`) |
| `--storage-mysql-dsn` | empty | MySQL connection string (required when `storage.type` is `mysql`) |

The Postgres family (`--storage-postgres-*`, `--storage-cockroachdb-*`, `--storage-yugabytedb-*`) and the MySQL family (`--storage-mysql-*`, `--storage-tidb-*`) share pool defaults:

| Family | `max-conns` | `min-conns` | `max-idle-conns` | `conn-max-lifetime` | Idle-time flag & default | `health-check-period` |
|---|---|---|---|---|---|---|
| Postgres (`postgres`, `cockroachdb`, `yugabytedb`) | 25 | 5 | — | `1h` | `max-conn-idle-time` (`5m`) | `30s` |
| MySQL (`mysql`, `tidb`) | 25 | — | 5 | `1h` | `conn-max-idle-time` (`5m`) | — |

CockroachDB/YugabyteDB use the Postgres driver; TiDB uses the MySQL driver. See [Configuration](/docs/admin/configuration/) for every storage flag and DSN format.

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |

{{< callout type="info" >}}
One hub config key has **no** CLI flag and is set only via YAML or env var: `encryption_key_path` (`LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `<data-dir>/encryption.key`). Runtime settings such as `secure_cookies` are database settings managed with `leapmux admin settings` — see [Admin CLI](/docs/admin/admin-cli/). See also [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).
{{< /callout >}}

## worker

Run a Worker that connects out to a Hub. Workers do not serve an inbound HTTP port. Register a Worker once with `worker register` before you start it. See [Managing Workers](/docs/admin/managing-workers/).

```bash
# Once: register, with a key from the Register worker dialog of the Hub.
leapmux daemon worker register --hub https://hub.example.com --registration-key <key>

# Then start it as a background service.
leapmux daemon worker start [flags]
```

`restart`, `enable`, and `run` take the same flags.

The Worker connects to the Hub URL that its registration saved, so the runtime commands take no `--hub` and no `--registration-key`. They refuse both flags.

**Worker options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--name` | empty (= hostname) | Worker display name |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory (holds `state.json`, `worker.db`) |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `--use-login-shell` | `true` | Wrap each agent invocation in the user's login shell |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Timeout and limit options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `--max-message-size` | `0` (= 16 MiB) | Max application payload size in bytes; reassembled ceiling adds 64 KiB headroom |
| `--agent-startup-timeout` | `5m` | Agent startup timeout |
| `--api-timeout` | `10s` | JSON-RPC request timeout |
| `--agent-startup-concurrency` | `0` (= 4, or the CPU count when lower) | How many agents the Worker brings back at once after a restart |

**SQLite database options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--db-max-conns` | `4` | Max open database connections |
| `--db-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `--db-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path |

### worker register

Register the Worker with a Hub, save its credentials in the data directory, and exit. It never starts the Worker.

```bash
leapmux daemon worker register --hub <url> --registration-key <key> [flags]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--hub` | required | URL of the Hub: `http[s]://...`, `unix:<socket>`, or `npipe:<name>`. The Worker connects to this URL from then on. |
| `--registration-key` | required | The key from the **Register worker** dialog of the Hub |
| `--timeout` | `5m` | How long to wait for the Hub, retries included |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path; it supplies `data_dir` |

`--hub` and `--registration-key` are flags only: no config key or environment variable sets them. When an earlier run stopped before it finished, the same command completes that registration without using another key. A data directory that already holds credentials makes the command fail; see [Register a Worker again](/docs/admin/managing-workers/#register-a-worker-again).

### worker deregister

Delete this Worker on its Hub, then remove its credentials from the data directory. Stop the Worker first.

```bash
leapmux daemon worker deregister [flags]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--timeout` | `30s` | How long to wait for the Hub |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path; it supplies `data_dir` |

The command keeps `worker.db`. When the Hub cannot confirm the deletion, it keeps the credentials too, so that you can run it again.

### worker cross-worker-pins

A local-only utility for the Worker's trust-on-first-use (TOFU) pin store. It works on local files alone, and no Worker process starts. See [Security & Threat Model](/docs/admin/security/) for what these pins protect.

```bash
leapmux daemon worker cross-worker-pins list [flags]
leapmux daemon worker cross-worker-pins get <worker-id> [flags]
leapmux daemon worker cross-worker-pins remove <worker-id> [flags]
```

| Command | Action |
|---------|--------|
| `list` | Print every pin: a table of the Worker ids and the times, or with `--json`, `{"data": {"pins": [...]}}` with the keys |
| `get <worker-id>` | Print the pin of one sibling Worker, with its keys |
| `remove <worker-id>` | Remove the pin of one sibling Worker; with `--json`, prints `{"data": {"removed": "<worker-id>"}}` |

`<worker-id>` is the full id of the sibling Worker, as `list` prints it. `get` and `remove` fail with `not_found` (exit status `4`) for a Worker that has no pin, so a mistyped id never reads as a removed pin.

The commands take `--data-dir` and `--config`, as every other command of the Worker does, so they read the pin store of the same data directory: default `~/.config/leapmux/worker`, overridable with `LEAPMUX_WORKER_DATA_DIR` (or a `data_dir` entry in `worker.yaml`).

## dev

Run a Hub and a Worker in one process with **real** password authentication — the same program as `solo` but with login enabled, binding all interfaces, and the first admin bootstrapped through the `/setup` flow. See [Running LeapMux](/docs/admin/running-leapmux/#dev-mode).

```bash
leapmux daemon dev start [flags]
```

Dev mode uses the **same flag set as solo**.

The other differences from solo: dev enables `signup_enabled=true` by default, and the bundled Worker's auto-registration is deferred until the first admin completes `/setup`. The runtime knobs (`session_duration_seconds`, `limits`, `timeouts`, …) are `leapmux admin settings` keys. The default `--listen` is `:4327` (all interfaces), and the config/data location is `~/.config/leapmux/dev/`.

## version

Print the CLI build version, plus the Hub version (and the host Worker version inside a tab) when connected. When no Hub is selected, `leapmux version` prints only the CLI version string; when a selected Hub is unreachable, it prints the CLI version along with the Hub connection error. The version line joins fields with ` · `:

```bash
$ leapmux --version
0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST
```

Fields are conditional: the version value is always present (falls back to `dev`), the commit hash and build time appear when set, and the branch is shown only when it is not `main`. Top-level `--version` and `-v` print the binary version offline without dialing a Hub.

## recover (command-group outline)

`leapmux recover` is the **offline break-glass** tree: it manages the Hub's database and encryption key file **directly, with the hub stopped** — no network call, no login. It is deliberately tiny. For full per-command flags and behavior, see [Recovery](/docs/admin/recover/).

```bash
leapmux recover <command> [arguments] [flags]
```

| Command / Group | Subcommands |
|-----------------|-------------|
| `create-admin <username>` | Bootstrap the first administrator (refuses once any admin exists) |
| `reset-password <user>` | Reset a password (`<user>` is a username, `username:<name>`, or `id:<id>`; prompts for the new password) |
| `encryption-key` | `rotate`, `remove <version>`, `reencrypt`, `rotate-pepper` |
| `db` | `path`, `migrate`, `version` |

Every recover command takes `--data-dir` to select the instance, and `--config` to read the storage settings of a Hub on PostgreSQL or MySQL. Without `--config`, a command reads no configuration file and opens the SQLite database in the data directory. A `--config` path that does not exist fails.

Commands that take `--password` prompt interactively when you omit the flag. Every other administration task is online — see [Admin CLI](/docs/admin/admin-cli/), and [Recovery](/docs/admin/recover/) for the full per-command reference.

## Control commands (outline)

Top-level `leapmux <group> <command>` drives a **running** Hub over RPC — it does not touch the database. It is used interactively, by external scripts (which authorize with `leapmux auth login`, or auto-discover a local solo/dev Hub), and by agents and terminals that LeapMux starts (which inherit `LEAPMUX_CONTROL_*` env vars). It prints text by default and supports `--format text|json|ndjson|tsv`, `--json`, `-q`, `--fields`, `--no-header`, `-w`, and `--color`; its exit status states the class of a failure. [Control CLI](/docs/using/control-cli/) explains targets, output and errors; the [Control Command Reference](/docs/reference/control-commands/) lists every command with its arguments, flags and examples.

```bash
leapmux <group> <command> [target] [flags]
leapmux auth login --hub https://hub.example.com   # authorize first
```

| Group | Commands |
|-------|----------|
| *(top level)* | `tree` (`overview`, `ps`), `events` (`ev`), `notify`, `whoami`, `doctor`, `version`, `guide`, `rpc` |
| `auth` | `login` (add `--scope` to ask for particular permissions), `logout`, `list`, `use`, `status`, `credentials`, `change-password` — `list` reads this machine's credential files, `credentials` asks the Hub what the whole account holds |
| `context` (`ctx`) | `show`, `use`, `clear` — persistent default hub, worker, workspace, and provider outside a tab |
| `settings` (`pref`, `prefs`) | `list`, `get`, `set`, `reset` — account preferences on the Hub |
| `workspace` (`ws`) | `list`, `get`, `create`, `rename`, `archive`, `unarchive`, `move`, `export`, `apply`, `delete` |
| `section` | `list`, `create`, `rename`, `move`, `delete` |
| `tab` | `list`, `get`, `close`, `rename`, `move`, `reveal`, `open-image`; subgroup `status`: `list`, `set`, `clear` |
| `agent` (`ag`) | `list`, `get`, `open`, `send`, `wait`, `interrupt`, `fork`, `clear`, `compact`, `set`, `messages`, `message`, `span`, `marks`, `export`, `search`, `sessions`, `providers`, `requests`, `approve`, `deny`, `answer`, `todos`, `tasks`, `usage`, `tree`, `open-tab`, `retry-turn`, `approve-review`, `recoveries`, `recover`, `request-status`, `close`, `rename`, `reveal`; subgroups `goal`: `get`, `set`, `pause`, `resume`, `clear`, `startup`: `approve`, `cancel`, and `queue` (`q`): `list`, `get`, `edit`, `move`, `remove`, `pause`, `resume`, `clear`, `steer`, `preempt`, `advance`, `retry` |
| `terminal` (`term`) | `list`, `get`, `open`, `run`, `send`, `read`, `wait`, `attach`, `resize`, `restart`, `ps`, `shells`, `close`, `rename`, `reveal`; subgroup `quake`: `open`, `close`, `toggle`, `get`, `read`, `run`, `send` |
| `notification` (`notif`) | `send`, `list`, `clear` (with top-level `notify <title>` shortcut for `notification send`) |
| `file` | `list`, `read`, `stat`, `roots`, `search`, `glob`, `open`, `set` |
| `git` | `status`, `diff`, `log`, `commit`, `read`, `checkout`, `push`; subgroup `branch`: `list`, `create`, `delete`, `inspect` |
| `worktree` (`wt`) | `list`, `get`, `create`, `remove`, `inspect` |
| `tile` | `split`, `close`, `resize`, `swap`, `make-grid`, `remove-grid`, `set-ratios`, `set-grid-ratios` |
| `window` (`win`) | `list`, `create`, `move`, `resize`, `set-opacity`, `close` |
| `layout` | `get`, `set`, `watch` |
| `worker` (`wk`) | `list`, `get`, `info`, `ping`, `deregister`; subgroups `key`: `create`, `extend`, `revoke`, `email`, and `pins`: `list`, `get`, `remove` |
| `tunnel` (`tun`) | `forward` (`fwd`), `connect` (`nc`) |
| `skill` (`skills`) | `list` (`ls`), `show`, `status`, `install` (`add`), `uninstall` (`rm`, `remove`) — manage curated `SKILL.md` playbooks locally or on a Worker |
| `admin` | subgroups `settings`, `user`, `session`, `worker` (with `reg-key`), `app`, `idp`, `captcha`, `rate-limit`, `api-token`, `delegation-token`, `network` — the online hub administration surface (requires an admin login; never runs inside a LeapMux tab), covered in [Admin CLI](/docs/admin/admin-cli/). `app` registers the apps a consent screen authorizes; `idp` configures the providers users sign in *with*. |

Each tab type opens through its own group (`agent open`, `terminal open`, `file open`, or `tab open-image`). The `tab` group lists, closes, renames, moves, reveals, and sets status on tabs of every type.

## commands and completion

### `commands [prefix...]`

Print the command tree (text summary or machine-readable JSON schema with `--json`).

```bash
leapmux commands
leapmux commands agent --depth 1 --compact --json
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--depth <n>` | `0` (unlimited) | Maximum command depth relative to `[prefix...]` |
| `--compact` | off | Omit flag tables, argument help, and examples in JSON output |

### `completion <shell>`

Generate dynamic shell completion scripts for `leapmux` and `lmx`. Supported shells: `bash`, `zsh`, `fish`, `powershell`, `elvish`, `nushell`.

```bash
leapmux completion zsh > "${fpath[1]}/_leapmux"
leapmux completion bash > /etc/bash_completion.d/leapmux
```

## Environment variables

LeapMux reads configuration and credentials from these environment variables.

### Daemon configuration (hub / solo / dev and worker)

Hub-family modes (`hub`, `solo`, `dev`) read variables prefixed `LEAPMUX_HUB_`; the Worker reads `LEAPMUX_WORKER_`. The variable name after the prefix is lowercased to form the config key — for example `LEAPMUX_HUB_LISTEN` sets `listen`, `LEAPMUX_WORKER_NAME` sets `name`. A service does not inherit these variables from the shell that starts it; see [Environment variables](/docs/admin/starting-and-stopping/#environment-variables).

| Variable | Sets | Example |
|----------|------|---------|
| `LEAPMUX_HUB_LISTEN` | hub `listen`, comma-delimited | `:4327,unix:/run/leapmux/hub.sock` |
| `LEAPMUX_HUB_DATA_DIR` | hub `data_dir` | `/var/lib/leapmux/hub` |
| `LEAPMUX_HUB_LOG_LEVEL` | hub `log_level` | `info` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | hub `encryption_key_path` (no CLI flag) | `/etc/leapmux/encryption.key` |

The Worker reads:

| Variable | Sets | Example |
|----------|------|---------|
| `LEAPMUX_WORKER_NAME` | worker `name` | `build-box-1` |
| `LEAPMUX_WORKER_DATA_DIR` | worker `data_dir` | `/var/lib/leapmux/worker` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | worker `encryption_mode` | `post-quantum` |
| `LEAPMUX_WORKER_LOG_LEVEL` | worker `log_level` | `info` |
| `LEAPMUX_WORKER_AGENT_STARTUP_CONCURRENCY` | worker `agent_startup_concurrency` | `4` |

The prefix strip lowercases the remainder but does **not** translate `_` into `.`, so nested storage keys such as `storage.type` and `storage.postgres.dsn` cannot be set cleanly via env vars — use the YAML config file or the dedicated `--storage-*` flags instead. See [Configuration](/docs/admin/configuration/) for the full list and precedence rules (defaults < config file < env vars < explicitly-set CLI flags).

### Remote CLI (`leapmux`)

| Variable | Used by | Meaning |
|----------|---------|---------|
| `LEAPMUX_HUB` | control CLI | Hub URL when neither `--hub` nor the socket of a LeapMux tab selects one |
| `LEAPMUX_CONTROL_CONFIG_DIR` | control CLI | Override the credential, context, and pin directory (default `$XDG_CONFIG_HOME/leapmux/control`, `%APPDATA%\leapmux\control` on Windows, or `~/.config/leapmux/control`) |
| `XDG_CONFIG_HOME` | control CLI | Base config directory on POSIX when `LEAPMUX_CONTROL_CONFIG_DIR` is unset |
| `LEAPMUX_CONTROL_NO_PROMPT` | control CLI | Disable interactive step-up elevation prompts |
| `LEAPMUX_FOCUS_NEW` | control CLI | Override default focus behavior for `agent open`, `terminal open`, and `file open` (`0` = background, `1` = focus) |
| `NO_COLOR`, `CLICOLOR_FORCE` | control CLI | Disable or force ANSI color escapes under `--color auto` (`NO_COLOR` also disables TTY wait spinners) |
| `LEAPMUX_CONTROL_CLI` | spawned agents/terminals | Full path to the Worker's matching `leapmux` binary for automatic version hand-off |
| `LEAPMUX_CONTROL_SOCK` | spawned agents | Local IPC socket URL (selects the Worker's socket; `leapmux admin` refuses to run where it is set) |
| `LEAPMUX_CONTROL_TOKEN` | spawned agents | Per-process bearer token for the local IPC socket |
| `LEAPMUX_CONTROL_USER_ID` | spawned agents | Authenticated user ID (informational; no flag defaults from it) |
| `LEAPMUX_CONTROL_WORKER_ID` | spawned agents | Host worker ID (`self` for a worker, and the default of `--worker`) |
| `LEAPMUX_CONTROL_TAB_ID` | spawned agents | Spawning tab's ID (`self` for a tab, an agent or a terminal) |
| `LEAPMUX_CONTROL_TAB_TYPE` | spawned agents | `agent` or `terminal` |
| `LEAPMUX_CONTROL_TERMINAL_ID` | spawned terminals | The terminal you are inside (`self` on `terminal ...` commands). In a Quake panel it is the only id set — a panel gets no `_TAB_ID` / `_TAB_TYPE`. |
| `LEAPMUX_CONTROL_WORKING_DIR` | spawned agents | Working directory at spawn (the default of `--dir`) |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | spawned agents | Agent provider (agents only; the default of `--provider`) |

The `LEAPMUX_CONTROL_*` variables other than `_CONFIG_DIR` are injected automatically by the Worker into the agents and terminals it spawns; you do not set them by hand. See [Control CLI](/docs/using/control-cli/#inside-a-leapmux-tab) for how they select `self` and the defaults of the placement flags.

## Config and data locations

Each mode reads an optional YAML config file named after the mode. Each mode stores data under its own directory. LeapMux skips a missing config file without a message.

| Mode | Config file | Default data dir |
|------|-------------|------------------|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` |
| `control` | `<ConfigDir>/<scheme>_<host>[_<port>][_<path-hash>].json` (or flattened socket name for `unix:`/`npipe:`, credentials, mode 0600), `default-hub`, `context.json` (persistent defaults), `<hub-key>/pins.json` (Worker key pins) | — |

In `solo` and `dev`, the data directory is split into `<data-dir>/hub` and `<data-dir>/worker` subdirectories. See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/) for the full layout and resolution rules.

### Files that LeapMux manages in a data directory

| Path | Mode | Holds |
| --- | --- | --- |
| `<data-dir>/.lifecycle/` | every mode | The lock files, the saved service settings, the token of the local control endpoint, and the logs of the last ten starts. It is private to your account. Do not edit it. |
| `<data-dir>/state.json` | `worker` | The Hub URL, the Worker ID, the auth token, and the private keys. A secret. |
| `<data-dir>/pending-registration.json` | `worker` | An unfinished registration, with the registration key and the private keys. A secret, deleted when the registration completes. |
| `<data-dir>/worker.db` | `worker` | Agent and terminal state |

[Files in the Worker's data directory](/docs/admin/managing-workers/#files-in-the-workers-data-directory) says which Worker files to back up. `status --json` is the interface for scripts; the files above have no stable format.
