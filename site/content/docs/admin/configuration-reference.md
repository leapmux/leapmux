---
title: "Configuration Reference"
description: "Complete reference of LeapMux Hub and Worker YAML keys, environment variables, CLI flags, database instance settings, storage backends, and Prometheus metrics."
type: docs
weight: 5
---

This page is the complete key-by-key reference for the LeapMux **Hub** and **Worker** daemons. For configuration precedence, deployment profiles, listen-address rules, and operational behavior, see [Configuration Guide](/docs/admin/configuration/).

## Hub configuration

Environment variable prefix: `LEAPMUX_HUB_` (also used by `solo` and `dev`). Defaults below are built-in values. Each flat key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `log_level` → `--log-level`).

### Server options

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `listen` | `--listen` (repeatable) | *(empty = platform default)* | Bind set: a YAML list of TCP addresses (`:4327`, `127.0.0.1:4327`) and local IPC URLs (`unix:<path>`, `npipe:<name>`). LeapMux adds the platform default local IPC URL when the list specifies none; see [Listen addresses](/docs/admin/configuration/#listen-addresses). |
| `data_dir` | `--data-dir` | `.` | Data directory; relative paths resolve against the config directory. |
| `dev_frontend` | `--dev-frontend` | *(empty)* | Frontend dev-server URL for the local reverse proxy (local development). |
| `log_level` | `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` (case-insensitive). |

These four keys, the `storage` block below, and `encryption_key_path` are the **entire** Hub config-file surface — only what the process needs before the database opens. All behavioral settings live in the Hub database so they can change without editing files.

### Keys with no CLI flag

| Config key | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `encryption_key_path` | `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | `{data_dir}/encryption.key` | Path to the encryption key ring and token pepper file used to open the database, encrypt all 37 protected columns across 15 Hub tables, and verify peppered bearer digests. No CLI flag exists. |

See [Encryption & Data](/docs/admin/encryption-and-data/) for key ring format, rotation, and backup guidance.

### Derived paths and behaviors

- **SQLite DB path:** `storage.sqlite.path` if set; otherwise `{data_dir}/hub.db`.
- **Encryption key file:** `encryption_key_path` if set; otherwise `{data_dir}/encryption.key`.
- **Base URL:** `public_url` if set; otherwise derived from `listen` (`https` only when `secure_cookies` is `true`; a bare `:port` resolves the host to `localhost`).
- **Metrics:** The Hub always exposes a Prometheus endpoint at `/metrics`. No flag enables, disables, or relocates it.

### Instance settings (database)

Behavioral settings live in the Hub database (`hub_settings`), managed with `leapmux admin settings`:

```bash
leapmux admin settings list
leapmux admin settings set signup_enabled true
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"from_address":"hub@example.com"}'
leapmux admin settings set-secret smtp '{"password":"..."}'
leapmux admin settings get smtp
leapmux admin settings reset signup_enabled
```

Values are JSON documents; omitted fields keep their current or default values. Both `value` and `secret` columns are encrypted at rest with the Hub keystore. Secret-bearing fields (SMTP password, captcha secrets) are written with `set-secret` and never returned by `list` or `get`. `reset` returns a key to its built-in default.

Propagation classes reported by `settings list`:

- **hot** — applied within ~30 seconds without restart (also the convergence window across Hubs sharing one database).
- **restart** — read at boot (queue pool floors, frame ceilings); applies after a Hub restart.

| Setting key | Shape | Default | Class |
| --- | --- | --- | --- |
| `signup_enabled` | boolean | `false` | hot |
| `session_duration_seconds` | integer | `604800` (7d) | hot (minimum `300`) |
| `secure_cookies` | boolean | `false` | hot (changes cookie name, signing everyone out) |
| `public_url` | string | *(empty)* | hot (scheme + host only, no path) |
| `trusted_proxy_ranges` | JSON list of IP selectors or provider tokens | `[]` | hot |
| `extra_listen_addresses` | JSON list of TCP addresses | `[]` | hot (solo mode only; see [Network access](/docs/admin/configuration/#network-access)) |
| `smtp` | `{host, port, username, from_address, tls_mode}` + secret `{password}` | disabled | hot |
| `timeouts` | `{api_seconds, agent_startup_seconds}` | `10` / `300` | hot |
| `limits` | `{max_connections_per_user, max_workers_per_user}` | `32` / `64` | hot (`0` = unlimited) |
| `mail_limits` | `{failure_cooldown_seconds, recipient_max, recipient_window_seconds}` | `10` / `10` / `3600` | hot (`recipient_max` `0` = unlimited) |
| `max_message_size_bytes` | integer | `16777216` (16 MiB) | restart (64 KiB–64 MiB) |
| `queue_budget` | `{relay_bytes, worker_bytes, userevents_bytes}` | `0` = auto-size | restart |
| `open_app_registration` | boolean | `false` | hot |
| `captcha.*` | `captcha.enabled`, `captcha.selected`, `captcha.altcha`, `captcha.recaptcha_v3`, `captcha.turnstile` | ALTCHA enabled, `PBKDF2/SHA-256`, cost `10000`, 20m expiry | hot |
| `rate_limit.*` | `elevation`, `email_change`, `login_anonymous`, `oauth_anonymous` | `5/15m`, `6/15m`, `10/15m`, `600/10m` | hot |

#### Solo-mode setting omissions

Solo mode omits settings a single-user Hub does not use from both `settings list` and the Preferences dialog:

| Omitted in solo | Reason |
| --- | --- |
| `signup_enabled` | Solo has no sign-up. |
| `smtp` | Solo has no sign-up and no outbound mail. |
| `captcha.*` | Solo runs no captcha. `rate_limit.login_anonymous` stays active to guard TCP sign-in. |
| `rate_limit.elevation`, `rate_limit.email_change` | Keyed by user; solo has one user and refuses email changes. |
| `mail_limits` | Solo sends no mail. |

`public_url`, `open_app_registration`, `session_duration_seconds`, `secure_cookies`, `trusted_proxy_ranges`, `rate_limit.login_anonymous`, and `rate_limit.oauth_anonymous` remain active in solo mode.

{{< callout type="info" >}}
Email verification is not a separate setting. Once `smtp` has both `host` and `from_address`, the Hub requires verification for new non-admin sign-ups and enables account recovery and Worker registration emails. Removing or disabling SMTP turns verification off at runtime.

**SMTP-enable transition:** users who signed up while SMTP was off keep `email_verified=false`. When an administrator later configures SMTP, the Hub requires those accounts to verify via `/verify-email` on their next request — no data migration is required. Administrators remain exempt at sign-in, though an unverified address still cannot receive recovery links.
{{< /callout >}}

### Solo and dev extras (worker-scoped)

`solo` and `dev` embed a Worker and read only `solo.yaml` or `dev.yaml`. These keys live in that Hub-family file but configure the **bundled Worker**. Hub mode rejects them.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `encryption_mode` | `--encryption-mode` | `post-quantum` | End-to-end encryption (E2EE) mode for the bundled Worker: `classic` or `post-quantum`. |
| `use_login_shell` | `--use-login-shell` | `true` | Wrap the bundled Worker's agent invocation in the user's login shell. |
| `max_incomplete_chunked` | `--max-incomplete-chunked` | `0` | Maximum in-flight chunked sequences per channel for the bundled Worker (`0` = 4 default). |
| `agent_startup_concurrency` | `--agent-startup-concurrency` | `0` | Maximum agents the bundled Worker restarts simultaneously (`0` = 4, or CPU count when lower). |

{{< callout type="info" >}}
`max_incomplete_chunked` caps the Worker's chunk-reassembly budget; peers exceeding it receive `RESOURCE_EXHAUSTED`. Hub mode has no equivalent key because the Hub admits only one in-flight chunked sequence per channel and direction.
{{< /callout >}}

## Worker configuration

Environment variable prefix: `LEAPMUX_WORKER_`. A Worker dials out to a Hub and serves no HTTP. Each key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `db_max_conns` → `--db-max-conns`). See [Managing Workers](/docs/admin/managing-workers/) for registration and pin management.

### Worker options

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `name` | `--name` | *(empty → hostname)* | Worker display name; defaults to the OS hostname when empty. |
| `data_dir` | `--data-dir` | `.` | Data directory; relative paths resolve against the config directory. |
| `log_level` | `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `encryption_mode` | `--encryption-mode` | `post-quantum` | E2EE mode: `classic` or `post-quantum`. |
| `use_login_shell` | `--use-login-shell` | `true` | Wrap agent invocation in the user's login shell. |

