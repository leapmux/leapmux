---
title: "Configuration"
type: docs
weight: 2
---

This chapter is the complete reference for configuring the LeapMux **Hub** and **Worker** services: how settings are layered and resolved, where config files live, every config key with its default and meaning, the supported storage backends, listen-address formats, encryption mode, and the timeout/limit knobs.

If you are looking for *how to launch* each mode (solo, hub, worker, dev) and what each is for, see [Running LeapMux](/docs/operating/running-leapmux/). For key management, encryption at rest, and database operations, see [Encryption & Data](/docs/operating/encryption-and-data/).

> **Note:** Everything here applies to the long-running daemons. `solo` and `dev` modes reuse the Hub's configuration loader with a restricted flag set; the differences are called out where relevant. The desktop app and the `remote`/`admin` CLIs are configured separately — see [Running LeapMux](/docs/operating/running-leapmux/), [Remote Control CLI](/docs/operating/remote-control-cli/), and [Admin CLI](/docs/operating/admin-cli/).

## Configuration precedence

Both the Hub and the Worker load configuration through the same pipeline. Layers are applied in order, and **later layers win**:

1. **Built-in defaults** — compiled into the binary.
2. **YAML config file** — loaded only if the file exists. A missing config file is silently skipped, not an error.
3. **Environment variables** — prefixed `LEAPMUX_HUB_` (Hub, solo, dev) or `LEAPMUX_WORKER_` (Worker).
4. **Explicitly-set CLI flags** — only flags you actually pass on the command line override. A flag left at its default value does *not* count as "set" and will not override an env var or config-file value.

```
built-in defaults  <  YAML config file  <  environment variables  <  CLI flags you set
       (lowest)                                                          (highest)
```

**Each layer overrides the ones below it (top wins):**

```text
            ┌───────────────────────────────────────────┐  ▲
  highest   │  Explicitly-set CLI flags                 │  │
  priority  │  (--listen, --log-level, ...)             │  │
            └───────────────────────────────────────────┘  │
            ┌───────────────────────────────────────────┐  │
            │  Environment variables                    │  │
            │  (LEAPMUX_HUB_* / LEAPMUX_WORKER_*)       │  │  each
            └───────────────────────────────────────────┘  │  layer
            ┌───────────────────────────────────────────┐  │  overrides
            │  YAML config file                         │  │  the ones
            │  (hub.yaml / worker.yaml, if present)     │  │  below
            └───────────────────────────────────────────┘  │
            ┌───────────────────────────────────────────┐  │
  lowest    │  Built-in defaults                        │  │
  priority  │  (compiled into the binary)               │  │
            └───────────────────────────────────────────┘  │
```

> **Tip:** Because only *explicitly passed* flags override lower layers, you can set a baseline in `hub.yaml`, override per-environment values with `LEAPMUX_HUB_*` env vars in your deployment, and still drop in a one-off `--log-level debug` on the command line for a single run.

## Config file locations

Each mode looks for a YAML config file in its own directory under `~/.config/leapmux/`. The `~` is expanded to your home directory.

| Mode     | Default config directory          | Default config file                      |
| -------- | --------------------------------- | ---------------------------------------- |
| `hub`    | `~/.config/leapmux/hub`           | `~/.config/leapmux/hub/hub.yaml`         |
| `worker` | `~/.config/leapmux/worker`        | `~/.config/leapmux/worker/worker.yaml`   |
| `solo`   | `~/.config/leapmux/solo`          | `~/.config/leapmux/solo/solo.yaml`       |
| `dev`    | `~/.config/leapmux/dev`           | `~/.config/leapmux/dev/dev.yaml`         |

Override the path with the `--config` (or `-config`) flag, which accepts `--config=PATH`, `--config PATH`, `-config=PATH`, or `-config PATH`. It is scanned out of the arguments before normal flag parsing, so it works regardless of position.

```bash
leapmux hub --config /etc/leapmux/hub.yaml
```

### Data directory resolution

The `data_dir` setting (`--data-dir`, default `.`) is where the SQLite database, the encryption key file, and the local IPC socket live. It resolves like this:

