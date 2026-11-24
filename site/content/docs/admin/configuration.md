---
title: "Configuration Guide"
description: "How LeapMux Hub and Worker settings resolve, common deployment profiles, listen addresses, storage backends overview, and operational defaults."
type: docs
weight: 4
---

This guide explains how the LeapMux **Hub** and **Worker** daemons load configuration, where files live, how to structure common deployments, and how listen addresses and operational defaults behave.

For the complete key-by-key reference of every YAML key, environment variable, CLI flag, storage backend option, and Prometheus metric, see [Configuration Reference](/docs/admin/configuration-reference/). For how to launch each run mode (`solo`, `hub`, `worker`, `dev`), see [Running LeapMux](/docs/admin/running-leapmux/). For encryption keys and database backups, see [Encryption & Data](/docs/admin/encryption-and-data/).

{{< callout type="info" >}}
Everything here applies to the long-running daemons. `solo` and `dev` modes reuse the Hub's configuration loader with a smaller flag set; differences are noted where they apply. The desktop app, the `leapmux` CLI, and `leapmux recover` are configured separately — see [Running LeapMux](/docs/admin/running-leapmux/), [Control CLI](/docs/using/control-cli/), and [Recovery](/docs/admin/recover/).
{{< /callout >}}

## Configuration precedence

Both the Hub and the Worker load configuration through the same pipeline. LeapMux applies four layers in order, and **later layers win**:

1. **Built-in defaults** — compiled into the binary.
2. **YAML config file** — loaded only when the file exists. A missing file is skipped without error.
3. **Environment variables** — prefixed `LEAPMUX_HUB_` (Hub, solo, dev) or `LEAPMUX_WORKER_` (Worker).
4. **Explicitly-set CLI flags** — only flags you pass on the command line override lower layers. A flag left at its default does not count as set and does not override an environment variable or config file value.

```mermaid
flowchart BT
    Defaults["1. Built-in defaults (lowest priority — compiled into binary)"]
    YAML["2. YAML config file (hub.yaml / worker.yaml, if present)"]
    Env["3. Environment variables (LEAPMUX_HUB_* / LEAPMUX_WORKER_*)"]
    Flags["4. Explicitly-set CLI flags (highest priority — --listen, --log-level, ...)"]
    Defaults -->|"overridden by"| YAML
    YAML -->|"overridden by"| Env
    Env -->|"overridden by"| Flags
```

{{< callout >}}
Because only *explicitly passed* flags override lower layers, you can set a baseline in `hub.yaml`, override per-environment values with `LEAPMUX_HUB_*` environment variables, and still pass a one-off `--log-level debug` for a single run.
{{< /callout >}}

## Config file locations

Each mode looks for a YAML config file in its own directory under `~/.config/leapmux/`. LeapMux expands `~` to your home directory.

| Mode | Default config directory | Default config file |
| --- | --- | --- |
| `hub` | `~/.config/leapmux/hub` | `~/.config/leapmux/hub/hub.yaml` |
| `worker` | `~/.config/leapmux/worker` | `~/.config/leapmux/worker/worker.yaml` |
| `solo` | `~/.config/leapmux/solo` | `~/.config/leapmux/solo/solo.yaml` |
| `dev` | `~/.config/leapmux/dev` | `~/.config/leapmux/dev/dev.yaml` |

Override the path with `--config` (`--config=PATH` or `--config PATH`). LeapMux scans `--config` before normal flag parsing, so it works in any argument position:

```bash
# Pass the same path to every command of the instance.
leapmux daemon hub start --config /etc/leapmux/hub.yaml
leapmux daemon hub status --config /etc/leapmux/hub.yaml
```

### Data directory resolution

The `data_dir` setting (`--data-dir`, default `.`) holds the SQLite database, the encryption key file, and the local interprocess communication (IPC) socket. LeapMux resolves it with three rules:

- `~` expands to your home directory.
- An **absolute** path is used as-is.
- A **relative** path (including `.`) resolves against the directory containing the config file if that file exists; otherwise it resolves against the default config directory (such as `~/.config/leapmux/hub`).

With no config file and no `--data-dir`, the Hub data directory is `~/.config/leapmux/hub`. LeapMux creates the directory with mode `0750` at startup.

{{< callout type="info" >}}
In `solo` and `dev` modes, LeapMux splits one `data_dir` in two: the in-process Hub uses `<data_dir>/hub`, and the in-process Worker uses `<data_dir>/worker`.
{{< /callout >}}

