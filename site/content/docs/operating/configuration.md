---
title: "Configuration"
description: "The complete configuration reference for the LeapMux Hub and Worker: how settings resolve, every config key and default, storage backends, and encryption mode."
type: docs
weight: 2
---

This chapter is the complete reference for configuring the LeapMux **Hub** and **Worker** services: how settings are layered and resolved, where config files live, every config key with its default and meaning, the supported storage backends, listen-address formats, encryption mode, and the timeout/limit knobs.

If you are looking for *how to launch* each mode (solo, hub, worker, dev) and what each is for, see [Running LeapMux](/docs/operating/running-leapmux/). For key management, encryption at rest, and database operations, see [Encryption & Data](/docs/operating/encryption-and-data/).

> **Note:** Everything here applies to the long-running daemons. `solo` and `dev` modes reuse the Hub's configuration loader with a restricted flag set; the differences are called out where relevant. The desktop app and the `control`/`admin` CLIs are configured separately — see [Running LeapMux](/docs/operating/running-leapmux/), [Remote Control CLI](/docs/operating/control-cli/), and [Admin CLI](/docs/operating/admin-cli/).

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

A value that cannot be read, or one past roughly 292 years (the largest duration LeapMux can represent), fails at startup naming the key, rather than silently becoming a duration nobody meant.

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

All behavioral configuration — auth policy, SMTP, timeouts, limits, queue budgets, bot protection — lives in the Hub's database, in one `hub_settings` table managed by the admin CLI:

```bash
leapmux admin settings list
leapmux admin settings set signup_enabled true
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"from_address":"hub@example.com"}'
leapmux admin settings set-secret smtp '{"password":"..."}'
leapmux admin settings get smtp
leapmux admin settings reset signup_enabled
```

A value is a JSON document; fields it omits keep their current (or default) values, so `{"port":465}` retunes one SMTP field without restating the host. Secret-bearing fields (the SMTP password, captcha signing keys) live in an encrypted half of the row and are written through `set-secret`; they never appear in `list` or `get` output. A key with no stored row sits at its built-in default — `reset` returns a key to exactly that.

Changes fall into two classes, shown by `settings list`:

- **hot** — a running Hub applies the new value within ~30 seconds (this is also the convergence bound between Hub instances sharing one database).
- **restart** — the value feeds startup-time arithmetic (queue pool floors, frame ceilings), so it is read once at boot; a change prints `applies after a hub restart`.

| Setting key | Shape | Default | Class |
| --- | --- | --- | --- |
| `signup_enabled` | boolean | `false` | hot |
| `email_verification_required` | boolean | `false` | hot (requires `smtp` first; the write is refused otherwise) |
| `session_duration_seconds` | integer | `604800` (7d) | hot (minimum 300) |
| `secure_cookies` | boolean | `false` | hot (changes the cookie name, so it signs everyone out) |
| `public_url` | string | *(empty)* | hot (scheme+host only, no path) |
| `smtp` | `{host, port, username, from_address, tls_mode}` + secret `{password}` | disabled | hot |
| `timeouts` | `{api_seconds, agent_startup_seconds, worktree_create_seconds}` | 10 / 300 / 60 | hot |
| `limits` | `{max_connections_per_user, max_workers_per_user}` | 32 / 64 | hot (`0` = unlimited) |
| `max_message_size_bytes` | integer | `16777216` (16 MiB) | restart (64 KiB–64 MiB) |
| `queue_budget` | `{relay_bytes, worker_bytes, userevents_bytes}` | `0` = auto-size | restart |
| `captcha.*`, `rate_limit.*` | see below | see below | hot |


See [Accounts & Authentication](/docs/using/accounts/) for the sign-up/verification flows, and [Authentication Providers](/docs/operating/authentication-providers/) for OAuth/OIDC.

### Bot protection (captcha & rate limits)

Captcha and rate-limit settings are instance settings (the `captcha.*` and `rate_limit.*` keys above), changed at runtime via the admin CLI:

