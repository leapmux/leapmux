---
title: "CLI Reference"
description: "Quick-lookup reference for the leapmux (lmx) CLI: command trees, daemon modes and flags, Hub resolution order, offline recovery, shell completions, and environment variables."
type: docs
weight: 1
---

Quick-lookup reference for `leapmux` (short alias `lmx`): command trees, daemon lifecycle (`up`, `down`, `daemon <solo|hub|worker|dev>`), Hub resolution order, `version`, `recover`, `commands`, `completion`, environment variables, and file locations.

For task guides, see [Running LeapMux](/docs/admin/running-leapmux/), [Starting and Stopping](/docs/admin/starting-and-stopping/), [Configuration Reference](/docs/admin/configuration-reference/), [Recovery](/docs/admin/recover/), and [CLI Guide](/docs/using/cli-guide/).

## Command trees

`leapmux` ships as one binary with four command trees:

| Tree | Purpose | Runs on | Auth | Reference |
|---|---|---|---|---|
| `up`, `down`, `daemon <solo\|hub\|worker\|dev>` | Manage local OS services or foreground daemons | Host | None (`worker register` takes a key) | [Starting and Stopping](/docs/admin/starting-and-stopping/) |
| Control (`agent`, `terminal`, `workspace`, `file`, `git`, …) | Drive a Hub and Workers over RPC | Any machine, scripts, or tabs | `auth login`, solo IPC, or tab token | [Control Command Reference](/docs/reference/commands/) |
| `admin` | Online Hub admin (users, sessions, settings, apps, Workers) | External shell (refuses in tab) | Admin login + step-up | [Admin CLI](/docs/admin/admin-cli/) |
| `recover` | Offline break-glass repair (admin, passwords, keys, DB) | Hub host (Hub **stopped**) | Local Hub data dir + config | [Recovery](/docs/admin/recover/) |

### Disambiguating Worker deregistration commands

| Command | Target | Where to run | Effect |
|---|---|---|---|
| `leapmux daemon worker deregister` | Local Worker in `--data-dir` | Worker host, **stopped** | Deletes Worker on Hub and removes `state.json` (keeps `worker.db`) |
| `leapmux worker deregister <worker>` | Your own Worker | Anywhere with user login | Deletes Worker record on Hub over RPC |
| `leapmux admin worker deregister <worker>` | Any user's Worker | External shell, **admin** login | Deletes Worker record on Hub over RPC |

## Hub resolution order

Control commands and `leapmux admin` resolve the Hub in this order:

| Priority | Source | Notes |
|---|---|---|
| 1 | `--hub <url>` | Always wins, even inside a tab. |
| 2 | `LEAPMUX_CONTROL_SOCK` | Worker-injected local inter-process communication (IPC) socket inside a tab (with `LEAPMUX_CONTROL_TOKEN`). |
| 3 | `LEAPMUX_HUB` | Environment variable for external shells and scripts. |
| 4 | `context.json` | Default set via `leapmux context use --hub <url>` or `leapmux auth use <url>`. |
| 5 | Stored credential default | Used when only one stored credential exists. |
| 6 | Local auto-discovery | Live local `solo` or `dev` IPC socket when no credential file exists. |

## Top-level command groups and global flags

Run `leapmux --help` or `leapmux help <command>` for live usage. Unambiguous command prefixes work interactively, though scripts should use full names or documented aliases (`ag`, `term`, `ws`, `ls`, `rm`, `mv`).

