---
title: "Configuration"
description: "The complete configuration reference for the LeapMux Hub and Worker: how settings resolve, every config key and default, storage backends, and encryption mode."
type: docs
weight: 2
---

This chapter is the complete reference for configuring the LeapMux **Hub** and **Worker** services: how settings are layered and resolved, where config files live, every config key with its default and meaning, the supported storage backends, listen-address formats, encryption mode, and the timeout/limit knobs.

For *how to launch* each mode (solo, hub, worker, dev) and what each is for, see [Running LeapMux](/docs/operating/running-leapmux/). For key management, encryption at rest, and database operations, see [Encryption & Data](/docs/operating/encryption-and-data/).

> **Note:** Everything here applies to the long-running daemons. `solo` and `dev` modes reuse the Hub's configuration loader with a restricted flag set; the differences are called out where relevant. The desktop app, the `leapmux control` CLI, and the `leapmux recover` CLI are configured separately — see [Running LeapMux](/docs/operating/running-leapmux/), [Control CLI](/docs/operating/control-cli/), and [Recovery](/docs/operating/recover/).

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
| `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`   | `encryption_key_path`  |
| `LEAPMUX_HUB_LOCAL_LISTEN`          | `local_listen`         |
| `LEAPMUX_WORKER_HUB`                | `hub`                  |
| `LEAPMUX_WORKER_ENCRYPTION_MODE`   | `encryption_mode`      |

> **Warning:** Nested **storage** keys live under dotted paths (`storage.type`, `storage.postgres.dsn`, `storage.sqlite.path`, …). Because the env mapping does not convert underscores to dots, you **cannot** reliably set storage settings via simple env vars. Configure storage through the **YAML config file** or the dedicated **CLI flags** instead (for example `--storage-type postgres --storage-postgres-dsn ...`).

```bash
# Flat keys work cleanly as env vars:
export LEAPMUX_HUB_LISTEN=":8080"
export LEAPMUX_HUB_LOG_LEVEL="debug"
leapmux hub
```

## Duration values

Every config-file key that holds a duration — each database pool setting and the Worker's timeouts — takes the same spellings. (The Hub's own timeouts and its session lifetime live in the database instead, as plain seconds: the `timeouts` and `session_duration_seconds` settings.)

| Unit | Meaning | Example |
| --- | --- | --- |
| `ns`, `us`, `ms` | Nanoseconds, microseconds, milliseconds | `500ms` |
| `s`, `m`, `h` | Seconds, minutes, hours | `90s`, `30m`, `2h` |
| `d` | Days (24 hours) | `7d` |
| `w` | Weeks (7 days) | `2w` |

Parts combine, in any order: `1h30m`, `1w2d`, `2d12h`.

**A bare number is a count of seconds.** That is what these keys took before they had units, so `api_timeout: 10` still means ten seconds and an existing config file keeps working. A number only means seconds when it is the whole value — `1d30` is rejected rather than read as a day and thirty seconds.

The same spellings work everywhere a key can be set: the config file, the environment variable, and the CLI flag. `0` means "use the default" for the Worker timeouts, and "leave the database driver's own default alone" for the pool settings.

A value that cannot be read, or one past roughly 292 years (the largest duration LeapMux can represent), fails at startup with a message that specifies the key, rather than silently becoming a duration nobody meant.