```bash
leapmux admin captcha show
leapmux admin captcha set --algorithm PBKDF2/SHA-256 --cost 10000
leapmux admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux admin rate-limit list
```

Out of the box, with no configuration at all: captcha is **enabled** with the built-in ALTCHA provider at `PBKDF2/SHA-256` cost `10000` (challenges expire after 20 minutes), and `change-password` is limited to 5 failed attempts per 15 minutes per user. Google reCAPTCHA v3 and Cloudflare Turnstile are one `--provider` switch away (see the admin CLI chapter). Solo mode enforces neither. Two knobs deserve a warning before you turn them:

- **Captcha cost** is the per-derivation iteration count, and the browser performs ~256 derivations per solve — total work scales as ~256 × cost. Raising it multiplies bot cost and your users' wait time equally, so large values mostly punish humans.
- The challenge-issuing endpoint is itself unauthenticated and costs the Hub one HMAC per challenge (the solver does the expensive side), so issuing challenges stays cheap even at high costs.
- **Browsers only solve challenges in a secure context** (HTTPS, or localhost): the ALTCHA solvers need WebCrypto, and the external providers' scripts refuse unusual origins. A hub reached over plain HTTP from another machine cannot present a solvable captcha — put TLS in front (reverse proxy), switch to an external provider behind TLS, or disable captcha for such deployments. The honeypot check works everywhere.
- **External providers verify online and uncapped**: every login/signup attempt with a non-empty token makes one siteverify call to Google or Cloudflare with no per-client throttle, so scripted garbage tokens can spend the operator's siteverify quota. Disabling captcha removes that egress but leaves the unauthenticated procedures limited only by Argon2 cost and the honeypot. The built-in ALTCHA provider has no egress and self-throttles through the client-side proof-of-work.

