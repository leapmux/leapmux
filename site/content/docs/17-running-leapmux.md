---
title: "Running LeapMux"
type: docs
weight: 17
---

LeapMux is a single Go binary (`leapmux`) that you run in one of four operating modes: `solo`, `hub`, `worker`, or `dev`. This chapter is the operator's guide to those modes — what each one does, how to launch it, where it stores data, and how to run LeapMux under Docker or behind a reverse proxy.

If you only want to try LeapMux on your own machine, the desktop app or `leapmux solo` is all you need. If you are standing up a shared service for a team, you will run a `hub` plus one or more `worker` processes. For the full list of configuration keys and storage backends referenced here, see [Configuration](/docs/18-configuration/).

## Run modes at a glance

The modes that accept inbound connections — `solo`, `hub`, and `dev` — all default to TCP port **4327**. A `worker` is the exception: it only makes outbound connections to a Hub and binds no listen port (its default `http://127.0.0.1:4327` is the Hub URL it dials, not a port it serves). Beyond the port, the modes differ in what they run, whether they require a login, and where they listen.

| Mode | What it runs | Default listen | Login required | Config / data location |
|------|--------------|----------------|----------------|------------------------|
| `solo` | Hub + Worker in one process | `127.0.0.1:4327` (loopback only) | No — every request is auto-authenticated as the admin | `~/.config/leapmux/solo/` |
| `hub` | Hub service only | `:4327` (all interfaces) | Yes — full authentication | `~/.config/leapmux/hub/` |
| `worker` | Worker only (connects out to a Hub) | n/a (no inbound port) | Registers with the Hub via a registration key | `~/.config/leapmux/worker/` |
| `dev` | Hub + Worker in one process | `:4327` (all interfaces) | Yes — admin bootstrapped via `/setup` | `~/.config/leapmux/dev/` |

Each mode reads a YAML config file named after the mode inside its config directory — for example `~/.config/leapmux/hub/hub.yaml` for the Hub. The file is optional; a missing config file is silently skipped. The default data directory for each mode is the same as its config directory.

> **Note:** `solo` and `dev` are the same program internally — both run a Hub and a Worker together in one process. The differences are that `solo` binds loopback only and skips login (it injects an admin user into every request), while `dev` binds all interfaces, uses real password authentication, and bootstraps its first admin through the `/setup` flow.

> **Warning:** Because `solo` mode auto-authenticates every request as the admin, anyone who can reach its port has full admin access with no credentials. That is safe on `127.0.0.1`, but if you bind `solo` to a non-loopback address LeapMux logs a warning: *"solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, so anyone who can reach this port has full admin access without credentials. Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication."* For a multi-user or network-exposed deployment, run `leapmux hub` (with separate Workers) or `leapmux dev` instead, both of which require a real login.

## Solo mode

Solo mode is the zero-configuration, single-user setup. Hub and Worker run in the same process on loopback, and the UI opens straight into the workspace with no sign-in.

```bash
leapmux solo
# Then open http://127.0.0.1:4327
```

Inside the solo data directory, the single `data_dir` is split into two subdirectories: the Hub stores its database and encryption key under `<data_dir>/hub/`, and the in-process Worker stores its state under `<data_dir>/worker/`. The Worker auto-registers itself and persists its credentials and end-to-end-encryption keypair to `<data_dir>/worker/state.json`, so your session survives restarts.

