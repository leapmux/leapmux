---
title: "Configuration Reference"
description: "Complete reference of LeapMux Hub and Worker YAML keys, environment variables, CLI flags, database instance settings, storage backends, and all 21 Prometheus metrics."
type: docs
weight: 5
---

This page is the complete key-by-key reference for the LeapMux **Hub** and **Worker** daemons. For configuration precedence, deployment profiles, listen-address rules, and operational behavior, see [Configuration Guide](/docs/admin/configuration/).

## Hub configuration

Environment variable prefix: `LEAPMUX_HUB_` (used by `hub`, `solo`, and `dev`). Each flat key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `log_level` → `--log-level`).

### Server options

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `listen` | `--listen` (repeatable) | *(empty = platform default)* | Bind set: a YAML list of TCP addresses (`:4327`, `127.0.0.1:4327`) and local IPC URLs (`unix:<path>`, `npipe:<name>`). LeapMux adds the default local IPC URL when none is given. |
| `data_dir` | `--data-dir` | `.` | Data directory; relative paths resolve against the config directory. |
| `dev_frontend` | `--dev-frontend` | *(empty)* | Frontend dev-server URL for local development. |
| `log_level` | `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` (case-insensitive). |

These four keys, the `storage` block below, and `encryption_key_path` make up the entire Hub startup configuration surface. All behavioral policy lives in the Hub database.

### Keys with no CLI flag

| Config key | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `encryption_key_path` | `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | `{data_dir}/encryption.key` | Path to the encryption key ring and token pepper file. No CLI flag exists. |

### Derived paths and behaviors

- **SQLite DB path:** `storage.sqlite.path` if set; otherwise `{data_dir}/hub.db` (`{data_dir}/hub/hub.db` in `solo` and `dev`).
- **Encryption key file:** `encryption_key_path` if set; otherwise `{data_dir}/encryption.key` (`{data_dir}/hub/encryption.key` in `solo` and `dev`).
- **Base URL:** `public_url` if set; otherwise derived from `listen` (`https` only when `secure_cookies` is `true`). Wildcard binds (`:4327`, `0.0.0.0:4327`, and `[::]:4327`) all resolve the host to `localhost`.
- **Metrics:** The Hub always exposes an unauthenticated Prometheus endpoint at `/metrics` with no flag to disable or relocate it. Block `/metrics` at your reverse proxy and scrape `http://127.0.0.1:4327/metrics` locally on the Hub host; see [Reverse Proxy](/docs/admin/reverse-proxy/).

### Instance settings (database)

Behavioral settings live in the Hub database and are managed with `leapmux admin settings` or **Preferences → Administration**. Values are JSON documents; omitted fields keep their current or default values. Secret-bearing fields (SMTP password, captcha secrets) are written with `set-secret` and never returned by `list` or `get`.

Propagation classes:

- **`hot`** — RPC writes (`leapmux admin settings` and the Web UI) apply **immediately** on commit with no restart. (A 30-second cache TTL applies only to out-of-band SQL edits. A singleton runtime lease permits only one serving Hub per database.)
- **`restart`** — read at boot; applies after restarting the Hub.