- `~` is expanded to your home directory.
- An **absolute** path is used as-is.
- A **relative** path (including the default `.`) resolves against the **directory containing the config file** if that file exists; otherwise against the default config directory (e.g. `~/.config/leapmux/hub`).

So with no config file and no `--data-dir`, the Hub's data dir is `~/.config/leapmux/hub`. The data directory is created with mode `0750` at startup.

> **Note:** In `solo` and `dev` modes a single `data_dir` is split in two: the in-process Hub uses `<data_dir>/hub` and the in-process Worker uses `<data_dir>/worker`.

## Environment variable mapping

Env vars are prefixed `LEAPMUX_HUB_` for the Hub (and solo/dev) or `LEAPMUX_WORKER_` for the Worker. The mapping rule is simple but has an important limitation:

- The prefix is **stripped** and the remainder is **lowercased**. That gives you the flat config key directly.
- The mapping does **not** translate underscores into dots.

This means flat, top-level keys map cleanly:

| Env var                               | Config key             |
| ------------------------------------- | ---------------------- |
| `LEAPMUX_HUB_LISTEN`                  | `listen`               |
| `LEAPMUX_HUB_LOG_LEVEL`              | `log_level`            |
| `LEAPMUX_HUB_PUBLIC_URL`             | `public_url`           |
| `LEAPMUX_HUB_SECURE_COOKIES`        | `secure_cookies`       |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`   | `encryption_key_path`  |
| `LEAPMUX_HUB_LOCAL_LISTEN`          | `local_listen`         |
| `LEAPMUX_WORKER_HUB`                | `hub`                  |
| `LEAPMUX_WORKER_ENCRYPTION_MODE`   | `encryption_mode`      |

> **Warning:** Nested **storage** keys live under dotted paths (`storage.type`, `storage.postgres.dsn`, `storage.sqlite.path`, …). Because the env mapping does not convert underscores to dots, you **cannot** reliably set storage settings via simple env vars. Configure storage through the **YAML config file** or the dedicated **CLI flags** instead (for example `--storage-type postgres --storage-postgres-dsn ...`).

```bash
# Flat keys work cleanly as env vars:
export LEAPMUX_HUB_LISTEN=":8080"
export LEAPMUX_HUB_LOG_LEVEL="debug"
export LEAPMUX_HUB_PUBLIC_URL="https://hub.example.com"
leapmux hub
```

## Listen addresses

LeapMux uses two kinds of listen addresses.

### TCP listen (`listen`)

The TCP address the Hub's HTTP server binds. Formats:

- `:4327` — all interfaces, port 4327.
- `127.0.0.1:4327` — loopback only.

Defaults differ by mode:

| Mode   | Default `listen`     | Notes                                                    |
| ------ | -------------------- | ------------------------------------------------------- |
| `hub`  | `:4327`              | All interfaces; real authentication required.           |
| `dev`  | `:4327`              | All interfaces.                                         |
| `solo` | `127.0.0.1:4327`     | Loopback only; every request is auto-authenticated as admin. |

> **Warning:** In solo mode every request is auto-authenticated as the admin. If you bind it to a non-loopback address, anyone who can reach the port has full admin access without credentials, and the Hub logs a warning to that effect. Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication.

### Local IPC listen (`local_listen`)

In addition to TCP, the Hub binds a **local IPC** listener for same-machine clients (including the auto-registered Worker in solo/dev). Two URL schemes are supported:

- `unix:<path>` — a Unix domain socket (Unix/macOS).
- `npipe:<name>` — a Windows named pipe.

If `local_listen` is empty, a platform default is used:

| Platform | Default local IPC URL                         |
| -------- | --------------------------------------------- |
| Unix/macOS | `unix:<data_dir>/hub.sock`                  |
| Windows  | `npipe:leapmux-hub-<SID>` (current user's SID) |

The same two schemes are also valid values for the Worker's `--hub` URL, so a local Worker can connect over the socket instead of TCP. An invalid value fails at startup with `invalid local_listen: ...`.

## Hub configuration reference

Env prefix: `LEAPMUX_HUB_`. Defaults shown are the built-in values. Each key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `log_level` → `--log-level`); the only keys without a flag are listed under [Keys with no CLI flag](#keys-with-no-cli-flag).

### Server options

| Config key | Default | Meaning |
| --- | --- | --- |
| `listen` | `:4327` | TCP listen address (e.g. `:4327` or `127.0.0.1:4327`). |
| `local_listen` | *(empty)* | Local IPC listen URL (`unix:<path>` or `npipe:<name>`); platform default used if empty. |
| `public_url` | *(empty)* | Public base URL when behind a reverse proxy (e.g. `https://hub.example.com`). |
| `data_dir` | `.` | Data directory; relative paths resolve against the config dir. |
| `dev_frontend` | *(empty)* | Frontend dev-server URL for the local reverse proxy (local development). |
| `log_level` | `info` | Log level: `debug`, `info`, `warn`, `error` (case-insensitive). |

