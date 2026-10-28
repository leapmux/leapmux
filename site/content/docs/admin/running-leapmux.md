---
title: "Running LeapMux"
description: "Administrator guide to the solo, hub, worker, and dev modes: what each does, how to launch it, where it stores data, and how to run under Docker or a proxy."
type: docs
weight: 1
---

Use the `leapmux` command to run a personal workspace, a shared Hub, or a Worker that connects to a Hub. This guide covers choosing a mode, starting it, keeping its data, and making it accessible through Docker or a reverse proxy.

If you only want to try LeapMux on your own machine, the desktop app or `leapmux solo` is all you need. To set up a shared service for a team, run a `hub` plus one or more `worker` processes. For the full list of configuration keys and storage backends, see [Configuration](/docs/admin/configuration/).

## Run modes at a glance

The modes that accept inbound connections — `solo`, `hub`, and `dev` — all default to TCP port **4327**. A `worker` is the exception: it only makes outbound connections to a Hub and binds no listen port (its default `http://127.0.0.1:4327` is the Hub URL it dials, not a port it serves). Beyond the port, the modes differ in what they run, whether they require a login, and where they listen.

| Mode | What it runs | Default listen | Login required | Config / data location |
|------|--------------|----------------|----------------|------------------------|
| `solo` | Hub + Worker in one process | `127.0.0.1:4327` (loopback only), plus any address you add | Desktop: no. Network: password setup, then sign-in. | `~/.config/leapmux/solo/` |
| `hub` | Hub service only | `:4327` (all interfaces) | Yes — full authentication | `~/.config/leapmux/hub/` |
| `worker` | Worker only (connects out to a Hub) | n/a (no inbound port) | Registers with the Hub via a registration key | `~/.config/leapmux/worker/` |
| `dev` | Hub + Worker in one process | `:4327` (all interfaces) | Yes — admin bootstrapped via `/setup` | `~/.config/leapmux/dev/` |

Each mode reads a YAML config file named after the mode inside its config directory — for example `~/.config/leapmux/hub/hub.yaml` for the Hub. The file is optional; omit it if you only need defaults, flags, or environment variables. The default data directory for each mode is the same as its config directory.

{{< callout type="info" >}}
Choose `solo` for one account on your own machine. The desktop app needs no sign-in; browser access starts with password setup. Choose `dev` to run a Hub and Worker together for multiple users. It listens on all interfaces and asks you to create the first administrator at `/setup`.
{{< /callout >}}

