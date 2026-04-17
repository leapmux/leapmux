[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)

<p align="center">
  <img src="icons/leapmux-icon-corners.svg" alt="LeapMux" width="128" height="128">
</p>

# LeapMux

LeapMux is a **multiplexer for AI coding agents**. Run multiple agent instances in parallel from a single workspace, in the browser or as a native desktop app. Connect local and remote development backends (even behind NATs), organize work across tiling workspaces, interact with terminals, browse and diff files with full git awareness, and collaborate with your team, all with end-to-end encrypted communication.

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

- **Multi-Agent Workspaces**
  - Run multiple local or remote coding agent instances simultaneously
- **Tiling Layout**
  - Split the workspace into resizable horizontal/vertical panes — run chats and terminals side by side
- **Desktop App**
  - Native macOS, Linux, and Windows desktop application (optional)
- **Git-Aware File Browser**
  - Browse files on remote backends with real-time git status, change/staged/unstaged filters, and inline diffs
- **Git Worktree & Branch Management**
  - Create new worktrees, use existing ones, switch branches, or create new branches when opening agents and terminals, with dirty-worktree protection on close
- **End-to-End Encryption**
  - All Frontend-Worker traffic is encrypted via hybrid post-quantum Noise_NK (X25519 + ML-KEM-1024 + SLH-DSA) over multiplexed WebSocket channels
- **Multi-Organization Support**
  - Create teams with role-based access control (Owner/Admin/Member)
- **Workspace Sharing**
  - Collaborate by sharing workspaces with specific users or organization members
- **NAT Traversal**
  - Workers initiate outbound connections, so they run behind firewalls without port forwarding

## Table of Contents

- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Development](#development)
- [Docker](#docker)
- [Technology Stack](#technology-stack)
- [Project Structure](#project-structure)
- [Contributing](#contributing)
- [License](#license)
- [Disclaimer](#disclaimer)

## Architecture

LeapMux is built as a Go binary (`leapmux`) that runs in two deployment modes. A native desktop app is also available as an alternative way to run solo mode.

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

**Frontend (SolidJS)**
- Web application providing the user interface
- Communicates with Hub via ConnectRPC (for auth and workspace management)
- Establishes end-to-end encrypted channels to Workers (hybrid post-quantum Noise_NK handshake, multiplexed WebSocket relay)
- Key pinning with TOFU (Trust On First Use) model for Worker identity verification
- Manages UI state for workspaces, agents, terminals, and file browsing

**Hub (Go)**
- Authentication, workspace management, and worker registration service
- Relays encrypted Frontend-Worker traffic without decrypting it
- Pluggable storage backend: SQLite (default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB
- No access to channel plaintext — acts as an authenticated relay

**Worker (Go)**
- Wraps coding agent instances and provides system access
- Handles agent lifecycle, terminal sessions, file browsing, and git operations
- Maintains its own SQLite database for agent and terminal state
- Communicates with Hub via gRPC (over TCP or Unix domain socket)
- Terminates E2EE channels from the Frontend (Noise_NK responder)
- Auto-reconnects to Hub on disconnection

### Communication

- **Frontend → Hub**: ConnectRPC (gRPC-compatible) for authentication, workspace management, and worker registration
- **Frontend → Worker (via Hub relay)**: End-to-end encrypted channels using hybrid post-quantum Noise_NK (X25519 + ML-KEM-1024 for key exchange, SLH-DSA for static key authentication, ChaChaPoly + BLAKE2b for transport), multiplexed over a single WebSocket connection through the Hub
- **Worker → Hub**: Standard gRPC with bidirectional streaming (over TCP or Unix domain socket).
  - Workers initiate outbound connections to the Hub, so they can run behind NATs, without requiring inbound port access.
  - For local workers on the same machine, connect via Unix domain socket using `unix:<socket-path>` as the Hub URL.
- **Message Format**: Protocol Buffers (defined in `/proto/leapmux/v1/`)

## Prerequisites

Before you begin, ensure you have the following installed:

- **Go** 1.26.1 or later
- **Bun** (latest version) - JavaScript runtime and package manager
- **Task** - Task runner (replaces Make)
- **buf** CLI - Protocol Buffer code generation ([authentication](https://buf.build/docs/bsr/authentication/) recommended to avoid rate-limit errors)
- **sqlc** - Type-safe SQL code generation
- **golangci-lint** - Go linter
- **yq** - YAML processor (used to read `versions.yaml`)
- **SQLite** (usually pre-installed on most systems)
- **Docker** - Required for building Docker images (on macOS, [Rancher Desktop](https://rancherdesktop.io/) is recommended)
- **mprocs** (optional, for easier multi-process development)
- **Rust toolchain** - Required for building the Tauri desktop app
- **Tauri desktop prerequisites** - WebView/system packages required by Tauri on your platform

### macOS

Install [Bun](https://bun.sh/) by following the instructions at https://bun.sh/.

Install the remaining dependencies with [Homebrew](https://brew.sh/):

```bash
brew install buf go go-task golangci-lint mprocs protobuf rust sqlc yq
```

### Arch Linux

Install the official repository packages with [pacman](https://wiki.archlinux.org/title/Pacman):

```bash
sudo pacman -S buf bun go go-task go-yq golangci-lint protobuf rust sqlc
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
sudo pacman -S webkit2gtk-4.1 libappindicator-gtk3 librsvg patchelf
```

### Operating System

LeapMux is developed and tested on macOS and Linux. Windows support may require WSL.

## Quick Start

Get LeapMux running locally:

```bash
# 1. Clone the repository
git clone https://github.com/leapmux/leapmux.git
cd leapmux

# 2. Generate code (protobuf and sqlc — not checked into git)
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

The `leapmux` binary is output to the repository root. The Tauri desktop bundle is emitted under `desktop/rust/target/`.

### Testing

Run all tests (except E2E):
```bash
task test
```

Run specific test suites:
```bash
task test-backend       # Backend tests
task test-frontend      # Frontend tests (Vitest)
task test-desktop       # Desktop sidecar tests
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
task lint-frontend   # Lint frontend code (ESLint)
task lint-desktop    # Lint desktop Go sidecar + Tauri Rust shell (clippy)
```

Auto-fix lint violations:
```bash
task lint-fix            # Fix all (Go, frontend, desktop)
task lint-fix-backend    # Fix Go code (golangci-lint --fix)
task lint-fix-frontend   # Fix frontend code (ESLint --fix)
task lint-fix-desktop    # Fix desktop Go code + Tauri Rust code (clippy --fix)
```

### Desktop and Mobile Prerequisites

Desktop builds use [Tauri v2](https://v2.tauri.app/start/prerequisites/):
- macOS: Xcode Command Line Tools, Rust, WebKit (system)
- Linux: Rust plus the WebKitGTK/Tauri native dependencies for your distro (see [Tauri Linux prerequisites](https://v2.tauri.app/start/prerequisites/#linux))
- Windows: Rust MSVC toolchain plus WebView2

Future mobile builds use Tauri mobile tooling:
- iOS: Xcode, CocoaPods, Rust iOS targets
- Android: Android Studio, Android SDK/NDK, Java, Rust Android targets

### Code Generation

Regenerate all generated code (Protocol Buffers and sqlc):
```bash
task generate
```

You can also run each generator individually:
```bash
task generate-proto   # Generate Protocol Buffer code (Go and TypeScript)
task generate-sqlc    # Generate type-safe SQL code (hub and worker)
```

Task uses checksums to skip generation when source files haven't changed. To force regeneration, use `task --force generate`.

Always run `task generate-proto` after modifying `.proto` files in `/proto/leapmux/v1/`.
Always run `task generate-sqlc` after modifying `.sql` files in `/backend/internal/hub/store/*/db/queries/` or `/backend/internal/worker/db/queries/`.

### Preparation

Prepare all dependencies (code generation + frontend install):
```bash
task prepare
```

You can also run each step individually:
```bash
task prepare-backend    # Generate protobuf and sqlc code
task prepare-frontend   # Install frontend dependencies (bun install)
```

Note: Build targets automatically run their required preparation steps, so `task build` works without running `task prepare` first.

### Third-Party License Notice

Generate `NOTICE.md` and `NOTICE.html` with all third-party dependency licenses:
```bash
task generate-notice
```

The build pipeline runs this automatically before building the frontend. The task fails if any dependency is missing a license file or if a vendored override's license identifier no longer matches the upstream package.

### Cleaning

Remove all build artifacts and generated code:
```bash
task clean
```

## Docker

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

The image uses a multi-stage build (buf, Bun, Go) and runs with [s6-overlay](https://github.com/just-containers/s6-overlay) for process supervision. The `LEAPMUX_MODE` environment variable selects the subcommand (`hub`, `worker`, `dev`, etc.) and is required. Data and configuration are stored under `/data/<mode>/` (e.g. `/data/hub/`) in the `/data` volume.

```bash
# Run as a hub (central service only)
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data leapmux:latest

# Run as hub + worker together (dev mode)
docker run -p 4327:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data leapmux:latest
```

Pre-built images are published to GHCR in two variants:

| Variant          | Tags                                     | Example                             |
|------------------|------------------------------------------|-------------------------------------|
| Alpine (default) | `:<version>`, `:<major>`, `:latest`, `:dev` | `ghcr.io/leapmux/leapmux:1.0.0` |
| Ubuntu           | `:<version>-ubuntu`, `:<major>-ubuntu`, `:latest-ubuntu`, `:dev-ubuntu` | `ghcr.io/leapmux/leapmux:1.0.0-ubuntu` |

Release tags (`:latest`, `:<version>`, `:<major>`) are published by the release workflow. The `:dev` tag is updated on every push to `main`.

Tool and base image versions are centralized in the `versions.yaml` file at the repository root.

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
- **Pluggable database** - SQLite (default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB
- **[sqlc](https://sqlc.dev/)** - Type-safe SQL code generation (per-backend: SQLite, PostgreSQL, MySQL)

### Worker (Agent Wrapper)

- **[CIRCL](https://github.com/cloudflare/circl)** - Post-quantum cryptographic primitives (ML-KEM, SLH-DSA) for E2EE channel handling
- **[Git](https://git-scm.com/)** - Repository info and worktree management
- **[Go](https://go.dev/)** - Primary language
- **[gRPC](https://grpc.io/)** - Communication with Hub
- **[SQLite](https://sqlite.org/)** - Embedded database for agent and terminal state

### Desktop (optional)

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
└── versions.yaml            # Version string and tool/image versions
```

## Contributing

We don't accept code contributions at the moment. Please feel free to create issues, preferably with the plan generated by a frontier model; we will follow them up.

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.

This means:
- You can use, modify, and distribute the software
- There are certain limitations on competitive use
- The license will automatically convert to Apache 2.0 after a specified period

See the [LICENSE](LICENSE.md) file for full details.

## Disclaimer

All product names, logos, and trademarks are the property of their respective owners. LeapMux is not affiliated with, endorsed by, or sponsored by Anthropic, OpenAI, Google, GitHub, Microsoft, Anysphere, Block, Kilo Code, Anomaly, or any other third party. Agent icons are used solely to indicate compatibility and are reproduced here for identification purposes only.