| Setting key | JSON shape | Default | Class & validation rules |
| --- | --- | --- | --- |
| `signup_enabled` | boolean | `false` (`true` in `dev`) | `hot` |
| `session_duration_seconds` | integer | `604800` (7d) | `hot` (`300` to `315360000` / 10 years) |
| `secure_cookies` | boolean | `false` | `hot` (enabling switches to `__Host-` cookies and signs out plain-cookie sessions; disabling keeps existing `__Host-` sessions valid) |
| `public_url` | string | *(empty)* | `hot` (scheme + host [+ port] only; no path, query, or fragment) |
| `trusted_proxy_ranges` | JSON array of IP selectors or provider presets | `[]` | `hot` (max `32` entries) |
| `extra_listen_addresses` | `{"addresses": [...]}` | `{"addresses": []}` | `hot` (solo mode only; max `8` addresses; no hostnames or port `0`) |
| `smtp` | `{"host","port","username","from_address","tls_mode"}` + secret `{"password"}` | disabled | `hot` (configuring both `host` and `from_address` enables email verification for new non-admin sign-ups, account recovery, and Worker registration emails) |
| `timeouts` | `{"api_seconds","agent_startup_seconds"}` | `10` / `300` | `hot` (`agent_startup_seconds` applies only to the bundled Worker in `solo`/`dev` when that Worker starts; standalone Workers use `--agent-startup-timeout`) |
| `limits` | `{"max_connections_per_user","max_workers_per_user"}` | `32` / `64` | `hot` (`max_connections_per_user`: `0` = unlimited or `>= 4`, values `1`–`3` are rejected; `max_workers_per_user`: `0` = unlimited or `>= 1`) |
| `mail_limits` | `{"failure_cooldown_seconds","recipient_max","recipient_window_seconds"}` | `10` / `10` / `3600` | `hot` (`failure_cooldown_seconds`: `0`–`60`; `recipient_max`: `0`–`1000`, `0` = unlimited; `recipient_window_seconds`: `60`–`86400`) |
| `max_message_size_bytes` | integer | `16777216` (16 MiB) | `restart` (`65519` bytes to `67108864` / 64 MiB) |
| `queue_budget` | `{"relay_bytes","worker_bytes","userevents_bytes"}` | `0` = auto-size | `restart` (`0` auto-sizes from memory limit, capped at 8 GiB per pool; non-zero values must meet the per-pool floor) |
| `open_app_registration` | boolean | `false` | `hot` |
| `captcha.*` | `captcha.enabled`, `captcha.selected`, `captcha.altcha`, `captcha.recaptcha_v3`, `captcha.turnstile` | ALTCHA enabled, `PBKDF2/SHA-256`, cost `10000`, 20m expiry | `hot` |
| `rate_limit.*` | `elevation`, `email_change`, `login_anonymous`, `oauth_anonymous`, `solo_password_setup` | `5/15m`, `6/15m`, `10/15m`, `600/10m`, `10/15m` | `hot` (`max_attempts`: `1`–`1000`; `window_seconds`: `60`–`86400`; `solo_password_setup` is active in solo mode) |

{{< callout type="info" >}}
**SMTP-enable transition:** When an administrator configures `smtp` on an existing Hub, non-admin accounts that signed up earlier with `email_verified=false` are prompted to verify via `/verify-email` on their next request — no migration needed. Administrators stay exempt at sign-in, though unverified addresses cannot receive recovery links.
{{< /callout >}}

#### Solo-mode setting scope

Solo mode omits multi-user settings that do not apply (`signup_enabled`, `smtp`, `captcha.*`, `rate_limit.elevation`, `rate_limit.email_change`, and `mail_limits`) from `settings list` and the Preferences dialog.

The following settings remain **active in solo mode**: `public_url`, `open_app_registration`, `session_duration_seconds`, `secure_cookies`, `trusted_proxy_ranges`, `extra_listen_addresses`, `timeouts`, `limits`, `max_message_size_bytes`, `queue_budget`, `rate_limit.login_anonymous`, `rate_limit.oauth_anonymous`, and `rate_limit.solo_password_setup`.

### Solo and dev extras (bundled Worker)

`solo` and `dev` modes embed a Worker and read these keys from `solo.yaml` or `dev.yaml`. **Hub mode rejects these CLI flags**, while unused extra YAML keys in `hub.yaml` are silently ignored.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `encryption_mode` | `--encryption-mode` | `post-quantum` | E2EE mode for the bundled Worker: `classic` or `post-quantum`. |
| `use_login_shell` | `--use-login-shell` | `true` | Wrap the bundled Worker's agent invocation in the user's login shell. |
| `max_incomplete_chunked` | `--max-incomplete-chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `agent_startup_concurrency` | `--agent-startup-concurrency` | `0` | Maximum agents restarted simultaneously after a restart (`0` = 4, or CPU count when lower). |

## Worker configuration

Environment variable prefix: `LEAPMUX_WORKER_`. Each key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `db_max_conns` → `--db-max-conns`). `--hub` and `--registration-key` exist only on `leapmux daemon worker register`; the Worker persists its Hub URL and credentials in `<data_dir>/state.json` (mode `0600`).

### Worker options

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `name` | `--name` | *(empty → hostname)* | Worker display name. |
| `data_dir` | `--data-dir` | `.` | Data directory; relative paths resolve against the config directory. |
| `log_level` | `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error`. |
| `encryption_mode` | `--encryption-mode` | `post-quantum` | E2EE handshake mode: `post-quantum` (`pq`, `post_quantum`, empty, or unrecognized values fail safe to `post-quantum`) or `classic`. See [Cryptography & Key Architecture](/docs/admin/cryptography/). |
| `use_login_shell` | `--use-login-shell` | `true` | Wrap agent invocation in your login shell. |

### Timeout and limit options

All timeout options accept [Duration values](/docs/admin/configuration/#duration-values).

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `max_incomplete_chunked` | `--max-incomplete-chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `max_message_size` | `--max-message-size` | `0` | Maximum application payload size in bytes (`0` = 16 MiB default; valid range `65519`–`67108864`). Negotiated per channel as `min(hub, worker)` plus 64 KiB of envelope headroom. |
| `agent_startup_timeout` | `--agent-startup-timeout` | `5m` | Agent startup timeout (`0` = default). |
| `api_timeout` | `--api-timeout` | `10s` | JSON-RPC request timeout (`0` = default). |
| `agent_startup_concurrency` | `--agent-startup-concurrency` | `0` | Simultaneous agent restarts on Worker recovery (`0` = 4, or CPU count when lower). |