> **Note:** `public_url` must be an absolute `http`/`https` URL with a host and **nothing else** — no userinfo, no path (sub-path proxying is rejected), no query, no fragment. One trailing slash is trimmed. It is **not supported in solo mode**, where setting it fails with `public_url is not supported in solo mode`. See [Running LeapMux](/docs/operating/running-leapmux/) for reverse-proxy setup.

### Auth options

| Config key | Default | Meaning |
| --- | --- | --- |
| `signup_enabled` | `false` | Enable user sign-up. |
| `email_verification_required` | `false` | Require email verification on sign-up. Requires `smtp_host`. |

See [Accounts & Authentication](/docs/using/accounts/) for the sign-up/verification flows, and [Authentication Providers](/docs/operating/authentication-providers/) for OAuth/OIDC.

### SMTP options

Email is needed for verification and notifications. Set `smtp_host` to enable it; when set, `smtp_from_address` is required and must be a valid email.

| Config key | Default | Meaning |
| --- | --- | --- |
| `smtp_host` | *(empty)* | SMTP server host. |
| `smtp_port` | `587` | SMTP server port (must be 1–65535 when host is set). |
| `smtp_username` | *(empty)* | SMTP username. |
| `smtp_password` | *(empty)* | SMTP password. |
| `smtp_from_address` | *(empty)* | From address (required and validated when host is set). |
| `smtp_tls_mode` | `starttls` | TLS mode: `starttls`, `implicit`, or `none`. |

The TLS modes:

- `starttls` — STARTTLS upgrade, typically port 587 (default; an empty value normalizes to this).
- `implicit` — direct TLS, typically port 465 (SMTPS).
- `none` — plaintext. Combining `none` with a username on a non-localhost host is rejected, because credentials cannot be sent safely over an unencrypted non-localhost connection.

### Timeout and limit options

| Config key | Default | Meaning |
| --- | --- | --- |
| `max_message_size` | `0` | Maximum reassembled channel message size in bytes (`0` = 16 MiB default). |
| `max_incomplete_chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `api_timeout_seconds` | `10` | General API timeout in seconds (`<=0` falls back to 10). |
| `agent_startup_timeout_seconds` | `300` | Agent startup timeout in seconds (`<=0` falls back to 300). |
| `worktree_create_timeout_seconds` | `60` | Worktree creation timeout in seconds (`<=0` falls back to 60). |

### Keys with no CLI flag

These two keys can only be set through the YAML file or an env var — there is no command-line flag.

| Config key            | Env var                            | Default                       | Meaning                                                              |
| --------------------- | ---------------------------------- | ----------------------------- | ------------------------------------------------------------------- |
| `secure_cookies`      | `LEAPMUX_HUB_SECURE_COOKIES`       | `false`                       | Issue secure cookies and use `https` scheme in the derived base URL. |
| `encryption_key_path` | `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`  | `{data_dir}/encryption.key`   | Path to the encryption key ring file.                               |

See [Encryption & Data](/docs/operating/encryption-and-data/) for the encryption key ring, rotation, and what is encrypted at rest.

### Derived paths and behaviors

- **SQLite DB path:** `storage.sqlite.path` if set, otherwise `{data_dir}/hub.db`.
- **Encryption key file:** `encryption_key_path` if set, otherwise `{data_dir}/encryption.key`.
- **Base URL:** `public_url` if set; otherwise derived from `listen` (scheme is `https` only when `secure_cookies` is true, and a bare `:port` listen resolves the host to `localhost`).
- **Metrics:** the Hub always mounts a Prometheus endpoint at `/metrics`. There is no config flag to enable, disable, or relocate it.

## Worker configuration reference

Env prefix: `LEAPMUX_WORKER_`. A Worker connects to a Hub over a URL; it does not serve HTTP itself. See [Managing Workers](/docs/operating/managing-workers/) for registration and approval. Each key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `db_max_conns` → `--db-max-conns`).

### Worker options

| Config key | Default | Meaning |
| --- | --- | --- |
| `hub` | `http://127.0.0.1:4327` | Hub server URL: `http[s]://...`, `unix:<socket-path>`, or `npipe:<pipe-name>`. Required. |
| `registration_key` | *(empty)* | Registration key from the Hub UI; required on first run. Never persisted to disk. |
| `name` | *(empty → hostname)* | Worker display name; defaults to the OS hostname when empty. |
| `data_dir` | `.` | Data directory; relative paths resolve against the config dir. |
| `log_level` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `encryption_mode` | `post-quantum` | E2EE mode: `classic` or `post-quantum`. |
| `use_login_shell` | `true` | Wrap the agent invocation in the user's login shell. |

