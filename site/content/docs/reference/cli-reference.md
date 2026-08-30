---
title: "CLI Reference"
description: "A quick-lookup cheat-sheet for the leapmux command line: every daemon mode and its flags, the version command, and the environment variables LeapMux reads."
type: docs
weight: 1
---

This is the quick-lookup cheat-sheet for the `leapmux` command line. It covers the top-level command list, a synopsis and flag table for each daemon mode (`solo`, `hub`, `worker`, `dev`), the `version` command, the environment variables LeapMux reads, and outlines of the three large command groups — `recover` (offline break-glass), `control` (user-facing scripting), and `control admin` (hub administration) — which have their own dedicated chapters.

For task-oriented walkthroughs rather than reference tables, see [Running LeapMux](/docs/admin/running-leapmux/) (run modes, ports, data dirs, Docker), [Configuration](/docs/admin/configuration/) (full config-key reference and storage backends), [Recovery](/docs/admin/recover/), and [Control CLI](/docs/using/control-cli/).

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
| `solo` | Hub + Worker in one process, loopback only, no login | [Running LeapMux](/docs/admin/running-leapmux/) |
| `hub` | Hub service only; Workers connect separately | [Running LeapMux](/docs/admin/running-leapmux/) |
| `worker` | Worker only; connects out to a Hub | [Managing Workers](/docs/admin/managing-workers/) |
| `dev` | Hub + Worker in one process with real auth (development) | [Running LeapMux](/docs/admin/running-leapmux/) |
| `recover` | Offline break-glass: first-admin bootstrap, password reset, keys, db | [Recovery](/docs/admin/recover/) |
| `control` | Drive a running Hub over RPC (scripts / spawned agents) | [Control CLI](/docs/using/control-cli/) |
| `version` | Print the build version and exit | [below](#version) |

Notes on dispatch:

- The default TCP port for every mode is **4327**.
- `-h`, `-help`, `--help`, and `help` are all recognized as help tokens (help prints to stdout, exit 0).
- `-version`, `--version`, and the `version` command all print the same version string.
- Daemon flags must follow the command keyword — `leapmux solo -listen ...`, not `leapmux -listen ... solo`. LeapMux rejects an unknown leading `-flag` because it is not a top-level flag.
- Help tokens are recognized at every level; LeapMux rejects an unexpected positional argument and points you at `--help`.

> **Tip:** Flags accept both single- and double-hyphen forms (`-listen` and `--listen` are equivalent). This chapter uses the single-hyphen form, matching the binary's help output.

> **Durations.** Every flag whose default is shown as a duration (`1h`, `5m`, …) takes a unit suffix — `ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w` — and combines parts. A bare number is a count of **seconds**. See [Duration values](/docs/admin/configuration/#duration-values).

## solo

Run a Hub and a Worker in one process on loopback, with no login (every request is auto-authenticated as the admin). See [Running LeapMux](/docs/admin/running-leapmux/#solo-mode) for details.

```bash
leapmux solo [flags]
# then open http://127.0.0.1:4327
```

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:4327` | TCP listen address |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory (split into `<data-dir>/hub` and `<data-dir>/worker`) |
| `-dev-frontend` | empty | Frontend dev-server URL for the local reverse proxy |
| `-storage-sqlite-max-conns` | `4` | SQLite max open connections |
| `-max-incomplete-chunked` | `4` | Max in-flight chunked sequences per channel (for the bundled Worker) |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` (for the bundled Worker) |
| `-use-login-shell` | `true` | Wrap the agent invocation in the user's login shell (for the bundled Worker) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |
| `-version` | — | Print version and exit |

> **Note:** Binding solo to a non-loopback address logs a warning because every request is auto-authenticated as the admin — use `hub` or `dev` for network-exposed deployments. The `public_url` setting applies in solo: it sets the URL in the startup banner, and the `--hub` address in the command that the **Register worker** dialog prints.

## hub

Run only the Hub: authentication, workspace management, Worker registration, and the encrypted relay. Binds all interfaces by default and requires a real login. See [Running LeapMux](/docs/admin/running-leapmux/#hub-mode) and [Configuration](/docs/admin/configuration/) for the complete key reference.

```bash
leapmux hub [flags]
```

This table lists the most common flags. The full set — including all PostgreSQL/MySQL/CockroachDB/YugabyteDB/TiDB pool-tuning flags — is in [Configuration](/docs/admin/configuration/).

**Server options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` | TCP listen address (e.g. `:4327` or `127.0.0.1:4327`) |
| `-local-listen` | platform default | Local IPC URL (`unix:<path>` or `npipe:<name>`); default `unix:<data-dir>/hub.sock` on Unix |
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

The Postgres family (`-storage-postgres-*`, `-storage-cockroachdb-*`, `-storage-yugabytedb-*`) defaults to `max-conns 25`, `min-conns 5`, `conn-max-lifetime-seconds 3600`, `max-conn-idle-time-seconds 300`, `health-check-period-seconds 30`. The MySQL family (`-storage-mysql-*`, `-storage-tidb-*`) defaults to `max-conns 25`, `max-idle-conns 5`, `conn-max-lifetime-seconds 3600`, `conn-max-idle-time-seconds 300`. CockroachDB/YugabyteDB use the Postgres driver; TiDB uses the MySQL driver. See [Configuration](/docs/admin/configuration/) for every storage flag and DSN format.

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |
| `-version` | — | Print version and exit |

> **Note:** One hub config key has **no** CLI flag and is set only via YAML or env var: `encryption_key_path` (`LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `<data-dir>/encryption.key`). Runtime settings such as `secure_cookies` are database settings managed with `leapmux control admin settings` — see [Admin CLI](/docs/admin/admin-cli/). See also [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).

## worker

Run a Worker that connects out to a Hub. Workers do not serve an inbound HTTP port; they register with the Hub using a key minted in the Hub UI. See [Managing Workers](/docs/admin/managing-workers/).

```bash
# First run — register with a key from the hub UI:
leapmux worker -hub https://hub.example.com -registration-key <key>

# Subsequent runs — credentials are saved, no key needed:
leapmux worker -hub https://hub.example.com
```

**Worker options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-hub` | `http://127.0.0.1:4327` | Hub URL (`http[s]://...`, `unix:<socket>`, or `npipe:<name>`) |
| `-registration-key` | empty | Registration key from the Hub UI (required on first run; never persisted) |
| `-name` | empty (= hostname) | Worker display name |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory (holds `state.json`, `worker.db`) |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-use-login-shell` | `true` | Wrap the agent invocation in the user's login shell |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Timeout and limit options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `-max-message-size` | `0` (= 16 MiB) | Max application payload size in bytes; reassembled ceiling adds 64 KiB headroom |
| `-agent-startup-timeout` | `5m` | Agent startup timeout |
| `-api-timeout` | `10s` | JSON-RPC request timeout |

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

> **Note:** An unregistered Worker with no saved credentials fails to start, and the error tells you to pass a registration key from the hub UI. Passing `-registration-key` again to an already-registered Worker also fails, which protects you from burning a one-time key.

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

When `--data-dir` is omitted, the data directory is resolved through the standard Worker config loader, so it matches what `leapmux worker` would use: default `~/.config/leapmux/worker`, overridable with `LEAPMUX_WORKER_DATA_DIR` (or a `data_dir` entry in `worker.yaml`).

> **Note:** The binary's own help text for this flag mentions `LEAPMUX_DATA_DIR`, but that variable is **not** read by the `leapmux` binary itself (only by the Docker entrypoint script), so it has no effect on this subcommand's data-dir resolution. Use `LEAPMUX_WORKER_DATA_DIR` (or `--data-dir`) here.

## dev

Run a Hub and a Worker in one process with **real** password authentication — the same program as `solo` but with login enabled, binding all interfaces, and the first admin bootstrapped through the `/setup` flow. See [Running LeapMux](/docs/admin/running-leapmux/#dev-mode).

```bash
leapmux dev [flags]
```

Dev mode uses the **same flag set as solo**.

The other differences from solo: dev seeds `signup_enabled=true`; the runtime knobs (`session_duration_seconds`, `limits`, `timeouts`, …) are `leapmux control admin settings` keys; the default `-listen` is `:4327` (all interfaces); the config/data location is `~/.config/leapmux/dev/`; and the bundled Worker's auto-registration is deferred until the first admin completes `/setup`.

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

Recover commands accept `--data-dir` to locate the data directory. The commands that open the database also accept `--config`, which loads the Hub's storage settings; pass it whenever the Hub runs on Postgres or MySQL. Three leaves need no database connection and therefore accept `--data-dir` only: `db path`, `encryption-key rotate`, and `encryption-key rotate-pepper`. Commands that take `--password` prompt interactively when you omit the flag. Every other administration task is online — see [Admin CLI](/docs/admin/admin-cli/). See [Recovery](/docs/admin/recover/) and [Encryption & Data](/docs/admin/encryption-and-data/).

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
| `terminal` | `send`, `get`, `shells` |
| `events` | `watch` |

> **Note:** Agents are opened, closed, listed, and renamed through the `tab` group (`tab open --type agent`, `tab close`, …) — there is no `agent open`/`agent close`/`agent list`. The `agent` group is for agent-specific operations only.

## Environment variables

LeapMux reads configuration and credentials from these environment variables.

### Daemon configuration (hub / solo / dev and worker)

Hub-family modes (`hub`, `solo`, `dev`) read variables prefixed `LEAPMUX_HUB_`; the Worker reads `LEAPMUX_WORKER_`. The variable name after the prefix is lowercased to form the config key — for example `LEAPMUX_HUB_LISTEN` sets `listen`, `LEAPMUX_WORKER_HUB` sets `hub`.

| Variable | Sets | Example |
|----------|------|---------|
| `LEAPMUX_HUB_LISTEN` | hub `listen` | `:4327` |
| `LEAPMUX_HUB_LOCAL_LISTEN` | hub `local_listen` | `unix:/run/leapmux/hub.sock` |
| `LEAPMUX_HUB_DATA_DIR` | hub `data_dir` | `/var/lib/leapmux/hub` |
| `LEAPMUX_HUB_LOG_LEVEL` | hub `log_level` | `info` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | hub `encryption_key_path` (no CLI flag) | `/etc/leapmux/encryption.key` |

| `LEAPMUX_WORKER_HUB` | worker `hub` | `https://hub.example.com` |
| `LEAPMUX_WORKER_NAME` | worker `name` | `build-box-1` |
| `LEAPMUX_WORKER_DATA_DIR` | worker `data_dir` | `/var/lib/leapmux/worker` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | worker `encryption_mode` | `post-quantum` |
| `LEAPMUX_WORKER_LOG_LEVEL` | worker `log_level` | `info` |

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
| `LEAPMUX_CONTROL_TAB_TYPE` | spawned agents | `agent`, `terminal`, or `file` |
| `LEAPMUX_CONTROL_WORKING_DIR` | spawned agents | Working directory at spawn |
| `LEAPMUX_CONTROL_AGENT_PROVIDER` | spawned agents | Agent provider (agents only) |

The `LEAPMUX_CONTROL_*` variables (the `_SOCK` / `_TOKEN` / `_*_ID` / `_TAB_*` family) are injected automatically by the Worker into the agents and terminals it spawns; you do not set them by hand. See [Control CLI](/docs/using/control-cli/) for how they drive entity-ID resolution.

## Config and data locations

Each mode reads an optional YAML config named after the mode, and stores data, under its own directory. LeapMux skips a missing config file without a message.

| Mode | Config file | Default data dir |
|------|-------------|------------------|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` |
| `control` | `~/.config/leapmux/control/<hub-host>.json` (credentials, mode 0600) | — |

In `solo` and `dev`, the data directory is split into `<data-dir>/hub` and `<data-dir>/worker` subdirectories. See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/) for the full layout and resolution rules.
