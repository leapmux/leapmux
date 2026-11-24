---
title: "Installation"
description: "Ways to install LeapMux: the desktop app for macOS, Linux, and Windows, the standalone leapmux binary, Docker images, and building from source."
type: docs
weight: 2
---

Pick the installation method that matches how you plan to run LeapMux:

- **Desktop app** — the fastest way to run LeapMux on your own machine. Download one installer, run it, and start a local Hub and Worker with no network setup.
- **Standalone binary** — the pre-built `leapmux` command-line interface (CLI) binary, for running any mode on a host without Docker and without compiling.
- **Docker** — pre-built multi-architecture images for running a Hub or Worker as a long-lived service on a server.
- **From source** — for administrators and developers who want to compile the binary, Docker images, or desktop app themselves.

{{< callout >}}
If you just want to run LeapMux, the pre-built desktop app installers, standalone binaries, and Docker images are all you need.
{{< /callout >}}

## Which install should I choose?

| Your goal | Choose | Why |
|-----------|--------|-----|
| Try LeapMux on your own laptop, run agents locally | **Desktop app (solo mode)** | One installer, no server, no login. Your data stays on your machine. |
| Run a shared Hub for a team, or a server you reach remotely | **Docker** | Supervised, persistent, multi-arch images. Run as `hub` (central service) or `dev` (Hub + Worker together). |
| Connect your laptop's desktop app to a team Hub | **Desktop app (distributed mode)** | Same installer; on launch you choose **Distributed** and enter the Hub URL. |
| Run a Hub or Worker on a host without Docker | **Standalone binary** | A single self-contained `leapmux` executable you run directly. |
| Modify LeapMux, or build your own images/artifacts | **From source** | Full control over the build via `task` targets. |

For a side-by-side comparison of deployment shapes, see [Ways to Run LeapMux](/docs/getting-started/ways-to-run/). For operational details on each run mode, see [Running LeapMux](/docs/admin/running-leapmux/).

## Supported platforms

LeapMux is developed and tested natively on macOS, Linux, and Windows.

| Platform | Architectures | Desktop app artifact |
|----------|---------------|----------------------|
| macOS    | arm64         | `.dmg`               |
| Linux    | amd64, arm64  | `.AppImage`, `.deb`  |
| Windows  | amd64         | `.msi`               |

Pre-built Docker images target `linux/amd64` and `linux/arm64`.

{{< callout type="info" >}}
The standalone server binary for macOS is distributed as `darwin_arm64` only.
{{< /callout >}}

## Desktop app

The desktop app bundles the LeapMux Frontend and an embedded local service. On first launch it opens a **LeapMux** launcher asking how you want to connect:

- **Solo** — run a Hub and Worker together on this machine over local inter-process communication (IPC), with no network setup.
- **Distributed** — connect to a remote Hub by URL and sign in through your system browser.