```yaml
# worker.yaml — the Worker's timeouts (Hub pool settings take the
# same spellings):
api_timeout: 10s
agent_startup_timeout: 5m
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

Env prefix: `LEAPMUX_HUB_`. Defaults shown are the built-in values. Each key's CLI flag is `--` followed by the key with underscores replaced by hyphens (for example, `log_level` → `--log-level`); the only key without a flag is listed under [Keys with no CLI flag](#keys-with-no-cli-flag).

### Server options

| Config key | Default | Meaning |
| --- | --- | --- |
| `listen` | `:4327` | TCP listen address (e.g. `:4327` or `127.0.0.1:4327`). |
| `local_listen` | *(empty)* | Local IPC listen URL (`unix:<path>` or `npipe:<name>`); platform default used if empty. |
| `data_dir` | `.` | Data directory; relative paths resolve against the config dir. |
| `dev_frontend` | *(empty)* | Frontend dev-server URL for the local reverse proxy (local development). |
| `log_level` | `info` | Log level: `debug`, `info`, `warn`, `error` (case-insensitive). |

These five keys (plus the storage block below and `encryption_key_path`) are the **entire** config-file surface: they are what the process needs before the database exists — sockets to bind, where the database and key ring live, how loudly to log. Everything that describes how the running Hub *behaves* is an instance setting in the database instead (next section), because a setting in the database can change under a running Hub while a file can only change under a restarted one.

### Instance settings (database)

All behavioral configuration — auth policy, SMTP, timeouts, limits, queue budgets, bot protection — lives in the Hub's database, in one `hub_settings` table managed by `leapmux control admin settings`:

```bash
leapmux control admin settings list
leapmux control admin settings set signup_enabled true
leapmux control admin settings set smtp '{"host":"smtp.example.com","port":587,"from_address":"hub@example.com"}'
leapmux control admin settings set-secret smtp '{"password":"..."}'
leapmux control admin settings get smtp
leapmux control admin settings reset signup_enabled
```

A value is a JSON document; fields it omits keep their current (or default) values, so `{"port":465}` retunes one SMTP field without restating the host. Secret-bearing fields (the SMTP password, captcha signing keys) live in an encrypted half of the row and are written through `set-secret`; they never appear in `list` or `get` output. A key with no stored row sits at its built-in default — `reset` returns a key to exactly that.

Changes fall into two classes, shown by `settings list`:

- **hot** — a running Hub applies the new value within ~30 seconds (this is also the convergence limit between Hub instances that share one database).
- **restart** — the value feeds startup-time arithmetic (queue pool floors, frame ceilings), so it is read once at boot; a change reports that it applies after a hub restart.

| Setting key | Shape | Default | Class |
| --- | --- | --- | --- |
| `signup_enabled` | boolean | `false` | hot |
| `session_duration_seconds` | integer | `604800` (7d) | hot (minimum 300) |
| `secure_cookies` | boolean | `false` | hot (changes the cookie name, so it signs everyone out) |
| `public_url` | string | *(empty)* | hot (scheme+host only, no path) |
| `smtp` | `{host, port, username, from_address, tls_mode}` + secret `{password}` | disabled | hot |
| `timeouts` | `{api_seconds, agent_startup_seconds, worktree_create_seconds}` | 10 / 300 / 60 | hot |
| `limits` | `{max_connections_per_user, max_workers_per_user}` | 32 / 64 | hot (`0` = unlimited) |
| `max_message_size_bytes` | integer | `16777216` (16 MiB) | restart (64 KiB–64 MiB) |
| `queue_budget` | `{relay_bytes, worker_bytes, userevents_bytes}` | `0` = auto-size | restart |
| `open_app_registration` | boolean | `false` | hot |
| `captcha.*`, `rate_limit.*` | see below | see below | hot |

Solo mode omits the settings a single-user Hub has no use for, from `settings list` and from the preferences dialog alike:

| Omitted in solo | Because |
| --- | --- |
| `signup_enabled`, `smtp` | Solo has no sign-up and no outbound mail. |
| `session_duration_seconds`, `secure_cookies` | Solo has no login, so there is no session and no cookie. |
| `smtp` | Solo has nowhere to send mail. |
| `captcha.*` | Solo has no sign-up and no login to protect. |
| `rate_limit.elevation` | Keyed by USER, and solo has one. |

`public_url` stays: it sets the URL in the startup banner, and the `--hub` address you give a remote Worker. `rate_limit.oauth_anonymous` stays too, and for the reason the omissions above give: it is keyed by client ADDRESS on endpoints a solo Hub also serves. `open_app_registration` stays because a solo Hub authorizes apps like any other — see [App Authorization](/docs/operating/app-authorization/).

See [Accounts & Authentication](/docs/using/accounts/) for sign-up, passkeys, verification, and password-reset flows, and [Sign-in Providers](/docs/operating/sign-in-providers/) for OAuth/OIDC.

> **Note:** Email verification is not a separate setting. Once the `smtp` block is fully configured (`host` **and** `from_address`), the hub requires verification for new non-admin sign-ups and exposes forgot-password / worker registration email features. Removing or disabling SMTP turns verification off at runtime.
>
> **SMTP-enable transition:** users who signed up while SMTP was off keep `email_verified=false` in the database. When an operator later configures SMTP, the Hub requires those accounts to verify on the next request until they do so via `/verify-email`. You need no data migration. Administrators stay exempt at the sign-in gate, but their address is stored unverified like anybody else's — the flag records only what somebody confirmed, and an unverified address still cannot receive a password-reset link.

### Bot protection (captcha & rate limits)

Captcha and rate-limit settings are instance settings (the `captcha.*` and `rate_limit.*` keys above), changed at runtime with `leapmux control admin`:

```bash
leapmux control admin captcha show
leapmux control admin captcha set --algorithm PBKDF2/SHA-256 --cost 10000
leapmux control admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux control admin rate-limit list
```

With no configuration at all: captcha is **enabled** with the built-in ALTCHA provider at `PBKDF2/SHA-256` cost `10000` (challenges expire after 20 minutes), and `elevation` — failed attempts to verify your identity for a sensitive account change, see [Session elevation](/docs/operating/security/#session-elevation) — is limited to 5 failed attempts per 15 minutes per user. A second limit, `oauth_anonymous`, caps the authorization server's three anonymous endpoints per client address; see [App Authorization](/docs/operating/app-authorization/). Solo mode enforces no captcha and no per-user limit, but it does enforce `oauth_anonymous`. ALTCHA runs only where a browser can solve it and somebody other than you can reach the Hub — see **When ALTCHA runs** below.

Selecting Google reCAPTCHA v3 or Cloudflare Turnstile needs its site key and its secret, because the Hub refuses a selected provider whose key pair is incomplete. Pass both in the same `captcha set` invocation, as the example above does, or store them first and select the provider after. The Preferences dialog's Bot Protection panel shows every provider's key fields at all times for the same reason: an operator fills a provider in, then switches to it.

A few caveats before you change defaults:

- **Captcha cost** is the per-derivation iteration count, and the browser performs ~256 derivations per solve — total work scales as ~256 × cost. Raising it multiplies bot cost and your users' wait time equally, so large values mostly punish humans. The challenge-issuing endpoint is itself unauthenticated and costs the Hub one HMAC per challenge (the solver does the expensive side), so issuance stays cheap even at high costs.
- **External providers verify online and uncapped**: every login/signup attempt with a non-empty token makes one siteverify call to Google or Cloudflare with no per-client throttle, so scripted garbage tokens can spend the operator's siteverify quota. Disabling captcha removes that egress but leaves the unauthenticated procedures limited only by Argon2 cost and the honeypot. The built-in ALTCHA provider has no egress and self-throttles through the client-side proof-of-work.

**When ALTCHA runs.** The Hub requires the built-in provider only where it can both work and matter. Two conditions, and both must hold:

1. **A browser can run the widget.** ALTCHA's solver uses WebCrypto (`SubtleCrypto`), which browsers supply only in a secure context: HTTPS, or plain HTTP on localhost / `127.0.0.1` / `*.localhost`.
2. **Somebody other than you can reach the Hub.** ALTCHA counters automated sign-up and sign-in abuse. A Hub published at a loopback address, or published nowhere at all, has no such audience, so the proof of work costs your own sign-in and buys nothing.

The Hub checks both conditions against its own configuration, reading two settings in order:

1. `public_url`, when set. This is the browser-facing URL you published, and the setting to use behind a TLS-terminating reverse proxy. ALTCHA runs when that URL is a secure context and its host is not loopback.
2. `secure_cookies`, when `public_url` is unset. It means the Hub itself is served over HTTPS, and a Hub with a certificate is a Hub somebody reaches.

With neither setting the Hub serves plain HTTP, so an **unpublished Hub runs no ALTCHA** — including the first-run setup form. When ALTCHA is off, the Hub issues no challenge, and sign-in and sign-up skip verification, but the Hub does not write `captcha.enabled`: the stored setting keeps its value, so publishing the Hub restores protection with no admin re-enable. The honeypot check still runs. The gate never restricts reCAPTCHA v3 or Turnstile, which both work on plain HTTP pages.

**Set `public_url` on a Hub that browsers really reach by a LAN address or a hostname.** It is a recommendation, not a requirement, and rung 2 above says why: a Hub with `secure_cookies` set and no `public_url` already runs ALTCHA. What `public_url` adds is the deployment where the Hub itself speaks plain HTTP behind a TLS-terminating reverse proxy — there `secure_cookies` describes the Hub's own listener rather than the browser's page, and the published URL is the only setting that states what the browser sees. Set it to the URL your users type, and serve that URL over HTTPS. `public_url` is already what mail links, the CLI login endpoints, and passkey sign-in need on such a deployment. Publish a plain-HTTP LAN URL and the Hub keeps ALTCHA down by itself, because no browser there could solve a challenge.

**How you find out that the Hub disabled ALTCHA.** The Hub disables it silently to the browser — a message there would tell a bot which control is off — so it reports the change to its own log instead. When the stored `captcha.enabled` is `true` and the gate disables the widget, the Hub logs once at `WARN`:

```text
WARN captcha is enabled in the settings but verifies nothing reason="the selected provider needs a secure browser context, and this hub publishes no secure address" remedy="set public_url to the https address that your users type"
```

It logs on the transition, not per request, so a busy Hub prints one line rather than one per sign-in. Set `public_url` (or `secure_cookies`) and the Hub logs `captcha enforcement restored: this hub publishes a secure browser address` at `INFO`. So a Hub whose settings read `captcha.enabled = true` while the browser shows no widget has one place to look for the reason.

See [Captcha](/docs/operating/admin-cli/#captcha) and [Rate limits](/docs/operating/admin-cli/#rate-limits) in the Admin CLI chapter for the full flag reference.

### Outbound queue memory

Every long-lived stream the Hub writes to — one per browser tab on `/ws/channel`, one per connected Worker, one per browser tab on `/ws/userevents` — buffers outbound frames, so that one slow reader cannot stall the others. The three fields of the `queue_budget` setting limit how much memory those buffers may hold, as one budget per connection kind: **channel relays**, **Worker streams**, and **user-event subscribers**. The split is deliberate, because the cheapest recovery differs by kind — a dropped relay or Worker connection simply reconnects, while a user-event subscriber can resynchronize from its cursor instead of being disconnected at all — so a backlog on one kind never costs the others.

Left at `0` (the default), each budget is a share of the memory the process may use — four thirty-seconds for channel relays, two for Workers, two for user events, together a quarter, and never above 8 GiB. Channel relays get the largest share because they carry the bulk of the traffic. The Hub derives that memory figure from `GOMEMLIMIT` when set, otherwise the cgroup memory limit on Linux (which is what makes the default correct inside a container), otherwise total physical memory, and 512 MiB when none of these can be read. The basis and all three resolved figures are logged at startup, so a wrong basis is visible; a Linux host whose cgroup limit could not be read also logs a one-time warning.

Set the fields explicitly whenever your fleet's shape differs from that assumption — many Workers and few tabs, or the reverse:

```bash
leapmux control admin settings set queue_budget '{"relay_bytes":1073741824}'
```

Three facts matter when you pick numbers:

- **Each budget has a floor.** It must be able to hold one largest frame of its kind plus a small guaranteed working set per connection, and the Hub refuses to start on a configured value below it rather than failing at runtime. `max_message_size_bytes` does **not** move this floor: the Hub is a relay on that path and its queues only ever carry individual encrypted chunks, never a reassembled message.
- **A frame does not cost its wire size.** A user-event frame is charged at twice its encoded size, so size a budget against `leapmux_sendq_pool_used_bytes` — what the process really holds — not against observed network traffic.
- **What the budgets do not cover.** They limit *queued* frames, not the moment the Hub builds a frame, so a reconnect storm on large accounts peaks above the budget for as long as those snapshots take to build.

**When a budget runs out**, the Hub disconnects that budget's largest holder and the peer reconnects, reported as `pool_pressure`. User-event subscribers instead drop the frame they cannot fit and resynchronize; those drops are counted by `leapmux_userevents_frames_dropped_total`. The one *permanent* failure is an account whose opening user-events frame — the snapshot of its whole visible state — is larger than the entire `userevents_bytes` budget: such a connect is refused as final, the browser reports that the workspace exceeds the server's limit, and the fix is to raise that field. On the dropped-frames metric, `bound="capacity"` marks exactly this case, while a merely full budget is the transient `bound="bytes"` one that resolves itself as connections drain.

`leapmux_sendq_pool_overcommits_total` increments whenever the Hub granted a per-connection working set the budget had no room for. Sustained growth means the deployment has more connections of that kind than its budget can honour — raise the budget (or run more Hubs). What keeps the connection count from growing without limit in the first place is [`max_connections_per_user`](#connections-per-user).

**Metrics.** `/metrics` exposes, labelled `pool="relay"`, `pool="worker"` and `pool="userevents"`:

| Metric | Meaning |
| --- | --- |
| `leapmux_sendq_pool_capacity_bytes` | The resolved budget. |
| `leapmux_sendq_pool_used_bytes` | Currently queued. Sustained occupancy near capacity means the Hub sheds connections to stay inside it. |
| `leapmux_sendq_pool_members` | Connections drawing from the budget. |
| `leapmux_sendq_pool_evictions_total` | Connections disconnected to reclaim memory. |
| `leapmux_sendq_pool_overcommits_total` | Times a guaranteed working set was granted without room for it. Sustained growth means the budget is too small for its connection count — raise it. |
| `leapmux_sendq_giveups_total{reason}` | Disconnects by cause: `over_budget` (that peer's own backlog), `pool_pressure` (the budget was full), `stall`, `write_timeout`, `write_error`. |

And, unlabelled by pool:

| Metric | Meaning |
| --- | --- |
| `leapmux_connections_refused_total{reason="too_many_connections"}` | Connections refused because a user was at [`max_connections_per_user`](#connections-per-user). Steady growth is either a client leaking sockets or a cap below the way your users actually work. |
| `leapmux_connections_refused_total{reason="credential"}` | Connections refused because the credential expired or was revoked between authenticating and being served. |
| `leapmux_userevents_frames_dropped_total{phase,bound}` | User-event frames a subscriber could not take. `bound="frames"` means the client was too far behind; `bound="bytes"` means the shared budget was full at that moment, which is the deployment's to fix; `bound="capacity"` means the frame was larger than the whole budget, which no occupancy would have admitted — only raising the budget clears it. `phase="park"` costs a snapshot instead of a delta, `phase="live"` costs a reconnect, and `phase="bootstrap"` refused the connect — with `bound="bytes"` the client retries, with `bound="capacity"` the Hub tells it to stop. |

### Solo and dev extras (worker-scoped)

`solo` and `dev` embed a Worker, but `solo.yaml` / `dev.yaml` is the only config file they read. These keys therefore live in the Hub-family config file yet configure the **bundled Worker**, not the Hub. They are rejected by `leapmux hub`, which has no Worker to configure.

| Config key | Default | Meaning |
| --- | --- | --- |
| `encryption_mode` | `post-quantum` | E2EE mode for the bundled Worker: `classic` or `post-quantum`. See [Encryption mode](#encryption-mode). |
| `use_login_shell` | `true` | Wrap the bundled Worker's agent invocation in the user's login shell. |
| `max_incomplete_chunked` | `0` | Maximum in-flight chunked sequences per channel for the bundled Worker (`0` = 4 default). |

> **Note:** `max_incomplete_chunked` caps the bundled Worker's chunk-reassembly budget; a peer that exceeds it gets `RESOURCE_EXHAUSTED`. There is no Hub-side equivalent — the Hub admits only one in-flight chunked sequence per channel and direction, which is a stricter rule than any count, so the key is meaningless on `leapmux hub`. The standalone Worker sets the same limit through its own `max_incomplete_chunked` key (see [Worker configuration reference](#worker-configuration-reference)).

### Connections per user

`max_connections_per_user` (default `32`) caps how many long-lived connections one account may hold at once — a guard against a client that leaks sockets, or one account crowding out the rest. It is not a quota anyone should meet in normal use; it exists because the queue budgets above guarantee every connection a small working set, so an unlimited connection count would add up to unlimited memory.

**It counts sockets, not tabs, and an active browser tab holds two** — one for live updates, and one more once it opens its first terminal or agent. Everything authenticated as the same account draws on the one allowance: browser tabs, the desktop app, and any `leapmux control` CLI session. At the default of `32` that is roughly sixteen active tabs alongside a CLI session or two. Tunnels are cheaper than they look — all of a machine's tunnels to one Worker share a single connection, so what counts is how many *distinct Workers* you hold tunnels to.

When a user is at the limit the Hub refuses the **newest** connection and everything already open keeps working; the refused tab says so and stops retrying, so the remedy is to close another tab and reload. Set the key to `0` to turn the cap off entirely. Refusals are counted in `leapmux_connections_refused_total{reason="too_many_connections"}` and logged with the user id and the limit — steady growth is either a client leaking sockets or a cap set below the way your users actually work.

> **Note:** In solo and desktop mode *everything* authenticates as the single local user, so all of the above shares one allowance. It is generous enough that this is unlikely to bite, but it is the first key to raise if it does.

### Workers per user

`max_workers_per_user` (default `64`) is the same limit for the Worker pool: a Worker's Connect stream draws on the Worker queue budget and carries the same guaranteed working set, but it is not a *lease*, so the connection cap does not see it. The limit applies at both **registration** and **connection** — a registered Worker and a live pool member are not the same thing, and counting only registrations would let an account cycle register and deregister to accumulate pool members. Either refusal returns a `resource_exhausted` error that names the key; the Hub counts it in `leapmux_worker_admissions_refused_total` (labelled `stage="register"` or `stage="connect"`) and logs it with the owner and the limit. The default is far past what an account plausibly runs; it exists so the pool whose eviction costs the most — dropping a Worker takes every user's channels on that machine — cannot be oversubscribed.

### Idle connections

Both long-lived sockets are probed every 30 seconds. Without the probe, a peer that stops receiving without sending a close frame — a suspended laptop, a dropped mobile link, a middlebox that forgot the flow — would hold its connections (counting against `max_connections_per_user`) until the operating system's own keepalive gave up, which is on the order of ten minutes and behind some proxies never. A probe only ever establishes liveness; a reply that does not match an outstanding probe is ignored, so a client cannot fake it. The Hub also rate-limits inbound WebSocket control frames per connection, so a client cannot make it do expensive work with a ping flood; nothing an ordinary client does comes close.

### Keys with no CLI flag

`encryption_key_path` (env `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `{data_dir}/encryption.key`) can only be set through the YAML file or an env var — there is no command-line flag. It specifies the encryption key ring file that the Hub needs before it can read the encrypted halves of its settings rows.