### Worker SQLite options

Each Worker stores agent and terminal state in `<data_dir>/worker.db`.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `db_max_conns` | `--db-max-conns` | `4` | Maximum open SQLite connections. |
| `db_cache_size` | `--db-cache-size` | `0` | SQLite page cache size (positive = pages, negative = KiB, e.g. `-65536` = 64 MiB; `0` = default). |
| `db_mmap_size` | `--db-mmap-size` | `0` | SQLite memory-mapped I/O size in bytes (`0` = disabled). |

## Storage backends

Select the Hub database with `storage.type` (`--storage-type`). Every storage CLI flag replaces dots and underscores with hyphens (for example, `storage.postgres.max_conns` → `--storage-postgres-max-conns`). Configure `storage.*` through YAML or CLI flags rather than environment variables.

### SQLite (default)

SQLite opens with write-ahead log (WAL) journaling, foreign keys enabled, file mode `0600`, and `busy_timeout` set to `math.MaxInt32` milliseconds (~24.8 days — effectively an indefinite wait so concurrent writers serialize instead of failing with `SQLITE_BUSY`). Expect `hub.db-wal` and `hub.db-shm` sidecars while running.

| Config key | CLI flag | Default | Meaning |
| --- | --- | --- | --- |
| `storage.sqlite.path` | `--storage-sqlite-path` | `{data_dir}/hub.db` | SQLite database file path. |
| `storage.sqlite.max_conns` | `--storage-sqlite-max-conns` | `4` | Maximum open connections. |
| `storage.sqlite.cache_size` | `--storage-sqlite-cache-size` | `0` | Page cache size (positive = pages, negative = KiB; `0` = SQLite default ≈ 2 MiB). |
| `storage.sqlite.mmap_size` | `--storage-sqlite-mmap-size` | `0` | Memory-mapped I/O size in bytes (`0` = disabled). |

### PostgreSQL, CockroachDB, and YugabyteDB

All three backends share the PostgreSQL driver, keys, and defaults (`storage.<name>.*` / `--storage-<name>-*`). Use `sslmode=require` (or stronger) outside local development.

