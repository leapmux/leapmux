---
title: "Installation"
description: "Install LeapMux with the desktop app for macOS, Linux, and Windows, the standalone leapmux binary, Docker images, or a source build."
type: docs
weight: 2
---

Choose the installation method that fits how you plan to run LeapMux:

| Your goal | Choose | Why |
| --- | --- | --- |
| Run agents on your own laptop with zero setup | **Desktop app (Solo)** | One installer with no server and no login. |
| Connect your laptop to a remote or team Hub | **Desktop app (Distributed)** | Same installer; pick **Distributed** on launch and enter the Hub URL. |
| Run a Hub, Worker, or local daemon without Docker | **Standalone binary** | Single `leapmux` executable for every mode and control command. |
| Host a central Hub or container service on a server | **Docker** | Multi-architecture images for `linux/amd64` and `linux/arm64`. |
| Modify LeapMux or build custom artifacts | **Building from source** | Full control through `task` build targets. |

For a topology decision tree and mode comparison, see [Concepts & Topologies](/docs/getting-started/concepts/).

## Desktop app

**LeapMux Desktop** bundles the Frontend and an embedded local service. On first launch, choose **Solo** (a local Hub and Worker over inter-process communication (IPC)) or **Distributed** (connect to a remote Hub by URL).

{{< screenshot src="launcher-view" alt="LeapMux Desktop launcher showing Solo and Distributed connection mode cards and a Connect button" caption="Choose Solo for a single-machine local workspace or Distributed to connect to a remote LeapMux Hub." width="920" height="640" >}}