> **Note:** `registration_key` is required on first run and is never persisted to disk. On subsequent runs you simply omit it — the saved credentials are reused. Do **not** pass it again to an already-registered Worker: that fails with `worker is already registered; remove --registration-key or wipe local state to re-register` (the key is rejected, not silently ignored, to keep you from accidentally burning it on a machine that is already configured). For the registration flow and the exact error messages, see [Managing Workers](/docs/operating/managing-workers/).

### Timeout and limit options

| Config key | Default | Meaning |
| --- | --- | --- |
| `max_message_size` | `0` | Maximum reassembled channel message size in bytes (`0` = 16 MiB default). |
| `max_incomplete_chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `agent_startup_timeout_seconds` | `300` | Agent startup timeout in seconds (`<=0` falls back to 300). |
| `api_timeout_seconds` | `10` | JSON-RPC request timeout in seconds (`<=0` falls back to 10). |

### SQLite database options

The Worker keeps its own SQLite database (`<data_dir>/worker.db`) for transient agent/session state. These tune that connection.

| Config key | Default | Meaning |
| --- | --- | --- |
| `db_max_conns` | `4` | Maximum open database connections. |
| `db_cache_size` | `0` | SQLite page cache size (positive = pages, negative = KiB, e.g. `-65536` = 64 MiB; `0` = default). |
| `db_mmap_size` | `0` | SQLite memory-mapped I/O size in bytes (`0` = disabled). |

### Worker state file

After registration, a Worker persists its identity to `<data_dir>/state.json` (mode `0600`): its Worker ID, Hub auth token, who registered it, and its private E2EE keypair (auto-generated on first run). For the underlying key primitives, see [Encryption & Data](/docs/operating/encryption-and-data/) and [Security & Threat Model](/docs/operating/security/).

> **Warning:** `state.json` holds the Worker's private E2EE keys and Hub auth token, and it is **not encrypted**. Treat it as a secret and back it up. Losing it forces re-registration with a new registration key and a new key identity.

### Encryption mode

The `encryption_mode` key (flag `--encryption-mode`, env `LEAPMUX_WORKER_ENCRYPTION_MODE`) selects the E2EE mode for the Frontend-to-Worker channel. Accepted values:

| Value          | Notes                                                                          |
| -------------- | ------------------------------------------------------------------------------ |
| `post-quantum` | Default. Aliases `pq`, `post_quantum`, and an empty value all map here.         |
| `classic`      | Classical-only mode.                                                            |

Any unrecognized value falls back to `post-quantum` (fail-safe). For what each mode protects and the underlying primitives, see [Security & Threat Model](/docs/operating/security/).

## Storage backends

The Hub stores all relational data (users, orgs, workers, sessions, workspaces, tokens, …) in a single SQL store. Select the backend with `storage.type` (flag `--storage-type`). Every storage setting's CLI flag is `--` plus the dotted key with dots and underscores replaced by hyphens — for example `storage.sqlite.path` → `--storage-sqlite-path`, `storage.postgres.max_conns` → `--storage-postgres-max-conns`. Schema migrations run automatically every time the store is opened — including normal Hub startup — so there is no manual migration step on a fresh database.

| `storage.type` | Driver family | Notes                                              |
| -------------- | ------------- | ------------------------------------------------- |
| *(empty)*      | SQLite        | Empty is treated as `sqlite`.                      |
| `sqlite`       | SQLite        | Default; embedded file database.                   |
| `postgres`     | PostgreSQL    | Requires `storage.postgres.dsn`.                   |
| `mysql`        | MySQL         | Requires `storage.mysql.dsn`.                      |
| `cockroachdb`  | PostgreSQL    | Reuses the Postgres driver; requires `storage.cockroachdb.dsn`. |
| `yugabytedb`   | PostgreSQL    | Reuses the Postgres driver; requires `storage.yugabytedb.dsn`.  |
| `tidb`         | MySQL         | Reuses the MySQL driver; requires `storage.tidb.dsn`. |

An unknown type is rejected at startup with `unsupported storage.type: "<type>" (valid: sqlite, postgres, mysql, cockroachdb, yugabytedb, tidb)`.

> **Note:** Configure storage via the YAML file or the dedicated CLI flags, not env vars (see the warning under [Environment variable mapping](#environment-variable-mapping)). For backups, key/DB interplay, and the `leapmux admin db` / `leapmux admin encryption-key` commands, see [Encryption & Data](/docs/operating/encryption-and-data/).

### SQLite (default)

SQLite is the zero-configuration default; it needs nothing beyond an optional path and tuning. Connections are opened with WAL journaling, a 60-second busy timeout, and foreign keys enabled, and the DB file is set to mode `0600`. Expect `hub.db-wal` and `hub.db-shm` sidecar files while the Hub is running.

| Config key | Default | Meaning |
| --- | --- | --- |
| `storage.sqlite.path` | `{data_dir}/hub.db` | SQLite database file path. |
| `storage.sqlite.max_conns` | `4` | Maximum open connections. |
| `storage.sqlite.cache_size` | `0` | Page cache size (positive = pages, negative = KiB, e.g. `-65536` = 64 MiB; `0` = SQLite default ≈ 2 MiB). |
| `storage.sqlite.mmap_size` | `0` | Memory-mapped I/O size in bytes (`0` = disabled). |

```yaml
# hub.yaml — SQLite with a custom path and 64 MiB page cache
data_dir: /var/lib/leapmux/hub
storage:
  type: sqlite
  sqlite:
    path: /var/lib/leapmux/hub/hub.db
    max_conns: 4
    cache_size: -65536   # 64 MiB
    mmap_size: 268435456 # 256 MiB
