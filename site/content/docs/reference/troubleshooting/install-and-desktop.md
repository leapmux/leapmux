---
title: "Install, Desktop & Services"
description: "Fixes for LeapMux installation, Desktop app launcher, macOS Full Disk Access, background OS services, Docker containers, ports, and reaching the UI."
type: docs
weight: 1
---

Fixes for installing LeapMux, launching the Desktop app, running background OS services, running Docker containers, and binding ports.

## Desktop launcher & macOS permissions

### macOS Full Disk Access required

**Symptom**
Selecting **Solo** in LeapMux Desktop on macOS shows a **Full Disk Access Required** card instead of opening a workspace.

**Cause**
Solo mode runs a local Worker that browses directories inside your home folder. macOS blocks access to protected folders until you grant Full Disk Access to LeapMux Desktop. Distributed mode connects to a remote Hub and does not require Full Disk Access.

**Fix**
Click **Open System Settings** on the card, enable **LeapMux Desktop** under **Privacy & Security → Full Disk Access**, and wait a few seconds. The app detects the permission change and restarts itself. See [Installation](/docs/getting-started/installation/).

### Desktop CLI symlink missing on macOS

**Symptom**
Running `leapmux` in a terminal prints `command not found` after installing LeapMux Desktop.

**Cause**
On macOS, the Desktop app installs the `/usr/local/bin/leapmux` symlink only after you accept the prompt upon entering a Solo workspace. (On Linux, the `.deb` package installs `/usr/bin/leapmux` automatically; `.AppImage` and Windows `.msi` do not add a global CLI binary.)