## Environment variable mapping

Environment variables use the prefix `LEAPMUX_HUB_` for Hub, solo, and dev modes, and `LEAPMUX_WORKER_` for Worker mode.

- LeapMux strips the prefix and lowercases the remainder to produce a flat config key.
- LeapMux does **not** convert underscores into dots.

Flat top-level keys map directly:

| Environment variable | Config key |
| --- | --- |
| `LEAPMUX_HUB_LISTEN` | `listen` (comma-delimited) |
| `LEAPMUX_HUB_LOG_LEVEL` | `log_level` |
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH` | `encryption_key_path` |
| `LEAPMUX_WORKER_NAME` | `name` |
| `LEAPMUX_WORKER_ENCRYPTION_MODE` | `encryption_mode` |

{{< callout type="warning" >}}
Nested **storage** keys live under dotted paths (`storage.type`, `storage.postgres.dsn`, `storage.sqlite.path`, …). Because environment variable mapping does not turn underscores into dots, configure storage through the **YAML config file** or dedicated **CLI flags** (such as `--storage-type postgres --storage-postgres-dsn ...`) instead.
{{< /callout >}}

A background service created by `start` does not inherit variables from your shell. Give a service its variables in an environment file:

```yaml
# /etc/leapmux/hub-environment.yaml
LEAPMUX_HUB_LISTEN: ":8080,unix:/run/leapmux/hub.sock"
LEAPMUX_HUB_LOG_LEVEL: "debug"
```

```bash
leapmux daemon hub start --environment-file /etc/leapmux/hub-environment.yaml
```

Keep secrets such as SMTP passwords and external captcha secrets out of YAML config files. Store them in the Hub database with `leapmux admin settings set-secret`, which encrypts the secret half at rest with `encryption.key` and never returns it in `list` or `get` output. Protect any environment file that holds a database password or proxy credential with owner-only permissions.

A container reads variables from its own environment:

```bash
docker run -d -p 4327:4327 -e LEAPMUX_MODE=hub -e LEAPMUX_HUB_LOG_LEVEL=debug \
  -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

## Common deployment profiles

### Minimal solo

Solo mode needs no config file — it defaults to loopback TCP and a local SQLite database. To pin the data directory and turn on debug logging:

```yaml
# ~/.config/leapmux/solo/solo.yaml
data_dir: ~/leapmux-data
log_level: debug
```

### Production Hub behind a reverse proxy

For the reverse proxy configuration itself, see [Reverse Proxy](/docs/admin/reverse-proxy/).

```yaml
# /etc/leapmux/hub.yaml
listen: "127.0.0.1:4327"      # only the reverse proxy reaches the Hub
data_dir: /var/lib/leapmux/hub

log_level: info

storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
```

```bash
# Start the Hub as a background service.
leapmux daemon hub start --config /etc/leapmux/hub.yaml

# Behavioral settings live in the database and can change while the Hub runs:
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
agent_startup_concurrency: 0   # 0 = 4, or the CPU count when that is lower
```

```bash
# Once: register with the Hub, using a key from its Register worker dialog.
leapmux daemon worker register --hub https://hub.example.com --registration-key "<key-from-hub-ui>"
# Then: start the Worker as a background service. It reads worker.yaml and
# connects to the Hub URL saved during registration.
leapmux daemon worker start
```

### Docker deployment

