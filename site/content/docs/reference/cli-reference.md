---
title: "CLI Reference"
description: "A quick-lookup cheat-sheet for the leapmux command line: every daemon mode and its flags, the version command, and the environment variables LeapMux reads."
type: docs
weight: 1
---

This is the quick-lookup cheat-sheet for the `leapmux` command line. It covers the top-level command list, the commands that every mode shares, a synopsis and flag table for each daemon mode (`solo`, `hub`, `worker`, `dev`), the `version` command, the environment variables LeapMux reads, and outlines of the three large command groups — `recover` (offline break-glass), `control` (user-facing scripting), and `control admin` (hub administration) — which have their own dedicated chapters.

For task-oriented walkthroughs rather than reference tables, see [Running LeapMux](/docs/admin/running-leapmux/) (run modes, ports, data dirs, Docker), [Running as a Service](/docs/admin/running-as-a-service/), [Configuration](/docs/admin/configuration/) (full config-key reference and storage backends), [Recovery](/docs/admin/recover/), and [Control CLI](/docs/using/control-cli/).

## Top-level usage

`leapmux` is a single binary with seven commands:

```text
Usage: leapmux <command> [flags]

Commands:
  solo      Run Hub + Worker locally for single-user use
  hub       Run the Hub service
  worker    Run a Worker connected to a Hub
  dev       Run Hub + Worker for development
  recover   Offline break-glass recovery (opens the database directly)
  control   Remotely control LeapMux from a script or another LeapMux agent
  version   Print version and exit

Common options:
  -h, --help     Print help and exit
  -version       Print version and exit
  --version      Print version and exit

Any command name can be shortened as far as it stays unambiguous.
```