```

### PostgreSQL, CockroachDB, YugabyteDB

These three share the same driver, config block layout, and pool defaults. Only the config-block name and the flag prefix differ: `storage.postgres.*` / `--storage-postgres-*`, `storage.cockroachdb.*` / `--storage-cockroachdb-*`, `storage.yugabytedb.*` / `--storage-yugabytedb-*`.

| Config key (under `storage.<name>`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection string (URL form). |
| `max_conns` | `25` | Maximum open connections. |
| `min_conns` | `5` | Minimum pool connections kept alive. |
| `conn_max_lifetime_seconds` | `3600` | Connection max lifetime in seconds. |
| `max_conn_idle_time_seconds` | `300` | Max idle time per connection in seconds. |
| `health_check_period_seconds` | `30` | Pool health-check period in seconds. |

DSN formats (the `dsn` is parsed as a connection URL):

- PostgreSQL: `postgres://user:password@host:5432/dbname?sslmode=disable`
- CockroachDB: `postgresql://root@host:26257/defaultdb?sslmode=disable`
- YugabyteDB: `postgresql://yugabyte@host:5433/yugabyte?sslmode=disable`

```yaml
# hub.yaml — PostgreSQL backend
storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
    max_conns: 25
    min_conns: 5
    conn_max_lifetime_seconds: 3600
    max_conn_idle_time_seconds: 300
    health_check_period_seconds: 30
```