| Config key (`storage.<name>.*`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(required)* | Connection URL (`postgres://user:pass@host:5432/db?sslmode=require`). |
| `max_conns` | `25` | Maximum open connections. |
| `min_conns` | `5` | Minimum idle connections kept alive. |
| `conn_max_lifetime` | `1h` | Connection maximum lifetime. |
| `max_conn_idle_time` | `5m` | Maximum idle time per connection. |
| `health_check_period` | `30s` | Pool health-check interval. |

### MySQL and TiDB

Both backends share the MySQL driver, keys, and defaults (`storage.<name>.*` / `--storage-<name>-*`). LeapMux automatically forces `ParseTime=true` and UTC on every MySQL and TiDB connection, so you do not need to append `parseTime=true` to the DSN.

| Config key (`storage.<name>.*`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(required)* | MySQL driver DSN (for example `leapmux:secret@tcp(db.internal:3306)/leapmux?tls=true`). |
| `max_conns` | `25` | Maximum open connections. |
| `max_idle_conns` | `5` | Maximum idle connections. |
| `conn_max_lifetime` | `1h` | Connection maximum lifetime. |
| `conn_max_idle_time` | `5m` | Maximum idle time per connection. |

{{< callout type="warning" >}}
**TiDB requirement:** TiDB ignores SQL `CHECK` constraints unless `tidb_enable_check_constraint` is enabled. Run `SET GLOBAL tidb_enable_check_constraint = ON` before running LeapMux on TiDB; LeapMux also enables foreign-key support best-effort on connect.
{{< /callout >}}

## Prometheus metrics

The Hub exposes **21** `leapmux_*` Prometheus metrics at `/metrics`. Block `/metrics` at your reverse proxy and scrape `http://127.0.0.1:4327/metrics` locally on the Hub host.

### HTTP and RPC metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `leapmux_http_requests_total` | Counter | `method`, `path`, `status` | Total HTTP requests served. |
| `leapmux_http_request_duration_seconds` | Histogram | `method`, `path` | HTTP request duration in seconds. |
| `leapmux_rpc_requests_total` | Counter | `service`, `method`, `code` | Total ConnectRPC requests served. |
| `leapmux_rpc_request_duration_seconds` | Histogram | `service`, `method` | ConnectRPC request duration in seconds. |

### Runtime and WebSocket activity metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `leapmux_active_workers` | Gauge | — | Currently connected Workers. |
| `leapmux_active_agents` | Gauge | — | Currently active coding agents. |
| `leapmux_active_terminals` | Gauge | — | Currently active terminals. |
| `leapmux_captcha_verifications_total` | Counter | `provider`, `result` | Captcha verification outcomes (`altcha`, `recaptcha_v3`, `turnstile`, or `unknown` on config-store outage). |
| `leapmux_ws_connections_active` | Gauge | — | Currently open WebSocket connections. |
| `leapmux_ws_messages_total` | Counter | — | Total WebSocket messages sent. |

### User-event sync metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `leapmux_userevents_subscribe_total` | Counter | `mode`, `reason` | Total `/ws/userevents` subscribe attempts by bootstrap mode and reason (failed connects record `mode="invalid"`). |
| `leapmux_userevents_subscribe_duration_seconds` | Histogram | `mode` | Latency to resolve access, register, and build the `/ws/userevents` bootstrap frame. |
| `leapmux_userevents_frames_dropped_total` | Counter | `phase`, `bound` | User-event frames a subscriber could not accept. `bound="frames"` indicates a slow client; `bound="bytes"` indicates transient pool exhaustion; `bound="capacity"` indicates a single frame larger than `userevents_bytes` (raise `userevents_bytes`). |

### Outbound queue pool metrics

Pool gauges and counters carry `pool="relay"`, `pool="worker"`, or `pool="userevents"`.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `leapmux_sendq_pool_capacity_bytes` | Gauge | `pool` | Resolved pool byte budget. |
| `leapmux_sendq_pool_used_bytes` | Gauge | `pool` | Currently queued bytes across the pool. |
| `leapmux_sendq_pool_members` | Gauge | `pool` | Active connections drawing from the pool. |
| `leapmux_sendq_pool_evictions_total` | Counter | `pool` | Connections disconnected to reclaim pool memory. |
| `leapmux_sendq_pool_overcommits_total` | Counter | `pool` | Guaranteed per-connection floors granted without spare pool room. Sustained growth means the budget is undersized. |
| `leapmux_sendq_giveups_total` | Counter | `pool`, `reason` | Outbound queues torn down by pool (`relay`, `worker`, `userevents`, plus `worker_client` for a Worker's private outbound queue to the Hub) and cause (`over_budget`, `pool_pressure`, `stall`, `write_timeout`, `write_error`). |

### Connection and admission refusal metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `leapmux_connections_refused_total` | Counter | `reason` | Authenticated long-lived connections refused (`too_many_connections` at `max_connections_per_user`, or `credential` when expired/revoked). |
| `leapmux_worker_admissions_refused_total` | Counter | `stage` | Workers refused at `max_workers_per_user` (`stage="register"` or `stage="connect"`). |

## Related chapters

- [Configuration Guide](/docs/admin/configuration/) — precedence, deployment profiles, listen addresses, and operational defaults.
- [Reverse Proxy](/docs/admin/reverse-proxy/) — blocking `/metrics` and configuring TLS proxies.
- [Admin CLI](/docs/admin/admin-cli/) — `leapmux admin settings`, `captcha`, `rate-limit`, and `network status`.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated command and flag cheat-sheet.
