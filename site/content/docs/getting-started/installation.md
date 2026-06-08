---
title: "Installation"
type: docs
weight: 3
---

There are several ways to get LeapMux. Pick the one that matches how you want to run it:

- **Desktop app** — the fastest way to try LeapMux on your own machine. Download a single installer, run it, and you have a Hub and Worker running together locally with no network setup.
- **Docker** — the recommended way to run a Hub (or a Hub plus Worker) as a long-lived service for a team or on a server.
- **Standalone binary** — the pre-built `leapmux` command-line binary, for running any mode on a host without Docker and without compiling.
- **From source** — for contributors and operators who want to build the binary, images, or desktop app themselves.

> **Tip:** If you just want to run LeapMux, the pre-built Docker images, desktop app installers, and standalone binaries are all you need.

## Which install should I choose?

| Your goal | Choose | Why |
|-----------|--------|-----|
| Try LeapMux on your own laptop, run agents locally | **Desktop app (solo mode)** | One installer, no server, no login. Your data stays on your machine. |
| Run a shared Hub for a team, or a server you reach remotely | **Docker** | Supervised, persistent, multi-arch images. Run as `hub` (central service) or `dev` (Hub + Worker together). |
| Connect your laptop's desktop app to a team Hub | **Desktop app (distributed mode)** | Same installer; on launch you choose **Distributed** and enter the Hub URL. |
| Run a Hub or Worker on a host without Docker | **Standalone binary** | A single self-contained `leapmux` executable you run directly. |
| Modify LeapMux, or build your own images/artifacts | **From source** | Full control over the build via `task` targets. |

For more on what each run mode does (solo vs. hub vs. worker vs. dev) and how to operate them, see [Running LeapMux](/docs/operating/running-leapmux/). For your very first session once LeapMux is installed, see [Quick Start](/docs/getting-started/quick-start/).

## Supported platforms

LeapMux is developed and tested natively on macOS, Linux, and Windows.

| Platform | Architectures | Desktop app artifact |
|----------|---------------|----------------------|
| macOS    | arm64         | `.dmg`               |
| Linux    | amd64, arm64  | `.AppImage`, `.deb`  |
| Windows  | amd64         | `.msi`               |

Pre-built Docker images target `linux/amd64` and `linux/arm64`.

> **Note:** The standalone server binary for macOS is distributed as `darwin_arm64` only.

## Desktop app

The desktop app is a self-contained application that bundles the LeapMux Frontend and a local service. On first launch it presents a **LeapMux** launcher asking how you want to connect — **Solo** (a Hub and Worker run together on this machine; no network setup, your data stays local) or **Distributed** (connect to a remote Hub by URL). See [Quick Start](/docs/getting-started/quick-start/) for what to do after the app opens.

### Download

