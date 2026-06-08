---
title: "CLI Reference"
type: docs
weight: 1
---

This is the quick-lookup cheat-sheet for the `leapmux` command line. It covers the top-level command list, a synopsis and flag table for each daemon mode (`solo`, `hub`, `worker`, `dev`), the `version` command, the environment variables LeapMux reads, and a pointer to the two large command groups — `admin` and `remote` — which have their own dedicated chapters.

For task-oriented walkthroughs rather than reference tables, see [Running LeapMux](/docs/operating/running-leapmux/) (run modes, ports, data dirs, Docker), [Configuration](/docs/operating/configuration/) (full config-key reference and storage backends), [Admin CLI](/docs/operating/admin-cli/), and [Remote Control CLI](/docs/operating/remote-control-cli/).

## Top-level usage

`leapmux` is a single binary with seven commands:

```text
Usage: leapmux <command> [flags]

Commands:
  solo      Run Hub + Worker locally for single-user use
  hub       Run the Hub service
  worker    Run a Worker connected to a Hub
  dev       Run Hub + Worker for development
  admin     Manage LeapMux resources
  remote    Drive LeapMux remotely (CLI / spawned agent)
  version   Print version and exit

Common options:
  -h, --help     Print help and exit
  -version       Print version and exit
  --version      Print version and exit
```