| Group | Commands | Reference |
|---|---|---|
| Daily Work | `tree`, `workspace`, `section`, `tab`, `agent`, `terminal`, `events`, `notify`, `notification` | [Workspaces & Tabs](/docs/reference/commands/workspaces-and-tabs/), [Agents](/docs/reference/commands/agents/), [Terminals](/docs/reference/commands/terminals/) |
| Files & Git | `file`, `git`, `worktree` | [Git & Files](/docs/reference/commands/git-and-files/) |
| Layout & Windows | `tile`, `window`, `layout` | [Layout & Windows](/docs/reference/commands/layout-and-windows/) |
| Fleet & Auth | `whoami`, `doctor`, `auth`, `context`, `settings`, `worker`, `tunnel` | [Auth & Session](/docs/reference/commands/auth-and-session/), [Workers & Events](/docs/reference/commands/workers-and-events/) |
| Admin & Recovery | `admin`, `rpc`, `recover` | [Admin](/docs/reference/commands/admin/), [Recovery](/docs/admin/recover/) |
| Meta | `guide`, `skill`, `version`, `commands`, `completion`, `help` | [CLI Guide](/docs/using/cli-guide/) |
| Local Daemon | `daemon`, `up` (`daemon solo start`), `down` (`daemon solo stop`) | [Mode commands](#mode-commands) |

**Global flags** (control commands; see [CLI Guide](/docs/using/cli-guide/) for output schemas):

| Flag | Default | Meaning |
|---|---|---|
| `--hub <url>` | auto | Target Hub URL or local IPC socket. |
| `--format <fmt>` | `text` | `text`, `json`, `ndjson`, or `tsv`. |
| `--json` | off | One `{"data": ...}` JSON line per result. |
| `-q`, `--quiet` | off | Bare result IDs, one per line. |
| `--fields <cols>` | all | Comma-separated columns or fields. |
| `--no-header` | off | Omit table/TSV header row. |
| `-w`, `--wide` | off | Do not truncate columns. |
| `--color <mode>` | `auto` | TTY ANSI color: `auto`, `always`, `never`. |
| `-v`, `--version` | — | Binary build version offline (top-level only). |

**Dispatch rules:** Default TCP port for `solo`/`hub`/`dev` is **4327**. `-h`/`--help` works everywhere (exit `0`). Daemon modes require a subcommand before flags (`leapmux daemon solo start --listen ...`). Long flags use `--`; single-letter flags use `-`.

{{< callout type="info" >}}
**Duration formats.** Daemon/config flags accept `ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w`, combined parts, or bare **seconds**. Control flags (`--timeout`, `--heartbeat`, `--since`) use Go units (`ms`, `s`, `m`, `h`).
{{< /callout >}}

## Mode commands

Every `leapmux daemon <mode>` requires a subcommand. The first eight work in every mode; `daemon worker` adds three Worker commands. Use `run` only in containers.

| Command | Action |
|---|---|
| `daemon <mode> start` | Creates OS service if needed, starts it, waits until ready, prints browser URL. |
| `daemon <mode> stop` | Stops service or foreground process and waits for shutdown. |
| `daemon <mode> restart` | Stops, then starts as a service. |
| `daemon <mode> status` | Reports state, readiness, browser URL, registration, and last failure. |
| `daemon <mode> logs` | Prints or follows (`-f`, `-n`) latest attempt log. |
| `daemon <mode> enable` | Registers OS startup (`login`/`boot`) without starting. |
| `daemon <mode> disable` | Removes OS service (requires stopped instance). |
| `daemon <mode> run` | Foreground process without OS service (containers). |
| `daemon worker register` | Registers Worker and saves credentials without starting. |
| `daemon worker deregister` | Deletes Worker on Hub and removes local credentials (stopped only). |
| `daemon worker cross-worker-pins` | Manages local trust-on-first-use (TOFU) pin store. |

`start`, `restart`, `enable`, and `run` accept and persist mode runtime flags. `stop`, `status`, `logs`, and `disable` take only `--data-dir`, `--config`, and the options below:

| Flag | Commands | Default | Meaning |
|---|---|---|---|
| `--data-dir`, `--config` | all | Mode defaults | Select the instance (pass identical values to all commands). |
| `--timeout` | `start`, `restart`, `stop`, `status`, `up`, `down`, `worker register`, `worker deregister` | `60s` (`start`/`up`), `4m` (`restart`), `3m` (`stop`/`down`), `30s` (`deregister`), `3s` (`status`), `5m` (`register`) | Max wait (duration or seconds); action may continue after timeout. |
| `--startup` | `start`, `restart`, `enable`, `up` | Saved or prompt | `login` or `boot`. |
| `--yes` | `start`, `restart`, `enable`, `up` | off | Skip consent prompt (does not choose `--startup`). |
| `--environment-file` | `start`, `restart`, `enable`, `up` | none | YAML env-var map for service (`--environment-file=` clears it). |
| `-f`, `--follow` | `logs` | off | Stream new log lines. |
| `-n`, `--lines` | `logs` | `100` | Trailing lines (`0` = full log). |
| `--json` | `status`, `logs`, `start`, `stop`, `restart`, `enable`, `disable`, `up`, `down`, `worker cross-worker-pins`, `recover *` | off | One `{"data": ...}` line (per-entry JSON lines for `logs --follow --json`). `worker register`/`deregister` accept `--json` syntactically but print plain text. |

**Exit codes:** `status` exits `0` running (including disconnected Worker or `dev` awaiting `/setup`), `8` stopped, `1` failed/unresponsive, `2` usage. Other mode commands and `recover` print `error: <code>: <message>` on stderr (`{"error": ...}` with `--json`) using the [CLI Guide](/docs/using/cli-guide/) exit codes (`Ctrl+C` exits `130`).

### `status --json`

Prints one `{"data": <report>}` line (`null` when unavailable):

| Field | Shape |
|---|---|
| `instance_id`, `mode`, `startup` | String or `null` |
| `execution` | `foreground`, `service`, or `null` |
| `configured` | Boolean: service matches saved config |
| `process` | `{state, pid, started_at}` (`starting`, `running`, `stopping`, `stopped`, `failed`, `unresponsive`) |
| `readiness` | `starting`, `ready`, `ready_for_setup`, `failed`, or `null` |
| `url` | Startup browser URL (`public_url` or `http://localhost:<port>`); `null` for Workers, IPC-only, or stopped |
| `registration` | `{state, worker_id, hub_url}` (`not_applicable`, `unregistered`, `pending`, `registered`, `invalid`) |
| `connection` | `{state, last_error}` (`not_applicable`, `pending_setup`, `connecting`, `connected`, `disconnected`) |
| `attempt` | `{id, started_at, ended_at, exit_code, error, log_path}` (nullable) |
| `diagnostics` | Array of `{kind, title, body, source}` |

## Daemon mode flags

See [Configuration Reference](/docs/admin/configuration-reference/) for complete key semantics, defaults, and database settings.

### `solo` and `dev`

`solo` (`leapmux up` / `leapmux daemon solo start`) runs a Hub + bundled Worker on loopback with credential-free local IPC and first-password TCP setup. `dev` (`leapmux daemon dev start`) binds `:4327`, defaults `signup_enabled=true`, stores data in `~/.config/leapmux/dev/`, and bootstraps the first admin via `/setup` (auto-registering its bundled Worker afterward).

| Flag | Default (`solo` / `dev`) | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:4327` (`solo`), `:4327` (`dev`) + IPC | Repeatable TCP or IPC (`unix:<path>`, `npipe:<name>`) bind; local IPC added if omitted. |
| `--data-dir` / `--config` | `~/.config/leapmux/solo` (`solo.yaml`) or `/dev` (`dev.yaml`) | Data dir (`hub/` and `worker/` subdirs) and config file. |
| `--dev-frontend` / `--log-level` | empty / `info` | Dev frontend proxy URL; log level (`debug`, `info`, `warn`, `error`). |
| `--storage-sqlite-max-conns` / `-cache-size` / `-mmap-size` | `4` / `0` / `0` | SQLite pool, page cache (`+` pages / `-` KiB), mmap bytes. |
| `--encryption-mode` / `--use-login-shell` | `post-quantum` / `true` | Bundled Worker E2EE (`post-quantum`/`classic`) and login-shell wrap. |
| `--agent-startup-concurrency` | `0` (`min(4, CPUs)`) | Concurrent agent restarts on bundled Worker. |

Binding `solo` to a non-loopback address logs a warning because the first TCP caller can claim an unset `solo` password. `public_url` also applies in `solo` for startup URLs and **Register worker** commands.

### `hub`

Standalone Hub (`leapmux daemon hub start [flags]`). Policies live in the database via `leapmux admin settings`. `encryption_key_path` has no CLI flag — use YAML or `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` (default `<data-dir>/encryption.key`).

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `:4327` + IPC | Repeatable TCP or IPC listen address. |
| `--data-dir` / `--config` | `~/.config/leapmux/hub` (`hub.yaml`) | Data dir and YAML config path. |
| `--dev-frontend` / `--log-level` | empty / `info` | Frontend proxy URL and log level. |
| `--storage-type` | `sqlite` | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb`. |
| `--storage-sqlite-path` / `-max-conns` / `-cache-size` / `-mmap-size` | `<data-dir>/hub.db` / `4` / `0` / `0` | SQLite path and tuning. |
| `--storage-postgres-dsn` / `--storage-mysql-dsn` | empty | External DB connection URL (required). |

Shared external pool defaults (`--storage-<name>-*`):

| Family | `max-conns` | `min-conns` | `max-idle-conns` | `conn-max-lifetime` | Idle-time flag | `health-check-period` |
|---|---|---|---|---|---|---|
| Postgres (`postgres`, `cockroachdb`, `yugabytedb`) | `25` | `5` | — | `1h` | `max-conn-idle-time` (`5m`) | `30s` |
| MySQL (`mysql`, `tidb`) | `25` | — | `5` | `1h` | `conn-max-idle-time` (`5m`) | — |

### `worker`

Standalone outbound-only Worker (`daemon worker register` once, then `start`/`restart`/`enable`/`run`). Runtime commands use the saved Hub URL and reject `--hub` and `--registration-key`.

| Flag | Default | Meaning |
|---|---|---|
| `--name` | hostname | Worker display name. |
| `--data-dir` / `--config` | `~/.config/leapmux/worker` (`worker.yaml`) | Data dir (`state.json`, `worker.db`) and config file. |
| `--encryption-mode` / `--use-login-shell` / `--log-level` | `post-quantum` / `true` / `info` | E2EE mode, login-shell wrap, log level. |
| `--max-message-size` | `0` (`16 MiB`) | Max payload size in bytes. |
| `--agent-startup-timeout` / `--api-timeout` | `5m` / `10s` | Agent startup and JSON-RPC timeouts. |
| `--agent-startup-concurrency` | `0` (`min(4, CPUs)`) | Concurrent agent restorations on restart. |
| `--db-max-conns` / `--db-cache-size` / `--db-mmap-size` | `4` / `0` / `0` | Worker SQLite pool, page cache, mmap bytes. |

#### `worker register`, `worker deregister`, and `worker cross-worker-pins`

- **`daemon worker register --hub <url> --registration-key <key> [--timeout 5m] [--data-dir] [--config]`** — registers and saves credentials without starting (`--hub` accepts `http[s]://`, `unix:`, `npipe:`; flags only). Re-running with identical values resumes an interrupted registration; existing credentials are refused.
- **`daemon worker deregister [--timeout 30s] [--data-dir] [--config]`** — deletes Worker on Hub and removes `state.json` (keeps `worker.db`). Stop Worker first; credentials remain if deletion cannot be confirmed.
- **`daemon worker cross-worker-pins <list|get <id>|remove <id>> [--data-dir] [--config]`** — local-only sibling-Worker TOFU pins (no running Worker). `get`/`remove` exit `4` (`not_found`) when absent; `--json` emits `{"data": {"pins": [...]}}` or `{"data": {"removed": "<id>"}}`.

## `version`, `recover`, `commands`, and `completion`

- **`version`:** Top-level `-v`/`--version` prints build info offline (`0.0.1-dev · <commit> · <branch> · <timestamp>`). `leapmux version` also prints connected Hub and host Worker versions.
- **`recover <command> [args] [--data-dir] [--config]`:** Offline Hub repair while **stopped** (opens `<data-dir>/hub.db` directly; pass `--config` for Postgres/MySQL). See [Recovery](/docs/admin/recover/):

| Command / Group | Subcommands |
|---|---|
| `create-admin <username>` | Bootstrap first admin (refuses if any admin exists) |
| `reset-password <user>` | Reset password (`<username>`, `username:<name>`, `id:<id>`) |
| `encryption-key` | `rotate`, `remove <version>`, `reencrypt`, `rotate-pepper` |
| `db` | `path`, `migrate`, `version` |

- **`commands [prefix...] [--depth <n>] [--compact] [--json]`:** Print command tree or JSON schema (`--depth 0` = unlimited; `--compact` omits flags, arg help, and examples in JSON).
- **`completion <shell>`:** Generate completion scripts for `leapmux`/`lmx` (`bash`, `zsh`, `fish`, `powershell`, `elvish`, `nushell`), e.g. `leapmux completion zsh > "${fpath[1]}/_leapmux"`.

## Environment variables

### Daemon configuration (`LEAPMUX_HUB_*` / `LEAPMUX_WORKER_*`)

Hub modes (`hub`, `solo`, `dev`) read `LEAPMUX_HUB_*`; standalone Workers read `LEAPMUX_WORKER_*` (lowercase suffix = top-level YAML key). Precedence: `defaults < config < env < flags`. Nested `storage.*` keys require YAML or `--storage-*` flags. OS services ignore shell env vars — use `--environment-file`.

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

### Control CLI (`leapmux`)

**User-set variables:**

| Variable | Meaning |
|---|---|
| `LEAPMUX_HUB` | Hub URL when neither `--hub` nor `LEAPMUX_CONTROL_SOCK` is set. |
| `LEAPMUX_CONTROL_CONFIG_DIR` | Override credential/context/pin dir (default `$XDG_CONFIG_HOME/leapmux/control`, `%APPDATA%\leapmux\control`, or `~/.config/leapmux/control`). |
| `XDG_CONFIG_HOME` | POSIX base config dir when `LEAPMUX_CONTROL_CONFIG_DIR` is unset. |
| `LEAPMUX_CONTROL_NO_PROMPT` | Disable interactive step-up elevation prompts. |
| `LEAPMUX_NO_CONTROL_HANDOFF` | Disable automatic hand-off to `$LEAPMUX_CONTROL_CLI` inside a managed tab when non-empty (for example, `1`). |
| `LEAPMUX_FOCUS_NEW` | Default focus for `agent`/`terminal`/`file open` (`0` = background, `1` = focus). |
| `NO_COLOR`, `CLICOLOR_FORCE` | Disable or force ANSI colors under `--color auto` (`NO_COLOR` also disables TTY spinners). |

**Worker-injected tab variables** (set automatically in spawned tabs; do not set manually):

| Variable | Meaning |
|---|---|
| `LEAPMUX_CONTROL_CLI` | Path to Worker's matching `leapmux` binary for version hand-off. |
| `LEAPMUX_CONTROL_SOCK` | Local IPC socket URL (`leapmux admin` refuses when set). |
| `LEAPMUX_CONTROL_TOKEN` | Per-process delegation token for the local IPC socket. |
| `LEAPMUX_CONTROL_USER_ID` | Authenticated user ID (informational). |
| `LEAPMUX_CONTROL_WORKER_ID` | Host Worker ID (`self` for Workers; default `--worker`). |
| `LEAPMUX_CONTROL_TAB_ID` | Spawning tab ID (`self` for tab/agent/terminal targets). |
| `LEAPMUX_CONTROL_TAB_TYPE` | `agent` or `terminal`. |
| `LEAPMUX_CONTROL_TERMINAL_ID` | Current terminal ID (`self` for `terminal`; set alone in Quake panels). |
| `LEAPMUX_CONTROL_WORKING_DIR` | Working directory at spawn (default `--dir`). |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | Agent provider in agent tabs (default `--provider`). |

## Config and data locations

Missing YAML config files are skipped silently.

| Mode | Config file | Default data directory |
|---|---|---|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` (`hub/`, `worker/`) |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` (`hub/`, `worker/`) |
| `control` | `<ConfigDir>/<hub-key>.json` (`0600`), `context.json`, `<hub-key>/pins.json` | — |

**Managed files inside `<data-dir>`** (internal schema; use `status --json` in scripts; see [Managing Workers](/docs/admin/managing-workers/) for backups):

| Path | Mode | Holds |
|---|---|---|
| `<data-dir>/.lifecycle/` | every mode | Locks, saved service settings, local control token, last ten start logs (do not edit). |
| `<data-dir>/state.json` | `worker` | Hub URL, Worker ID, auth token, private keys (secret). |
| `<data-dir>/pending-registration.json` | `worker` | Unfinished registration state and private keys (secret; deleted on completion). |
| `<data-dir>/worker.db` | `worker` | Agent transcripts, input queues, terminal state. |