> **Tip:** `sslmode=disable` is fine for local testing but you should use `sslmode=require` (or stronger) for any networked database. CockroachDB and YugabyteDB use the same config block shape — just set `type: cockroachdb` / `type: yugabytedb` and fill in `storage.cockroachdb` / `storage.yugabytedb`.

### MySQL and TiDB

MySQL and TiDB share the MySQL driver, config layout, and pool defaults. Prefixes are `storage.mysql.*` / `--storage-mysql-*` and `storage.tidb.*` / `--storage-tidb-*`.

| Config key (under `storage.<name>`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection string (go-sql-driver DSN). |
| `max_conns` | `25` | Maximum open connections. |
| `max_idle_conns` | `5` | Maximum idle connections. |
| `conn_max_lifetime_seconds` | `3600` | Connection max lifetime in seconds. |
| `conn_max_idle_time_seconds` | `300` | Max idle time per connection in seconds. |

DSN formats:

- MySQL: `user:password@tcp(host:3306)/dbname?parseTime=true`
- TiDB: `root@tcp(host:4000)/leapmux?parseTime=true`

```yaml
# hub.yaml — MySQL backend
storage:
  type: mysql
  mysql:
    dsn: "leapmux:secret@tcp(db.internal:3306)/leapmux?parseTime=true"
    max_conns: 25
    max_idle_conns: 5
    conn_max_lifetime_seconds: 3600
    conn_max_idle_time_seconds: 300
```

> **Warning:** MySQL and TiDB DSNs **must** include `parseTime=true`, or time columns will fail to decode. For TiDB, the store best-effort enables foreign-key support on connect (a no-op on real MySQL, which already enforces them).

## Example configurations

### Minimal solo

In solo mode you need no config file at all — it defaults to loopback TCP and a local SQLite database. To pin the data directory and turn on debug logging:

```yaml
# ~/.config/leapmux/solo/solo.yaml
data_dir: ~/leapmux-data
log_level: debug
```

### Production Hub behind a reverse proxy

```yaml
# /etc/leapmux/hub.yaml
listen: "127.0.0.1:4327"      # only the reverse proxy reaches the Hub
public_url: "https://hub.example.com"
secure_cookies: true
data_dir: /var/lib/leapmux/hub

log_level: info

signup_enabled: true
email_verification_required: true

smtp_host: "smtp.example.com"
smtp_port: 587
smtp_username: "leapmux@example.com"
smtp_password: "${SMTP_PASSWORD}"   # substitute via your secret manager
smtp_from_address: "no-reply@example.com"
smtp_tls_mode: starttls

storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
```

```bash
leapmux hub --config /etc/leapmux/hub.yaml
```

### Worker connecting to a remote Hub

```yaml
# ~/.config/leapmux/worker/worker.yaml
hub: "https://hub.example.com"
name: "build-box-01"
encryption_mode: post-quantum
log_level: info
data_dir: ~/.config/leapmux/worker
```

```bash
# First run only: pass the registration key minted in the Hub UI
leapmux worker --registration-key "<key-from-hub-ui>"
# Subsequent runs: the key is already saved in state.json
leapmux worker
```

## Help and version

Every mode supports the standard help tokens, and each mode honors `--version`:

- `-h`, `-help`, `--help`, or `help` prints categorized usage to stdout.
- `--version` (or `-version`) prints the build version and exits.

The bare `version` subcommand is a **top-level** command (`leapmux version`), not a per-mode token. Passing it inside a mode — `leapmux hub version` — is rejected as an unexpected positional argument. Unexpected positional arguments are rejected with `unexpected argument: "<arg>" (use --help for usage)`.

## Related chapters

- [Running LeapMux](/docs/operating/running-leapmux/) — run modes, ports, data dirs, Docker, reverse proxy.
- [Encryption & Data](/docs/operating/encryption-and-data/) — encryption key ring, rotation, DB migrations, backup/restore.
- [Managing Workers](/docs/operating/managing-workers/) — registration keys, approval, Worker selection.
- [Authentication Providers](/docs/operating/authentication-providers/) — configuring OAuth/OIDC as an operator.
- [Security & Threat Model](/docs/operating/security/) — E2EE protocol, encryption modes, trust boundaries.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated command and flag cheat-sheet.