{{< callout type="warning" >}}
While the `solo` account has no password, the first TCP caller can claim it by setting that password. Other protected RPCs stay closed. If you bind solo to a non-loopback address in that state, LeapMux logs a startup warning. The app shows only the password-setup screen. See [Security & Threat Model](/docs/admin/security/#solo-mode-a-reduced-threat-model).
{{< /callout >}}

### Reaching a solo hub from another machine

Set the `solo` account's password and add the address in **Preferences → Administration → Network access**. The Hub starts answering there at once, and again on every later start. From then on every network address asks you to sign in as `solo` — `127.0.0.1` included — while the desktop app, which reaches its Hub over a local socket, never does. [Network access](/docs/admin/configuration/#network-access) covers the panel, and [Security & Threat Model](/docs/admin/security/#solo-mode-a-reduced-threat-model) the rule.

Session settings apply to solo mode when you sign in over the network. For HTTPS access, follow [Reverse proxy and public URL](#reverse-proxy-and-public-url).

A solo hub still has no sign-up, no passkeys, no account recovery and no email: one account, one password. For a multi-user deployment, run `leapmux hub` (with separate Workers) or `leapmux dev` instead.

## Solo mode

Solo mode runs a personal Hub and Worker with no required configuration. By default, only your own machine can connect. Set the first password when you open it in a browser. The desktop app connects without a sign-in.

```bash
leapmux solo
# Then open http://127.0.0.1:4327
```

Solo mode keeps Hub data in `<data_dir>/hub/` and Worker data in `<data_dir>/worker/`. Keep both directories when moving or backing up the installation. You do not need to register the bundled Worker separately.

Use these flags to customize solo mode:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:4327` plus a local socket address | Listen address, repeatable: a TCP address or a local socket address (`unix:`, `npipe:`) |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |

For all supported flags and runtime settings, see [Configuration](/docs/admin/configuration/).

The desktop app runs solo mode without opening a network port by default. See [Installation](/docs/getting-started/installation/) for desktop setup.

## Hub mode

`leapmux hub` runs only the Hub: authentication, workspace management, Worker registration, and the encrypted relay between Frontends and Workers. It does **not** run a Worker — Workers connect separately (see [Running Workers](#running-workers)). The Hub binds `:4327` on all interfaces by default and requires real authentication.

```bash
leapmux hub -listen :4327
```

A fresh Hub has no users and (by default) sign-up disabled. Open it in a browser and every path shows the `/setup` form — the first person to register becomes the administrator (see [Accounts & Authentication](/docs/using/accounts/)). You can also create that first admin offline with [`leapmux recover bootstrap create-admin`](/docs/admin/recover/), which refuses once any admin exists. Then log in as that administrator.

Enable sign-up with `leapmux control admin settings set signup_enabled true`, or create each account with `leapmux control admin user create`. The most important Hub flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` plus a local socket address | A TCP address or local socket address. Repeat the flag to listen on multiple addresses. |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `-storage-type` | empty (= `sqlite`) | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `-log-level` | `info` | Log level |
| `-config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |

Set listening addresses and database connections at startup. Change account, email, session, timeout, and per-user limit settings on the running Hub with `leapmux control admin settings`:

```bash
leapmux control admin settings set signup_enabled true
leapmux control admin settings set smtp '{"host":"smtp.example.com","port":587,"from_address":"no-reply@example.com"}'
```

By default the Hub uses an embedded SQLite database at `<data_dir>/hub.db` with its encryption key ring at `<data_dir>/encryption.key`. For a shared, durable deployment you will usually point it at an external database via `-storage-type` and the matching `*-dsn` flag. The full reference is in [Configuration](/docs/admin/configuration/); the [Hub settings](/docs/admin/admin-cli/#hub-settings) section of the Admin CLI chapter covers the settings table.

{{< callout type="info" >}}
For HTTPS access, put a reverse proxy in front of the Hub. See [Reverse proxy and public URL](#reverse-proxy-and-public-url) below.
{{< /callout >}}

## Running Workers

Run a Worker on each machine where you want to run agents and terminals. Workers connect to the Hub without accepting inbound connections, so they need no port forwarding. Register each Worker with a one-time registration key from the Hub.

On first run, point the Worker at the Hub and supply a registration key:

```bash
leapmux worker -hub https://hub.example.com -registration-key YOUR_REGISTRATION_KEY
```

After a successful registration, the Worker saves its credentials and keypair to `<data_dir>/state.json` and reconnects automatically on every subsequent start — you do **not** pass `-registration-key` again.

| Flag | Default | Meaning |
|------|---------|---------|
| `-hub` | `http://127.0.0.1:4327` | Hub URL (`http[s]://…`, `unix:<socket-path>`, or `npipe:<pipe-name>`) |
| `-registration-key` | empty | Registration key from the Hub UI; required on first run only, never persisted |
| `-name` | hostname | Worker display name |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-use-login-shell` | `true` | Wrap agent invocation in your login shell |
| `-log-level` | `info` | Log level |
| `-config` | `~/.config/leapmux/worker/worker.yaml` | Config file path |

If a Worker has no saved credentials and no key, it refuses to start, and the error tells you to pass a registration key from the Hub UI. If you pass `-registration-key` to a Worker that is already registered, it stops rather than consuming the key by accident; the error tells you to remove `-registration-key` or to wipe the local state.

For creating registration keys, approving Workers, verifying their identities, and choosing a Worker for a tab, see [Managing Workers](/docs/admin/managing-workers/).

## Dev mode

Use `leapmux dev` to run a Hub and Worker together with multiple user accounts. It listens on all interfaces and requires sign-in. Choose this mode when you want both services in one container.

```bash
leapmux dev -listen :4327
```

Open the Hub in your browser and complete `/setup` to create the first administrator. The bundled Worker becomes available after setup.

Use these flags to customize dev mode:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` plus a local socket address | Listen address, repeatable: a TCP address or a local socket address (`unix:`, `npipe:`) |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/dev`) | Data directory |
| `-log-level` | `info` | Log level |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-config` | `~/.config/leapmux/dev/dev.yaml` | Config file path |

Like solo, dev also accepts the SQLite tuning flags plus `-dev-frontend` (see [Configuration](/docs/admin/configuration/)).

Dev mode enables sign-up by default unless you override the `signup_enabled` setting. For account and session settings, see [Configuration](/docs/admin/configuration/).

## Running under Docker

Use the images at `ghcr.io/leapmux/leapmux` to run a Hub, a Worker, or both in one container.

### Image variants and tags

| Variant | Tags | Example |
|---------|------|---------|
| Alpine (default) | `:<version>`, `:<major>`, `:latest`, `:dev` | `ghcr.io/leapmux/leapmux:latest` |
| Ubuntu | `:<version>-ubuntu`, `:<major>-ubuntu`, `:latest-ubuntu`, `:dev-ubuntu` | `ghcr.io/leapmux/leapmux:latest-ubuntu` |

Both variants support `linux/amd64` and `linux/arm64`. Use a specific `:<version>` for a predictable deployment. The `:latest` tag follows releases, and `:dev` follows changes on `main`.

### Selecting the mode

Set `LEAPMUX_MODE` to `hub`, `worker`, `dev`, or `solo`. The container requires a valid mode to start with its default command.

Configure the selected mode through `/data/<mode>/<mode>.yaml` or its supported environment variables. For example, a Hub reads `/data/hub/hub.yaml` and `LEAPMUX_HUB_*` variables. Use the YAML file for database storage settings. See [Configuration](/docs/admin/configuration/) for the supported options.

To use a different base directory instead of `/data`, set `LEAPMUX_DATA_DIR` and mount your persistent volume at that directory.

### Volume layout

Mount a named volume or host directory at `/data` to preserve configuration, databases, encryption keys, and Worker credentials when you replace the container.

| Mode | Configuration file | Default data directories |
|------|--------------------|------------------|
| `hub` | `/data/hub/hub.yaml` | `/data/hub/` |
| `worker` | `/data/worker/worker.yaml` | `/data/worker/` |
| `dev` | `/data/dev/dev.yaml` | `/data/dev/hub/` and `/data/dev/worker/` |
| `solo` | `/data/solo/solo.yaml` | `/data/solo/hub/` and `/data/solo/worker/` |

The Hub's data directory contains `hub.db` when you use SQLite, and `encryption.key` for its encryption keys. The Worker's data directory contains `worker.db` and its registration credentials in `state.json`.

Keep the volume even when the Hub uses an external database: its encryption keys still need persistent storage. See [Encryption & Data](/docs/admin/encryption-and-data/) for backup instructions.

### Exposing the port

Publish port **4327** for a Hub or a `dev` container. Choose one of these examples:

```bash
# Run a Hub with separate Workers.
docker run -d --name leapmux-hub --restart unless-stopped \
  -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest

# Run a Hub and Worker together.
docker run -d --name leapmux-dev --restart unless-stopped \
  -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

Open `http://localhost:4327` and complete `/setup` to create the first administrator. The examples publish the port on all host interfaces. For HTTPS access, follow [Reverse proxy and public URL](#reverse-proxy-and-public-url).

{{< callout type="warning" >}}
Solo mode listens on `127.0.0.1:4327` inside the container by default, so publishing its port does not make it reachable from the host. Use `dev` when you want a Hub and Worker in one container that accepts network connections and requires sign-in.
{{< /callout >}}

### Running a Worker container

Set the Hub URL and a registration key from the Hub. Workers need no published port.

```bash
docker run --name leapmux-worker -e LEAPMUX_MODE=worker \
  -e LEAPMUX_WORKER_HUB=https://hub.example.com \
  -e LEAPMUX_WORKER_REGISTRATION_KEY=YOUR_REGISTRATION_KEY \
  -v leapmux-worker-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

After registration, recreate the container with the same volume and without `LEAPMUX_WORKER_REGISTRATION_KEY`. A registered Worker refuses to start if you supply a registration key again. Keep its volume so it can reconnect with its saved credentials. See [Managing Workers](/docs/admin/managing-workers/) for registration and approval.

### Restart policy

Use `--restart unless-stopped` with `docker run`, or `restart: unless-stopped` in Compose, to restart the container automatically after an exit or a Docker restart. Containers you stop manually stay stopped. Remove a Worker's initial registration key before enabling automatic restarts.

## Reverse proxy and public URL

To serve LeapMux over HTTPS, put a reverse proxy such as nginx, Caddy, or Traefik in front of the Hub. Configure the Hub to use its public address:

1. Set `public_url` to the external HTTPS URL: `leapmux control admin settings set public_url "https://hub.example.com"`.
2. Enable secure cookies with `leapmux control admin settings set secure_cookies true`. This signs out current sessions, so configure it during initial setup.
3. Trust the proxy's address with `leapmux control admin settings set trusted_proxy_ranges '["127.0.0.1", "::1"]'`. This example trusts a proxy on the same machine over IPv4 or IPv6. Use the actual address or range for a remote proxy. The Hub ignores forwarding headers from untrusted proxies.
4. Point each Worker's `-hub` URL at the same external `https://` address. Workers always initiate outbound connections, so they need no inbound ports of their own.

The Hub applies these three settings within about 30 seconds. No restart is needed.

For the supported proxy address formats and forwarding headers, see [Configuration](/docs/admin/configuration/).

{{< callout type="warning" >}}
If you use `cloudflare` or `cloudfront` in `trusted_proxy_ranges`, those values trust the bundled ranges for the entire provider. Restrict access to your distribution or use authenticated origin requests. Configure every proxy to remove client-supplied forwarding headers or append only verified values. LeapMux does not download updated provider ranges automatically.
{{< /callout >}}

{{< callout type="warning" >}}
`public_url` must be a bare scheme + host — for example `https://hub.example.com`. Sub-path mounting (such as `https://example.com/leapmux`) is **not** supported and is rejected at write time. Give LeapMux its own hostname or subdomain.
{{< /callout >}}

The proxy must also forward WebSocket upgrades, since Frontend traffic and the relayed Worker streams ride over long-lived connections. For details about encryption and Worker identity verification, see [Security & Threat Model](/docs/admin/security/).

The proxy must carry **HTTP/2 to the Hub** as well. A Worker holds one bidirectional stream to the Hub, and HTTP/1.1 cannot carry it, so a Worker behind an HTTP/1.1-only proxy cannot connect and says so:

```
endpoint does not support cleartext HTTP/2 (h2c)
```

Configure the proxy to accept HTTPS and forward cleartext HTTP/2 (`h2c`) to the Hub. Use nginx `grpc_pass`, Caddy `transport http { versions h2c }`, or Traefik `h2c://`. Alternatively, connect the Worker directly to the Hub. Browser and command-line requests can work over HTTP/1.1 even when the Worker connection fails.

## Upgrading

LeapMux runs database migrations automatically on startup, for both the Hub and each Worker, so there is no separate migration command to run during a routine upgrade.

- **Docker:** Pull a newer image tag (a pinned `:<version>`, `:<major>`, or `:latest`) and recreate the container against the same `/data` volume. Migrations run on the next start.
- **CLI binary:** Replace the `leapmux` binary from the newer server tarball or zip on the [Releases page](https://github.com/leapmux/leapmux/releases) and restart.
- **Desktop app:** Download and install the newer artifact from the Releases page.

{{< callout >}}
Back up your Hub data before a major upgrade — at minimum the database and `encryption.key` (or your external database, if you use one). The encryption key ring is required to read encrypted data, so keep it with your backups. See [Encryption & Data](/docs/admin/encryption-and-data/) for backup, restore, and key-rotation details.
{{< /callout >}}

## Checking the version

Every mode and the dedicated `version` command print the same build string:

```bash
leapmux version
leapmux --version
```

## See also

- [Configuration](/docs/admin/configuration/) — full flag and config-key reference, storage backends, listen addresses, env-var precedence.
- [Managing Workers](/docs/admin/managing-workers/) — registration keys, Worker approval, TOFU pinning, Worker selection.
- [Recovery](/docs/admin/recover/) — the offline break-glass tree: first-admin bootstrap, password reset, schema migrations, and encryption-key surgery.
- [Control CLI](/docs/using/control-cli/) and [Admin CLI](/docs/admin/admin-cli/) — users, sessions, workers, OAuth providers, instance settings, and tokens on a running Hub.
- [Installation](/docs/getting-started/installation/) — desktop app, Docker images, and building from source.
- [Security & Threat Model](/docs/admin/security/) — trust boundaries, the E2EE relay, and solo-mode caveats.
- [CLI Reference](/docs/reference/cli-reference/) — consolidated cheat-sheet for every subcommand.
