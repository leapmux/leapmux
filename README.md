<div align="center">
  <img src="icons/leapmux-icon.svg" alt="LeapMux" width="128" height="128">
</div>

# LeapMux

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
  <a href="https://kilo.ai/cli"><img src="icons/agents/kilo.svg" width="64" height="64" title="Kilo"></a>&nbsp;
  <a href="https://opencode.ai/"><img src="icons/agents/opencode.svg" width="64" height="64" title="OpenCode"></a>&nbsp;
  <a href="https://block.github.io/goose/"><img src="icons/agents/goose.svg" width="64" height="64" title="Goose"></a>
</p>

## Key Features

Beyond the basics in the pitch above:

- **Git worktree management** — Open each agent or terminal in a new or existing worktree; switch or create branches at open time, with dirty-worktree protection on close.
- **Git-aware file browser** — Real-time git status with staged / unstaged / change filters and inline diffs, even on a remote worker.
- **Integrated terminals** — PTY sessions alongside your agents, in the same tiling or floating layout, on the same worker.
- **NAT-friendly workers** — Workers initiate outbound connections; they can run behind firewalls without inbound port access.
- **Multi-org with RBAC** — Organizations with Owner / Admin / Member roles; workspaces shared per user or per org member.
- **Pluggable Hub storage** — SQLite (default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB.

## Table of Contents

- [Architecture](#architecture)
- [Communication and Threat Model](#communication-and-threat-model)
- [Supported Platforms](#supported-platforms)
- [Docker](#docker)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Development](#development)
- [Technology Stack](#technology-stack)
- [Project Structure](#project-structure)
- [Contributing](#contributing)
- [License](#license)
- [Disclaimer](#disclaimer)

## Architecture

LeapMux is built as a Go binary (`leapmux`) that runs in two deployment modes. A native desktop app is also available — it can run solo mode locally or connect to a remote hub.

### Solo Mode (default)

Run `leapmux` with no subcommand for a zero-config, single-user setup. Hub and Worker run in the same process, bound to localhost only. No login is required — the UI opens directly into the workspace.

```
                 LeapMux (127.0.0.1:4327)
┌──────────────────────────────────────────────────────┐
│                                                      │
│  ┌─────────────┐  in-process   ┌──────────────────┐  │
│  │     Hub     │◄─────────────►│     Worker       │  │
│  │  (no auth)  │               │  ┌────────────┐  │  │
│  │  + SQLite   │               │  │   Agents   │  │  │
│  │             │               │  │ (multiple) │  │  │
│  └─────────────┘               │  └────────────┘  │  │
│         ▲                      │  + SQLite        │  │
│         │                      └──────────────────┘  │
│         │ ConnectRPC + WebSocket                     │
└─────────┼────────────────────────────────────────────┘
          │
          ▼
  ┌───────────────┐
  │   Frontend    │
  │  (Browser /   │
  │  Desktop App) │
  └───────────────┘
```

### Distributed Mode

For multi-user and remote setups, run `leapmux hub` and `leapmux worker` separately. The Hub handles authentication and relays end-to-end encrypted traffic between the Frontend and Workers. Workers can be on different machines, behind NATs — they initiate outbound connections to the Hub.

The Hub supports multiple database backends — **SQLite** (default), **PostgreSQL**, **MySQL**, **CockroachDB**, **YugabyteDB**, and **TiDB** — configured via the `storage.type` option. Workers always use SQLite locally.

```
┌────────────────┐              ┌──────────────────┐              ┌──────────────────┐
│                │  ConnectRPC  │                  │     gRPC     │  Worker 1        │
│   Frontend     │◄────────────►│       Hub        │◄────────────►│  ┌────────────┐  │
│  (Browser /    │  WebSocket   │     (Relay)      │              │  │   Agents   │  │
│  Desktop App)  │              │                  │              │  │ (multiple) │  │
│                │              │    Go Service    │              │  └────────────┘  │
└────────────────┘              │  + Database      │              │  + SQLite        │
                                │   (SQLite,       │              └──────────────────┘
                                │    PostgreSQL,   │                        ⋮
                                │    MySQL, ...)   │              ┌──────────────────┐
                                │                  │              │  Worker N        │
                                └──────────────────┘              │  ┌────────────┐  │
                                                                  │  │   Agents   │  │
                                                                  │  │ (multiple) │  │
                                                                  │  └────────────┘  │
                                                                  │  + SQLite        │
                                                                  └──────────────────┘
```

### Modes

LeapMux is a single binary with these subcommands:

| Command | Mode | Description |
|---------|------|-------------|
| `leapmux` | Solo | Hub + Worker on `127.0.0.1:4327`, no login, single-user |
| `leapmux hub` | Hub | Central service only (authentication, relay, database) |
| `leapmux worker` | Worker | Connects to a remote Hub |
| `leapmux dev` | Dev | Hub + Worker on `:4327` (all interfaces), login required, all features |
| `leapmux admin` | Admin | CLI for managing orgs, users, sessions, workers, OAuth providers, encryption keys, and database |
| `leapmux version` | — | Prints version and exits |
| Desktop app | — | Native desktop app — runs solo mode or connects to a remote hub in an embedded WebView |

### Components

- **Frontend** — a SolidJS web app that renders the workspace UI (tiling layout, agents, terminals, file browser).
- **Hub** — a Go service that handles login, workspace management, and worker registration, and relays encrypted Frontend↔Worker traffic. Storage is pluggable (see [Distributed Mode](#distributed-mode)).
- **Worker** — a Go process that runs agent instances, PTYs, file browsing, and git operations. Keeps its own SQLite database and auto-reconnects to the Hub on disconnection.

Protocol details and the Hub's visibility into channel traffic are covered in [Communication and Threat Model](#communication-and-threat-model).

## Communication and Threat Model

LeapMux treats the Hub as an **authenticated relay, not a trusted peer** — it sees who is talking to whom, but never what they say. This is load-bearing in Distributed Mode, where the Hub may be operated by a teammate or platform team. In Solo Mode the distinction collapses (see below).

### Protocols

- **Frontend → Hub** — ConnectRPC (gRPC-compatible), for login, workspace management, and worker registration.
- **Frontend → Worker** — hybrid post-quantum Noise_NK (X25519 + ML-KEM-1024 for key exchange, SLH-DSA-SHAKE-256f for static-key authentication, ChaCha20-Poly1305 + BLAKE2b for transport), multiplexed over a single WebSocket relayed through the Hub.
- **Worker → Hub** — standard gRPC with bidirectional streaming. The Worker always initiates the connection, so it can live behind a NAT without inbound ports. Local workers can use a Unix domain socket (`unix:<path>`) or a Windows named pipe (`npipe:<name>`) in place of TCP.
- **Wire format** — Protocol Buffers defined in `/proto/leapmux/v1/`.

### Trust boundaries

**The Hub can see:**

- Account metadata: user names, emails, password hashes, OAuth tokens, session tokens.
- Organization, workspace, and membership records.
- Workspace **titles**, tab positions, and tiling layout geometry.
- Worker registration data: worker ID, composite public keys, online status, last-seen time.
- Per-message transport metadata: channel ID, correlation ID, ciphertext size, timing. Traffic analysis is in scope.

**The Hub cannot see:**

- Agent chat transcripts, tool-call arguments, or tool outputs.
- Terminal I/O, shell history, or PTY state.
- File contents, diffs, or git status.
- Worker hostname, OS, or filesystem paths (sent only inside the encrypted channel).
- Any plaintext of Frontend↔Worker traffic.

Agent and terminal state live only in the Worker's local SQLite database. Sharing a workspace grants the invited user routing permission via the Hub; reading content still requires opening their own encrypted channel to the Worker.

### Worker identity

Worker identity is pinned **TOFU** on first connection: the Frontend records the Worker's composite static key and rejects any later handshake whose key doesn't match. A compromised Hub therefore cannot silently swap a Worker underneath a user.

### Solo Mode

Solo Mode runs the Hub and Worker in the same process on `127.0.0.1:4327` with no authentication. Any local process that can reach the port can drive the Worker, so the threat model reduces to local trust — the protocol-level separation above is still in effect but offers no protection against a local attacker.

## Supported Platforms

LeapMux is developed and tested natively on macOS, Linux, and Windows. CI builds and tests the full stack — including the Tauri desktop app — on every commit:

| Platform | Architectures       | Desktop app artifact       |
|----------|---------------------|----------------------------|
| macOS    | arm64               | `.dmg`                     |
| Linux    | amd64, arm64        | `.AppImage`, `.deb`        |
| Windows  | amd64               | `.msi`                     |

Download desktop app artifacts and standalone server binaries from the [Releases page](https://github.com/leapmux/leapmux/releases).

Pre-built Docker images target `linux/amd64` and `linux/arm64` — see [Docker](#docker) below.

## Docker

Pre-built images are published to [GHCR](https://github.com/leapmux/leapmux/pkgs/container/leapmux) in two variants:

| Variant          | Tags                                     | Example                             |
|------------------|------------------------------------------|-------------------------------------|
| Alpine (default) | `:<version>`, `:<major>`, `:latest`, `:dev` | `ghcr.io/leapmux/leapmux:1.0.0` |
| Ubuntu           | `:<version>-ubuntu`, `:<major>-ubuntu`, `:latest-ubuntu`, `:dev-ubuntu` | `ghcr.io/leapmux/leapmux:1.0.0-ubuntu` |

Release tags (`:latest`, `:<version>`, `:<major>`) are published by the release workflow. The `:dev` tag is updated on every push to `main`.

The image runs with [s6-overlay](https://github.com/just-containers/s6-overlay) for process supervision. The `LEAPMUX_MODE` environment variable selects the subcommand (`hub`, `worker`, `dev`, etc.) and is required. Data and configuration are stored under `/data/<mode>/` (e.g. `/data/hub/`) in the `/data` volume.

```bash
# Run as a hub (central service only)
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest

# Run as hub + worker together (dev mode)
docker run -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

### Building images yourself

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

## Prerequisites

The rest of this section is for people building LeapMux from source or hacking on it. If you just want to run LeapMux, the pre-built Docker images and desktop app artifacts above are enough.

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

For desktop app builds, install the [Tauri prerequisites for Arch Linux](https://v2.tauri.app/start/prerequisites/#linux):

```bash
sudo pacman -S webkit2gtk-4.1 libayatana-appindicator librsvg patchelf
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
| `task dev-solo` | Go backend (`leapmux` solo) + Bun frontend dev server | Localhost-only, no login, single-user |
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
task test-e2e -- tests/e2e/25-chat-message-rendering.spec.ts
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
- Linux: Rust plus the WebKitGTK/Tauri native dependencies for your distro (see [Tauri Linux prerequisites](https://v2.tauri.app/start/prerequisites/#linux))
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

## Technology Stack

### Frontend

- **[Bun](https://bun.sh/)** - Runtime and package manager
- **[ConnectRPC](https://connectrpc.com/)** - RPC client for browser
- **[Noble](https://paulmillr.com/noble/)** - Cryptographic primitives for E2EE (X25519, ML-KEM-1024, SLH-DSA, ChaCha20-Poly1305, BLAKE2b)
- **[Corvu](https://corvu.dev/)** - Resizable panel components
- **[Lucide](https://lucide.dev/)** - Icon library
- **[Milkdown](https://milkdown.dev/)** - Markdown editor
- **[Oat](https://oat.ink/)** - Classless CSS framework
- **[Playwright](https://playwright.dev/)** - End-to-end testing
- **[Shiki](https://shiki.style/)** - Syntax highlighting
- **[Solid DnD](https://solid-dnd.com/)** - Drag-and-drop support
- **[SolidJS](https://www.solidjs.com/)** - Reactive UI framework
- **[Vanilla Extract](https://vanilla-extract.style/)** - Type-safe CSS-in-JS
- **[Vinxi](https://vinxi.vercel.app/)** - Build framework (Vite-based)
- **[Vitest](https://vitest.dev/)** - Unit testing
- **[xterm.js](https://xtermjs.org/)** - Terminal emulator

### Hub (Central Service)

- **[ConnectRPC](https://connectrpc.com/)** - Modern gRPC-compatible RPC framework (Frontend communication)
- **[Go](https://go.dev/)** - Primary language
- **[Goose](https://pressly.github.io/goose/)** - Database migrations
- **[gRPC](https://grpc.io/)** - Standard gRPC (Worker communication)
- **[Protocol Buffers](https://protobuf.dev/)** - Service and message definitions
- **Pluggable database** - see [Distributed Mode](#distributed-mode) for the supported backends
- **[sqlc](https://sqlc.dev/)** - Type-safe SQL code generation (per-backend: SQLite, PostgreSQL, MySQL)

### Worker (Agent Wrapper)

- **[CIRCL](https://github.com/cloudflare/circl)** - Post-quantum cryptographic primitives (ML-KEM, SLH-DSA) for E2EE channel handling
- **[Git](https://git-scm.com/)** - Repository info and worktree management
- **[Go](https://go.dev/)** - Primary language
- **[gRPC](https://grpc.io/)** - Communication with Hub
- **[SQLite](https://sqlite.org/)** - Embedded database for agent and terminal state

### Desktop

- **[Tauri v2](https://v2.tauri.app/)** - Desktop application framework (Rust + native WebView)
- **Go desktop sidecar** - Desktop-only service for Solo startup, local proxying, tunnels, and OS integrations

### Build Tools

- **[buf](https://buf.build/)** - Protocol Buffer tooling
- **[ESLint](https://eslint.org/)** - TypeScript/JavaScript linting
- **[golangci-lint](https://golangci-lint.run/)** - Go linting
- **[mprocs](https://github.com/pvolok/mprocs)** - Multi-process runner for development
- **[Task](https://taskfile.dev/)** - Build orchestration with checksum-based caching

## Project Structure

```
leapmux/
├── .github/workflows/       # CI, Docker, and release workflows
│
├── backend/                 # Go backend module
│   ├── channelwire/         # E2EE channel wire format definitions
│   │
│   ├── cmd/leapmux/         # Unified binary entry point
│   │   ├── admin*.go        # Admin CLI (org, user, session, worker, oauth, encryption, db)
│   │   ├── hub.go           # Hub mode
│   │   ├── main.go          # Subcommand routing (hub, worker, solo, dev, admin)
│   │   ├── solo.go          # Solo/dev mode (hub + worker, default)
│   │   └── worker.go        # Worker mode
│   │
│   ├── generated/proto/     # Generated Go protobuf code (gitignored)
│   │
│   ├── hub/                 # Hub public API (thin wrapper)
│   │   └── server.go        # NewServer(), Serve(), RegisterBackend(), etc.
│   │
│   ├── internal/
│   │   ├── config/          # Shared configuration loading (koanf-based)
│   │   │
│   │   ├── hub/             # Hub implementation
│   │   │   ├── auth/        # Session-based authentication
│   │   │   ├── bootstrap/   # Database initialization and seeding
│   │   │   ├── channelmgr/  # E2EE channel routing and chunk validation
│   │   │   ├── cleanup/     # Periodic cleanup of expired data
│   │   │   ├── config/      # Hub configuration (incl. storage backend selection)
│   │   │   ├── frontend/    # Frontend asset embedding and dev proxy
│   │   │   ├── keystore/    # Encryption key management and rotation
│   │   │   ├── layout/      # Workspace tiling layout management
│   │   │   ├── notifier/    # Worker notification queue (persistent delivery with retries)
│   │   │   ├── oauth/       # OAuth/OIDC provider integrations (GitHub, OIDC)
│   │   │   ├── password/    # Password hashing and verification
│   │   │   ├── service/     # RPC service implementations (auth, workspace, channel relay)
│   │   │   ├── store/       # Storage abstraction and backend implementations
│   │   │   │   ├── sqlite/      # SQLite backend (default)
│   │   │   │   ├── postgres/    # PostgreSQL backend (also used by CockroachDB, YugabyteDB)
│   │   │   │   ├── mysql/       # MySQL backend (also used by TiDB)
│   │   │   │   ├── cockroachdb/ # CockroachDB integration tests
│   │   │   │   ├── yugabytedb/  # YugabyteDB integration tests
│   │   │   │   ├── tidb/        # TiDB integration tests
│   │   │   │   ├── sqlutil/     # Shared SQL helpers (migrations, bulk ops, converters)
│   │   │   │   └── storetest/   # Backend-agnostic test suite
│   │   │   ├── storeopen/   # Store factory (opens backend from config)
│   │   │   ├── testutil/    # Shared test helpers for hub tests
│   │   │   └── workermgr/   # Worker connection registry and pending approvals
│   │   │
│   │   ├── logging/         # Structured logging and middleware
│   │   ├── metrics/         # Prometheus metrics and interceptors
│   │   ├── noise/           # Noise_NK protocol and key fingerprinting
│   │   ├── util/            # Shared utilities (id, lexorank, msgcodec, ptrconv, sqlitedb, timefmt, validate, testutil)
│   │   │
│   │   └── worker/          # Worker implementation
│   │       ├── agent/       # Agent process management
│   │       ├── channel/     # E2EE channel session management and dispatch
│   │       ├── config/      # Worker configuration
│   │       ├── db/          # Worker database (SQLite-only), migrations, and queries
│   │       ├── filebrowser/ # File system access
│   │       ├── gitutil/     # Git repository utilities
│   │       ├── hub/         # gRPC client to Hub (with auto-reconnect)
│   │       ├── service/     # Agent, terminal, file, and git service handlers
│   │       ├── terminal/    # PTY session management
│   │       └── wakelock/    # System wake lock management
│   │
│   ├── locallisten/         # Local-socket listeners (Unix domain sockets and Windows named pipes)
│   ├── solo/                # Shared solo mode startup logic
│   ├── spautil/             # SPA HTTP handler utilities
│   ├── tunnel/              # Tunnel channel and connection management
│   ├── util/version/        # Build version information
│   │
│   └── worker/              # Worker public API (thin wrapper)
│       └── runner.go        # Run(), RunConfig
│
├── desktop/                 # Tauri v2 desktop app + Go desktop sidecar
│   ├── go/                  # Go desktop sidecar (solo startup, proxy, tunnels, OS integrations)
│   └── rust/                # Tauri v2 Rust shell (WebView, packaging, icons)
│       └── scripts/         # Packaging helpers (DMG creation, icon generation)
│
├── docker/                  # Dockerfile and s6-overlay service definitions
│
├── frontend/                # SolidJS web application
│   ├── patches/             # Bun patch overrides for dependencies
│   ├── public/              # Static assets (fonts, icons, sounds, PWA manifest)
│   ├── scripts/             # Build and development scripts
│   ├── src/
│   │   ├── api/             # ConnectRPC client setup
│   │   ├── components/      # UI components (chat, terminal, filebrowser, shell, etc.)
│   │   ├── context/         # Auth, Org, Workspace, and Preferences providers
│   │   ├── generated/       # Generated TypeScript protobuf code (gitignored)
│   │   ├── hooks/           # Custom hooks
│   │   ├── lib/             # Utility libraries
│   │   ├── routes/          # Route definitions
│   │   ├── spinners/        # Spinner verb JSON files (generated, gitignored)
│   │   ├── stores/          # State management (agents, chat, terminals, etc.)
│   │   ├── styles/          # Global styles and themes
│   │   ├── types/           # TypeScript type definitions
│   │   └── utils/           # Shared utility functions
│   └── tests/
│       ├── e2e/             # End-to-end tests (Playwright)
│       └── unit/            # Unit tests (Vitest)
│
├── icons/                   # SVG icons (app logo and agent provider icons)
│
├── proto/                   # Protocol Buffer definitions
│   └── leapmux/v1/          # Service and message definitions
│
├── scripts/                 # Utility scripts
│   ├── build-ico.mjs        # ICO file builder
│   ├── generate-notice.mjs  # License collection and NOTICE.md/HTML generation
│   └── license-overrides/   # Vendored licenses for packages missing them
│
├── buf.gen.yaml             # Protocol Buffer code generation targets
├── buf.yaml                 # Protocol Buffer linting configuration
├── go.work                  # Go workspace (backend + desktop/go modules)
├── mprocs.yaml              # Dev mode process configuration (task dev)
├── mprocs-desktop.yaml      # Desktop dev mode process configuration (task dev-desktop)
├── mprocs-solo.yaml         # Solo mode process configuration (task dev-solo)
├── NOTICE.md                # Third-party dependency licenses (generated)
├── README.md                # This file
├── Taskfile.yaml            # Build orchestration (go-task.dev)
└── versions.env             # Version string and tool/image versions
```

## Contributing

We don't accept code contributions at the moment. Please feel free to create issues, preferably with the plan generated by a frontier model; we will follow them up.

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.

This means:
- You can use, modify, and distribute the software
- There are certain limitations on competitive use
- The license automatically converts to Apache 2.0 two years after each release is first made available

See the [LICENSE](LICENSE.md) file for full details.

## Disclaimer

All product names, logos, and trademarks are the property of their respective owners. LeapMux is not affiliated with, endorsed by, or sponsored by Anthropic, OpenAI, Google, GitHub, Microsoft, Anysphere, Block, Kilo Code, Anomaly, or any other third party. Agent icons are used solely to indicate compatibility and are reproduced here for identification purposes only.