See [Encryption & Data](/docs/operating/encryption-and-data/) for the encryption key ring, rotation, and what is encrypted at rest.

### Derived paths and behaviors

- **SQLite DB path:** `storage.sqlite.path` if set, otherwise `{data_dir}/hub.db`.
- **Encryption key file:** `encryption_key_path` if set, otherwise `{data_dir}/encryption.key`.
- **Base URL:** the `public_url` setting if set; otherwise derived from `listen` (scheme is `https` only when the `secure_cookies` setting is true, and a bare `:port` listen resolves the host to `localhost`).
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

> **Note:** `registration_key` is required on first run and is never persisted to disk. On subsequent runs you simply omit it — the saved credentials are reused. Do **not** pass it again to an already-registered Worker: the Worker refuses the key rather than ignoring it, so you cannot burn it by accident on a machine that is already configured. For the registration flow, see [Managing Workers](/docs/operating/managing-workers/).

### Timeout and limit options

Every timeout below takes a unit suffix; see [Duration values](#duration-values).

| Config key | Default | Meaning |
| --- | --- | --- |
| `max_incomplete_chunked` | `0` | Maximum in-flight chunked sequences per channel (`0` = 4 default). |
| `max_message_size` | `0` | Maximum application payload size in bytes (`0` = 16 MiB default). Negotiated per channel as `min(hub, worker)` — the Hub's side is the `max_message_size_bytes` setting; the reassembled ceiling is this plus 64 KiB of envelope headroom. |
| `agent_startup_timeout` | `5m` | Agent startup timeout. `0` means the default. |
| `api_timeout` | `10s` | JSON-RPC request timeout. `0` means the default. |

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

The Hub stores all relational data (users, workers, sessions, workspaces, tokens, …) in a single SQL store. Select the backend with `storage.type` (flag `--storage-type`). Every storage setting's CLI flag is `--` plus the dotted key with dots and underscores replaced by hyphens — for example `storage.sqlite.path` → `--storage-sqlite-path`, `storage.postgres.max_conns` → `--storage-postgres-max-conns`. Schema migrations run automatically every time the store is opened — including normal Hub startup — so there is no manual migration step on a fresh database.

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

> **Note:** Configure storage via the YAML file or the dedicated CLI flags, not env vars (see the warning under [Environment variable mapping](#environment-variable-mapping)). For backups, key/DB interplay, and the `leapmux recover db` / `leapmux recover encryption-key` commands, see [Encryption & Data](/docs/operating/encryption-and-data/).

### SQLite (default)

SQLite is the zero-configuration default; it needs nothing beyond an optional path and tuning. LeapMux opens connections with WAL journaling, a 60-second busy timeout, and foreign keys enabled, and sets the DB file to mode `0600`. Expect `hub.db-wal` and `hub.db-shm` sidecar files while the Hub runs.

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

Every duration below takes a unit suffix; see [Duration values](#duration-values).

| Config key (under `storage.<name>`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection string (URL form). |
| `max_conns` | `25` | Maximum open connections. |
| `min_conns` | `5` | Minimum pool connections kept alive. |
| `conn_max_lifetime` | `1h` | Connection max lifetime. |
| `max_conn_idle_time` | `5m` | Max idle time per connection. |
| `health_check_period` | `30s` | Pool health-check period. |

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
    conn_max_lifetime: 1h
    max_conn_idle_time: 5m
    health_check_period: 30s
```

> **Tip:** `sslmode=disable` is fine for local testing but you should use `sslmode=require` (or stronger) for any networked database. CockroachDB and YugabyteDB use the same config block shape — just set `type: cockroachdb` / `type: yugabytedb` and fill in `storage.cockroachdb` / `storage.yugabytedb`.

### MySQL and TiDB

MySQL and TiDB share the MySQL driver, config layout, and pool defaults. Prefixes are `storage.mysql.*` / `--storage-mysql-*` and `storage.tidb.*` / `--storage-tidb-*`.

Every duration below takes a unit suffix; see [Duration values](#duration-values).

| Config key (under `storage.<name>`) | Default | Meaning |
| --- | --- | --- |
| `dsn` | *(empty, required)* | Connection string (go-sql-driver DSN). |
| `max_conns` | `25` | Maximum open connections. |
| `max_idle_conns` | `5` | Maximum idle connections. |
| `conn_max_lifetime` | `1h` | Connection max lifetime. |
| `conn_max_idle_time` | `5m` | Max idle time per connection. |

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
    conn_max_lifetime: 1h
    conn_max_idle_time: 5m
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
data_dir: /var/lib/leapmux/hub

log_level: info

storage:
  type: postgres
  postgres:
    dsn: "postgres://leapmux:secret@db.internal:5432/leapmux?sslmode=require"
```

```bash
leapmux hub --config /etc/leapmux/hub.yaml

# Behavioral settings live in the database now — set once, then they
# survive and can change under the running Hub:
leapmux control admin settings set public_url "https://hub.example.com"
leapmux control admin settings set secure_cookies true
leapmux control admin settings set signup_enabled true
leapmux control admin settings set smtp '{"host":"smtp.example.com","port":587,"username":"leapmux@example.com","from_address":"no-reply@example.com","tls_mode":"starttls"}'
leapmux control admin settings set-secret smtp '{"password":"..."}'   # or read from your secret manager
leapmux control admin settings set session_duration_seconds 86400     # sign an idle user out after a day
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

The bare `version` subcommand is a **top-level** command (`leapmux version`), not a per-mode token. Passing it inside a mode — `leapmux hub version` — is rejected as an unexpected positional argument. LeapMux rejects an unexpected positional argument, identifies it, and points you at `--help`.

## Related chapters

- [Running LeapMux](/docs/operating/running-leapmux/) — run modes, ports, data dirs, Docker, reverse proxy.
- [Encryption & Data](/docs/operating/encryption-and-data/) — encryption key ring, rotation, DB migrations, backup/restore.
- [Managing Workers](/docs/operating/managing-workers/) — registration keys, approval, Worker selection.
- [Sign-in Providers](/docs/operating/sign-in-providers/) — configuring OAuth/OIDC as an operator.
- [Security & Threat Model](/docs/operating/security/) — E2EE protocol, encryption modes, trust boundaries.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated command and flag cheat-sheet.
