---
title: "Configuration Guide"
description: "How LeapMux Hub and Worker settings resolve, deployment profiles, listen addresses, storage backend trade-offs, and operational defaults."
type: docs
weight: 4
---

This guide explains how the LeapMux **Hub** and **Worker** daemons load configuration, how to structure common deployments, how listen addresses and reverse proxies work, which storage backend to choose, and how operational defaults behave.

For the complete key-by-key reference of every YAML key, environment variable, CLI flag, storage pool option, and Prometheus metric, see [Configuration Reference](/docs/admin/configuration-reference/). For launch modes and Docker, see [Running LeapMux](/docs/admin/running-leapmux/). For encryption keys and backups, see [Encryption & Data](/docs/admin/encryption-and-data/).

## Configuration precedence

Both the Hub and the Worker load startup configuration through the same four-layer pipeline. **Later layers override earlier layers:**

1. **Built-in defaults** — compiled into the binary.
2. **YAML config file** — loaded when present; a missing default file is skipped without error.
3. **Environment variables** — prefixed `LEAPMUX_HUB_` (`hub`, `solo`, `dev`) or `LEAPMUX_WORKER_` (`worker`).
4. **Explicitly passed CLI flags** — only flags passed on the command line override lower layers. An omitted flag does not override an environment variable or config file value.

```mermaid
flowchart BT
    Defaults["1. Built-in defaults (lowest priority)"]
    YAML["2. YAML config file (hub.yaml / worker.yaml)"]
    Env["3. Environment variables (LEAPMUX_HUB_* / LEAPMUX_WORKER_*)"]
    Flags["4. Explicitly passed CLI flags (highest priority)"]
    Defaults -->|"overridden by"| YAML
    YAML -->|"overridden by"| Env
    Env -->|"overridden by"| Flags
```

## Config files and data directories

Each mode looks for an optional YAML file under `~/.config/leapmux/` (`~` expands to your home directory):

| Mode | Default config directory | Default config file |
| --- | --- | --- |
| `hub` | `~/.config/leapmux/hub` | `~/.config/leapmux/hub/hub.yaml` |
| `worker` | `~/.config/leapmux/worker` | `~/.config/leapmux/worker/worker.yaml` |
| `solo` | `~/.config/leapmux/solo` | `~/.config/leapmux/solo/solo.yaml` |
| `dev` | `~/.config/leapmux/dev` | `~/.config/leapmux/dev/dev.yaml` |

Override the file path with `--config` (accepted in any argument position) and pass the same `--config` or `--data-dir` to every command targeting that instance.

`data_dir` (`--data-dir`, default `.`) holds the SQLite database, `encryption.key`, and the local inter-process communication (IPC) socket. Absolute paths are used verbatim; relative paths resolve against the config file's directory when it exists, or the default config directory otherwise. LeapMux creates `data_dir` with mode `0750`. In `solo` and `dev` modes, the in-process Hub uses `<data_dir>/hub/` and the bundled Worker uses `<data_dir>/worker/`.

## Environment variable mapping

LeapMux strips the `LEAPMUX_HUB_` or `LEAPMUX_WORKER_` prefix and lowercases the remainder to match a flat top-level config key. It does **not** convert underscores to dots:

| Environment variable | Config key |
| --- | --- |
| `LEAPMUX_HUB_LISTEN` | `listen` (comma-delimited) |
| `LEAPMUX_HUB_LOG_LEVEL` | `log_level` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | `encryption_key_path` |
| `LEAPMUX_WORKER_NAME` | `name` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | `encryption_mode` |

Because nested `storage.*` keys use dotted paths (`storage.type`, `storage.postgres.dsn`, `storage.sqlite.path`), configure storage in the **YAML config file** or with **CLI flags** (`--storage-type`, `--storage-postgres-dsn`) rather than environment variables.