Download the installer for your platform from the [Releases page](https://github.com/leapmux/leapmux/releases). Artifact names follow these patterns (where `<version>` is the release version):

| Platform | Artifact |
|----------|----------|
| macOS (arm64)        | `LeapMuxDesktop_<version>_arm64.dmg` |
| Windows (amd64)      | `LeapMuxDesktop_<version>_x64.msi` |
| Linux (amd64)        | `leapmux-desktop_<version>_amd64.AppImage`, `leapmux-desktop_<version>_amd64.deb` |
| Linux (arm64)        | `leapmux-desktop_<version>_aarch64.AppImage`, `leapmux-desktop_<version>_arm64.deb` |

### macOS

1. Download the `.dmg`.
2. Open it and drag **LeapMux Desktop** into your Applications folder.
3. Launch **LeapMux Desktop**.

The macOS build is signed with a Developer ID and notarized, so it opens without Gatekeeper warnings.

> **Note:** Solo mode on macOS requires **Full Disk Access** so LeapMux can traverse directories in your home folder. The first time you select **Solo**, the launcher shows a **Full Disk Access Required** card with an **Open System Settings** button. Grant access there; the app detects the change and restarts itself automatically. Distributed mode does not require Full Disk Access.

> **Tip:** On macOS the desktop app can install a `leapmux` command-line symlink at `/usr/local/bin/leapmux` so you can use the [Remote control CLI](/docs/operating/remote-control-cli/) from a terminal. When you enter a Solo workspace and the CLI isn't on your `PATH`, LeapMux offers to install it. This integration is macOS-only.

### Linux

Two formats are provided; pick the one that suits your distribution:

- **`.deb`** — for Debian/Ubuntu-based systems. Install with your package manager, for example:

  ```bash
  sudo apt install ./leapmux-desktop_<version>_amd64.deb
  ```

  The `.deb` also places the `leapmux` command-line binary at `/usr/bin/leapmux`.

- **`.AppImage`** — a portable, single-file executable that needs no installation:

  ```bash
  chmod +x leapmux-desktop_<version>_amd64.AppImage
  ./leapmux-desktop_<version>_amd64.AppImage
  ```

After installing the `.deb`, launch **LeapMux Desktop** from your application menu (the desktop entry's command is `leapmux-desktop`).

### Windows

1. Download the `.msi`.
2. Run it. The installer is **per-user** — it requires no administrator elevation and installs under `%LOCALAPPDATA%\Programs\LeapMux Desktop`.
3. Launch **LeapMux Desktop** from the Start menu or the desktop shortcut.

The installer also ships the `leapmux.exe` command-line tool in a `cli\` subdirectory and adds that directory to your user `PATH`. WebView2 is installed automatically if it isn't already present.

> **Note:** The desktop app has no built-in auto-updater. To upgrade, download the newer installer from the Releases page and install it over the existing version.

## Docker

Pre-built multi-arch images (`linux/amd64` + `linux/arm64`) are published to [GHCR](https://github.com/leapmux/leapmux/pkgs/container/leapmux) at `ghcr.io/leapmux/leapmux`. The image is configured through two environment variables and the `/data` volume:

- **`LEAPMUX_MODE`** — **required.** Selects the subcommand and must be one of `hub`, `worker`, `dev`, or `solo`.
- **`LEAPMUX_DATA_DIR`** — data directory inside the container. Default `/data`.
- The image **exposes port `4327`**, the single TCP listen port for `hub`, `dev`, and `solo`. (A `worker` makes only outbound connections to a Hub, so it needs no inbound port.)
- State and configuration live under `/data/<mode>/` — e.g. `/data/hub/` for the Hub database, encryption key ring, and config file. Mount a volume at `/data` so this survives container recreation.

### Run examples

```bash
# Run as a hub (central service only) — login required
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest

# Run as hub + worker together (dev mode) — listens on all interfaces, login required
docker run -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

For the full image-tag matrix (Alpine vs. Ubuntu variants, version pinning, the `:dev` tag), the s6-overlay supervision and startup mechanics, and the `/data` volume layout, see [Running LeapMux](/docs/operating/running-leapmux/), which is canonical for Docker operations. Anything beyond `LEAPMUX_MODE` and `LEAPMUX_DATA_DIR` is configured through the per-mode YAML file or through `LEAPMUX_HUB_*` / `LEAPMUX_WORKER_*` environment variables — see [Configuration](/docs/operating/configuration/).

> **Note:** Use `dev` (not `solo`) for an all-in-one container. In `solo` mode the binary defaults to binding loopback only (`127.0.0.1:4327`), so the port is not reachable from outside the container unless you override the listen address in `/data/solo/solo.yaml`. `dev` mode binds all interfaces (`:4327`) and is the container-friendly all-in-one variant.

> **Warning:** The Hub does not terminate TLS itself. To serve LeapMux over HTTPS, put a reverse proxy in front of the container and set `public_url` and `secure_cookies` in the Hub config. See [Running LeapMux](/docs/operating/running-leapmux/) and [Configuration](/docs/operating/configuration/) for reverse-proxy guidance.

### Running a Worker container

A `worker` container connects out to a Hub and must be registered with a registration key minted in the Hub UI. The container doesn't pass any Hub URL or key flags on your behalf, so supply them via the Worker config (`/data/worker/worker.yaml`) or via `LEAPMUX_WORKER_HUB` and `LEAPMUX_WORKER_REGISTRATION_KEY` environment variables. An unregistered Worker with no key exits with `worker is unregistered: pass --registration-key <key> from the hub UI`. See [Managing Workers](/docs/operating/managing-workers/) for the full registration flow.

### Upgrading

Pull a newer tag and recreate the container against the **same `/data` volume**. Database migrations run automatically on startup, so no separate migration command is needed:

```bash
docker pull ghcr.io/leapmux/leapmux:latest
# stop and remove the old container, then re-run with the same -v leapmux-data:/data
```

## Standalone binary

Each release also publishes a **LeapMux Server** package on the [Releases page](https://github.com/leapmux/leapmux/releases): the pre-built `leapmux` command-line binary, bundled with `LICENSE.md` and `NOTICE.md`. This is the simplest way to run a Hub, Worker, dev, or solo instance on a host where you don't want Docker and don't want to compile from source. The same binary serves every run mode via its subcommands — see the [CLI reference](/docs/reference/cli-reference/).

| Platform | Artifact |
|----------|----------|
| macOS (arm64)        | `leapmux_<version>_darwin_arm64.tar.gz` |
| Linux (amd64)        | `leapmux_<version>_linux_amd64.tar.gz` |
| Linux (arm64)        | `leapmux_<version>_linux_arm64.tar.gz` |
| Windows (amd64)      | `leapmux_<version>_windows_amd64.zip` |

Download the archive for your platform, extract it, and run the `leapmux` executable. For example, on Linux:

```bash
tar -xzf leapmux_<version>_linux_amd64.tar.gz
cd leapmux_<version>_linux_amd64
./leapmux hub          # or: solo, worker, dev
```

The macOS package is signed with a Developer ID and notarized.

> **Tip:** Put `leapmux` somewhere on your `PATH` (e.g. `/usr/local/bin`) so you can invoke it from any directory and use the [Remote control CLI](/docs/operating/remote-control-cli/). To upgrade, replace the binary with the one from a newer release archive; Hub and Worker database migrations run automatically the next time you start.

For run modes, ports, and data directories, see [Running LeapMux](/docs/operating/running-leapmux/); for the full subcommand and flag reference, see [CLI Reference](/docs/reference/cli-reference/).

## From source

Building from source is for contributors and operators who want to compile the binary, Docker images, or desktop app themselves. The full developer guide lives in the project [`README.md`](https://github.com/leapmux/leapmux); this section is a high-level pointer.

### Prerequisites

- **Go** 1.26.1 or later
- **Node.js** 24 or later
- **Bun** (latest) — JavaScript runtime and package manager
- **Task** — the build runner
- **buf** CLI — Protocol Buffer code generation
- **protobuf** (`protoc`) — required by Tauri's `prost-build`
- **SQLite** (usually pre-installed)
- **mprocs** — needed for `task dev`, `task dev-solo`, and `task dev-desktop`
- **Rust toolchain** and your platform's Tauri WebView/system packages — only for the desktop app
- **Docker** — only for building Docker images

The Go-based tools `sqlc`, `golangci-lint`, and `gotestsum` are declared as `tool` dependencies in `go.mod` and invoked via `go tool <name>`; you don't install them separately.

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

For the platform-specific dependency install commands (Homebrew, pacman, winget) and the full set of dev workflows, follow the project [`README.md`](https://github.com/leapmux/leapmux). For what each run mode does once built, see [Running LeapMux](/docs/operating/running-leapmux/).

## Next steps

- [Quick Start](/docs/getting-started/quick-start/) — launch LeapMux and open your first agent.
- [Running LeapMux](/docs/operating/running-leapmux/) — run modes, ports, data directories, Docker, and reverse proxies in depth.
- [Configuration](/docs/operating/configuration/) — the full configuration key reference and storage backends.
- [Managing Workers](/docs/operating/managing-workers/) — register and approve Workers connecting to a Hub.
</content>
</invoke>