In Docker, mount a persistent volume at `/data` and place your mode config file at `/data/<mode>/<mode>.yaml` (for example `/data/hub/hub.yaml`). Select the mode with `LEAPMUX_MODE` (`hub`, `worker`, `dev`, or `solo`) and publish loopback port `127.0.0.1:4327:4327` when running behind a reverse proxy. See [Running under Docker](/docs/admin/running-leapmux/#running-under-docker) for full commands and Worker container registration.

## Listen addresses

LeapMux uses TCP addresses for browsers and remote callers, and local IPC endpoints for clients on the same machine. In **Preferences → Administration → Network access**, **TCP listeners** shows the live TCP bind results and **Local access** lists the local endpoints the Hub opened at startup. Both lists are read-only and appear above the **Additional addresses** editor.

### Bind addresses (`listen`)

`listen` sets every address the Hub binds at startup. Each entry is either a TCP address or a local IPC URL:

- **TCP:** `:4327` (all interfaces, port 4327) or `127.0.0.1:4327` (loopback only).
- **Local IPC (Unix/macOS):** `unix:<path>` (a Unix domain socket).
- **Local IPC (Windows):** `npipe:<name>` (a Windows named pipe).

Defaults differ by mode:

| Mode | Default TCP `listen` | Notes |
| --- | --- | --- |
| `hub` | `:4327` | All interfaces; real authentication required. |
| `dev` | `:4327` | All interfaces. |
| `solo` | `127.0.0.1:4327` | Loopback only; TCP starts with first-password setup. |

The CLI flag is repeatable (`--listen :8080 --listen unix:/run/leapmux/hub.sock`), `LEAPMUX_HUB_LISTEN` is comma-delimited, and the YAML file takes a list. Put any address containing a comma in a flag or the YAML file so it is read verbatim.

The list is the bind set: LeapMux binds what you give, and adds the platform's default local IPC URL whenever the list specifies no local entry:

| You give | Bound |
| --- | --- |
| *(nothing)* | `<default TCP>` + `<default local>` |
| `--listen :8080` | `:8080` + `<default local>` |
| `--listen unix:/tmp/x.sock` | `unix:/tmp/x.sock` alone, no TCP address |
| `--listen :8080 --listen :9090` | `:8080` + `:9090` + `<default local>` |
| `--listen :8080 --listen unix:/tmp/x` | `:8080` + `unix:/tmp/x` |

**At least one local IPC address is always bound.** `leapmux` on the Hub's host reaches it without a login over that socket, so a TCP-only bind set is impossible. Specifying your own local entry moves the socket: the default socket is not bound, and `leapmux` needs `--hub` pointed at the URL you specified.

Default local IPC URL by platform:

| Platform | Default local IPC URL |
| --- | --- |
| Unix/macOS | `unix:<data_dir>/hub.sock` |
| Windows | `npipe:leapmux-hub-<SID>` (current user's security identifier) |

A Worker's `--hub` flag accepts the same `unix:` and `npipe:` schemes for co-located setups. An invalid entry fails at startup and reports its index in the list. No database setting changes `--listen`; solo mode adds extra addresses beside it through [Network access](#network-access).

{{< callout type="warning" >}}
Before the `solo` account has a password, a TCP caller can only set the first password. The first successful caller claims the account and receives an elevated session. If you bind solo mode to a non-loopback address in that state, another machine can win this setup race. The Hub logs a warning. Set the password from a trusted connection and restrict access with a firewall or tunnel. See [Solo mode: a reduced threat model](/docs/admin/security/#solo-mode-a-reduced-threat-model).
{{< /callout >}}

### Network access

Solo mode serves additional addresses beside `--listen`. Open **Preferences → Administration → Network access**, pick an interface and port under **Additional addresses**, and select **Apply**. The Hub binds those addresses immediately and on every later start. The Hub stores them in the `extra_listen_addresses` setting, which you can also edit with `leapmux admin settings`.

Two rules apply when you add an address:

- **Every network address requires a sign-in as `solo`**, including `127.0.0.1`. The panel requires you to set that password before it publishes an address. Only the desktop app's local socket skips sign-in. See [Solo mode: a reduced threat model](/docs/admin/security/#solo-mode-a-reduced-threat-model).
- **Overlapping addresses merge.** Asking for every interface on a port `--listen` already uses causes the Hub to serve one socket (`*:4327`) rather than two, which still answers on `127.0.0.1`. The panel previews the merge before you apply, and **TCP listeners** reports each active listener and its reason.

An address that cannot be bound — such as a down VPN interface or a busy port — does not stop the Hub. It stays configured, the remaining addresses serve normally, and the panel displays the operating system error beside it.

The additional listen-address editor is solo-only. The **Trusted reverse proxies** row below it appears in solo, hub, and dev modes.

### Trusted reverse proxies

The `trusted_proxy_ranges` setting is a JSON list. It defaults to `[]`, so LeapMux trusts no proxy, including loopback. The setting is hot and accepts five selector forms:

- An IPv4 or IPv6 address: `192.0.2.10`, `2001:db8::10`
- A CIDR block: `192.168.0.0/24`, `2001:db8::/64`
- A full inclusive range: `192.168.0.1-192.168.0.100`, `2001:db8::1-2001:db8::ffff`
- A short IPv4 range: `192.168.0.1-100`
- A provider preset (case-insensitive): `cloudflare`, `cloudfront`

The Hub canonicalizes manual selectors and stores provider presets as symbolic tokens. LeapMux expands provider tokens from its bundled `realclientip-go` snapshot and does not download updates automatically. Add a manual range when the bundled snapshot omits a proxy you use.

LeapMux rejects ports, IPv6 zones, unknown provider names, mixed address families, reversed ranges, duplicates, overlaps, malformed selectors, and lists above the configured limit.

{{< callout type="warning" >}}
Provider ranges identify shared infrastructure, not your provider account. Restrict your origin to your own distribution or require authenticated origin requests. Configure every proxy to strip client-supplied forwarding headers or append only verified values; otherwise another customer on that provider can spoof client addresses.
{{< /callout >}}

How LeapMux parses forwarding headers:

- LeapMux reads forwarding headers only when the physical TCP peer matches the expanded trusted set.
- It prefers `Forwarded` when present; otherwise it reads `X-Forwarded-For` and `X-Forwarded-Proto`.
- It walks the address chain from right to left and picks the first address outside the trusted set. A malformed preferred header or a chain with no untrusted address produces an unknown client without falling back to another header.
- For `Forwarded`, LeapMux reads `proto` on the selected element. For `X-Forwarded-Proto`, one value applies globally, or an equal-length list aligns by position. Only `http` and `https` are valid; absent, empty, invalid, or misaligned protocol values become `http`.
- The verified client IP controls address-keyed rate limits and new session records. `RemoteAddr` stays the physical peer.
- A verified `https` protocol turns `secure_cookies` on for that request (issuing `__Host-` cookies and `https` base URLs) without a separate setting. This rule is one-way: it never turns `secure_cookies` off, and headers from untrusted peers are ignored.

### State file (`state.json`)

Each data directory holds a `state.json` file beside the database. LeapMux writes it atomically (temp file, then rename) with `pid` first and lists the **resolved** bind set — so `--listen 127.0.0.1:0` records the ephemeral port chosen by the operating system. The Hub deletes its copy on clean shutdown and overwrites a stale copy on the next start. A Worker's `state.json` stores its registration credentials and survives restarts.

## Storage backends overview

The Hub stores all relational data in one SQL backend selected by `storage.type` (`--storage-type`). Schema migrations run automatically on Hub startup and during first-run bootstrap; offline `leapmux recover` commands open existing databases without auto-migrating.

| `storage.type` | Driver family | Notes |
| --- | --- | --- |
| *(empty)* or `sqlite` | SQLite | Default; embedded file database at `{data_dir}/hub.db` in write-ahead log (WAL) mode. |
| `postgres` | PostgreSQL | Requires `storage.postgres.dsn`. |
| `mysql` | MySQL | Requires `storage.mysql.dsn` (must include `parseTime=true`). |
| `cockroachdb` | PostgreSQL | Reuses the PostgreSQL driver; requires `storage.cockroachdb.dsn`. |
| `yugabytedb` | PostgreSQL | Reuses the PostgreSQL driver; requires `storage.yugabytedb.dsn`. |
| `tidb` | MySQL | Reuses the MySQL driver; requires `storage.tidb.dsn` (must include `parseTime=true`). |

See [Configuration Reference — Storage backends](/docs/admin/configuration-reference/#storage-backends) for every connection-pool key, DSN example, and SQLite tuning option. See [Encryption & Data](/docs/admin/encryption-and-data/) for backups and `leapmux recover db`.

## Operational defaults

The Hub splits configuration into two surfaces:

- **Config file and CLI flags** hold only what the process needs before the database opens: listen addresses, `data_dir`, `storage.*`, `encryption_key_path`, `dev_frontend`, and `log_level`.
- **Database instance settings** (`hub_settings`, managed with `leapmux admin settings`) hold all behavioral policy: sign-up, sessions, SMTP, timeouts, per-user limits, queue budgets, app registration, captcha, and rate limits. Hot keys apply within ~30 seconds without restart; restart keys apply on the next Hub start.

See [Configuration Reference — Instance settings](/docs/admin/configuration-reference/#instance-settings-database) for the full table of keys, shapes, defaults, solo-mode omissions, and SMTP email-verification transition rules.

### Duration values

Every config-file key that holds a duration — database pool settings and Worker timeouts — uses the same syntax. (Hub timeouts and session lifetime live in the database as plain integer seconds: `timeouts` and `session_duration_seconds`.)

| Unit | Meaning | Example |
| --- | --- | --- |
| `ns`, `us`, `ms` | Nanoseconds, microseconds, milliseconds | `500ms` |
| `s`, `m`, `h` | Seconds, minutes, hours | `90s`, `30m`, `2h` |
| `d` | Days (24 hours) | `7d` |
| `w` | Weeks (7 days) | `2w` |

Parts combine in any order: `1h30m`, `1w2d`, `2d12h`.

**A bare number is a count of seconds:** `api_timeout: 10` means ten seconds. A bare number works only as the entire value — `1d30` is rejected.

The same syntax works in the YAML file, environment variables, and CLI flags. `0` means "use the default" for Worker timeouts and "use the database driver default" for pool settings. An unparseable value or a duration past ~292 years fails at startup with an error identifying the key.

```yaml
# worker.yaml — Worker timeouts (Hub pool settings use the same syntax):
api_timeout: 10s
agent_startup_timeout: 5m
```

### Bot protection (captcha & rate limits)

Captcha and rate-limit settings are database instance settings (`captcha.*` and `rate_limit.*`), updated at runtime with `leapmux admin`:

```bash
leapmux admin captcha get
leapmux admin captcha set --algorithm PBKDF2/SHA-256 --cost 10000
leapmux admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux admin rate-limit list
```

Default protection with no configuration:

| Control | Scope / key | Default | Notes |
| --- | --- | --- | --- |
| `captcha.*` | Built-in ALTCHA | Enabled, `PBKDF2/SHA-256`, cost `10000`, 20-minute challenge expiry | Runs only when a browser can solve it and the Hub is externally reachable (see below). |
| `rate_limit.elevation` | Per user | 5 failed attempts / 15 minutes | Limits failed identity checks for sensitive changes ([Session elevation](/docs/admin/authentication-security/#session-elevation)). |
| `rate_limit.email_change` | Per user | 6 requests / 15 minutes | Caps email-change requests reaching the mail relay; elevation prompts and validation refusals spend nothing. |
| `mail_limits` | Per recipient address | 10 delivered emails / hour; 10-second failure cooldown | Counts delivered mail only; folds plus-tagged addresses (`user+tag@`) onto one inbox budget. Failure cooldown never exceeds the 60-second resend cooldown. |
| `rate_limit.login_anonymous` | Verified client IP | 10 failed passwords / 15 minutes | Enforced in solo mode too; a successful sign-in clears the window. |
| `rate_limit.oauth_anonymous` | Verified client IP | 600 requests / 10 minutes | Caps anonymous OAuth endpoints ([App Authorization](/docs/admin/app-authorization/)). Enforced in solo mode too. |

The two address-keyed limits use the direct TCP peer unless `trusted_proxy_ranges` trusts a proxy (see [Trusted reverse proxies](#trusted-reverse-proxies)).

Operational caveats:

- **External providers require both keys.** Selecting Google reCAPTCHA v3 or Cloudflare Turnstile requires both a site key and a secret. Pass both in one `captcha set` command, or save them before switching providers.
- **Captcha cost scales linearly with wait time.** The browser runs ~256 derivations per solve (~256 × cost total). Issuance costs the Hub only one HMAC per challenge, so raising cost penalizes humans and bots equally.
- **External providers verify online without a per-client cap.** Every request with a non-empty token makes an outbound siteverify call to Google or Cloudflare, so scripted garbage tokens can consume your quota. Built-in ALTCHA has no outbound egress and self-throttles via client-side proof-of-work. Disabling captcha still keeps Argon2 password cost and the honeypot active.

#### When ALTCHA runs

The Hub requires built-in ALTCHA only when both conditions hold:

1. **The browser can run WebCrypto (`SubtleCrypto`).** Browsers expose WebCrypto only in a secure context: HTTPS, or plain HTTP on loopback (`localhost`, `127.0.0.1`, `*.localhost`).
2. **External users can reach the Hub.** A loopback-only or unpublished Hub has no external audience, so proof-of-work adds friction without value.

The Hub checks two settings in order:

1. `public_url` (when set): ALTCHA runs if the URL is a secure context and its host is not loopback.
2. `secure_cookies` (when `public_url` is unset): ALTCHA runs if `secure_cookies` is `true`, indicating the Hub is served over HTTPS.

With neither setting, an unpublished Hub skips ALTCHA on sign-in, sign-up, account recovery, and email verification without modifying the stored `captcha.enabled` setting. The honeypot still runs. Publishing `public_url` over HTTPS (recommended behind a TLS-terminating reverse proxy) restores ALTCHA automatically. ReCAPTCHA v3 and Turnstile are never restricted by this check because they work over plain HTTP.

Because showing a browser notice would tell bots that captcha is off, the Hub reports the state change in its server log once per transition:

```text
WARN captcha is enabled in the settings but verifies nothing reason="the selected provider needs a secure browser context, and this hub publishes no secure address" remedy="set public_url to the https address that your users type"
```

Setting `public_url` or `secure_cookies` logs `captcha enforcement restored: this hub publishes a secure browser address` at `INFO`.

See [Captcha](/docs/admin/admin-cli/#captcha) and [Rate limits](/docs/admin/admin-cli/#rate-limits) for the full flag reference.

### Passkeys

Passkeys have no separate on/off switch. A WebAuthn ceremony runs only when both parties allow it:

1. **The browser** requires a secure context (HTTPS, or plain HTTP on loopback) and WebAuthn support. Serve the Hub over HTTPS wherever users reach it by hostname.
2. **The Hub** permits ceremonies only at its published origins: `public_url` when set (scheme and host, no path), or otherwise the TCP `listen` address plus loopback spellings (`localhost`, `127.0.0.1`, `[::1]`) when listening on loopback. The match checks scheme, host, and port.

When the Hub rejects the origin (for example, a user visits a LAN IP behind a reverse proxy), the sign-in and sign-up forms hide the **Passkey** option. When the browser refuses the context, the option remains visible but disabled with the reason shown. In **Preferences → Account**, either refusal disables **Add passkey** with the reason.

Changing `public_url` (via CLI or **Preferences → Administration → General**) takes effect immediately without a restart or page reload. See [Accounts & Authentication](/docs/using/accounts/#passkeys) and [Troubleshooting](/docs/reference/troubleshooting/#passkey-sign-in-fails-or-the-authenticator-never-appears).

### Outbound queue memory

Every long-lived stream the Hub writes to — browser tabs on `/ws/channel`, connected Workers on `/ws/worker`, and browser tabs on `/ws/userevents` — buffers outbound frames so one slow reader cannot stall others. The `queue_budget` setting (`restart` class) sets three separate byte budgets so backlog in one pool never starves the others.

When left at `0` (default), the Hub allocates a quarter of its detected memory limit across the three pools (capped at 8 GiB total):

| Pool (`queue_budget` field) | Default share | Recovery on pressure |
| --- | --- | --- |
| Channel relays (`relay_bytes`) | 4/32 (12.5%) | Disconnects largest holder (`pool_pressure`); peer reconnects. |
| Worker streams (`worker_bytes`) | 2/32 (6.25%) | Disconnects largest holder (`pool_pressure`); Worker reconnects. |
| User events (`userevents_bytes`) | 2/32 (6.25%) | Drops frame and resynchronizes from cursor (`leapmux_userevents_frames_dropped_total`). |

The Hub determines its memory limit from the first available source in order:

1. `GOMEMLIMIT` (if set)
2. Linux cgroup memory limit (ensuring accurate container defaults; logs a one-time warning if unreadable)
3. Total physical memory
4. `512 MiB` fallback

The Hub logs the resolved basis and all three pool budgets at startup. Override any field explicitly when your fleet skews toward many Workers or many tabs:

```bash
leapmux admin settings set queue_budget '{"relay_bytes":1073741824}'
```

Key sizing rules:

- **Each budget has a floor.** Every pool must fit one largest frame of its kind plus a small per-connection working set; values below the floor are rejected on write. `max_message_size_bytes` does not raise the floor because the Hub relays individual encrypted chunks rather than reassembled messages.
- **Account for memory overhead.** A user-event frame is charged at twice its encoded wire size. Size budgets against `leapmux_sendq_pool_used_bytes`, not raw network bytes.
- **Transient assembly spikes are outside the budget.** Budgets limit *queued* frames, not snapshot construction during a reconnect storm.
- **Oversized initial snapshots fail permanently.** If an account's opening user-events snapshot exceeds the entire `userevents_bytes` budget, the Hub refuses the connection (`bound="capacity"` on `leapmux_userevents_frames_dropped_total`, versus transient `bound="bytes"`). Raise `userevents_bytes` to fix it.
- **Watch overcommits.** Sustained growth in `leapmux_sendq_pool_overcommits_total` means more connections exist than the budget can guarantee working sets for. Raise the budget or scale out.

See [Configuration Reference — Prometheus metrics](/docs/admin/configuration-reference/#prometheus-metrics) for the complete `/metrics` reference.

### Connections per user

`max_connections_per_user` (default `32`, `0` = unlimited) caps concurrent long-lived sockets per account so a leaking client cannot exhaust queue-pool working sets.

- **It counts sockets, not tabs.** An active browser tab holds two sockets (one for live updates and one for encrypted channels). Browser tabs, the desktop app, and `leapmux` CLI sessions share the same account allowance — roughly 16 active tabs alongside CLI sessions at the default of `32`.
- **Tunnels share one connection per Worker.** Multiple tunnels to the same Worker count as one connection.
- **Refusal keeps existing tabs alive.** At the limit, the Hub refuses the newest connection and increments `leapmux_connections_refused_total{reason="too_many_connections"}`. Close an unused tab and reload, or raise the setting.

{{< callout type="info" >}}
In solo and desktop mode, every client authenticates as the single local account and shares one allowance. Raise `max_connections_per_user` first if you regularly keep more than 16 active tabs open.
{{< /callout >}}

### Workers per user

`max_workers_per_user` (default `64`, `0` = unlimited) caps live Worker pool membership per account at both **registration** and **connection**. Counting live pool members prevents an account from bypassing the pool limit by repeatedly registering and deregistering Workers. Worker streams do not count against `max_connections_per_user`.

A registration refusal returns `resource_exhausted`. A connection refusal closes the WebSocket with status `1013` and reason `max_workers_per_user`, which the Worker logs and retries. The Hub counts both in `leapmux_worker_admissions_refused_total` (`stage="register"` or `stage="connect"`).

### Idle connections

The Hub probes every browser WebSocket and every Worker stream every 10 seconds with a 10-second timeout. Two consecutive failed probes close the connection so dead peers release `max_connections_per_user` and Worker pool slots promptly instead of waiting for OS TCP keepalives. The Hub ignores unmatched probe replies and rate-limits inbound WebSocket control frames per connection.

### Agents after a Worker restart

A Worker restart stops every agent process it hosts, then brings qualifying agents back automatically once the Worker reconnects to its Hub:

- An agent resumes its prior conversation automatically if its tab is open and the agent ran at least once before.
- Idle tabs (never sent a message), closed tabs, and agents whose last start failed are skipped.
- A Worker that cannot reach its Hub starts no agents and retries on each later reconnect.
- `agent_startup_concurrency` (`0` = 4, or the CPU count when lower) limits how many agents start simultaneously on recovery. It does not cap total running agents or delay manually started agents. Set it to `1` on constrained hosts, and close unneeded tabs so restarts do not respawn unused processes.

## Configuration of a service

A service reads the config file at its saved path on every start and does not inherit variables from the shell that started it. The service saves the runtime flags given to `start`, `restart`, or `enable`, plus the config file path. See [Change the configuration of a service](/docs/admin/starting-and-stopping/#change-the-configuration-of-a-service) to update flags, config files, or environment files.

## Help and version

- `-h`, `--help`, and `leapmux help <command>` print grouped help for any mode or subcommand.
- `leapmux --version`, `leapmux -v`, and `leapmux version` print the build version. Subcommands take no `--version` flag, and `version` is a top-level command only (`leapmux daemon hub version` fails). Unexpected positional arguments are rejected with a pointer to `--help`.

## Related chapters

- [Configuration Reference](/docs/admin/configuration-reference/) — complete key-by-key reference for Hub, Worker, storage backends, and Prometheus metrics.
- [Running LeapMux](/docs/admin/running-leapmux/) — run modes, ports, data directories, and Docker.
- [Reverse Proxy](/docs/admin/reverse-proxy/) — requirements and tested configs for nginx, Caddy, Traefik, HAProxy, and Apache httpd.
- [Encryption & Data](/docs/admin/encryption-and-data/) — encryption key ring, rotation, DB migrations, and backup/restore.
- [Managing Workers](/docs/admin/managing-workers/) — registration keys, TOFU pinning, and Worker selection.
- [Sign-in Providers](/docs/admin/sign-in-providers/) — configuring OAuth/OIDC as an administrator.
- [Security & Threat Model](/docs/admin/security/) — trust boundaries, E2EE overview, and solo-mode rules.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated command and flag cheat-sheet.