A managed service created by `leapmux daemon <mode> start` does not inherit variables exported in your shell. Pass service variables through `--environment-file` (see [Starting and Stopping](/docs/admin/starting-and-stopping/#environment-variables)), and store SMTP passwords and external captcha secrets in the Hub database with `leapmux admin settings set-secret` so they are encrypted at rest.

## Common deployment profiles

### Minimal solo

Solo mode needs no config file. To pin the data directory and log level:

```yaml
# ~/.config/leapmux/solo/solo.yaml
data_dir: ~/leapmux-data
log_level: debug
```

### Production Hub behind a reverse proxy

For the reverse proxy configuration itself, see [Reverse Proxy](/docs/admin/reverse-proxy/).

```yaml
# /etc/leapmux/hub.yaml
listen:
  - "127.0.0.1:4327"
data_dir: /var/lib/leapmux/hub
log_level: info

storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
```

```bash
# Start the Hub as a boot service.
leapmux daemon hub start --config /etc/leapmux/hub.yaml --startup=boot

# Behavioral settings live in the database and apply immediately on commit:
leapmux admin settings set public_url "https://hub.example.com"
leapmux admin settings set secure_cookies true
leapmux admin settings set trusted_proxy_ranges '["127.0.0.1", "::1"]'
leapmux admin settings set signup_enabled true
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"username":"leapmux@example.com","from_address":"no-reply@example.com","tls_mode":"starttls"}'
leapmux admin settings set-secret smtp '{"password":"..."}'
leapmux admin settings set session_duration_seconds 86400
```

### Worker connecting to a remote Hub

```yaml
# ~/.config/leapmux/worker/worker.yaml
name: "build-box-01"
encryption_mode: post-quantum
log_level: info
data_dir: ~/.config/leapmux/worker
agent_startup_concurrency: 0   # 0 = 4, or the CPU count when lower
```

```bash
# Register once using a key from the Hub's Register worker dialog:
leapmux daemon worker register --hub https://hub.example.com --registration-key "<key-from-hub-ui>"
# Start the Worker as a background service:
leapmux daemon worker start
```

### Docker deployment

Mount a persistent volume at `/data`, place your config file at `/data/<mode>/<mode>.yaml`, set `LEAPMUX_MODE` (`hub`, `worker`, `dev`, or `solo`), and publish loopback port `127.0.0.1:4327:4327` behind a reverse proxy. See [Running under Docker](/docs/admin/running-leapmux/#running-under-docker).

## Listen addresses

The Hub binds TCP addresses for browsers and remote callers, plus a local IPC endpoint for co-located clients. In **Preferences → Administration → Network access**, **TCP listeners** shows live bind status and **Local access** lists the local IPC endpoints opened at startup.

### Bind addresses (`listen`)

`listen` accepts TCP addresses (`:4327`, `127.0.0.1:4327`) and local IPC URLs (`unix:<path>` on Unix/macOS, `npipe:<name>` on Windows). In YAML, write `listen` as a list (`listen: ["127.0.0.1:4327"]`). On the CLI, repeat `--listen`; in `LEAPMUX_HUB_LISTEN`, separate entries with commas.

| Mode | Default TCP `listen` | Local IPC authentication |
| --- | --- | --- |
| `hub` | `:4327` | **Authentication required** on TCP and local IPC |
| `dev` | `:4327` | **Authentication required** on TCP and local IPC |
| `solo` | `127.0.0.1:4327` | **Credential-free on local IPC** (OS-account verified); TCP requires password setup, then sign-in |

If your `listen` list includes no local IPC entry, LeapMux appends the platform default (`unix:<data_dir>/hub.sock` on Unix/macOS, `npipe:leapmux-hub-<SID>` on Windows):

| Configured `listen` | Bound listeners |
| --- | --- |
| *(omitted)* | `<default TCP>` + `<default local IPC>` |
| `listen: [":8080"]` | `:8080` + `<default local IPC>` |
| `listen: ["unix:/tmp/hub.sock"]` | `unix:/tmp/hub.sock` only (no TCP) |
| `listen: [":8080", ":9090"]` | `:8080` + `:9090` + `<default local IPC>` |
| `listen: [":8080", "unix:/tmp/hub.sock"]` | `:8080` + `unix:/tmp/hub.sock` |

Only `solo` mode grants credential-free administrator access over local IPC. In `hub` and `dev` modes, clients connecting over local IPC still present normal authentication credentials (session cookies or bearer tokens); the local socket simply avoids TCP exposure for co-located clients and Workers. If you move the local socket away from its default path, pass `--hub unix:<path>` (or `npipe:<name>`) to `leapmux` and co-located Workers.

Each data directory also records its resolved bind set in `state.json` (so ephemeral `:0` ports are discoverable). The Hub removes its `state.json` on clean shutdown; a Worker's `state.json` stores its registration credentials and persists across restarts.

### Network access

In `solo` mode, **Preferences → Administration → Network access** lets you add up to **8** additional TCP addresses beside `--listen` (stored in the `extra_listen_addresses` database setting).

- Setting a `solo` password is required before adding network addresses. Once set, every TCP address — including `127.0.0.1` — requires signing in as `solo`; only the local IPC socket stays credential-free.
- Overlapping addresses merge into one socket (for example, adding `*:4327` supersedes `127.0.0.1:4327`).
- An address that fails to bind (such as a down VPN interface) displays its OS error without stopping the remaining listeners.

The **Additional addresses** editor is solo-only. The **Trusted reverse proxies** section below it appears in `solo`, `hub`, and `dev` modes.

{{< screenshot src="preferences-admin" alt="LeapMux Preferences Administration Network access panel showing verified session banner and Trusted reverse proxies settings" caption="Administrators configure trusted reverse proxies and live Hub settings under Preferences → Administration." width="1280" height="856" >}}

### Trusted reverse proxies

`trusted_proxy_ranges` is a hot JSON list (default `[]`, maximum **32** entries). With the default empty list, the Hub trusts no proxy — including loopback — and ignores forwarding headers. Five selector formats are accepted:

- Single IPv4 or IPv6 address: `192.0.2.10`, `2001:db8::10`
- CIDR block: `192.168.0.0/24`, `2001:db8::/64`
- Inclusive range: `192.168.0.1-192.168.0.100`, `2001:db8::1-2001:db8::ffff`
- Short IPv4 range: `192.168.0.1-100`
- Provider preset (case-insensitive): `cloudflare`, `cloudfront`

LeapMux expands provider presets from a bundled IP snapshot and does not fetch updates over the network; add manual ranges if a proxy address is missing. LeapMux rejects ports, IPv6 zones, unknown provider presets, mixed address families, reversed ranges, duplicates, overlaps, and lists exceeding 32 entries.

{{< callout type="warning" >}}
Provider presets (`cloudflare`, `cloudfront`) match shared provider infrastructure, not only your own account. Restrict origin access to your distribution or require authenticated origin requests, and configure every proxy to strip untrusted client forwarding headers.
{{< /callout >}}

Forwarding header rules:

- The Hub reads forwarding headers only when the physical TCP peer matches `trusted_proxy_ranges`.
- It prefers `Forwarded` when present; otherwise it reads `X-Forwarded-For` and `X-Forwarded-Proto`.
- It walks the address list right to left and selects the first address outside the trusted set. A malformed header or an all-trusted chain yields an unknown client IP without falling back to another header.
- The verified client IP controls IP-keyed rate limits and session IP records. A verified `https` protocol enables `secure_cookies` behavior (`__Host-` cookies and `https` base URLs) for that request (one-way; it never disables `secure_cookies`).

## Storage backends overview

The Hub stores relational metadata in one SQL backend selected by `storage.type` (`--storage-type`). Schema migrations run automatically on Hub startup and during `leapmux recover create-admin`.

{{< callout type="warning" >}}
**Singleton Hub lease:** Every LeapMux database supports **at most one running Hub process at a time**. The Hub acquires a runtime lease on startup and refuses a second concurrent Hub sharing the same database. Choose an external SQL backend for operational backup, durability, and managed hosting — not for multi-Hub horizontal scaling.
{{< /callout >}}

### Storage backend decision matrix

| Backend (`storage.type`) | Driver family | Best for | Key considerations |
| --- | --- | --- | --- |
| `sqlite` *(default)* | SQLite | Personal `solo`, `dev`, or single-host small teams | Zero external dependencies; embedded file at `{data_dir}/hub.db` in write-ahead log (WAL) mode. Stop the Hub or use SQLite online backup when copying. |
| `postgres` **(recommended external DB)** | PostgreSQL | Team and production deployments | Best-supported external backend with full `CHECK` constraint enforcement, managed backups, and TLS (`sslmode=require`). |
| `mysql` | MySQL | Teams standardized on MySQL | LeapMux forces `ParseTime=true` and UTC on the driver automatically; no manual DSN time flag is needed. |
| `cockroachdb` | PostgreSQL | Distributed SQL environments already running CockroachDB | Reuses the PostgreSQL driver and pool settings; single-Hub runtime lease still applies. |
| `yugabytedb` | PostgreSQL | Distributed SQL environments already running YugabyteDB | Reuses the PostgreSQL driver and pool settings; single-Hub runtime lease still applies. |
| `tidb` | MySQL | Teams running TiDB | Reuses the MySQL driver (time parsing forced automatically). **Requires `SET GLOBAL tidb_enable_check_constraint = ON`** — otherwise TiDB silently ignores `CHECK` constraints. |

See [Configuration Reference — Storage backends](/docs/admin/configuration-reference/#storage-backends) for DSN formats, connection pool defaults, and SQLite tuning keys.

## Operational defaults

The Hub separates configuration into two surfaces:

- **Config file and CLI flags** hold startup-only bootstrap values (`listen`, `data_dir`, `storage.*`, `encryption_key_path`, `dev_frontend`, `log_level`).
- **Database instance settings** (managed with `leapmux admin settings` or **Preferences → Administration**) hold all behavioral policy.
  - **`hot` keys** take effect **immediately** when updated through the Hub RPC (`leapmux admin settings` or the Web UI), because the Hub replaces its cached snapshot on commit. (A 30-second cache TTL applies only if a row is modified out of band directly in SQL.)
  - **`restart` keys** apply on the next Hub restart.

See [Configuration Reference — Instance settings](/docs/admin/configuration-reference/#instance-settings-database) for every key, JSON shape, default, and validation rule.

### Duration values

Config-file duration keys (database pool settings and Worker timeouts) accept compound duration units (`ns`, `us`, `ms`, `s`, `m`, `h`, `d`, `w` — for example `1h30m`, `2d12h`) or a bare number of seconds (`api_timeout: 10`). `0` selects the built-in or driver default. Database settings (`timeouts`, `session_duration_seconds`) use plain integer seconds instead.

### Bot protection (captcha and rate limits)

Captcha and rate-limit rules are `hot` database settings managed through `leapmux admin captcha` and `leapmux admin rate-limit`:

| Control | Keyed by | Default | Notes |
| --- | --- | --- | --- |
| `captcha.*` | Built-in ALTCHA | Enabled, `PBKDF2/SHA-256`, cost `10000`, 20m expiry | Runs when the browser has WebCrypto and the Hub is externally reachable (`public_url` over HTTPS or `secure_cookies=true`). |
| `rate_limit.elevation` | User | 5 failed attempts / 15m | Protects identity verification for sensitive changes. |
| `rate_limit.email_change` | User | 6 requests / 15m | Caps email-change requests reaching SMTP. |
| `mail_limits` | Recipient address | 10 delivered emails / hour; 10s failure cooldown | Folds `+`-tagged addresses onto one inbox budget. |
| `rate_limit.login_anonymous` | Verified client IP | 10 failed passwords / 15m | Active in `solo`, `dev`, and `hub` modes; a successful sign-in clears the window. |
| `rate_limit.oauth_anonymous` | Verified client IP | 600 requests / 10m | Caps anonymous OAuth 2.1 endpoints; active in `solo`, `dev`, and `hub` modes. |
| `rate_limit.solo_password_setup` | Verified client IP | 10 admitted requests / 15m | Active in `solo` mode; caps initial password-setup attempts over TCP. |

Operational notes:

- **External captcha providers** (`recaptcha_v3`, `turnstile`) require both `--site-key` and `--secret` and perform an outbound verification call on every non-empty token. Built-in ALTCHA requires no outbound network access.
- **Unpublished Hubs skip ALTCHA silently** (logging a server warning) so browsers on plain HTTP LAN addresses are not blocked, while the honeypot and Argon2id password hashing remain active. Setting an HTTPS `public_url` restores ALTCHA automatically.

### Passkeys

WebAuthn passkeys run automatically whenever the browser is in a secure context (HTTPS or loopback) and the page origin matches the Hub's published origin (`public_url` when set, otherwise the TCP listen address plus loopback spellings when bound to loopback). Updating `public_url` takes effect immediately with no restart. See [Accounts & Connected Apps](/docs/using/accounts-and-apps/) and [Troubleshooting](/docs/reference/troubleshooting/auth-and-data/#passkey-sign-in-fails).

### Outbound queue memory

Every long-lived stream — browser channels (`/ws/channel`), Worker streams (`/ws/worker`), and user-event streams (`/ws/userevents`) — draws from its own outbound buffer pool configured by `queue_budget` (`restart` class) so one slow reader cannot starve other traffic.

When left at `0` (default), the Hub sizes each pool as a fraction of its detected memory limit, capped at **8 GiB per pool** (up to 24 GiB combined across all three pools):

| Pool (`queue_budget` field) | Default share of memory limit | Auto-size cap | Recovery under pressure |
| --- | --- | --- | --- |
| Channel relays (`relay_bytes`) | `12.5%` | 8 GiB | Disconnects largest holder (`pool_pressure`); peer reconnects. |
| Worker streams (`worker_bytes`) | `6.25%` | 8 GiB | Disconnects largest holder (`pool_pressure`); Worker reconnects. |
| User events (`userevents_bytes`) | `6.25%` | 8 GiB | Drops event frame and resynchronizes from cursor or reconnects. |

The Hub detects its memory limit in order from `GOMEMLIMIT`, the Linux cgroup memory limit, total physical RAM, or a `512 MiB` fallback, and logs the resolved budgets at startup. Override any pool explicitly when needed:

```bash
leapmux admin settings set queue_budget '{"relay_bytes":1073741824}'
```

Key sizing rules:

- Each pool enforces a minimum floor large enough for one maximum frame plus per-connection working sets; smaller values are rejected.
- User-event frames are charged at twice their encoded wire size to account for resident memory overhead. Size budgets against `leapmux_sendq_pool_used_bytes`.
- If an account's opening user-events snapshot exceeds the entire `userevents_bytes` pool, the Hub refuses the connection until you raise `userevents_bytes`.
- Sustained growth in `leapmux_sendq_pool_overcommits_total` means active connections exceed guaranteed working sets — raise the pool budget or increase host memory.

### Per-user connection and Worker caps

- **`max_connections_per_user`** (default `32`, `0` = unlimited, minimum non-zero value `4`): caps concurrent long-lived sockets per user account. An active browser tab uses two sockets (`/ws/userevents` and `/ws/channel`), so the default supports roughly 16 active tabs alongside CLI sessions. Multiple desktop tunnels to one Worker share one connection. Refusals keep existing tabs open and increment `leapmux_connections_refused_total{reason="too_many_connections"}`.
- **`max_workers_per_user`** (default `64`, `0` = unlimited): caps live Worker pool membership per account at both registration (`resource_exhausted`) and stream connect (WebSocket close `1013`), counted in `leapmux_worker_admissions_refused_total`. Worker streams do not count toward `max_connections_per_user`.
- **Idle probes:** the Hub pings every browser and Worker WebSocket every 10 seconds (10-second timeout) and closes a connection after two consecutive missed pings so dead peers release slots promptly.

### Agents after a Worker restart

When a Worker restarts, it reconnects to the Hub and automatically resumes agents whose tabs are open and have run at least once (skipping idle tabs, closed tabs, and agents whose previous startup failed). `agent_startup_concurrency` (`0` = 4, or the CPU count when lower) limits simultaneous agent restarts; set it to `1` on memory-constrained hosts.

## Related chapters

- [Configuration Reference](/docs/admin/configuration-reference/) — every YAML key, CLI flag, storage option, instance setting, and Prometheus metric.
- [Production Checklist](/docs/admin/production-checklist/) — end-to-end team Hub deployment checklist.
- [Reverse Proxy](/docs/admin/reverse-proxy/) — tested configurations for Caddy, nginx, Traefik, HAProxy, and Apache.
- [Security & Authentication](/docs/admin/security-and-auth/) — trust boundaries, elevation, and solo-mode rules.
- [Encryption & Data](/docs/admin/encryption-and-data/) — key ring, rotation, and backups.