See the [`captcha`](/docs/operating/admin-cli/#captcha--bot-protection-altcha-recaptcha-v3-turnstile) and [`rate-limit`](/docs/operating/admin-cli/#rate-limit--per-user-operation-limits) admin CLI chapters for the full flag reference.

### Outbound queue memory

Every long-lived stream the Hub writes to — one per browser tab on `/ws/channel`, one per connected Worker, one per browser tab on `/ws/userevents` — buffers outbound frames, so that one slow reader cannot stall the goroutine serving everyone else. The three fields of the `queue_budget` setting limit how much those buffers may hold. They are totals rather than per-connection figures because a per-connection number tells you nothing without a connection count, and nothing limits how many browser tabs a fleet has open.

**Each kind of connection has its own budget.** When a budget runs out it is that budget's own connections that pay, so a browser tab's backlog can only ever cost another connection of the same kind. That matters because the three failures are not comparable:

- A dropped **channel relay** reconnects and re-establishes an encrypted session per channel.
- A dropped **Worker** discards every user's channels on that machine, and the Frontend-to-Worker direction has no replay at all.
- A dropped **user-event subscriber** reconnects and resumes from its cursor, replaying only what it missed. Before its first frame is even on the wire it is not disconnected at all — it receives a full snapshot instead of a delta.

Separate budgets make the blast radius structural rather than a matter of which connection happened to be largest at the time.

Left at `0` (the default), each budget is a share of the memory the process may use — four thirty-seconds for channel relays, two for Workers, two for user events, together a quarter — never below what one largest frame of its kind needs, and never above 8 GiB. Channel relays get the largest share because they carry the bulk direction: terminal output is one frame per PTY read, where a Worker stream carries keystrokes, RPCs and control frames.

User events gets an equal share to Workers despite carrying the least traffic, because throughput is not what limits it. Its ongoing frames are the smallest in the Hub and are held once however many tabs are waiting on them, but the frame that *opens* a connection carries an account's whole visible state and is unique to that tab — so a Hub restart, when every tab reconnects at once, is the case that sizes this budget rather than the traffic that follows. The memory figure comes from the first of these that applies:

1. `GOMEMLIMIT`, when set. Setting it is the most direct way to state the budget, and it is what the Go runtime holds the heap to anyway.
2. The cgroup memory limit, on Linux. This is what makes the default correct in a container, where the machine's physical memory is the host's and can be two orders of magnitude larger than what the container may use. The Hub resolves its own cgroup and takes the tightest limit on the chain up to the root, so a limit set on a parent — a `systemd` unit's `MemoryMax=`, say — binds as it should rather than being missed.
3. Total physical memory.
4. 512 MiB, if none of the above can be read. This is a guess rather than a probe — a platform with no memory-limit API, or a container without `/proc` mounted — and it says so in the log as `source=fallback`. Seeing that on a large host means the budgets are a fraction of a number the Hub could not determine, and it is the one case where you should set the fields explicitly (`leapmux admin settings set queue_budget`, then restart).

The basis and all three resolved figures are logged at startup, the basis once:

```
outbound queue memory budgets basis="8.0 GiB (source=gomemlimit)" relay="1.0 GiB (source=auto, 4/32 of basis)" worker="512.0 MiB (source=auto, 2/32 of basis)" userevents="512.0 MiB (source=auto, 2/32 of basis)"
```

A budget you set explicitly says so in place of its share: `relay="1.0 GiB (source=config)"`.

On Linux, when the cgroup limit could not be read at all — so the basis may be the host's memory rather than the limit that actually binds this process — a warning follows that line, once:

```
cgroup memory limit could not be read; outbound queue budgets may be sized off a figure that does not bind this process error="open /custom/inner/memory.max: permission denied"
```

That warning is the one signal separating a confined machine the probe could not read from a genuinely unconfined one; both otherwise report `source=physical`.

Every budget also has to be able to hold one largest frame of its kind, and enough guaranteed per-connection working sets for the sharing rule to have anything to decide — a frame bigger than the whole budget could never be admitted at any occupancy, and a budget the size of a single working set caps every connection at that figure regardless of how idle the Hub is. Those two together are the minimum, and the Hub refuses to start on a configured value below it rather than failing at runtime. It is derived per class, not a flat number, so the shares still hold on a small host: a 512 MiB container gets its quarter rather than having the floor quietly claim 40% of the machine. Set the keys explicitly whenever your fleet's shape differs from the assumption, such as many Workers and few tabs, or the reverse. The metrics below tell you which budget is actually binding.

Note that `max_message_size_bytes` does **not** move any of these minimums. That key limits the reassembled application message the two endpoints rebuild; the Hub is a relay on that path and never holds one, so its queues only ever carry individual encrypted chunks.

**How a budget is shared.** A connection's share is not a fixed slice. Each one may queue up to whatever is still free, so a single backed-up connection on an otherwise-idle Hub can use up to half the budget, while many backed-up connections settle at an even split with a reserve still held back for connections that have not arrived yet. Each connection is also guaranteed a small working set of its own, so a connection whose queue is near empty keeps being served while others are backed up.

Frames that the Hub sends to several connections at once — a user-event broadcast reaches every tab the user has open — are held once and counted once, however many queues are waiting to send them. `leapmux_sendq_pool_used_bytes` is therefore memory the process is really holding, not a sum inflated by fan-out.

**When a budget runs out**, what happens depends on what the cheapest recovery is:

- For channel relays and Workers, the Hub disconnects that budget's largest holder — not whichever connection happened to send next — and the peer reconnects. A connection dropped this way is reported as `pool_pressure` rather than `over_budget`.
- For user events the subscriber that cannot fit a frame drops it and resynchronises, rather than taking a peer down: its own recovery is already the cheapest outcome available. Those drops are counted by `leapmux_userevents_frames_dropped_total`.
- A user-event **connect** that arrives when the budget cannot hold its opening frame is closed with a retry-later status rather than served. The frame that opens such a connection carries the account's whole visible state, so a reconnect storm is exactly when the Hub would otherwise hold one per tab at once; the browser retries, and the connect succeeds as soon as there is room.

**What the numbers do and do not bound.** They bound queued frames, and — for user events — the opening frame of each connection while it is being written. What they do not cover is the moment that frame is being *built*, before there is a size to charge: tabs reconnecting together really do build their snapshots at the same time, so a reconnect storm on large accounts peaks above the budget for as long as that takes.

**A frame does not cost its wire size.** How much it costs differs by pool, so sizing a budget from observed network traffic will under-provision it:

| Pool | Charged per frame |
| --- | --- |
| `relay` | the ciphertext, plus 256 bytes of framing |
| `worker` | the encoded message, plus 256 bytes of framing |
| `userevents` | **twice** the encoded size, and no framing term |

User events are charged double because the queue holds the decoded message tree alongside the bytes to be written, and both are live until the frame is sent. `leapmux_sendq_pool_used_bytes` reports what is charged, which is what the process is really holding — so it is the figure to size against, and it will read about twice the traffic you can see on the wire for that pool.

Resident memory can exceed a budget, and by how much depends on the connection count. Up to roughly one connection per megabyte of budget, the overshoot is bounded and twice the sum is a fair provisioning figure. Past that the per-connection guaranteed working set stops shrinking, and the promised floors alone grow with the number of connections — a Hub can then hold several times a budget without any single connection misbehaving. `leapmux_sendq_pool_overcommits_total` counts exactly that: it increments whenever a working set was granted that the budget had no room for, so sustained growth means the deployment has more connections of that kind than its budget can honour, and the fix is to raise the budget (or run more Hubs), not to wait for a reclaim that will not come.

What keeps that from growing without limit is [`max_connections_per_user`](#connections-per-user): the count these budgets are exposed to is at most your user count times that cap, rather than whatever a client decides to open.

**Metrics.** `/metrics` exposes, labelled `pool="relay"`, `pool="worker"` and `pool="userevents"`:

| Metric | Meaning |
| --- | --- |
| `leapmux_sendq_pool_capacity_bytes` | The resolved budget. |
| `leapmux_sendq_pool_used_bytes` | Currently queued. Sustained occupancy near capacity means the Hub is shedding connections to stay inside it. |
| `leapmux_sendq_pool_members` | Connections drawing from the budget. |
| `leapmux_sendq_pool_evictions_total` | Connections disconnected to reclaim memory. |
| `leapmux_sendq_pool_overcommits_total` | Times a guaranteed working set was granted without room for it. Sustained growth means the budget is too small for its connection count — raise it. |
| `leapmux_sendq_giveups_total{reason}` | Disconnects by cause: `over_budget` (that peer's own backlog), `pool_pressure` (the budget was full), `stall`, `write_timeout`, `write_error`. |

And, unlabelled by pool:

| Metric | Meaning |
| --- | --- |
| `leapmux_connections_refused_total{reason="too_many_connections"}` | Connections refused because a user was at [`max_connections_per_user`](#connections-per-user). Steady growth is either a client leaking sockets or a cap below the way your users actually work. |
| `leapmux_connections_refused_total{reason="credential"}` | Connections refused because the credential expired or was revoked between authenticating and being served. |
| `leapmux_userevents_frames_dropped_total{phase,bound}` | User-event frames a subscriber could not take. `bound="frames"` means the client was too far behind; `bound="bytes"` means the shared budget was full at that moment, which is the deployment's to fix; `bound="capacity"` means the frame was larger than the whole budget, which no occupancy would have admitted — only raising the budget clears it. `phase="park"` costs a snapshot instead of a delta, `phase="live"` costs a reconnect, and `phase="bootstrap"` refused the connect — with `bound="bytes"` the client retries, with `bound="capacity"` it is told to stop. |

### Solo and dev extras (worker-scoped)

`solo` and `dev` embed a Worker, but `solo.yaml` / `dev.yaml` is the only config file they read. These keys therefore live in the Hub-family config file yet configure the **bundled Worker**, not the Hub. They are rejected by `leapmux hub`, which has no Worker to configure.

| Config key | Default | Meaning |
| --- | --- | --- |
| `encryption_mode` | `post-quantum` | E2EE mode for the bundled Worker: `classic` or `post-quantum`. See [Encryption mode](#encryption-mode). |
| `use_login_shell` | `true` | Wrap the bundled Worker's agent invocation in the user's login shell. |
| `max_incomplete_chunked` | `0` | Maximum in-flight chunked sequences per channel for the bundled Worker (`0` = 4 default). |

> **Note:** `max_incomplete_chunked` caps the bundled Worker's chunk-reassembly budget; a peer that exceeds it gets `RESOURCE_EXHAUSTED`. There is no Hub-side equivalent — the Hub admits only one in-flight chunked sequence per channel and direction, which is a stricter rule than any count, so the key is meaningless on `leapmux hub`. The standalone Worker sets the same limit through its own `max_incomplete_chunked` key (see [Worker configuration reference](#worker-configuration-reference)).

### Connections per user

`max_connections_per_user` bounds how many long-lived connections one account may hold at once. It is a guard against a client that has started leaking sockets, and against one account crowding out the rest — not a quota anyone should meet in normal use.

It exists because the queue budgets above cannot bound themselves. Those are shared by *class* of connection, and each connection in a class is guaranteed a small working set it cannot be refused. Guarantee that to an unlimited number of connections and the total grows without limit, however carefully the budget was sized. This is the number that stops it.

**It counts sockets, not tabs, and an active browser tab holds two** — one for live updates, and one more once it opens its first terminal or agent. Everything authenticated as the same account draws on the one allowance: browser tabs, the desktop app, and any `leapmux control` CLI session. At the default of `32` that is roughly sixteen active tabs alongside a CLI session or two.

**Tunnels are cheaper than they look.** All of a machine's tunnels to the same Worker share one encrypted channel, and the individual forwarded connections inside them share it too, so what counts is how many *distinct Workers* you hold tunnels to — not how many tunnels, and not how much traffic they carry. Twenty tunnels to one Worker cost one connection; one tunnel each to three Workers costs three.

> **Note:** in solo and desktop mode *everything* authenticates as the single local user, so all of the above shares one allowance. It is generous enough that this is unlikely to bite, but it is the first key to raise if it does — and because it is the mode where it binds soonest, `solo` and `dev` document it in `leapmux admin settings list`. The queue budgets size themselves from the machine; `settings set queue_budget '{"relay_bytes":...}'` covers the rare case that is wrong.

When a user is at the limit the **newest** connection is refused and everything already open keeps working. Nothing is evicted: the alternative moves the failure to a window the user is not looking at, and a connection dropped to make room for a new one would be dropped again on the next reconnect. In the browser the refused tab says so and stops retrying — for either socket, so opening a terminal while at the limit explains itself rather than failing as a generic connection error. Closing another tab and reloading is all that is needed. Set the key to `0` to turn the cap off entirely.

Refusals are counted in `leapmux_connections_refused_total{reason="too_many_connections"}` and logged with the user id and the limit, so a client that is leaking connections is distinguishable from a cap set too low for the way your users actually work.

### Workers per user

`max_workers_per_user` is the same bound for the third pool. A Worker's Connect stream draws on the Worker queue budget and is guaranteed the same working set the Hub may not reclaim — but it is not a *lease*, so the connection cap above does not see it, and registration keys carry no quota of their own. Without this key, one account could register Workers until the Worker pool had promised more guaranteed memory than it has.

The limit applies twice, to two different populations, because neither one alone is the number that matters. **Registering** a Worker past the limit is refused, which is where an operator gets an error they can act on. **Connecting** past it is refused as well, which is where the pool member is actually created.

Both are needed because a registered Worker and a live pool member are not the same thing. The registration count sees only *active* Workers, while a Connect stream is served for any Worker that has not been deleted — including one that is deregistering, which keeps its stream because that stream is how the Hub tells it to stop. Counting registrations alone, an account could cycle register and deregister to accumulate pool members without ever exceeding the limit.

Either refusal returns a `resource_exhausted` error naming the key, is counted in `leapmux_worker_admissions_refused_total` (labelled `stage="register"` or `stage="connect"`), and is logged with the owner and the limit. The default of `64` is far past what an account plausibly runs; it exists so the pool whose eviction costs the most — dropping a Worker takes every user's channels on that machine — cannot be oversubscribed.

### Idle connections

Both long-lived sockets are probed every 30 seconds. Every other bound on them fires on a *write*, so a peer that stops receiving without sending a close frame — a suspended laptop, a dropped mobile link, a middlebox that forgot the flow — would otherwise hold its connections until the operating system's own keepalive gave up, which is on the order of ten minutes and behind some proxies is never. Those connections count against `max_connections_per_user` for the whole time, and closing a tab does not release a socket that is already gone.

Probes only ever *establish* liveness; they cannot be used to fake it. Each one carries an identifier the Hub is waiting for, and a reply that does not match an outstanding probe is ignored — so a client cannot keep a dead connection alive by replaying or pre-sending acknowledgements.

The reverse direction is bounded too. A WebSocket peer may send its own pings, and the answer is a frame the Hub has to write, so an unbounded ping rate would be a cheap way to make the Hub do expensive work on the same lock its real traffic needs. Inbound control frames are therefore rate-limited per connection — two per second sustained, with a burst allowance far above anything a working client produces — and a connection that keeps flooding past that is closed. Nothing an ordinary client does comes close: a browser answers one probe every thirty seconds.

### When one account's state outgrows the budget

The frame that opens a `/ws/userevents` connection carries the account's whole visible state, so a large enough account can produce one bigger than the entire user-events budget. Such a frame cannot be admitted at *any* occupancy, so there is nothing to wait for: the Hub closes the connection as final rather than asking the client to retry, the browser says the workspace exceeds the server's limit, and the Hub logs the frame size and the budget at error level.

Raising the `userevents_bytes` field of `queue_budget` is the fix. It is worth distinguishing from ordinary pressure, which looks similar from the client: a *merely full* budget produces a retry-later close and resolves itself as other connections drain. The `bound` label separates them outright — `leapmux_userevents_frames_dropped_total{phase="bootstrap",bound="bytes"}` is the transient one, and `bound="capacity"` is the permanent one. Do not read occupancy to tell them apart: a frame larger than the whole budget is refused on an *empty* pool just as readily, so `leapmux_sendq_pool_used_bytes` near capacity is evidence for the transient case and its absence is no evidence at all.

### Keys with no CLI flag

`encryption_key_path` (env `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`, default `{data_dir}/encryption.key`) can only be set through the YAML file or an env var — there is no command-line flag. It names the encryption key ring file the Hub needs before it can read the encrypted halves of its settings rows.

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

> **Note:** `registration_key` is required on first run and is never persisted to disk. On subsequent runs you simply omit it — the saved credentials are reused. Do **not** pass it again to an already-registered Worker: that fails with `worker is already registered; remove --registration-key or wipe local state to re-register` (the key is rejected, not silently ignored, to keep you from accidentally burning it on a machine that is already configured). For the registration flow and the exact error messages, see [Managing Workers](/docs/operating/managing-workers/).

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
leapmux admin settings set public_url "https://hub.example.com"
leapmux admin settings set secure_cookies true
leapmux admin settings set signup_enabled true
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"username":"leapmux@example.com","from_address":"no-reply@example.com","tls_mode":"starttls"}'
leapmux admin settings set-secret smtp '{"password":"..."}'   # or read from your secret manager
leapmux admin settings set email_verification_required true   # requires the smtp key above
leapmux admin settings set session_duration_seconds 86400     # sign an idle user out after a day
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