| Command | What it does | Reference |
|---------|--------------|-----------|
| `solo` | Hub + Worker in one process; local IPC is credential-free and TCP starts with password setup | [Running LeapMux](/docs/admin/running-leapmux/) |
| `hub` | Hub service only; Workers connect separately | [Running LeapMux](/docs/admin/running-leapmux/) |
| `worker` | Worker only; connects out to a Hub | [Managing Workers](/docs/admin/managing-workers/) |
| `dev` | Hub + Worker in one process with real auth (development) | [Running LeapMux](/docs/admin/running-leapmux/) |
| `recover` | Offline break-glass: first-admin bootstrap, password reset, keys, db | [Recovery](/docs/admin/recover/) |
| `control` | Drive a running Hub over RPC (scripts / spawned agents) | [Control CLI](/docs/using/control-cli/) |
| `version` | Print the build version and exit | [below](#version) |

Notes on dispatch:

- The default TCP port for every mode is **4327**.
- `-h`, `-help`, and `--help` are recognized at every level (help prints to stdout, exit 0). The bare `help` token works only at the top level.
- `-version`, `--version`, and the `version` command all print the same version string. `--version` works at the top level and after a mode command (`leapmux hub run --version`).
- A mode needs a command, and flags follow the command: `leapmux solo run -listen ...`. `leapmux solo -listen ...` fails, and the error suggests `leapmux solo run`. LeapMux also rejects an unknown leading `-flag` such as `leapmux -listen ...`, because it is not a top-level flag.
- LeapMux rejects an unexpected positional argument and points you at `--help`.

{{< callout >}}
Flags accept both single- and double-hyphen forms (`-listen` and `--listen` are equivalent). The daemon-mode flag tables below use the single-hyphen form, matching the binary's help output; the `control` and `recover` sections keep the double-hyphen form used by their chapters.
{{< /callout >}}

> **Durations.** Every flag whose default is shown as a duration (`1h`, `5m`, …) takes a unit suffix — `ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w` — and combines parts. A bare number is a count of **seconds**. See [Duration values](/docs/admin/configuration/#duration-values).

## Mode commands

Every mode requires a command. The seven commands below work in every mode, and the `worker` mode adds three. [Running as a Service](/docs/admin/running-as-a-service/) explains the service that `start` and `enable` create.

| Command | What it does |
| --- | --- |
| `<mode> run` | Runs in the foreground. Never creates a service. |
| `<mode> start` | Creates the service when necessary, starts it, and waits until it is ready. |
| `<mode> stop` | Stops the instance, whether `run` or `start` started it, and waits until its shutdown completes. |
| `<mode> restart` | Stops the instance, then starts it as a service. |
| `<mode> status` | Reports the process state, readiness, registration, and the last failure. |
| `<mode> enable` | Creates the service for startup at login or boot, without starting it, and prints the `start` and `status` commands of the instance. |
| `<mode> disable` | Removes the service. Requires a stopped instance. |
| `worker register` | Registers the Worker with a Hub and saves its credentials. Never starts it. See [below](#worker-register). |
| `worker deregister` | Deletes the Worker on the Hub, then its credentials. Requires a stopped Worker. See [below](#worker-deregister). |
| `worker cross-worker-pins` | Manages the Worker's pin store. See [below](#worker-cross-worker-pins). |

`run`, `start`, `restart`, and `enable` also take the runtime flags of the mode, which its section below lists. A service saves the runtime flags that you give it. `stop`, `status`, and `disable` take only `--data-dir` and `--config` from the mode, plus their own options below.

**Options of the mode commands**

| Flag | Commands | Default | Meaning |
|------|----------|---------|---------|
| `--data-dir`, `--config` | all; `cross-worker-pins` takes only `--data-dir` | the defaults of the mode | Select the instance. Pass the same values to every command for one instance. |
| `--timeout` | `start`, `restart`, `stop`, `status`, `worker register`, `worker deregister` | `60s` for `start` and `restart`, `30s` for `stop` and `deregister`, `3s` for `status`, `5m` for `register` | How long to wait: a positive duration, or a bare number of seconds. The operation can continue after the wait ends. |
| `--startup` | `start`, `restart`, `enable` | the saved choice, else a prompt | `login` or `boot` |
| `--yes` | `start`, `restart`, `enable` | off | Accept the proposed configuration without the consent prompt. It does not choose `--startup`. |
| `--environment-file` | `start`, `restart`, `enable` | none | Path to a YAML map of environment variables for the service. `--environment-file=` removes the saved file. |
| `--json` | `status` | off | Print the report as one JSON object. |

`status` exits with `0` when the instance runs, `3` when it is stopped, and `1` for a failure, an instance that does not answer, or invalid use. A Worker without a Hub connection and a `dev` instance that waits for `/setup` both count as running.

### `status --json`

`status --json` prints one JSON object and nothing else. A value that is not available is `null`.

| Field | Shape |
| --- | --- |
| `instance_id`, `mode`, `startup` | String or `null` |
| `execution` | `foreground` or `service`, or `null` |
| `configured` | Boolean: the service matches the saved configuration |
| `process` | `state` (string), `pid` (integer or `null`), and `started_at` (timestamp or `null`) |
| `readiness` | `starting`, `ready`, `ready_for_setup`, `failed`, or `null` |
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
leapmux solo run [flags]
# then open http://127.0.0.1:4327
```

`start`, `restart`, and `enable` take the same flags.

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:4327` plus the platform's local IPC URL | One listen address, repeatable: a TCP address or a local IPC URL (`unix:<path>`, `npipe:<name>`). The list is the bind set; the platform's local IPC URL is added when the list names none. Additional addresses are a setting; see [Network access](/docs/admin/configuration/#network-access). |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory (split into `<data-dir>/hub` and `<data-dir>/worker`) |
| `-dev-frontend` | empty | Frontend dev-server URL for the local reverse proxy |
| `-storage-sqlite-max-conns` | `4` | SQLite max open connections |
| `-storage-sqlite-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `-storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel (for the bundled Worker) |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` (for the bundled Worker) |
| `-use-login-shell` | `true` | Wrap the agent invocation in the user's login shell (for the bundled Worker) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |
| `-version` | — | Print version and exit |

{{< callout type="info" >}}
Binding solo to a non-loopback address logs a warning. Before the account has a password, the first TCP caller can claim it through the restricted setup flow. Set the password first, or use `hub` or `dev` for a multi-user deployment.
{{< /callout >}}

{{< callout type="info" >}}
The `public_url` setting applies in solo: it sets the URL in the startup banner, and the `--hub` address in the command that the **Register worker** dialog prints.
{{< /callout >}}

## hub

Run only the Hub: authentication, workspace management, Worker registration, and the encrypted relay. Binds all interfaces by default and requires a real login. See [Running LeapMux](/docs/admin/running-leapmux/#hub-mode) and [Configuration](/docs/admin/configuration/) for the complete key reference.

```bash
leapmux hub run [flags]
```

`start`, `restart`, and `enable` take the same flags.

This table lists the most common flags. The full set — including all PostgreSQL/MySQL/CockroachDB/YugabyteDB/TiDB pool-tuning flags — is in [Configuration](/docs/admin/configuration/).

**Server options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | platform default | One listen address, repeatable. A TCP address (`:4327`, `127.0.0.1:4327`) or a local IPC URL (`unix:<path>`, `npipe:<name>`). The list is the bind set, and at least one local IPC address is always bound: the platform's local IPC URL is added when the list names none. |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `-dev-frontend` | empty | Frontend dev-server URL for the reverse proxy |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Behavioral settings.** Auth policy (sign-up, verification, sessions), SMTP, timeouts, and per-user limits are not flags: they are instance settings in the Hub's database, managed by `leapmux control admin settings` (see [Configuration](/docs/admin/configuration/) and the [`control admin` section](/docs/admin/admin-cli/)).

**Storage options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-storage-type` | empty (= `sqlite`) | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `-storage-sqlite-path` | empty (= `<data-dir>/hub.db`) | SQLite database file path |
| `-storage-sqlite-max-conns` | `4` | SQLite max open connections |
| `-storage-sqlite-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `-storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |
| `-storage-postgres-dsn` | empty | PostgreSQL connection string (required when `storage.type` is `postgres`) |
| `-storage-mysql-dsn` | empty | MySQL connection string (required when `storage.type` is `mysql`) |

The Postgres family (`-storage-postgres-*`, `-storage-cockroachdb-*`, `-storage-yugabytedb-*`) and the MySQL family (`-storage-mysql-*`, `-storage-tidb-*`) share pool defaults:

| Family | `max-conns` | `min-conns` | `max-idle-conns` | `conn-max-lifetime` | `conn-max-idle-time` | `health-check-period` |
|---|---|---|---|---|---|---|
| Postgres (`postgres`, `cockroachdb`, `yugabytedb`) | 25 | 5 | — | 3600 | 300 | 30 |
| MySQL (`mysql`, `tidb`) | 25 | — | 5 | 3600 | 300 | — |

CockroachDB/YugabyteDB use the Postgres driver; TiDB uses the MySQL driver. See [Configuration](/docs/admin/configuration/) for every storage flag and DSN format.

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |
| `-version` | — | Print version and exit |

{{< callout type="info" >}}
One hub config key has **no** CLI flag and is set only via YAML or env var: `encryption_key_path` (`LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `<data-dir>/encryption.key`). Runtime settings such as `secure_cookies` are database settings managed with `leapmux control admin settings` — see [Admin CLI](/docs/admin/admin-cli/). See also [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).
{{< /callout >}}

## worker

Run a Worker that connects out to a Hub. Workers do not serve an inbound HTTP port. Register a Worker once with `worker register` before you run or start it. See [Managing Workers](/docs/admin/managing-workers/).

```bash
# Once: register, with a key from the Register worker dialog of the Hub.
leapmux worker register --hub https://hub.example.com --registration-key <key>

# Then run it in the foreground, or start it as a service.
leapmux worker run [flags]
leapmux worker start [flags]
```

The Worker connects to the Hub URL that its registration saved, so the runtime commands take no `--hub` and no `--registration-key`. They refuse both flags.

**Worker options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-name` | empty (= hostname) | Worker display name |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory (holds `state.json`, `worker.db`) |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-use-login-shell` | `true` | Wrap each agent invocation in the user's login shell |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Timeout and limit options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `-max-message-size` | `0` (= 16 MiB) | Max application payload size in bytes; reassembled ceiling adds 64 KiB headroom |
| `-agent-startup-timeout` | `5m` | Agent startup timeout |
| `-api-timeout` | `10s` | JSON-RPC request timeout |
| `-agent-startup-concurrency` | `0` (= 4, or the CPU count when lower) | How many agents the Worker brings back at once after a restart |

**SQLite database options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-db-max-conns` | `4` | Max open database connections |
| `-db-cache-size` | `0` | Page cache: positive = pages, negative = KiB (e.g. `-65536` = 64 MiB) |
| `-db-mmap-size` | `0` | Memory-mapped I/O size in bytes (0 = disabled) |

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-config` | `~/.config/leapmux/worker/worker.yaml` | Config file path |
| `-version` | — | Print version and exit |

### worker register

Register the Worker with a Hub, save its credentials in the data directory, and exit. It never starts the Worker.

```bash
leapmux worker register --hub <url> --registration-key <key> [flags]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-hub` | required | URL of the Hub: `http[s]://...`, `unix:<socket>`, or `npipe:<name>`. The Worker connects to this URL from then on. |
| `-registration-key` | required | The key from the **Register worker** dialog of the Hub |
| `-timeout` | `5m` | How long to wait for the Hub, retries included |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `-config` | `~/.config/leapmux/worker/worker.yaml` | Config file path; it supplies `data_dir` |

`-hub` and `-registration-key` are flags only: no config key or environment variable sets them. When an earlier run stopped before it finished, the same command completes that registration without using another key. A data directory that already holds credentials makes the command fail; see [Register a Worker again](/docs/admin/managing-workers/#register-a-worker-again).

### worker deregister

Delete this Worker on its Hub, then remove its credentials from the data directory. Stop the Worker first.

```bash
leapmux worker deregister [flags]
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-timeout` | `30s` | How long to wait for the Hub |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `-config` | `~/.config/leapmux/worker/worker.yaml` | Config file path; it supplies `data_dir` |

The command keeps `worker.db`. When the Hub cannot confirm the deletion, it keeps the credentials too, so that you can run it again.

### worker cross-worker-pins

A local-only utility for inspecting the Worker's TOFU pin store. It runs entirely against local files — no Worker process starts. See [Security & Threat Model](/docs/admin/security/) for what these pins protect.

```bash
leapmux worker cross-worker-pins list|show|remove [--target-worker-id=<id>] [--data-dir=<dir>]
```

| Subcommand | Requires | Action |
|------------|----------|--------|
| `list` | — | Print all pins as JSON |
| `show` | `--target-worker-id` | Print one pin (errors `no pin recorded for target_worker_id=<id>` if absent) |
| `remove` | `--target-worker-id` | Remove the pin; prints `{"removed_target_worker_id": <id>}` |

When `--data-dir` is omitted, the data directory is resolved through the standard Worker config loader, so it matches what `leapmux worker run` would use: default `~/.config/leapmux/worker`, overridable with `LEAPMUX_WORKER_DATA_DIR` (or a `data_dir` entry in `worker.yaml`).

## dev

Run a Hub and a Worker in one process with **real** password authentication — the same program as `solo` but with login enabled, binding all interfaces, and the first admin bootstrapped through the `/setup` flow. See [Running LeapMux](/docs/admin/running-leapmux/#dev-mode).

```bash
leapmux dev run [flags]
```

Dev mode uses the **same flag set as solo**.

The other differences from solo: dev enables `signup_enabled=true` by default, and the bundled Worker's auto-registration is deferred until the first admin completes `/setup`. The runtime knobs (`session_duration_seconds`, `limits`, `timeouts`, …) are `leapmux control admin settings` keys. The default `-listen` is `:4327` (all interfaces), and the config/data location is `~/.config/leapmux/dev/`.

## version

Print the build version and exit. The output is a single line with fields joined by ` · `:

```bash
$ leapmux version
0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST
```

Fields are conditional: the version value is always present (falls back to `dev`), the commit hash and build time appear when set, and the branch is shown only when it is not `main`. The top-level `-version` / `--version` flags print the same string.

## recover (command-group outline)

`leapmux recover` is the **offline break-glass** tree: it manages the Hub's database and encryption key file **directly, with the hub stopped** — no network call, no login. It is deliberately tiny. For full per-command flags and behavior, see [Recovery](/docs/admin/recover/).

```bash
leapmux recover <group> <command> [flags]
```

| Group | Commands |
|-------|----------|
| `bootstrap` | `create-admin` (refuses once any admin exists) |
| `password` | `reset` (`--id` or `--username`; prompts for the new password) |
| `encryption-key` | `rotate`, `remove`, `reencrypt`, `rotate-pepper` |
| `db` | `path`, `migrate`, `version` |

Recover commands accept `--data-dir` to locate the data directory. The commands that open the database also accept `--config`, which loads the Hub's storage settings; pass it whenever the Hub runs on Postgres or MySQL. Three leaves need no database connection and take no `--config`: `db path`, `encryption-key rotate`, and `encryption-key rotate-pepper` (which also takes `--yes`).

Commands that take `--password` prompt interactively when you omit the flag. Every other administration task is online — see [Admin CLI](/docs/admin/admin-cli/), and [Recovery](/docs/admin/recover/) for the full per-command reference.

## control (command-group outline)

`leapmux control` drives a **running** Hub over RPC — it does not touch the database. It is used both by external scripts (which authorize with `leapmux control auth login`) and by agents/terminals that LeapMux spawns (which inherit `LEAPMUX_CONTROL_*` env vars). Every command emits a JSON envelope — `{"data": ...}` on success, `{"error": {"code", "message"}}` on failure (both on stdout) — with a non-zero exit on failure. For full per-command flags, entity-ID resolution, and output shapes, see [Control CLI](/docs/using/control-cli/).

```bash
leapmux control <group> <command> [flags]
leapmux control auth login --hub https://hub.example.com   # authorize first
```

| Group | Commands |
|-------|----------|
| *(top level)* | `whoami`, `version` |
| `auth` | `login` (add `--scope` to ask for particular permissions), `logout`, `list`, `status`, `credentials` — `list` reads this machine's credential files, `credentials` asks the Hub what the whole account holds |
| `admin` | subgroups `settings`, `user`, `session`, `worker` (with `reg-key`), `app`, `idp`, `captcha`, `rate-limit`, `api-token`, `delegation-token` — the online hub administration surface (requires an admin login; never available over the worker-IPC transport), covered in [Admin CLI](/docs/admin/admin-cli/). `app` registers the apps a consent screen authorizes; `idp` configures the providers users sign in *with*. |
| `workspace` | `list`, `get`, `create`, `rename`, `delete` |
| `tab` | `list`, `get`, `open`, `close`, `rename`, `move` |
| `worker` | `list`, `get`; subgroup `pins`: `list`, `show`, `remove` |
| `agent` | `send`, `interrupt`, `get`, `providers`, `messages`, `set`, `send-control-response` |
| `tile` | `list`, `split`, `close`, `make-grid`, `remove-grid`, `set-ratios`, `set-grid-ratios` |
| `layout` | `get`, `set` |
| `file` | `list`, `read`, `stat` |
| `git` | `status`, `branches`, `worktrees`, `read` |
| `terminal` | `send`, `get`, `shells`; subgroup `quake`: `open`, `close`, `toggle` |
| `events` | `watch` |

{{< callout type="info" >}}
Agents are opened, closed, listed, and renamed through the `tab` group (`tab open --type agent`, `tab close`, …) — there is no `agent open`/`agent close`/`agent list`. The `agent` group is for agent-specific operations only.
{{< /callout >}}

## Environment variables

LeapMux reads configuration and credentials from these environment variables.

### Daemon configuration (hub / solo / dev and worker)

Hub-family modes (`hub`, `solo`, `dev`) read variables prefixed `LEAPMUX_HUB_`; the Worker reads `LEAPMUX_WORKER_`. The variable name after the prefix is lowercased to form the config key — for example `LEAPMUX_HUB_LISTEN` sets `listen`, `LEAPMUX_WORKER_NAME` sets `name`. A service does not inherit these variables from the shell that starts it; see [Environment variables](/docs/admin/running-as-a-service/#environment-variables).

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

The prefix strip lowercases the remainder but does **not** translate `_` into `.`, so nested storage keys such as `storage.type` and `storage.postgres.dsn` cannot be set cleanly via env vars — use the YAML config file or the dedicated `-storage-*` flags instead. See [Configuration](/docs/admin/configuration/) for the full list and precedence rules (defaults < config file < env vars < explicitly-set CLI flags).

### Remote CLI (`leapmux control`)

| Variable | Used by | Meaning |
|----------|---------|---------|
| `LEAPMUX_HUB` | `control` (and `auth login --hub` fallback) | Hub URL when `--hub` is not passed |
| `LEAPMUX_CONTROL_CONFIG_DIR` | `control` | Override the credential/pin directory (default `~/.config/leapmux/control`) |
| `LEAPMUX_CONTROL_SOCK` | spawned agents | Local IPC socket URL (selects the worker-IPC transport; `control admin` refuses it) |
| `LEAPMUX_CONTROL_TOKEN` | spawned agents | Per-process bearer token for the local IPC socket |
| `LEAPMUX_CONTROL_USER_ID` | spawned agents | Authenticated user ID (informational; no flag defaults from it) |
| `LEAPMUX_CONTROL_WORKER_ID` | spawned agents | Host worker ID (default for `--worker-id`) |
| `LEAPMUX_CONTROL_TAB_ID` | spawned agents | Spawning tab's ID (default for `--tab-id`) |
| `LEAPMUX_CONTROL_TAB_TYPE` | spawned agents | `agent` or `terminal` |
| `LEAPMUX_CONTROL_TERMINAL_ID` | spawned terminals | The terminal you are inside (default for `--tab-id` on `terminal ...`). In a Quake panel it is the only id set — a panel gets no `_TAB_ID` / `_TAB_TYPE`. |
| `LEAPMUX_CONTROL_WORKING_DIR` | spawned agents | Working directory at spawn |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | spawned agents | Agent provider (agents only) |

The `LEAPMUX_CONTROL_*` variables other than `_CONFIG_DIR` are injected automatically by the Worker into the agents and terminals it spawns; you do not set them by hand. See [Control CLI](/docs/using/control-cli/) for how they drive entity-ID resolution.

## Config and data locations

Each mode reads an optional YAML config file named after the mode. Each mode stores data under its own directory. LeapMux skips a missing config file without a message.

| Mode | Config file | Default data dir |
|------|-------------|------------------|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` |
| `control` | `~/.config/leapmux/control/<hub-host>.json` (credentials, mode 0600) | — |

In `solo` and `dev`, the data directory is split into `<data-dir>/hub` and `<data-dir>/worker` subdirectories. See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/) for the full layout and resolution rules.

### Files that LeapMux manages in a data directory

| Path | Mode | Holds |
| --- | --- | --- |
| `<data-dir>/.lifecycle/` | every mode | The lock files, the saved service settings, the token of the local control endpoint, and the logs of the last ten starts. It is private to your account. Do not edit it. |
| `<data-dir>/state.json` | `worker` | The Hub URL, the Worker ID, the auth token, and the private keys. A secret. |
| `<data-dir>/pending-registration.json` | `worker` | An unfinished registration, with the registration key and the private keys. A secret, deleted when the registration completes. |
| `<data-dir>/worker.db` | `worker` | Agent and terminal state |

[Files in the Worker's data directory](/docs/admin/managing-workers/#files-in-the-workers-data-directory) says which Worker files to back up. `status --json` is the interface for scripts; the files above have no stable format.