Download the installer for your platform from the [Releases page](https://github.com/leapmux/leapmux/releases). Artifact names follow these patterns (where `<version>` is the release version):

| Platform | Artifact |
|----------|----------|
| macOS (arm64)   | `LeapMuxDesktop_<version>_arm64.dmg` |
| Windows (amd64) | `LeapMuxDesktop_<version>_x64.msi` |
| Linux (amd64)   | `leapmux-desktop_<version>_x86_64.AppImage`, `leapmux-desktop_<version>_amd64.deb` |
| Linux (arm64)   | `leapmux-desktop_<version>_aarch64.AppImage`, `leapmux-desktop_<version>_arm64.deb` |

{{< tabs >}}
{{< tab name="macOS" selected=true >}}
1. Download `LeapMuxDesktop_<version>_arm64.dmg`.
2. Open the `.dmg` and drag **LeapMux Desktop** into your Applications folder.
3. Launch **LeapMux Desktop**.

The macOS build is signed with a Developer ID and notarized, so it opens without Gatekeeper warnings.

{{< callout type="info" >}}
Solo mode on macOS requires **Full Disk Access** so LeapMux can traverse directories in your home folder. The first time you select **Solo**, the launcher shows a **Full Disk Access Required** card with an **Open System Settings** button. Grant access there. The app detects the change and restarts itself. Distributed mode does not require Full Disk Access.
{{< /callout >}}

{{< callout >}}
On macOS, the desktop app can install a `leapmux` symlink at `/usr/local/bin/leapmux` so you can use the [Control CLI](/docs/using/control-cli/) from a terminal. When you enter a Solo workspace and `leapmux` is not on your `PATH`, LeapMux offers to install it. This integration is macOS-only.
{{< /callout >}}
{{< /tab >}}

{{< tab name="Linux" >}}
Two formats are provided; pick the one that fits your distribution:

- **`.deb`** — for Debian and Ubuntu systems. The package also installs the `leapmux` CLI binary at `/usr/bin/leapmux`.

  ```bash
  sudo apt install ./leapmux-desktop_<version>_amd64.deb
  ```

  After installation, launch **LeapMux Desktop** from your application menu.

- **`.AppImage`** — a portable, single-file executable that needs no installation:

  ```bash
  chmod +x leapmux-desktop_<version>_x86_64.AppImage
  ./leapmux-desktop_<version>_x86_64.AppImage
  ```
{{< /tab >}}

{{< tab name="Windows" >}}
1. Download `LeapMuxDesktop_<version>_x64.msi`.
2. Run the installer. It is **per-user** — it requires no administrator elevation and installs under `%LOCALAPPDATA%\Programs\LeapMux Desktop`.
3. Launch **LeapMux Desktop** from the Start menu or desktop shortcut.

The installer also ships `leapmux.exe` in a `cli\` subdirectory and adds that directory to your user `PATH`. WebView2 installs automatically if it is missing.
{{< /tab >}}
{{< /tabs >}}

{{< callout type="info" >}}
The desktop app has no built-in auto-updater. To upgrade, download the newer installer from the Releases page and install it over the existing version.
{{< /callout >}}

## Standalone binary

Each release publishes a **LeapMux Server** archive on the [Releases page](https://github.com/leapmux/leapmux/releases) containing the pre-built `leapmux` binary alongside `LICENSE.md` and `NOTICE.md`. One binary serves every run mode (`solo`, `hub`, `worker`, `dev`) and every control command.

| Platform | Artifact |
|----------|----------|
| macOS (arm64)   | `leapmux_<version>_darwin_arm64.tar.gz` |
| Linux (amd64)   | `leapmux_<version>_linux_amd64.tar.gz` |
| Linux (arm64)   | `leapmux_<version>_linux_arm64.tar.gz` |
| Windows (amd64) | `leapmux_<version>_windows_amd64.zip` |

{{< tabs >}}
{{< tab name="macOS & Linux" selected=true >}}
Download the `.tar.gz` archive for your OS and architecture, extract it, and place `leapmux` on your `PATH` (for example `/usr/local/bin`):

```bash
tar -xzf leapmux_<version>_linux_amd64.tar.gz
cd leapmux_<version>_linux_amd64
./leapmux daemon hub start        # Start a Hub in the background as a service.
./leapmux daemon hub stop         # Stop it.
```

The macOS package is signed with a Developer ID and notarized.
{{< /tab >}}

{{< tab name="Windows" >}}
Download `leapmux_<version>_windows_amd64.zip`, extract it, and add the folder containing `leapmux.exe` to your `PATH`:

```powershell
Expand-Archive .\leapmux_<version>_windows_amd64.zip -DestinationPath .\leapmux
cd .\leapmux
.\leapmux.exe daemon hub start    # Start a Hub in the background as a service.
.\leapmux.exe daemon hub stop     # Stop it.
```
{{< /tab >}}
{{< /tabs >}}

The same `start` and `stop` commands work for `solo` and `dev`. A service keeps running after you log out and restarts after a reboot; see [Starting and Stopping](/docs/admin/starting-and-stopping/). Register a standalone Worker before starting it; see [Register a Worker](/docs/admin/managing-workers/#register-a-worker).

{{< callout >}}
Put `leapmux` on your `PATH` so you can invoke it from any directory and use the [Control CLI](/docs/using/control-cli/). To upgrade, replace the binary with the newer release; Hub and Worker database migrations run automatically on startup.
{{< /callout >}}

## Docker

Pre-built multi-architecture images (`linux/amd64` and `linux/arm64`) are published to [GitHub Container Registry (GHCR)](https://github.com/leapmux/leapmux/pkgs/container/leapmux) at `ghcr.io/leapmux/leapmux`.

- **`LEAPMUX_MODE`** — **required.** Selects the subcommand: `hub`, `worker`, `dev`, or `solo`.
- **`LEAPMUX_DATA_DIR`** — data directory inside the container. Defaults to `/data`.
- The image **exposes port `4327`**, the single TCP listen port for `hub`, `dev`, and `solo`. A `worker` makes only outbound connections and needs no inbound port.
- State and configuration live under `/data/<mode>/` (for example `/data/hub/` for the Hub database, encryption key ring, and config file). Mount a persistent volume at `/data`.

{{< tabs >}}
{{< tab name="Hub (central service)" selected=true >}}
```bash
# Run as a central Hub — login required
docker run -p 4327:4327 \
  -e LEAPMUX_MODE=hub \
  -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```
{{< /tab >}}

{{< tab name="All-in-one (dev)" >}}
```bash
# Run Hub + Worker together — listens on all interfaces, login required
docker run -p 4327:4327 \
  -e LEAPMUX_MODE=dev \
  -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```
{{< /tab >}}

{{< tab name="Upgrade" >}}
Pull a newer tag and recreate the container against the **same `/data` volume**. Database migrations run automatically on startup:

```bash
docker pull ghcr.io/leapmux/leapmux:latest
# Stop and remove the old container, then re-run with the same -v leapmux-data:/data
```
{{< /tab >}}
{{< /tabs >}}

For the full image-tag matrix (Alpine vs. Ubuntu variants, version pinning, `:dev`), container restart policies, and the `/data` layout, see [Running LeapMux](/docs/admin/running-leapmux/). Configure options beyond `LEAPMUX_MODE` and `LEAPMUX_DATA_DIR` through the per-mode YAML file or `LEAPMUX_HUB_*` / `LEAPMUX_WORKER_*` environment variables; see [Configuration](/docs/admin/configuration/).

{{< callout type="info" >}}
Use `dev` (not `solo`) for an all-in-one container. In `solo` mode the binary defaults to binding loopback only (`127.0.0.1:4327`), so the port is unreachable from outside the container unless you override the listen address in `/data/solo/solo.yaml`. `dev` binds all interfaces (`:4327`).
{{< /callout >}}

{{< callout type="warning" >}}
The Hub does not terminate Transport Layer Security (TLS) itself. To serve LeapMux over HTTPS, put a reverse proxy in front of the container and configure `public_url`, `secure_cookies`, and `trusted_proxy_ranges` with `leapmux admin settings set`. See [Reverse Proxy](/docs/admin/reverse-proxy/) for tested configurations.
{{< /callout >}}

### Running a Worker container

A `worker` container dials out to a Hub. Register it once with a key from the Hub's **Register worker** dialog in a temporary container that mounts the same volume, then start the Worker container. [Running a Worker container](/docs/admin/running-leapmux/#running-a-worker-container) gives both commands.

## From source

Building from source lets you compile the binary, Docker images, or desktop app yourself. The full developer guide lives in the project [`README.md`](https://github.com/leapmux/leapmux).

### Prerequisites

- **Go** 1.27.4 or later
- **Node.js** 26 or later
- **Bun** 1.4.4 or later — JavaScript runtime and package manager
- **Task** — the build runner
- **buf** CLI — Protocol Buffer code generation
- **protobuf** (`protoc`) — required by Tauri's `prost-build`
- **SQLite** (usually pre-installed)
- **mprocs** — needed for `task dev`, `task dev-solo`, and `task dev-desktop`
- **Rust toolchain** and your platform's Tauri WebView/system packages — only for the desktop app
- **Docker** — only for building Docker images

The Go tools `sqlc`, `golangci-lint`, and `gotestsum` each live in a separate module under `tools/`. Task builds and runs them automatically, so you do not need to install them separately.

### Build and run

```bash
git clone https://github.com/leapmux/leapmux.git
cd leapmux
task generate   # generate proto + sqlc code and download spinner assets (not checked into git)
task dev        # run Hub + Worker for development (requires mprocs)
# then open http://localhost:4327
```

Common build targets:

| Command | What it builds |
|---------|----------------|
| `task build` | The full stack (backend + frontend + desktop) |
| `task build-backend` | The `leapmux` binary (output to the repository root; `.exe` on Windows) |
| `task build-frontend` | The web frontend |
| `task build-desktop` | The Tauri desktop app |
| `task docker-build` | Both Alpine and Ubuntu Docker images |

For platform-specific dependency install commands (Homebrew, pacman, winget), see the project [`README.md`](https://github.com/leapmux/leapmux).

## Next steps

- [Quick Start](/docs/getting-started/quick-start/) — launch LeapMux and run your first parallel agent session.
- [Ways to Run LeapMux](/docs/getting-started/ways-to-run/) — compare Solo, remote Workers behind NAT, and multi-user Hubs.
- [Running LeapMux](/docs/admin/running-leapmux/) — run modes, ports, data directories, Docker, and reverse proxies.
- [Managing Workers](/docs/admin/managing-workers/) — register, pin, and manage Workers connected to a Hub.