{{< callout type="info" >}}
The Hub URL (`--hub`) and registration key (`--registration-key`) are not config keys or environment variables. Both are flags of `leapmux daemon worker register` only, and the Worker saves the Hub URL in `state.json`. See [Register a Worker](/docs/admin/managing-workers/#register-a-worker).
{{< /callout >}}

### Timeout and limit options

All timeout values accept duration units; see [Duration values](/docs/admin/configuration/#duration-values).

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `max_incomplete_chunked` | `--max-incomplete-chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `max_message_size` | `--max-message-size` | `0` | Maximum application payload size in bytes (`0` = 16 MiB default). Negotiated per channel as `min(hub, worker)` with 64 KiB of envelope headroom on top. |
| `agent_startup_timeout` | `--agent-startup-timeout` | `5m` | Agent startup timeout (`0` = default). |
| `api_timeout` | `--api-timeout` | `10s` | JSON-RPC request timeout (`0` = default). |
| `agent_startup_concurrency` | `--agent-startup-concurrency` | `0` | How many agents the Worker brings back simultaneously after a restart (`0` = 4, or CPU count when lower). See [Agents after a Worker restart](/docs/admin/configuration/#agents-after-a-worker-restart). |

### SQLite database options

Each Worker keeps a local SQLite database (`<data_dir>/worker.db`) for agent and terminal state.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `db_max_conns` | `--db-max-conns` | `4` | Maximum open database connections. |
| `db_cache_size` | `--db-cache-size` | `0` | SQLite page cache size (positive = pages, negative = KiB, e.g. `-65536` = 64 MiB; `0` = default). |
| `db_mmap_size` | `--db-mmap-size` | `0` | SQLite memory-mapped I/O size in bytes (`0` = disabled). |

### Worker state file

`leapmux daemon worker register` saves the Worker's identity to `<data_dir>/state.json` (mode `0600`): Hub URL, Worker ID, Hub auth token, and private E2EE keypairs.

{{< callout type="warning" >}}
`state.json` holds the Worker's private E2EE keys and Hub auth token in **unencrypted** JSON. Protect and back it up as a secret; losing it requires re-registration with a new registration key and Worker ID.
{{< /callout >}}

### Encryption mode

`encryption_mode` (`--encryption-mode`, `LEAPMUX_WORKER_ENCRYPTION_MODE`) chooses the Frontend↔Worker handshake mode:

| Value | Notes |
| --- | --- |
| `post-quantum` | Default. Aliases `pq`, `post_quantum`, and empty string map here. |
| `classic` | Static Module-Lattice-Based Key-Encapsulation Mechanism (ML-KEM) authentication without the Stateless Hash-Based Digital Signature Algorithm (SLH-DSA) signature or post-quantum forward secrecy. |

Any unrecognized value falls back to `post-quantum` (fail-safe). See [Cryptography & Key Architecture](/docs/admin/cryptography/) for protocol details.

## Storage backends

Select the Hub database with `storage.type` (`--storage-type`). Every storage CLI flag is `--` followed by the dotted key with dots and underscores replaced by hyphens (for example, `storage.sqlite.path` → `--storage-sqlite-path`, `storage.postgres.max_conns` → `--storage-postgres-max-conns`).

An unknown `storage.type` fails at startup with:

```text
unsupported storage.type: "<type>" (valid: sqlite, postgres, mysql, cockroachdb, yugabytedb, tidb)
```

{{< callout type="info" >}}
Configure `storage.*` through the YAML file or CLI flags rather than environment variables; see [Environment variable mapping](/docs/admin/configuration/#environment-variable-mapping).
{{< /callout >}}

### SQLite (default)

SQLite opens with write-ahead log (WAL) journaling, a 60-second busy timeout, foreign keys enabled, and file mode `0600`. Expect `hub.db-wal` and `hub.db-shm` sidecars while the Hub runs.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `storage.sqlite.path` | `--storage-sqlite-path` | `{data_dir}/hub.db` | SQLite database file path. |
| `storage.sqlite.max_conns` | `--storage-sqlite-max-conns` | `4` | Maximum open connections. |
| `storage.sqlite.cache_size` | `--storage-sqlite-cache-size` | `0` | Page cache size (positive = pages, negative = KiB, e.g. `-65536` = 64 MiB; `0` = SQLite default ≈ 2 MiB). |
| `storage.sqlite.mmap_size` | `--storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (`0` = disabled). |

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

All three backends share the PostgreSQL driver, config layout, and defaults. Replace `<name>` with `postgres`, `cockroachdb`, or `yugabytedb` (`storage.<name>.*` / `--storage-<name>-*`). Duration fields accept unit suffixes; see [Duration values](/docs/admin/configuration/#duration-values).

| Config key (`storage.<name>.*`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection URL. |
| `max_conns` | `25` | Maximum open connections. |
| `min_conns` | `5` | Minimum pool connections kept alive. |
| `conn_max_lifetime` | `1h` | Connection maximum lifetime. |
| `max_conn_idle_time` | `5m` | Maximum idle time per connection. |
| `health_check_period` | `30s` | Pool health-check interval. |

DSN formats:

- **PostgreSQL:** `postgres://user:password@host:5432/dbname?sslmode=disable`
- **CockroachDB:** `postgresql://root@host:26257/defaultdb?sslmode=disable`
- **YugabyteDB:** `postgresql://yugabyte@host:5433/yugabyte?sslmode=disable`

```yaml
# hub.yaml — PostgreSQL backend
storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
    max_conns: 25
    min_conns: 5
    conn_max_lifetime: 1h
    max_conn_idle_time: 5m
    health_check_period: 30s
```

{{< callout >}}
Use `sslmode=require` (or stronger) for any networked database; reserve `sslmode=disable` for local testing.
{{< /callout >}}

### MySQL and TiDB

Both backends share the MySQL driver, config layout, and defaults. Replace `<name>` with `mysql` or `tidb` (`storage.<name>.*` / `--storage-<name>-*`). Duration fields accept unit suffixes; see [Duration values](/docs/admin/configuration/#duration-values).

| Config key (`storage.<name>.*`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection string (`go-sql-driver` DSN). |
| `max_conns` | `25` | Maximum open connections. |
| `max_idle_conns` | `5` | Maximum idle connections. |
| `conn_max_lifetime` | `1h` | Connection maximum lifetime. |
| `conn_max_idle_time` | `5m` | Maximum idle time per connection. |

DSN formats:

- **MySQL:** `user:password@tcp(host:3306)/dbname?parseTime=true`
- **TiDB:** `root@tcp(host:4000)/leapmux?parseTime=true`

```yaml
# hub.yaml — MySQL backend
storage:
  type: mysql
  mysql:
    dsn: "leapmux:secret@tcp(db.internal:3306)/leapmux?parseTime=true"
    max_conns: 25
    max_idle_conns: 5
    conn_max_lifetime: 1h
    conn_max_idle_time: 5m
```

{{< callout type="warning" >}}
MySQL and TiDB DSNs **must** include `parseTime=true`, or time columns fail to decode. For TiDB, the store enables foreign-key support best-effort on connect.
{{< /callout >}}

## Prometheus metrics

The Hub always exposes Prometheus metrics at `/metrics`.

### Outbound queue pool metrics

Labelled by `pool="relay"`, `pool="worker"`, and `pool="userevents"`:

| Metric | Meaning |
| --- | --- |
| `leapmux_sendq_pool_capacity_bytes` | Resolved pool byte budget. |
| `leapmux_sendq_pool_used_bytes` | Currently queued bytes. Sustained occupancy near capacity means the Hub is evicting connections. |
| `leapmux_sendq_pool_members` | Active connections drawing from the pool. |
| `leapmux_sendq_pool_evictions_total` | Connections disconnected to reclaim pool memory. |
| `leapmux_sendq_pool_overcommits_total` | Guaranteed working sets granted without room in the pool. Sustained growth means the budget is too small for the connection count. |
| `leapmux_sendq_giveups_total{reason}` | Disconnects by cause: `over_budget`, `pool_pressure`, `stall`, `write_timeout`, `write_error`. |

### Connection and admission metrics

| Metric | Meaning |
| --- | --- |
| `leapmux_connections_refused_total{reason="too_many_connections"}` | Connections refused at `max_connections_per_user`. |
| `leapmux_connections_refused_total{reason="credential"}` | Connections refused because the credential expired or was revoked before serving. |
| `leapmux_worker_admissions_refused_total{stage}` | Workers refused at `max_workers_per_user` (`stage="register"` or `stage="connect"`). |
| `leapmux_userevents_frames_dropped_total{phase,bound}` | User-event frames a subscriber could not accept. `bound="frames"` = client fell too far behind; `bound="bytes"` = shared budget full (transient); `bound="capacity"` = single frame larger than the whole budget (raise `userevents_bytes`). `phase="park"` triggers a snapshot; `phase="live"` triggers a reconnect; `phase="bootstrap"` refuses the connect (clients retry on `bytes` and stop on `capacity`). |

## Environment variable quick reference

| Prefix | Applies to | Mapping rule |
| --- | --- | --- |
| `LEAPMUX_HUB_` | `hub`, `solo`, `dev` | Strip prefix and lowercase remainder (`LEAPMUX_HUB_LOG_LEVEL` → `log_level`). |
| `LEAPMUX_WORKER_` | `worker` | Strip prefix and lowercase remainder (`LEAPMUX_WORKER_NAME` → `name`). |

Underscores are **not** converted to dots, so nested `storage.*` keys require YAML or CLI flags.

## Related chapters

- [Configuration Guide](/docs/admin/configuration/) — precedence, deployment profiles, listen addresses, and operational behavior.
- [Encryption & Data](/docs/admin/encryption-and-data/) — encryption key ring, rotation, and backups.
- [Admin CLI](/docs/admin/admin-cli/) — `leapmux admin settings`, `captcha`, and `rate-limit`.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated command and flag cheat-sheet.
