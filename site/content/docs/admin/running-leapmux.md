---
title: "Running LeapMux"
description: "Administrator guide to the solo, hub, worker, and dev modes: capability matrix, launch commands, data directories, Docker, and upgrades."
type: docs
weight: 2
---

Use the `leapmux` command to run a personal workspace, a shared Hub, or a Worker that connects to a Hub. This guide covers choosing a mode, starting it, preserving its data, and running under Docker.

If you want to run LeapMux on your own machine, the desktop app or `solo` mode is all you need. For a shared team service, deploy a `hub` with separate `worker` processes following the [Production Checklist](/docs/admin/production-checklist/). For key-by-key flags and storage backends, see [Configuration Reference](/docs/admin/configuration-reference/).

Start each mode with `leapmux daemon <mode> start` (or `leapmux up` for `solo`), and stop it with `leapmux daemon <mode> stop` (or `leapmux down`). `start` installs and runs LeapMux as a background service of your operating system; see [Starting and Stopping](/docs/admin/starting-and-stopping/). Inside a container, run `leapmux daemon <mode> run` in the foreground instead; see [Running under Docker](#running-under-docker).

## Run modes at a glance

The modes that accept inbound connections — `solo`, `hub`, and `dev` — all default to TCP port **4327**. A `worker` only connects out to a Hub and listens on no port.

| Mode | What it runs | Default listen | Login required | Config / data location |
| --- | --- | --- | --- | --- |
| `solo` | Hub + Worker in one process | `127.0.0.1:4327` (loopback only), plus any added address | Local IPC: no. Network TCP: password setup, then sign-in. | `~/.config/leapmux/solo/` |
| `hub` | Hub service only | `:4327` (all interfaces) | Yes — full authentication (including local IPC) | `~/.config/leapmux/hub/` |
| `worker` | Worker only (connects out to a Hub) | n/a (no inbound port) | Registers with the Hub via a registration key | `~/.config/leapmux/worker/` |
| `dev` | Hub + Worker in one process | `:4327` (all interfaces) | Yes — admin bootstrapped via `/setup` (including local IPC) | `~/.config/leapmux/dev/` |

Each mode reads an optional YAML config file inside its config directory — for example `~/.config/leapmux/hub/hub.yaml` for the Hub. The default data directory matches the config directory.

### Mode capability matrix

| Capability | `solo` | `dev` | `hub` |
| --- | --- | --- | --- |
| Bundled in-process Worker | Yes | Yes | No (connect standalone Workers) |
| Multiple user accounts & open sign-up | No (single `solo` account) | Yes (`signup_enabled` on by default) | Yes (`signup_enabled` off by default) |
| External SQL databases (PostgreSQL, MySQL, etc.) | Supported | Supported | Supported |
| Credential-free local IPC (desktop app / CLI) | Yes (OS-account verified) | No | No |
| Dynamic `extra_listen_addresses` panel | Yes | No | No |
| SMTP email, account recovery & captcha | No | Yes | Yes |
| Passkeys & OAuth / OIDC sign-in providers | No | Yes | Yes |
| OAuth 2.1 app authorization | Yes | Yes | Yes |
| Standalone remote Workers can also attach | Yes | Yes | Yes |

{{< callout type="warning" >}}
While the `solo` account has no password, the first TCP caller can claim it by setting that password. Other protected RPCs remain closed. If you bind `solo` to a non-loopback address in that state, LeapMux logs a startup warning. Set the password from a trusted connection first; see [Security & Authentication](/docs/admin/security-and-auth/#solo-mode-a-reduced-threat-model).
{{< /callout >}}

### Reaching a solo Hub from another machine

Set the `solo` account's password and add the network address in **Preferences → Administration → Network access**. The Hub binds that address immediately and on every later start. From then on, every network address asks you to sign in as `solo` — `127.0.0.1` included — while the desktop app over its local socket skips sign-in. See [Network access](/docs/admin/configuration/#network-access).

Session settings apply to solo mode when you sign in over the network. For HTTPS access, follow [Reverse Proxy](/docs/admin/reverse-proxy/). Solo mode keeps one account and one password (no sign-up, passkeys, recovery, or email); switch to `hub` or `dev` mode for multiple users.

## Solo mode

Solo mode runs a personal Hub and Worker with no required configuration. By default, only your machine can connect. Set the first password when you open it in a browser; the desktop app connects over local inter-process communication (IPC) without sign-in.

```bash
leapmux daemon solo start
# Then open http://127.0.0.1:4327
```

Solo mode stores Hub data in `<data_dir>/hub/` and Worker data in `<data_dir>/worker/`. Keep both directories when moving or backing up the installation. You do not register the bundled Worker separately.

Common flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:4327` plus a local socket | Listen address, repeatable: TCP address or local IPC (`unix:`, `npipe:`) |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/solo`) | Data directory |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` for the bundled Worker |
| `--config` | `~/.config/leapmux/solo/solo.yaml` | Config file path |

For all supported flags and runtime settings, see [Configuration Reference](/docs/admin/configuration-reference/). The desktop app runs solo mode without opening a network port by default; see [Installation](/docs/getting-started/installation/).

## Hub mode

Hub mode runs only the Hub: authentication, workspace management, Worker registration, and the encrypted relay between Frontends and Workers. Workers connect separately (see [Running Workers](#running-workers)). Hub mode binds `:4327` on all interfaces by default and requires authentication on every transport, including local IPC.

```bash
leapmux daemon hub start
```

A fresh Hub has no users and sign-up disabled. Open it in a browser and every path shows `/setup` — the first person to register becomes the administrator (see [Accounts & Connected Apps](/docs/using/accounts-and-apps/)). You can also create the first administrator offline with [`leapmux recover create-admin`](/docs/admin/recover/), which refuses once any administrator exists.

Enable sign-up with `leapmux admin settings set signup_enabled true`, or create accounts with `leapmux admin user create`. Common Hub flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `:4327` plus a local socket | TCP address or local IPC URL; repeatable |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/hub`) | Data directory |
| `--storage-type` | empty (= `sqlite`) | `sqlite`, `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, or `tidb` |
| `--log-level` | `info` | Log level |
| `--config` | `~/.config/leapmux/hub/hub.yaml` | Config file path |

Configure listening addresses and database connections at startup. Change account, email, session, timeout, and per-user limit settings on the running Hub with `leapmux admin settings`:

```bash
leapmux admin settings set signup_enabled true
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587,"from_address":"no-reply@example.com"}'
```

By default, the Hub uses an embedded SQLite database at `<data_dir>/hub.db` and its encryption key ring at `<data_dir>/encryption.key`. For shared deployments, point it at an external database via `storage.type` and the matching DSN; see [Configuration Reference](/docs/admin/configuration-reference/#storage-backends) and [Admin CLI](/docs/admin/admin-cli/#hub-settings). For HTTPS access, place a reverse proxy in front of the Hub; see [Reverse Proxy](/docs/admin/reverse-proxy/).

## Running Workers

Run a Worker on each machine where you want to host agents and terminals. A Worker dials out to the Hub and accepts no inbound connections, so it needs no open port or port forwarding.

Register a Worker once, then start it:

```bash
# 1. Register with a key from the Register worker dialog of the Hub.
leapmux daemon worker register --hub https://hub.example.com --registration-key YOUR_REGISTRATION_KEY

