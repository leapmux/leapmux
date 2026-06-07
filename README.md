<div align="center">
  <img src="icons/leapmux-icon.svg" alt="LeapMux" width="128" height="128">
</div>

# LeapMux

[![Docs](https://img.shields.io/badge/docs-leapmux.dev-0d9488)](https://leapmux.dev/)
[![Release](https://img.shields.io/github/v/release/leapmux/leapmux?include_prereleases&label=release)](https://github.com/leapmux/leapmux/releases)
[![Container](https://img.shields.io/badge/container-ghcr.io%2Fleapmux%2Fleapmux-2496ED?logo=docker&logoColor=white)](https://github.com/leapmux/leapmux/pkgs/container/leapmux)
[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)


A terminal works fine for one or two coding agents side-by-side. At three or four — one refactoring, one on tests, one chasing a failing build — shell tabs stop helping: you lose track of which one owns which branch, the agents clobber each other's working tree, and a stray tmux crash or dev-box reboot means re-launching each agent with `--resume` and rebuilding the layout by hand.

LeapMux is a workspace for running several coding agents and shell terminals at once, each in a git worktree and branch you pick, tiled or floating, on a local or remote machine. Sessions stay attached across restarts, and Frontend↔Worker traffic is end-to-end encrypted. Runs in the browser or as a native desktop app.

## Supported Agents

<p>
  <a href="https://claude.com/product/claude-code"><img src="icons/agents/claude-code.svg" width="64" height="64" title="Claude Code"></a>&nbsp;
  <a href="https://openai.com/codex/"><img src="icons/agents/codex.svg" width="64" height="64" title="Codex"></a>&nbsp;
  <a href="https://geminicli.com/"><img src="icons/agents/gemini-cli.svg" width="64" height="64" title="Gemini CLI"></a>&nbsp;
  <a href="https://cursor.com/cli"><img src="icons/agents/cursor.svg" width="64" height="64" title="Cursor"></a>&nbsp;
  <a href="https://github.com/features/copilot/cli"><img src="icons/agents/github-copilot.svg" width="64" height="64" title="GitHub Copilot"></a>&nbsp;
  <a href="https://opencode.ai/"><img src="icons/agents/opencode.svg" width="64" height="64" title="OpenCode"></a>&nbsp;
  <a href="https://pi.dev/"><img src="icons/agents/pi.svg" width="64" height="64" title="Pi"></a>
  <a href="https://kilo.ai/cli"><img src="icons/agents/kilo.svg" width="64" height="64" title="Kilo"></a>&nbsp;
  <a href="https://block.github.io/goose/"><img src="icons/agents/goose.svg" width="64" height="64" title="Goose"></a>&nbsp;
</p>

> **📖 Want to use LeapMux?**
>
> Read the docs and grab a download at **[leapmux.dev](https://leapmux.dev)**. The rest of this README covers building and developing LeapMux from source.

## Table of Contents

- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Development](#development)
- [Technology Stack](#technology-stack)
- [Project Structure](#project-structure)
- [Contributing](#contributing)
- [License](#license)
- [Disclaimer](#disclaimer)

## Architecture

LeapMux is a single Go binary (`leapmux`) plus a SolidJS frontend and a Tauri desktop shell, organized into three components:

- **Frontend** — SolidJS web app that renders the workspace UI (tiling layout, agents, terminals, file browser); also embedded in the desktop app.
- **Hub** — Go service for login, workspace management, and worker registration, and an authenticated **relay** for end-to-end-encrypted Frontend↔Worker traffic. Storage is pluggable: SQLite (default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB.
- **Worker** — Go process that runs agents, PTYs, file browsing, and git operations; keeps its own SQLite and connects **outbound** to the Hub, so it can live behind a NAT.

The binary runs in several modes:

| Command | Description |
|---------|-------------|
| `leapmux solo` | Hub + Worker on `127.0.0.1:4327`, no login, single-user |
| `leapmux hub` | Central service only (auth, relay, database) |
| `leapmux worker` | Connects to a remote Hub |
| `leapmux dev` | Hub + Worker on all interfaces, login required |
| `leapmux admin` | CLI for orgs, users, workers, OAuth, encryption keys, and the database |

Frontend↔Hub uses ConnectRPC; Frontend↔Worker uses hybrid post-quantum Noise_NK multiplexed over a single Hub-relayed WebSocket; Worker↔Hub uses gRPC. The Hub routes traffic but can't read Frontend↔Worker content. The wire format is Protocol Buffers in [`/proto/leapmux/v1/`](proto/leapmux/v1/).

For the full architecture, deployment modes, and threat model, see the **[Concepts](https://leapmux.dev/docs/02-concepts/)** and **[Security & Threat Model](https://leapmux.dev/docs/23-security-and-threat-model/)** chapters at [leapmux.dev](https://leapmux.dev).

## Prerequisites

The rest of this README is for people building LeapMux from source or hacking on it. To just run LeapMux, grab a desktop app or server build from the [releases page](https://github.com/leapmux/leapmux/releases), or read the docs at [leapmux.dev](https://leapmux.dev).

Before you begin, ensure you have the following installed:

- **Go** 1.26.1 or later
- **Node.js** 24 or later
- **Bun** (latest version) - JavaScript runtime and package manager
- **Task** - Task runner (replaces Make)
- **buf** CLI - Protocol Buffer code generation ([authentication](https://buf.build/docs/bsr/authentication/) recommended to avoid rate-limit errors)
- **protobuf** (`protoc`) - Protocol Buffer compiler (required by Tauri's `prost-build`)
- **SQLite** (usually pre-installed on most systems)
- **Docker** - Required for building Docker images (on macOS, [Rancher Desktop](https://rancherdesktop.io/) is recommended)
- **mprocs** - Multi-process runner (required for `task dev`, `task dev-solo`, and `task dev-desktop`)
- **Rust toolchain** - For the Tauri desktop app (built by `task build`)
- **Tauri desktop prerequisites** - WebView/system packages required by Tauri on your platform

Go-based build tools — `sqlc`, `golangci-lint`, and `gotestsum` — are declared as `tool` dependencies in `backend/go.mod` and `desktop/go/go.mod`, and invoked automatically via `go tool <name>`. You don't need to install them separately.

### macOS

Install [Bun](https://bun.sh/) by following the instructions at https://bun.sh/.

Install the remaining dependencies with [Homebrew](https://brew.sh/):

```bash
brew install buf go go-task mprocs node protobuf rust
```

For building Docker images, install [Rancher Desktop](https://rancherdesktop.io/) (or any Docker-compatible runtime such as Docker Desktop or OrbStack) separately.

### Arch Linux

Install the official repository packages with [pacman](https://wiki.archlinux.org/title/Pacman):

```bash
sudo pacman -S buf bun go go-task nodejs npm protobuf rust
```

The Arch `go-task` package installs the binary as `go-task`. Add a shell alias so that `task` works:

```bash
# Add to your ~/.bashrc or ~/.zshrc
alias task=go-task
```

Install the remaining dependencies from the [AUR](https://wiki.archlinux.org/title/Arch_User_Repository) (using [yay](https://github.com/Jguer/yay) or your preferred AUR helper):

```bash
yay -S mprocs-bin
```

For desktop app builds, install the [Tauri prerequisites for Arch Linux](https://v2.tauri.app/start/prerequisites/#linux) plus GStreamer (bundled into the AppImage by `bundleMediaFramework`) and `dpkg` (its `dpkg-deb` builds the `.deb` bundle; not installed by default on Arch):

```bash
sudo pacman -S webkit2gtk-4.1 libayatana-appindicator librsvg patchelf dpkg \
  gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad-libs gst-libav
```

### Windows

Install dependencies with [winget](https://learn.microsoft.com/en-us/windows/package-manager/winget/):

```powershell
winget install --id Microsoft.PowerShell --source winget
winget install --id GoLang.Go --source winget
winget install --id OpenJS.NodeJS.LTS --source winget  # or OpenJS.NodeJS for the current (non-LTS) release
winget install --id Oven-sh.Bun --source winget
winget install --id Task.Task --source winget
winget install --id bufbuild.buf --source winget
winget install --id pvolok.mprocs --source winget
winget install --id SUSE.RancherDesktop --source winget  # or any other Docker-compatible runtime (e.g. Docker.DockerDesktop, Podman.Podman)
winget install --id Rustlang.Rust.MSVC --source winget
winget install --id Google.Protobuf --source winget
winget install --id Microsoft.VisualStudio.BuildTools --source winget  # or Microsoft.VisualStudio.2022.Community if you prefer the full IDE
```

After `Microsoft.VisualStudio.BuildTools` installs, open the Visual Studio Installer and modify the installation to enable the **"Desktop development with C++"** workload — winget installs the bootstrapper but does not select any workloads automatically.

For the remaining Tauri Windows prerequisites (WebView2, etc.), see the [Tauri Windows prerequisites](https://v2.tauri.app/start/prerequisites/#windows).

## Quick Start

Get LeapMux running locally:

```bash
# 1. Clone the repository
git clone https://github.com/leapmux/leapmux.git
cd leapmux

# 2. Generate code and download assets (protobuf, sqlc, and spinner JSON — not checked into git)
task generate

# 3. Start all services (requires mprocs)
task dev
```

Once all services are running, open your browser to:
```
http://localhost:4327
```

Each `dev` target generates code and builds prerequisites, then launches `mprocs` to run the processes concurrently:

| Command | Processes | Description |
|---------|-----------|-------------|
| `task dev` | Go backend (`leapmux dev`) + Bun frontend dev server | Full-featured dev mode on all interfaces, login required |
| `task dev-solo` | Go backend (`leapmux solo`) + Bun frontend dev server | Localhost-only, no login, single-user |
| `task dev-desktop` | Bun frontend dev server + Tauri desktop app | Desktop app development (builds sidecar first) |

## Development

### Building

Build all components:
```bash
task build
```

Build individual components:
```bash
task build-backend    # Build leapmux binary (Go)
task build-frontend   # Build frontend assets
task build-desktop    # Build desktop app for current platform (Tauri v2 + Rust)
```

The `leapmux` binary is output to the repository root. Tauri emits the desktop bundles under `desktop/rust/target/`, and the final artifacts (`.dmg` on macOS, `.AppImage`/`.deb` on Linux, `.msi`/`.exe` on Windows) are also copied to the repository root.

### Testing

Run all tests (except E2E):
```bash
task test
```

Run specific test suites:
```bash
task test-backend       # Backend tests
task test-frontend      # Frontend tests (Vitest)
task test-desktop       # Desktop Go sidecar + Tauri Rust shell tests
task test-e2e           # End-to-end tests (Playwright)
```

Run specific tests by passing arguments after `--`:
```bash
# Backend tests: -run <regex> <packages>
task test-backend -- -run TestMyFunction ./internal/hub/...

# Frontend unit tests: pass a file path to Vitest
task test-frontend -- src/lib/validate.test.ts

# E2E tests: pass a file path or --grep <pattern> to Playwright
task test-e2e -- tests/e2e/040-chat-message-rendering.spec.ts
task test-e2e -- --grep "should persist theme"
```

### Linting

Run all linters:
```bash
task lint
```

Run specific linters:
```bash
task lint-proto      # Lint Protocol Buffer definitions
task lint-backend    # Lint Go code (hub + worker)
task lint-frontend   # Lint frontend code (TypeScript typecheck + ESLint)
task lint-desktop    # Lint desktop Go sidecar (golangci-lint) + Tauri Rust shell (clippy)
```

Auto-fix lint violations:
```bash
task lint-fix            # Fix all (Go, frontend, desktop)
task lint-fix-backend    # Fix Go code (golangci-lint --fix)
task lint-fix-frontend   # Fix frontend code (ESLint --fix)
task lint-fix-desktop    # Fix desktop Go code + Tauri Rust code (clippy --fix)
```

### Desktop Prerequisites

Desktop builds use [Tauri v2](https://v2.tauri.app/start/prerequisites/):
- macOS: Xcode Command Line Tools, Rust, WebKit (system)
- Linux: Rust plus the WebKitGTK/Tauri native dependencies for your distro (see [Tauri Linux prerequisites](https://v2.tauri.app/start/prerequisites/#linux)) and GStreamer (see the Arch Linux section above)
- Windows: Rust MSVC toolchain plus WebView2

### Code Generation

Regenerate all generated code and downloaded assets (Protocol Buffers, sqlc, and spinner JSON):
```bash
task generate
```

You can also run each generator individually:
```bash
task generate-proto      # Generate Protocol Buffer code (Go and TypeScript)
task generate-sqlc       # Generate type-safe SQL code (hub and worker)
task generate-spinners   # Download spinner verb JSON files from awesome-claude-spinners
```

Task uses checksums to skip generation when source files haven't changed. To force regeneration, use `task --force generate`.

Always run `task generate-proto` after modifying `.proto` files in `/proto/leapmux/v1/`.
Always run `task generate-sqlc` after modifying `.sql` files in `/backend/internal/hub/store/*/db/queries/` or `/backend/internal/worker/db/queries/`.

### Preparation

Prepare every module for builds (code generation, frontend install, asset generation, icon generation, and embedding the frontend into the backend):
```bash
task prepare
```

You can also run each step individually:
```bash
task prepare-frontend   # Generate proto/spinners, run bun install, generate icons, copy NOTICE.html
task prepare-backend    # Generate proto/sqlc, build the frontend, and embed it into the backend
task prepare-desktop    # Generate proto, build the frontend, prepare the backend, and generate desktop icons
```

Note: Build targets automatically run their required preparation steps, so `task build` works without running `task prepare` first.

### Third-Party License Notice

Generate `NOTICE.md` and `NOTICE.html` with all third-party dependency licenses:
```bash
task generate-notice
```

Run this manually after changing dependencies; regular build targets do not trigger it. The task fails if any dependency is missing a license file or if a vendored override's license identifier no longer matches the upstream package.

### Cleaning

Remove all build artifacts and generated code:
```bash
task clean
```

Clean a specific module:
```bash
task clean-backend    # Remove leapmux binaries and generated/ directories
task clean-frontend   # Remove .output, .vinxi, node_modules, and generated/ directories
task clean-desktop    # Remove desktop binaries, bundles, and Rust target/
```

### Docker images

Build Docker images containing the full LeapMux stack:

```bash
# Build both Alpine and Ubuntu images
task docker-build

# Build only Alpine
task docker-build-alpine

# Build only Ubuntu
task docker-build-ubuntu
```

By default this builds for `linux/amd64` and `linux/arm64`. You can override the platform and tag:

```bash
task docker-build-alpine PLATFORM=linux/amd64 TAG=leapmux:dev
```

The image uses a multi-stage build (buf, Bun, Go). Tool and base image versions are centralized in `versions.env` at the repository root.

### Documentation site

The site at [leapmux.dev](https://leapmux.dev) is a [Hugo](https://gohugo.io/) + [Hextra](https://imfing.github.io/hextra/) project under `site/`. Hugo is declared as a `go tool` dependency (see `site/go.mod`), so there's nothing extra to install.

```bash
task site        # Build the static site into site/public/
task dev-site    # Live-reload dev server at http://localhost:1313
```

## Technology Stack

### Frontend

- **[Bun](https://bun.sh/)** - Runtime and package manager
- **[ConnectRPC](https://connectrpc.com/)** - RPC client for browser
- **[Noble](https://paulmillr.com/noble/)** - Cryptographic primitives for E2EE (X25519, ML-KEM-1024, SLH-DSA, ChaCha20-Poly1305, BLAKE2b)
- **[Lucide](https://lucide.dev/)** - Icon library
- **[Milkdown](https://milkdown.dev/)** - Markdown editor
- **[Oat](https://oat.ink/)** - Classless CSS framework
- **[Playwright](https://playwright.dev/)** - End-to-end testing
- **[Shiki](https://shiki.style/)** - Syntax highlighting
- **[Solid DnD](https://solid-dnd.com/)** - Drag-and-drop support
- **[SolidJS](https://www.solidjs.com/)** - Reactive UI framework
- **[SolidStart](https://start.solidjs.com/)** - Solid meta-framework (routing, build)
- **[Vanilla Extract](https://vanilla-extract.style/)** - Type-safe CSS-in-JS
- **[Vinxi](https://vinxi.vercel.app/)** - Build framework (Vite-based)
- **[Vitest](https://vitest.dev/)** - Unit testing
- **[xterm.js](https://xtermjs.org/)** - Terminal emulator

### Hub (Central Service)

- **[ConnectRPC](https://connectrpc.com/)** - Modern gRPC-compatible RPC framework (Frontend communication)
- **[Go](https://go.dev/)** - Primary language
- **[Goose](https://pressly.github.io/goose/)** - Database migrations
- **[gRPC](https://grpc.io/)** - Standard gRPC (Worker communication)
- **[koanf](https://github.com/knadh/koanf)** - Layered configuration (defaults, file, env)
- **[Protocol Buffers](https://protobuf.dev/)** - Service and message definitions
- **Pluggable database** - SQLite, PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB (see [Architecture](#architecture))
- **[sqlc](https://sqlc.dev/)** - Type-safe SQL code generation (per-backend: SQLite, PostgreSQL, MySQL)

### Worker (Agent Wrapper)

- **[CIRCL](https://github.com/cloudflare/circl)** - Post-quantum cryptographic primitives (ML-KEM, SLH-DSA) for E2EE channel handling
- **[Git](https://git-scm.com/)** - Repository info and worktree management
- **[Go](https://go.dev/)** - Primary language
- **[gRPC](https://grpc.io/)** - Communication with Hub
- **[SQLite](https://sqlite.org/)** - Embedded database for agent and terminal state

### Desktop

- **[Tauri v2](https://v2.tauri.app/)** - Desktop application framework (Rust + native WebView)

### Site (Documentation)

- **[Hugo](https://gohugo.io/)** - Static site generator for [leapmux.dev](https://leapmux.dev)
- **[Hextra](https://imfing.github.io/hextra/)** - Hugo theme, imported as a Hugo module

### Build Tools

- **[buf](https://buf.build/)** - Protocol Buffer tooling
- **[ESLint](https://eslint.org/)** - TypeScript/JavaScript linting
- **[golangci-lint](https://golangci-lint.run/)** - Go linting
- **[mprocs](https://github.com/pvolok/mprocs)** - Multi-process runner for development
- **[Task](https://taskfile.dev/)** - Build orchestration with checksum-based caching

## Project Structure

```
leapmux/
├── backend/             # Go backend: the unified `leapmux` binary (hub + worker)
│   ├── cmd/leapmux/     # Entry point, subcommand routing, and the admin CLI
│   └── internal/
│       ├── hub/         # Hub: auth, channel relay, pluggable store, keystore, OAuth
│       └── worker/      # Worker: agents, terminals, file browser, git, E2EE channel
├── desktop/             # Tauri v2 desktop app (Rust shell + Go sidecar)
├── docker/              # Dockerfile and s6-overlay service definitions
├── frontend/            # SolidJS web app
│   └── src/components/  # UI: chat (+ per-agent renderers), terminal, files, shell
├── icons/               # App and agent-provider SVG icons
├── proto/leapmux/v1/    # Protocol Buffer service and message definitions
├── scripts/             # Utility scripts (NOTICE generation, ICO builder, ...)
├── site/                # Hugo + Hextra documentation site (leapmux.dev)
│   └── content/docs/    # The user manual
├── go.work              # Go workspace (backend + desktop/go)
├── Taskfile.yaml        # Build orchestration (go-task.dev)
└── versions.env         # Version string and tool/image versions
```

## Contributing

We don't accept code contributions yet. The reason is licensing: LeapMux is under FSL-1.1-ALv2, which automatically converts to Apache 2.0 over time, and that relicensing is only possible if we hold the rights to every line of code. Without a Contributor License Agreement (CLA) in place, accepting outside contributions now would make that switch extremely hard — we'd have to track down every past contributor for their consent. Once a CLA is ready, we expect to open up to external contributions.

In the meantime, please feel free to create issues, preferably with a plan generated by a frontier model; we will follow them up.

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.

This means:
- You can use, modify, and distribute the software
- There are certain limitations on competitive use
- The license automatically converts to Apache 2.0 two years after each release is first made available

See the [LICENSE](LICENSE.md) file for full details.

## Disclaimer

All product names, logos, and trademarks are the property of their respective owners. LeapMux is not affiliated with, endorsed by, or sponsored by Anomaly, Anthropic, Anysphere, Apple, Block, Cognition, Don Ho, Earendil, GitHub, Google, JetBrains, Kilo Code, Microsoft, OpenAI, Sublime HQ, Zed Industries, or any other third party. Coding agent, editor, and IDE icons are used solely to indicate compatibility and are reproduced here for identification purposes only.