Download the installer from the [Releases page](https://github.com/leapmux/leapmux/releases). Where `<version>` is the release version:

| Platform | Artifact | Bundled CLI |
| --- | --- | --- |
| macOS (`arm64`) | `LeapMuxDesktop_<version>_arm64.dmg` | Offers to install symlink `/usr/local/bin/leapmux` on first Solo workspace |
| Windows (`amd64`) | `LeapMuxDesktop_<version>_x64.msi` | Installs `cli\leapmux.exe` and adds it to your user `PATH` |
| Linux `.deb` (`amd64`, `arm64`) | `leapmux-desktop_<version>_amd64.deb`, `leapmux-desktop_<version>_arm64.deb` | Installs `/usr/bin/leapmux` |
| Linux `.AppImage` (`amd64`, `arm64`) | `leapmux-desktop_<version>_x86_64.AppImage`, `leapmux-desktop_<version>_aarch64.AppImage` | **None** — use `.deb` or the standalone binary if you need the CLI or Agent Skills |

{{< tabs >}}
{{< tab name="macOS" selected=true >}}
1. Download `LeapMuxDesktop_<version>_arm64.dmg`, open it, and drag **LeapMux Desktop** into **Applications**.
2. Launch **LeapMux Desktop**. The build is signed with a Developer ID and notarized.
3. In Solo mode, grant **Full Disk Access** when prompted so LeapMux can browse your home directory. Distributed mode does not require Full Disk Access.
{{< /tab >}}

{{< tab name="Linux" >}}
- **`.deb`** (recommended on Debian and Ubuntu; includes `/usr/bin/leapmux`):

  ```bash
  sudo apt install ./leapmux-desktop_<version>_amd64.deb
  ```

- **`.AppImage`** (portable executable; bundles no `leapmux` CLI):

  ```bash
  chmod +x leapmux-desktop_<version>_x86_64.AppImage
  ./leapmux-desktop_<version>_x86_64.AppImage
  ```
{{< /tab >}}

{{< tab name="Windows" >}}
1. Download and run `LeapMuxDesktop_<version>_x64.msi`. The installer runs per-user under `%LOCALAPPDATA%\Programs\LeapMux Desktop` without administrator elevation.
2. Launch **LeapMux Desktop** from the Start menu. WebView2 installs automatically if missing.
{{< /tab >}}
{{< /tabs >}}

The desktop app does not auto-update. To upgrade, download the newer installer and install it over the existing version.

## Standalone binary

Each release publishes a **LeapMux Server** archive on the [Releases page](https://github.com/leapmux/leapmux/releases). One binary serves every run mode (`solo`, `hub`, `worker`, `dev`) and every control command.

| Platform | Artifact | Notes |
| --- | --- | --- |
| macOS (`arm64`) | `leapmux_<version>_darwin_arm64.tar.gz` | Signed and notarized; includes `lmx` symlink |
| Linux (`amd64`, `arm64`) | `leapmux_<version>_linux_amd64.tar.gz`, `leapmux_<version>_linux_arm64.tar.gz` | Includes `lmx` symlink |
| Windows (`amd64`) | `leapmux_<version>_windows_amd64.zip` | Contains `leapmux.exe` only (no `lmx` symlink) |

Place `leapmux` on your `PATH` so you can invoke it from any directory and use the [CLI Guide](/docs/using/cli-guide/). Use `leapmux up` and `leapmux down` to start or stop solo mode as a background service:

{{< tabs >}}
{{< tab name="macOS & Linux" selected=true >}}
```bash
tar -xzf leapmux_<version>_linux_amd64.tar.gz
cd leapmux_<version>_linux_amd64
./leapmux up      # Start solo mode in the background (same as: daemon solo start)
./leapmux down    # Stop it (same as: daemon solo stop)
```
{{< /tab >}}

{{< tab name="Windows" >}}
`Expand-Archive` extracts into a nested versioned folder (`.\leapmux\leapmux_<version>_windows_amd64\`):

```powershell
Expand-Archive .\leapmux_<version>_windows_amd64.zip -DestinationPath .\leapmux
cd .\leapmux\leapmux_<version>_windows_amd64
.\leapmux.exe up      # Start solo mode in the background
.\leapmux.exe down    # Stop it
```
{{< /tab >}}
{{< /tabs >}}

For distributed deployments, use `leapmux daemon hub start` on your Hub host and `leapmux daemon worker register` followed by `leapmux daemon worker start` on each Worker host. See [Starting and Stopping](/docs/admin/starting-and-stopping/) and [Managing Workers](/docs/admin/managing-workers/). To upgrade, replace the binary with the newer release; database migrations run automatically on startup.

## Docker

Multi-architecture images (`linux/amd64` and `linux/arm64`) are published to GitHub Container Registry (GHCR) at `ghcr.io/leapmux/leapmux`.

- **`LEAPMUX_MODE`** (required) — selects `hub`, `worker`, `dev`, or `solo`.
- **`LEAPMUX_DATA_DIR`** — data directory inside the container (default `/data`). Mount a persistent volume at `/data`.
- Port **`4327`** is the single TCP port for `hub` and `dev`. A `worker` makes only outbound connections and needs no inbound port.

{{< tabs >}}
{{< tab name="Hub (central service)" selected=true >}}
```bash
docker run -p 4327:4327 \
  -e LEAPMUX_MODE=hub \
  -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```
{{< /tab >}}

{{< tab name="All-in-one (dev)" >}}
```bash
# Runs Hub + Worker together on all interfaces (:4327) with login enabled
docker run -p 4327:4327 \
  -e LEAPMUX_MODE=dev \
  -v leapmux-data:/data \
  ghcr.io/leapmux/leapmux:latest
```
{{< /tab >}}

{{< tab name="Upgrade" >}}
Pull the new tag and recreate the container with the same `/data` volume. Database migrations run automatically on startup:

```bash
docker pull ghcr.io/leapmux/leapmux:latest
```
{{< /tab >}}
{{< /tabs >}}

Use `dev` rather than `solo` for an all-in-one container because `solo` listens on loopback (`127.0.0.1:4327`) by default. For image variants, Worker containers, and configuration options, see [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration Guide](/docs/admin/configuration/).

{{< callout type="warning" >}}
The Hub does not terminate Transport Layer Security (TLS) itself. Place an HTTPS reverse proxy in front of any network-reachable Hub and follow the [Production Checklist](/docs/admin/production-checklist/) and [Reverse Proxy](/docs/admin/reverse-proxy/) guides.
{{< /callout >}}

## Building from source

Clone the [LeapMux repository](https://github.com/leapmux/leapmux) and follow its `README.md` for toolchain prerequisites and platform packages. Run `task generate` followed by `task build` to compile the stack, or `task dev` (using `dekit`) to run the Hub and Worker locally during development.

## Next steps

- [Quick Start](/docs/getting-started/quick-start/) — run your first parallel agent session in under five minutes.
- [Concepts & Topologies](/docs/getting-started/concepts/) — choose between Solo Desktop, Solo Daemon, and Distributed Hub + Workers.
- [Production Checklist](/docs/admin/production-checklist/) — harden a self-hosted Hub before exposing it to your team.