**Fix**
Enter a Solo workspace in LeapMux Desktop on macOS and accept the CLI symlink prompt, or download the standalone `leapmux` binary from the [Releases page](https://github.com/leapmux/leapmux/releases) and place it on your `PATH`.

## Background service

These entries apply to instances managed with `leapmux daemon <mode> start` (or `leapmux up`). Run `leapmux daemon <mode> status` with the instance's `--data-dir` or `--config` to inspect process state, the last failure, and the log path. See [Starting and Stopping](/docs/admin/starting-and-stopping/).

### Startup wait expired

**Symptom**
`start` or `restart` prints `Startup wait expired` and notes that startup may continue.

**Cause**
LeapMux did not become ready within `--timeout` (60 seconds by default). First-run database migrations or a slow external database can exceed 60 seconds while the service continues starting.

**Fix**
Run `leapmux daemon <mode> status` until the instance reports `running` or `failed`. Pass a longer `--timeout` (for example `--timeout 3m`) for slow initial starts.

### Login startup fails on headless Linux

**Symptom**
`start` fails and reports that login startup requires an active desktop session or recommends boot startup.

**Cause**
On Linux, `--startup=login` attaches to a graphical user session. An SSH shell does not have one.

**Fix**
Use boot startup, which runs even when nobody is logged in:

```bash
leapmux daemon hub start --startup=boot
```

Boot startup asks for `sudo` to install the service unit; LeapMux still runs as your account.

### Script fails on noninteractive setup

**Symptom**
`start` or `enable` fails in a script and reports that noninteractive setup requires `--startup=login` or `--startup=boot` and `--yes`.

**Cause**
The first `start` prompts interactively for startup timing and configuration consent.

**Fix**
Pass both flags explicitly:

```bash
leapmux daemon worker start --startup=boot --yes
```

### Service fails after Windows password change

**Symptom**
`start` or `status` reports that Windows rejected the account password as incorrect or expired.

**Cause**
Windows stores a copy of your account password to launch the scheduled task or service, and that stored password is now stale.

**Fix**
Run `leapmux daemon <mode> enable` and enter your new Windows password when prompted.

### Service definition missing or modified

**Symptom**
`status` reports that the native OS service definition is missing or differs from the saved configuration.

**Cause**
An external cleanup tool or manual edit modified or removed the launchd, systemd, or Windows service definition.

**Fix**
Run `leapmux daemon <mode> enable` to rewrite the native service entry from the saved configuration.

### Shutdown could not be confirmed

**Symptom**
`stop` prints `Shutdown could not be confirmed.`

**Cause**
The instance did not finish closing databases and stopping agents within `--timeout` (3 minutes by default).

**Fix**
Run `leapmux daemon <mode> status` until it reports `stopped`, or pass a longer `--timeout` to `stop`.

### Command refuses while instance is running

**Symptom**
`start` says the instance is already running, or `disable` / `daemon worker deregister` refuses because the instance is running.

**Cause**
`start` only starts stopped instances, and `disable` and `daemon worker deregister` modify state that a running process holds.

**Fix**
Use `leapmux daemon <mode> restart` to restart a running instance. Run `leapmux daemon <mode> stop` before `disable` or `daemon worker deregister`.

### Data directory belongs to another instance

**Symptom**
A daemon command fails and reports that the data directory belongs to another `hub`, `worker`, `solo`, or `dev` instance.

**Cause**
Each data directory belongs to one mode and one operating-system account. Conflicts occur when two modes share a directory, another user runs against the directory, or a `hub` or `worker` subdirectory is separated from its parent `solo` or `dev` directory.

**Fix**
Give each instance its own `--data-dir`, pass the matching `--data-dir` or `--config` to every command, and run the instance under the account that created it. Moving a complete data directory to a new path is supported; see [Starting and Stopping](/docs/admin/starting-and-stopping/).

## Docker

For full container usage, see [Running LeapMux](/docs/admin/running-leapmux/).

### Container exits immediately

**Symptom**
The container exits immediately with an error stating that `LEAPMUX_MODE` must be `hub`, `worker`, `dev`, or `solo`.

**Cause**
The container entrypoint requires `LEAPMUX_MODE` to choose which daemon mode to run.

**Fix**
Set `LEAPMUX_MODE` explicitly:

```bash
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

{{< callout type="info" >}}
Use `LEAPMUX_MODE=dev` (not `solo`) for an all-in-one container reachable from your host. `solo` binds `127.0.0.1:4327` inside the container unless you override `listen` to `:4327` in `/data/solo/solo.yaml`, whereas `dev` binds `:4327` by default.
{{< /callout >}}

### Worker container is unregistered

**Symptom**
A `LEAPMUX_MODE=worker` container exits with ``worker is unregistered: run `leapmux daemon worker register` first``.

**Cause**
The container runs `leapmux daemon worker run`, which never registers automatically. The `/data` volume has no saved credentials under `/data/worker`.

**Fix**
Run the one-shot registration container from [Running LeapMux](/docs/admin/running-leapmux/) against the same volume with `--data-dir /data/worker`, then start the Worker container again. Inspect a running Worker container with:

```bash
docker exec <container> /usr/local/bin/leapmux daemon worker status --data-dir /data/worker
```

### Storage env vars are ignored

**Symptom**
Setting `LEAPMUX_HUB_STORAGE_TYPE=postgres` still starts the Hub with SQLite.

**Cause**
`LEAPMUX_HUB_*` environment variables map only top-level keys and do not translate underscores into nested dots (`storage.type`, `storage.postgres.dsn`).

**Fix**
Configure external storage in `/data/hub/hub.yaml` (or via `--storage-type` and `--storage-postgres-dsn` flags):

```yaml
storage:
  type: postgres
  postgres:
    dsn: postgres://user:password@db:5432/leapmux?sslmode=disable
```

See [Configuration Reference](/docs/admin/configuration-reference/).

## Ports, listen address, and reaching the UI

### Port 4327 already in use

**Symptom**
`hub`, `solo`, or `dev` fails to start because port `4327` is already bound.

**Cause**
Another process — often an earlier LeapMux instance — holds port `4327`.

**Fix**
Stop the conflicting process, or restart LeapMux on another port (the service persists the new flag):

```bash
leapmux daemon hub restart --listen :4400
```

In `solo` mode, keep a loopback address such as `--listen 127.0.0.1:4400`. In Docker, remap the published host port instead (`-p 4400:4327`).

### Connection refused from another machine

**Symptom**
A browser on another host gets connection refused or times out when connecting to the Hub.

**Cause**
`solo` mode binds `127.0.0.1:4327` (loopback only) by default.

**Fix**
- For single-user local use, open `http://127.0.0.1:4327` on the same machine.
- For multi-user or remote access, run `hub` or `dev` mode (both bind `:4327` with real authentication).
- If you expose `solo` mode intentionally, set the `solo` password first, add the listen address under **Preferences → Administration → Network access**, and restrict access with a firewall, VPN, or SSH tunnel. See [Security & Authentication](/docs/admin/security-and-auth/).

### Blank UI in source development

**Symptom**
The browser loads a blank page or missing assets when running LeapMux built from source.

**Cause**
Development builds reverse-proxy the UI to a separate Frontend dev server configured with `--dev-frontend`. If that server is not running, no UI assets are served.

**Fix**
Start both processes with `task dev` or `task dev-solo`, or verify that your Frontend dev server is running at the `--dev-frontend` URL. Release binaries and Docker images bundle the built Frontend and never need `--dev-frontend`.