# 2. Start it as a background service.
leapmux daemon worker start
```

Registration saves the Hub URL and Worker credentials in the Worker's data directory, so `start` takes neither `--hub` nor `--registration-key`. See [Register a Worker](/docs/admin/managing-workers/#register-a-worker).

Common Worker flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | hostname | Worker display name |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/worker`) | Data directory |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` |
| `--use-login-shell` | `true` | Wrap agent invocation in your login shell |
| `--log-level` | `info` | Log level |
| `--config` | `~/.config/leapmux/worker/worker.yaml` | Config file path |

See [Managing Workers](/docs/admin/managing-workers/) for key pinning, reconnects, and deregistration.

## Dev mode

Use `dev` mode to run a Hub and Worker together with multiple user accounts. It listens on all interfaces, requires sign-in, and enables open sign-up by default unless you override `signup_enabled`. Choose `dev` when you want both services in one process or container with multi-user authentication.

```bash
leapmux daemon dev start
```

Open the Hub in your browser and complete `/setup` to create the first administrator. The bundled Worker registers automatically after setup.

Common dev flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `:4327` plus a local socket | Listen address, repeatable: TCP or local IPC (`unix:`, `npipe:`) |
| `--data-dir` | `.` (resolves to `~/.config/leapmux/dev`) | Data directory |
| `--log-level` | `info` | Log level |
| `--encryption-mode` | `post-quantum` | `classic` or `post-quantum` for the bundled Worker |
| `--config` | `~/.config/leapmux/dev/dev.yaml` | Config file path |

Like `solo`, `dev` also accepts `--dev-frontend` and the three SQLite tuning flags (`--storage-sqlite-max-conns`, `--storage-sqlite-cache-size`, and `--storage-sqlite-mmap-size`) on the CLI. Configure `storage.type`, `storage.sqlite.path`, or an external database DSN in `dev.yaml` (or `solo.yaml`); see [Configuration Reference](/docs/admin/configuration-reference/).

## Running under Docker

Use the images at `ghcr.io/leapmux/leapmux` to run a Hub, a Worker, or both in one container.

### Image variants and tags

| Variant | Tags | Example |
| --- | --- | --- |
| Alpine (default) | `:<version>`, `:<major>`, `:latest`, `:dev` | `ghcr.io/leapmux/leapmux:latest` |
| Ubuntu | `:<version>-ubuntu`, `:<major>-ubuntu`, `:latest-ubuntu`, `:dev-ubuntu` | `ghcr.io/leapmux/leapmux:latest-ubuntu` |

Both variants support `linux/amd64` and `linux/arm64`. Pin a specific `:<version>` tag for predictable deployments. `:latest` tracks releases; `:dev` tracks `main`.

### Selecting the mode and volume layout

Set `LEAPMUX_MODE` to `hub`, `worker`, `dev`, or `solo`. Mount a persistent volume or host directory at `/data` (or override the base path with `LEAPMUX_DATA_DIR`) to preserve configuration, databases, encryption keys, and Worker credentials across container replacements.

| Mode | Configuration file | Default data directories |
| --- | --- | --- |
| `hub` | `/data/hub/hub.yaml` | `/data/hub/` |
| `worker` | `/data/worker/worker.yaml` | `/data/worker/` |
| `dev` | `/data/dev/dev.yaml` | `/data/dev/hub/` and `/data/dev/worker/` |
| `solo` | `/data/solo/solo.yaml` | `/data/solo/hub/` and `/data/solo/worker/` |

Configure the selected mode through `/data/<mode>/<mode>.yaml` or its environment variables (`LEAPMUX_HUB_*` or `LEAPMUX_WORKER_*`). Use the YAML file for nested `storage.*` settings; see [Configuration Reference](/docs/admin/configuration-reference/). Keep the persistent volume even with an external database because `encryption.key` still lives on disk.

### Running Hub, Dev, and Worker containers

Publish port `4327` (or `127.0.0.1:4327:4327` behind a host reverse proxy) for `hub` or `dev`:

```bash
# Run a Hub with separate Workers.
docker run -d --name leapmux-hub --restart unless-stopped \
  -p 127.0.0.1:4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest

# Run a Hub and Worker together.
docker run -d --name leapmux-dev --restart unless-stopped \
  -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

Solo mode binds `127.0.0.1:4327` inside the container by default, so publishing its port does not expose it to the host. Use `dev` when you want a Hub and Worker together in a container that accepts network connections.

For a standalone Worker container, register once in a temporary container, then start the long-lived container on the same volume:

```bash
# 1. Register. Keep the Hub's Register worker dialog open until this finishes.
docker run --rm --entrypoint /usr/local/bin/leapmux \
  -v leapmux-worker-data:/data ghcr.io/leapmux/leapmux:latest \
  daemon worker register --data-dir /data/worker \
  --hub https://hub.example.com --registration-key YOUR_REGISTRATION_KEY

# 2. Run the Worker with the saved credentials.
docker run -d --name leapmux-worker --restart unless-stopped \
  -e LEAPMUX_MODE=worker -v leapmux-worker-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

The container runs `leapmux daemon <mode> run` in the foreground, and Docker's `--restart unless-stopped` policy restarts it automatically. Do not use `start` or `enable` inside a container.

## Upgrading

LeapMux runs database migrations automatically on startup for both the Hub and each Worker.

- **Docker:** Pull the newer image tag and recreate the container against the same `/data` volume.
- **CLI binary:** Download the newer archive from the [Releases page](https://github.com/leapmux/leapmux/releases), replace the `leapmux` binary, and run `leapmux daemon <mode> restart`.
- **Desktop app:** Download and install the newer desktop package from the Releases page.

{{< callout >}}
Back up your Hub database and `encryption.key` together before upgrading. See [Encryption & Data](/docs/admin/encryption-and-data/#backup-and-restore).
{{< /callout >}}

## Checking the version

`leapmux version` is a top-level command only — subcommands do not accept `version` or `--version` (`leapmux daemon hub version` fails). Run `leapmux version` to print the CLI build version together with the connected Hub and Worker versions when reachable, or `leapmux --version` (`-v`) to print the local binary version only:

```bash
leapmux version
leapmux --version
```

## See also

- [Production Checklist](/docs/admin/production-checklist/) — end-to-end team Hub deployment checklist.
- [Starting and Stopping](/docs/admin/starting-and-stopping/) — run LeapMux as an OS service at login or boot.
- [Configuration Guide](/docs/admin/configuration/) and [Configuration Reference](/docs/admin/configuration-reference/) — deployment profiles, listen addresses, storage backends, and all keys.
- [Managing Workers](/docs/admin/managing-workers/) — registration keys, TOFU pinning, and deregistration.
- [Recovery](/docs/admin/recover/) — offline break-glass administration.
- [CLI Guide](/docs/using/cli-guide/) and [Admin CLI](/docs/admin/admin-cli/) — controlling workspaces and administering a running Hub.
- [Security & Authentication](/docs/admin/security-and-auth/) — trust boundaries, E2EE, and solo-mode caveats.