| Command | What it does | Reference |
|---------|--------------|-----------|
| `solo` | Hub + Worker in one process, loopback only, no login | [Running LeapMux](/docs/operating/running-leapmux/) |
| `hub` | Hub service only; Workers connect separately | [Running LeapMux](/docs/operating/running-leapmux/) |
| `worker` | Worker only; connects out to a Hub | [Managing Workers](/docs/operating/managing-workers/) |
| `dev` | Hub + Worker in one process with real auth (development) | [Running LeapMux](/docs/operating/running-leapmux/) |
| `admin` | Manage Hub data directly against the database | [Admin CLI](/docs/operating/admin-cli/) |
| `remote` | Drive a running Hub over RPC (scripts / spawned agents) | [Remote Control CLI](/docs/operating/remote-control-cli/) |
| `version` | Print the build version and exit | [below](#version) |

Notes on dispatch:

- The default TCP port for every mode is **4327**.
- `-h`, `-help`, `--help`, and `help` are all recognized as help tokens (help prints to stdout, exit 0).
- `-version`, `--version`, and the `version` command all print the same version string.
- Daemon flags must follow the command keyword — `leapmux solo -listen ...`, not `leapmux -listen ... solo`. An unknown leading `-flag` errors with `<flag> is not a top-level flag`.
- Help tokens are recognized at every level; unexpected positional args are rejected with `unexpected argument: "<arg>" (use --help for usage)`.

> **Tip:** Flags accept both single- and double-hyphen forms (`-listen` and `--listen` are equivalent). This chapter uses the single-hyphen form, matching the binary's help output.

## solo

Run a Hub and a Worker in one process on loopback, with no login (every request is auto-authenticated as the admin). See [Running LeapMux](/docs/operating/running-leapmux/#solo-mode) for details.

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
| `-max-message-size` | `0` (= 16 MiB) | Max reassembled channel message size in bytes |
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `-api-timeout-seconds` | `10` | General API timeout |
| `-agent-startup-timeout-seconds` | `300` | Agent startup timeout |
| `-worktree-create-timeout-seconds` | `60` | Worktree creation timeout |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` (for the bundled Worker) |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |
| `-version` | — | Print version and exit |

> **Note:** `-public-url` is **not** available in solo mode; `public_url` set by any means is rejected with `public_url is not supported in solo mode`. Binding solo to a non-loopback address logs a warning because every request is auto-authenticated as the admin — use `hub` or `dev` for network-exposed deployments.

## hub

Run only the Hub: authentication, workspace management, Worker registration, and the encrypted relay. Binds all interfaces by default and requires a real login. See [Running LeapMux](/docs/operating/running-leapmux/#hub-mode) and [Configuration](/docs/operating/configuration/) for the complete key reference.

```bash
leapmux hub [flags]
```

This table lists the most common flags. The full set — including all PostgreSQL/MySQL/CockroachDB/YugabyteDB/TiDB pool-tuning flags — is in [Configuration](/docs/operating/configuration/).

**Server options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` | TCP listen address (e.g. `:4327` or `127.0.0.1:4327`) |
| `-local-listen` | platform default | Local IPC URL (`unix:<path>` or `npipe:<name>`); default `unix:<data-dir>/hub.sock` on Unix |
| `-public-url` | empty | Public base URL behind a reverse proxy (e.g. `https://hub.example.com`) |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `-dev-frontend` | empty | Frontend dev-server URL for the reverse proxy |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |

**Auth options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-signup-enabled` | `false` | Enable user sign-up |
| `-email-verification-required` | `false` | Require email verification on sign-up (needs `-smtp-host`) |

**SMTP options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-smtp-host` | empty | SMTP server host |
| `-smtp-port` | `587` | SMTP server port |
| `-smtp-username` | empty | SMTP username |
| `-smtp-password` | empty | SMTP password |
| `-smtp-from-address` | empty | From address (required when `-smtp-host` is set) |
| `-smtp-tls-mode` | `starttls` | `starttls`, `implicit`, or `none` |

**Timeout and limit options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-max-message-size` | `0` (= 16 MiB) | Max reassembled channel message size in bytes |
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `-api-timeout-seconds` | `10` | General API timeout |
| `-agent-startup-timeout-seconds` | `300` | Agent startup timeout |
| `-worktree-create-timeout-seconds` | `60` | Worktree creation timeout |

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

The Postgres family (`-storage-postgres-*`, `-storage-cockroachdb-*`, `-storage-yugabytedb-*`) defaults to `max-conns 25`, `min-conns 5`, `conn-max-lifetime-seconds 3600`, `max-conn-idle-time-seconds 300`, `health-check-period-seconds 30`. The MySQL family (`-storage-mysql-*`, `-storage-tidb-*`) defaults to `max-conns 25`, `max-idle-conns 5`, `conn-max-lifetime-seconds 3600`, `conn-max-idle-time-seconds 300`. CockroachDB/YugabyteDB use the Postgres driver; TiDB uses the MySQL driver. See [Configuration](/docs/operating/configuration/) for every storage flag and DSN format.

**Common options**

| Flag | Default | Meaning |
|------|---------|---------|
| `-config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |
| `-version` | — | Print version and exit |

> **Note:** Two hub config keys have **no** CLI flag and are set only via YAML or env var: `secure_cookies` (`LEAPMUX_HUB_SECURE_COOKIES`) and `encryption_key_path` (`LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `<data-dir>/encryption.key`). See [Configuration](/docs/operating/configuration/) and [Encryption & Data](/docs/operating/encryption-and-data/).

## worker

Run a Worker that connects out to a Hub. Workers do not serve an inbound HTTP port; they register with the Hub using a key minted in the Hub UI. See [Managing Workers](/docs/operating/managing-workers/).

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
| `-max-message-size` | `0` (= 16 MiB) | Max reassembled channel message size in bytes |
| `-max-incomplete-chunked` | `0` (= 4) | Max in-flight chunked sequences per channel |
| `-agent-startup-timeout-seconds` | `300` | Agent startup timeout |
| `-api-timeout-seconds` | `10` | JSON-RPC request timeout |

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

> **Note:** An unregistered Worker with no saved credentials errors with `worker is unregistered: pass --registration-key <key> from the hub UI`. Passing `-registration-key` again to an already-registered Worker errors with `worker is already registered; remove --registration-key or wipe local state to re-register`, which protects you from burning a one-time key.

### worker cross-worker-pins

A local-only utility for inspecting the Worker's TOFU pin store. It runs entirely against local files — no Worker process starts. See [Security & Threat Model](/docs/operating/security/) for what these pins protect.

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

Run a Hub and a Worker in one process with **real** password authentication — the same program as `solo` but with login enabled, binding all interfaces, and the first admin bootstrapped through the `/setup` flow. See [Running LeapMux](/docs/operating/running-leapmux/#dev-mode).

```bash
leapmux dev [flags]
```

Dev mode uses the **same flag set as solo**, with one addition:

| Flag | Default | Meaning |
|------|---------|---------|
| `-public-url` | empty | Public base URL when behind a reverse proxy |

The other differences from solo: the default `-listen` is `:4327` (all interfaces), the config/data location is `~/.config/leapmux/dev/`, and the bundled Worker's auto-registration is deferred until the first admin completes `/setup`.

## version

Print the build version and exit. The output is a single line with fields joined by ` · `:

```bash
$ leapmux version
0.0.1-dev · 9c81b87 · feature/foo · Thu, 4/23/2026, 11:45:00 PM KST
```

Fields are conditional: the version value is always present (falls back to `dev`), the commit hash and build time appear when set, and the branch is shown only when it is not `main`. The top-level `-version` / `--version` flags print the same string.

## admin (command-group outline)

`leapmux admin` manages a Hub's persistent data **directly against its database and on-disk encryption key** — no running Hub or network call required. It is a tree of command groups. For full per-command flags and behavior, see [Admin CLI](/docs/operating/admin-cli/).

```bash
leapmux admin <group> <command> [flags]
```

| Group | Commands |
|-------|----------|
| `org` | `list` |
| `user` | `list`, `get`, `create`, `update`, `delete`, `reset-password`, `grant-admin`, `revoke-admin`, `list-sessions` |
| `session` | `list`, `revoke`, `revoke-user`, `purge-expired` |
| `worker` | `list`, `get`, `deregister`; subgroup `reg-key`: `list`, `revoke`, `purge-expired` |
| `oauth-provider` | `add`, `list`, `remove`, `enable`, `disable` |
| `encryption-key` | `rotate`, `remove`, `reencrypt`, `rotate-pepper` |
| `db` | `path`, `migrate`, `version` |
| `api-token` | `list`, `issue`, `revoke` |
| `delegation-token` | `list`, `revoke` |

Most admin commands accept `--data-dir` and `--config` to locate the database and encryption key; output is indented JSON or a tabular listing. Commands that take `--password` prompt interactively when the flag is omitted (and require `--password` when stdin is not a terminal). See [Admin CLI](/docs/operating/admin-cli/), [Authentication Providers](/docs/operating/authentication-providers/), and [Encryption & Data](/docs/operating/encryption-and-data/).

## remote (command-group outline)

`leapmux remote` drives a **running** Hub over RPC — it does not touch the database. It is used both by external scripts (which authorize with `leapmux remote auth login`) and by agents/terminals that LeapMux spawns (which inherit `LEAPMUX_REMOTE_*` env vars). Every command emits a JSON envelope — `{"data": ...}` on success, `{"error": {"code", "message"}}` on failure (both on stdout) — with a non-zero exit on failure. For full per-command flags, entity-ID resolution, and output shapes, see [Remote Control CLI](/docs/operating/remote-control-cli/).

```bash
leapmux remote <group> <command> [flags]
leapmux remote auth login --hub https://hub.example.com   # authorize first
```

| Group | Commands |
|-------|----------|
| *(top level)* | `whoami`, `version` |
| `auth` | `login`, `logout`, `list`, `status` |
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
| `LEAPMUX_HUB_PUBLIC_URL` | hub `public_url` | `https://hub.example.com` |
| `LEAPMUX_HUB_DATA_DIR` | hub `data_dir` | `/var/lib/leapmux/hub` |
| `LEAPMUX_HUB_LOG_LEVEL` | hub `log_level` | `info` |
| `LEAPMUX_HUB_SIGNUP_ENABLED` | hub `signup_enabled` | `true` |
| `LEAPMUX_HUB_SECURE_COOKIES` | hub `secure_cookies` (no CLI flag) | `true` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | hub `encryption_key_path` (no CLI flag) | `/etc/leapmux/encryption.key` |
| `LEAPMUX_WORKER_HUB` | worker `hub` | `https://hub.example.com` |
| `LEAPMUX_WORKER_NAME` | worker `name` | `build-box-1` |
| `LEAPMUX_WORKER_DATA_DIR` | worker `data_dir` | `/var/lib/leapmux/worker` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | worker `encryption_mode` | `post-quantum` |
| `LEAPMUX_WORKER_LOG_LEVEL` | worker `log_level` | `info` |

The prefix strip lowercases the remainder but does **not** translate `_` into `.`, so nested storage keys such as `storage.type` and `storage.postgres.dsn` cannot be set cleanly via env vars — use the YAML config file or the dedicated `-storage-*` flags instead. See [Configuration](/docs/operating/configuration/) for the full list and precedence rules (defaults < config file < env vars < explicitly-set CLI flags).

### Remote CLI (`leapmux remote`)

| Variable | Used by | Meaning |
|----------|---------|---------|
| `LEAPMUX_HUB` | `remote` (and `auth login --hub` fallback) | Hub URL when `--hub` is not passed |
| `LEAPMUX_REMOTE_CONFIG_DIR` | `remote` | Override the credential/pin directory (default `~/.config/leapmux/remote`) |
| `LEAPMUX_REMOTE_SOCK` | spawned agents | Local IPC socket URL (selects local-IPC transport) |
| `LEAPMUX_REMOTE_TOKEN` | spawned agents | Per-process bearer token for the local IPC socket |
| `LEAPMUX_REMOTE_USER_ID` | spawned agents | Authenticated user ID (entity default) |
| `LEAPMUX_REMOTE_WORKER_ID` | spawned agents | Host worker ID (default for `--worker-id`) |
| `LEAPMUX_REMOTE_ORG_ID` | spawned agents | Org ID (default for `--org-id`) |
| `LEAPMUX_REMOTE_TAB_ID` | spawned agents | Spawning tab's ID (default for `--tab-id`) |
| `LEAPMUX_REMOTE_TAB_TYPE` | spawned agents | `agent`, `terminal`, or `file` |
| `LEAPMUX_REMOTE_WORKING_DIR` | spawned agents | Working directory at spawn |
| `LEAPMUX_REMOTE_AGENT_PROVIDER` | spawned agents | Agent provider (agents only) |

The `LEAPMUX_REMOTE_*` variables (the `_SOCK` / `_TOKEN` / `_*_ID` / `_TAB_*` family) are injected automatically by the Worker into the agents and terminals it spawns; you do not set them by hand. See [Remote Control CLI](/docs/operating/remote-control-cli/) for how they drive entity-ID resolution.

## Config and data locations

Each mode reads an optional YAML config named after the mode, and stores data, under its own directory. A missing config file is silently skipped.

| Mode | Config file | Default data dir |
|------|-------------|------------------|
| `solo` | `~/.config/leapmux/solo/solo.yaml` | `~/.config/leapmux/solo` |
| `hub` | `~/.config/leapmux/hub/hub.yaml` | `~/.config/leapmux/hub` |
| `worker` | `~/.config/leapmux/worker/worker.yaml` | `~/.config/leapmux/worker` |
| `dev` | `~/.config/leapmux/dev/dev.yaml` | `~/.config/leapmux/dev` |
| `remote` | `~/.config/leapmux/remote/<hub-host>.json` (credentials, mode 0600) | — |

In `solo` and `dev`, the data directory is split into `<data-dir>/hub` and `<data-dir>/worker` subdirectories. See [Running LeapMux](/docs/operating/running-leapmux/) and [Configuration](/docs/operating/configuration/) for the full layout and resolution rules.