Solo mode exposes a deliberately small set of flags (a subset of the Hub's flags) plus an `--encryption-mode` flag for the bundled Worker:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:4327` | TCP listen address |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |

> **Note:** `--public-url` is **not** available in solo mode. If you set `public_url` by any means, solo mode rejects it with `public_url is not supported in solo mode`. Reverse-proxy fronting is a job for `hub` or `dev`.

The desktop app runs solo mode under the hood, but with no TCP port at all — it serves the Hub only over a local IPC socket (a Unix domain socket or Windows named pipe). See [Installation](/docs/03-installation/) for the desktop app.

## Hub mode

`leapmux hub` runs only the Hub: authentication, workspace management, Worker registration, and the encrypted relay between Frontends and Workers. It does **not** run a Worker — Workers connect separately (see [Running Workers](#running-workers)). The Hub binds `:4327` on all interfaces by default and requires real authentication.

```bash
leapmux hub -listen :4327
```

A fresh Hub has no users and (by default) sign-up disabled. To allow accounts to be created, enable sign-up, or pre-create users with the [Admin CLI](/docs/20-admin-cli/). The most important Hub flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` | TCP listen address (e.g. `:4327` or `127.0.0.1:4327`) |
| `-local-listen` | platform default | Local IPC URL (`unix:<path>` or `npipe:<name>`); defaults to `unix:<data-dir>/hub.sock` on Unix |
| `-public-url` | empty | Public base URL when behind a reverse proxy (e.g. `https://hub.example.com`) |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `-signup-enabled` | `false` | Allow user sign-up |
| `-email-verification-required` | `false` | Require email verification on sign-up (needs SMTP configured) |
| `-storage-type` | empty (= `sqlite`) | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `-log-level` | `info` | Log level |
| `-config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |

By default the Hub uses an embedded SQLite database at `<data_dir>/hub.db` with its encryption key ring at `<data_dir>/encryption.key`. For a shared, durable deployment you will usually point it at an external database via `-storage-type` and the matching `*-dsn` flag. The Hub also has SMTP settings (for email verification and notifications) and timeout/limit knobs. The full reference — every flag, every storage backend, every config key, and the YAML layout — is in [Configuration](/docs/18-configuration/).

> **Note:** The Hub does not terminate TLS itself. For HTTPS you put a reverse proxy in front of it; see [Reverse proxy and public URL](#reverse-proxy-and-public-url) below.

## Running Workers

A Worker runs your agents and terminals and connects **out** to a Hub — it never accepts inbound connections, so it works behind a NAT or firewall with no port forwarding. A Worker is not a user account; it **registers** with the Hub using a one-time registration key minted in the Hub UI.

On first run, point the Worker at the Hub and supply a registration key:

```bash
leapmux worker --hub https://hub.example.com --registration-key <key>
```

After a successful registration, the Worker saves its credentials and keypair to `<data_dir>/state.json` and reconnects automatically on every subsequent start — you do **not** pass `--registration-key` again.

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

If a Worker has no saved credentials and no key, it refuses to start with:

```
worker is unregistered: pass --registration-key <key> from the hub UI
```

And if you pass `--registration-key` to a Worker that is already registered, it stops with `worker is already registered; remove --registration-key or wipe local state to re-register` — this protects you from burning a key by accident.

Minting registration keys, approving Workers, the trust-on-first-use (TOFU) pinning of Worker keys, and choosing which Worker a tab runs on are all covered in [Managing Workers](/docs/19-managing-workers/).

## Dev mode

`leapmux dev` runs a Hub and Worker together like solo, but binds all interfaces and requires a real login. It is the all-features, network-reachable variant — and the one to use inside a container when you want a single-process Hub + Worker that is actually reachable from outside.

```bash
leapmux dev -listen :4327
```

Because dev mode uses real authentication, it bootstraps its first admin through the `/setup` flow rather than auto-authenticating. The in-process Worker's auto-registration is deferred until that first admin signs up; until then the log shows *"dev mode: deferring worker auto-registration until first admin signs up via /setup"*. Open the URL, complete `/setup` to create the admin, and the bundled Worker comes online.

Dev mode accepts the same flags as solo, plus `--public-url`, which solo does not have:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `:4327` | TCP listen address |
| `-public-url` | empty | Public base URL when behind a reverse proxy |
| `-data-dir` | `.` (resolves to `~/.config/leapmux/dev`) | Data directory |
| `-log-level` | `info` | Log level |
| `-encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `-config` | `~/.config/leapmux/dev/dev.yaml` | Config file path |

## Running under Docker

Pre-built multi-arch images are published to `ghcr.io/leapmux/leapmux` and supervised by [s6-overlay](https://github.com/just-containers/s6-overlay). The single environment variable `LEAPMUX_MODE` selects which subcommand the container runs.

### Image variants and tags

| Variant | Tags | Example |
|---------|------|---------|
| Alpine (default) | `:<version>`, `:<major>`, `:latest`, `:dev` | `ghcr.io/leapmux/leapmux:latest` |
| Ubuntu | `:<version>-ubuntu`, `:<major>-ubuntu`, `:latest-ubuntu`, `:dev-ubuntu` | `ghcr.io/leapmux/leapmux:latest-ubuntu` |

Both variants target `linux/amd64` and `linux/arm64`. Release tags (`:latest`, `:<version>`, `:<major>`) come from the release workflow; the `:dev` tag is rebuilt on every push to `main`. For production, pin a specific `:<version>` rather than tracking `:latest` or `:dev`.

### Selecting the mode

`LEAPMUX_MODE` is **required** and must be one of `hub`, `worker`, `dev`, or `solo`. If it is unset or invalid the container exits with:

```
error: LEAPMUX_MODE must be one of: hub, worker, dev, solo
```

The supervisor always invokes `leapmux <mode> -config /data/<mode>/<mode>.yaml`, creating an empty `0600` config file if none exists. It passes no other flags — so any additional settings must come from the YAML config file or from `LEAPMUX_HUB_*` / `LEAPMUX_WORKER_*` environment variables (see [Configuration](/docs/18-configuration/)).

### Volume layout

The image declares a single `/data` volume. Each mode keeps its files under `/data/<mode>/`:

| Path | Contents |
|------|----------|
| `/data/<mode>/<mode>.yaml` | Config file (e.g. `/data/hub/hub.yaml`) |
| `/data/hub/hub.db` | Hub SQLite database (when using the default SQLite backend) |
| `/data/hub/encryption.key` | Hub encryption key ring |
| `/data/worker/state.json` | Worker registration credentials and E2EE keypair |
| `/data/worker/worker.db` | Worker SQLite database |

Mount a named volume (or a host path) at `/data` so this state survives container recreation.

**The `/data` volume laid out for a `hub` and a `worker`:**

```text
/data                          ← mounted volume (-v leapmux-data:/data)
├── hub/                       ← LEAPMUX_MODE=hub (also dev, solo)
│   ├── hub.yaml               ← config file
│   ├── hub.db                 ← Hub SQLite database (default backend)
│   └── encryption.key         ← Hub encryption key ring
└── worker/                    ← LEAPMUX_MODE=worker (also dev, solo)
    ├── worker.yaml            ← config file
    ├── worker.db              ← Worker SQLite database
    └── state.json             ← registration credentials + E2EE keypair
```

### Exposing the port

The image exposes port **4327**, the single TCP listen port for `hub`, `dev`, and `solo`. (`worker` makes only outbound connections, so it needs no published port.) Map it with `-p`:

```bash
# Run as a hub (central service only)
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest

# Run as Hub + Worker together (dev mode, login required)
docker run -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

> **Warning:** Do not run `LEAPMUX_MODE=solo` in a container expecting to reach it from outside. Solo's default listen address is `127.0.0.1:4327` (loopback only), which is not reachable from the host, and solo auto-authenticates every request as admin. Use `dev` for a single-process Hub + Worker that binds all interfaces with a real login, or run a `hub` container plus separate `worker` containers.

### Running a Worker container

The supervisor passes no `--hub` or `--registration-key` flags, so a `worker` container needs those supplied another way — via the Worker YAML config at `/data/worker/worker.yaml` or via environment variables:

```bash
docker run -e LEAPMUX_MODE=worker \
  -e LEAPMUX_WORKER_HUB=https://hub.example.com \
  -e LEAPMUX_WORKER_REGISTRATION_KEY=<key> \
  -v leapmux-worker-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

The registration key is consumed only on first registration; once the Worker's `state.json` exists you can drop `LEAPMUX_WORKER_REGISTRATION_KEY` from later runs.

### Supervision behavior

The image entrypoint is s6-overlay's `/init`, which runs the `leapmux` process as a long-running service. If that service exits non-zero, the `finish` script tears the whole container down so your orchestrator (Docker restart policy, Kubernetes, etc.) can restart it cleanly. A clean shutdown (for example, `SIGTERM` during a graceful stop) is treated as normal and does not trigger a restart loop.

## Reverse proxy and public URL

The Hub never terminates TLS on its own. To serve LeapMux over HTTPS, put a reverse proxy (nginx, Caddy, Traefik, etc.) in front of it and tell the Hub its external address:

1. Set `public_url` to the external HTTPS URL, e.g. `https://hub.example.com` (the `-public-url` flag, the `public_url` YAML key, or `LEAPMUX_HUB_PUBLIC_URL`).
2. Set `secure_cookies: true` in the config (or `LEAPMUX_HUB_SECURE_COOKIES=true`) so cookies are marked secure and the derived base URL uses `https`. There is no CLI flag for this key.
3. Point each Worker's `-hub` URL at the same external `https://` address. Workers always initiate outbound connections, so they need no inbound ports of their own.

> **Warning:** `public_url` must be a bare scheme + host — for example `https://hub.example.com`. Sub-path mounting (such as `https://example.com/leapmux`) is **not** supported and is rejected at startup. Give LeapMux its own hostname or subdomain.

The proxy must also forward WebSocket upgrades, since Frontend traffic and the relayed Worker streams ride over long-lived connections. For the security implications of the relay, the end-to-end-encryption boundary, and Worker TOFU pinning, see [Security & Threat Model](/docs/23-security-and-threat-model/).

## Upgrading

LeapMux runs database migrations automatically on startup, for both the Hub and each Worker, so there is no separate migration command to run during a routine upgrade.

- **Docker:** Pull a newer image tag (a pinned `:<version>`, `:<major>`, or `:latest`) and recreate the container against the same `/data` volume. Migrations run on the next start.
- **CLI binary:** Replace the `leapmux` binary from the newer server tarball or zip on the [Releases page](https://github.com/leapmux/leapmux/releases) and restart.
- **Desktop app:** Download and install the newer artifact from the Releases page.

> **Tip:** Back up your Hub data before a major upgrade — at minimum the database and `encryption.key` (or your external database, if you use one). The encryption key ring is required to read encrypted data, so keep it with your backups. See [Encryption & Data](/docs/22-encryption-and-data/) for backup, restore, and key-rotation details.

## Checking the version

Every mode and the dedicated `version` command print the same build string:

```bash
leapmux version
leapmux --version
```

## See also

- [Configuration](/docs/18-configuration/) — full flag and config-key reference, storage backends, listen addresses, env-var precedence.
- [Managing Workers](/docs/19-managing-workers/) — registration keys, Worker approval, TOFU pinning, Worker selection.
- [Admin CLI](/docs/20-admin-cli/) — manage orgs, users, sessions, workers, OAuth providers, and the database directly.
- [Installation](/docs/03-installation/) — desktop app, Docker images, and building from source.
- [Security & Threat Model](/docs/23-security-and-threat-model/) — trust boundaries, the E2EE relay, and solo-mode caveats.
- [CLI Reference](/docs/24-cli-reference/) — consolidated cheat-sheet for every subcommand.
