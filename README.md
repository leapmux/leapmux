[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)

<p align="center">
  <img src="icons/leapmux-icon-corners.svg" alt="LeapMux" width="128" height="128">
</p>

# LeapMux

LeapMux is a **multiplexer for AI coding agents**. Run multiple agent instances in parallel from a single workspace, in the browser or as a native desktop app. Connect local and remote development backends (even behind NATs), organize work across tiling workspaces, interact with terminals, browse and diff files with full git awareness, and collaborate with your team, all with end-to-end encrypted communication.

## Key Features

- **Multi-Agent Workspaces**
  - Run multiple local or remote Claude Code instances simultaneously
- **Tiling Layout**
  - Split the workspace into resizable horizontal/vertical panes — run chats and terminals side by side
- **Desktop App**
  - Native macOS, Linux, and Windows desktop application (optional)
- **Git-Aware File Browser**
  - Browse files on remote backends with real-time git status, change/staged/unstaged filters, and inline diffs
- **Git Worktree Management**
  - Agents and terminals auto-create isolated git worktrees per task, with dirty-worktree protection
- **End-to-End Encryption**
  - All Frontend-Worker traffic is encrypted via hybrid post-quantum Noise_NK (X25519 + ML-KEM-1024 + SLH-DSA) over multiplexed WebSocket channels
- **Multi-Organization Support**
  - Create teams with role-based access control (Owner/Admin/Member)
- **Workspace Sharing**
  - Collaborate by sharing workspaces with specific users or organization members
- **NAT Traversal**
  - Workers initiate outbound connections, so they run behind firewalls without port forwarding

---

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
- [Project Status](#project-status)

---

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
│  │  + SQLite   │               │  │Claude Code │  │  │
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

```
┌─────────────────┐              ┌──────────────────┐              ┌───────────────────┐
│                 │  ConnectRPC  │                  │     gRPC     │  Worker 1         │
│    Frontend     │◄────────────►│       Hub        │◄────────────►│  ┌─────────────┐  │
│   (Browser /    │  WebSocket   │     (Relay)      │              │  │ Claude Code │  │
│   Desktop App)  │              │                  │              │  │ (multiple)  │  │
│                 │              │    Go Service    │              │  └─────────────┘  │
└─────────────────┘              │    + Database    │              │  + SQLite         │
                                 │                  │              └───────────────────┘
                                 └──────────────────┘                        ⋮
                                                                   ┌───────────────────┐
                                                                   │  Worker N         │
                                                                   │  ┌─────────────┐  │
                                                                   │  │ Claude Code │  │
                                                                   │  │ (multiple)  │  │
                                                                   │  └─────────────┘  │
                                                                   │  + SQLite         │
                                                                   └───────────────────┘
```

### Modes

LeapMux is a single binary with these subcommands:

| Command | Mode | Description |
|---------|------|-------------|
| `leapmux` | Solo | Hub + Worker on `127.0.0.1:4327`, no login, single-user |
| `leapmux hub` | Hub | Central service only (authentication, relay, database) |
| `leapmux worker` | Worker | Connects to a remote Hub |
| `leapmux dev` | Dev | Hub + Worker on `:4327` (all interfaces), login required, all features |
| `leapmux version` | — | Prints version and exits |
| Desktop app | Solo | Native desktop app — runs solo mode in an embedded WebView |

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
- Stores persistent data in SQLite (users, workspaces, worker registry)
- No access to channel plaintext — acts as an authenticated relay

**Worker (Go)**
- Wraps Claude Code instances and provides system access
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

---

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
- **Wails** (optional, for building the desktop app)

### macOS

Install [Bun](https://bun.sh/) by following the instructions at https://bun.sh/.

Install the remaining dependencies with [Homebrew](https://brew.sh/):

```bash
brew install buf go go-task golangci-lint mprocs sqlc wails yq
```

### Arch Linux

Install the official repository packages with [pacman](https://wiki.archlinux.org/title/Pacman):

```bash
sudo pacman -S buf bun go go-task go-yq golangci-lint sqlc
```

The Arch `go-task` package installs the binary as `go-task`. Add a shell alias so that `task` works:

```bash
# Add to your ~/.bashrc or ~/.zshrc
alias task=go-task
```

Install the remaining dependencies from the [AUR](https://wiki.archlinux.org/title/Arch_User_Repository) (using [yay](https://github.com/Jguer/yay) or your preferred AUR helper):

```bash
yay -S mprocs-bin wails
```

### Operating System

LeapMux is developed and tested on macOS and Linux. Windows support may require WSL.

---

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

The `task dev` command uses `mprocs` to run two processes concurrently:
- **Backend** — Runs Hub + Worker together in dev mode (with `-dev-frontend` flag to proxy to the frontend dev server)
- **Frontend** — Bun dev server for the SolidJS web application

To run in solo mode (localhost-only, no login) instead of dev mode during development:
```bash
task dev-solo
```

---

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
task build-desktop    # Build desktop app for current platform (requires wails)
```

The `leapmux` binary is output to `backend/build/bin/`. The desktop app is output to `desktop/build/bin/`. On macOS, a `.dmg` installer is also created.

`task build` skips the desktop build automatically if `wails` is not installed.

### Testing

Run all tests (except E2E):
```bash
task test
```

Run specific test suites:
```bash
task test-backend       # Backend tests
task test-frontend      # Frontend tests (Vitest)
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
task lint-desktop    # Lint desktop Go code (requires wails)
```

Auto-fix lint violations:
```bash
task lint-fix            # Fix all (Go, frontend, desktop)
task lint-fix-backend    # Fix Go code (golangci-lint --fix)
task lint-fix-frontend   # Fix frontend code (ESLint --fix)
task lint-fix-desktop    # Fix desktop Go code (requires wails)
```

### Code Generation

Regenerate all generated code (Protocol Buffers and sqlc):
```bash
task generate
```

You can also run each generator individually:
```bash
task generate-proto   # Generate Protocol Buffer code (Go and TypeScript)
task generate-sqlc    # Generate type-safe SQL code for the hub
```

Task uses checksums to skip generation when source files haven't changed. To force regeneration, use `task --force generate`.

Always run `task generate-proto` after modifying `.proto` files in `/proto/leapmux/v1/`.
Always run `task generate-sqlc` after modifying `.sql` files in `/backend/internal/hub/db/queries/` or `/backend/internal/worker/db/queries/`.

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

### Cleaning

Remove all build artifacts and generated code:
```bash
task clean
```

---

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

| Variant          | Tag pattern         | Example                             |
|------------------|---------------------|-------------------------------------|
| Alpine (default) | `:<version>`        | `ghcr.io/org/leapmux:1.0.0`        |
| Ubuntu           | `:<version>-ubuntu` | `ghcr.io/org/leapmux:1.0.0-ubuntu` |

Tool and base image versions are centralized in the `versions.yaml` file at the repository root.

---

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
- **[SQLite](https://sqlite.org/)** - Embedded database
- **[sqlc](https://sqlc.dev/)** - Type-safe SQL code generation

### Worker (Claude Code Wrapper)

- **[flynn/noise](https://github.com/flynn/noise)** - Noise protocol implementation for E2EE channel handling
- **[Git](https://git-scm.com/)** - Repository info and worktree management
- **[Go](https://go.dev/)** - Primary language
- **[gRPC](https://grpc.io/)** - Communication with Hub
- **[SQLite](https://sqlite.org/)** - Embedded database for agent and terminal state

### Desktop (optional)

- **[Wails](https://wails.io/)** - Desktop application framework (Go + WebView)

### Build Tools

- **[buf](https://buf.build/)** - Protocol Buffer tooling
- **[ESLint](https://eslint.org/)** - TypeScript/JavaScript linting
- **[golangci-lint](https://golangci-lint.run/)** - Go linting
- **[mprocs](https://github.com/pvolok/mprocs)** - Multi-process runner for development
- **[Task](https://taskfile.dev/)** - Build orchestration with checksum-based caching

---

## Project Structure

```
leapmux/
├── .github/workflows/      # CI, Docker, and release workflows
│
├── backend/                # Go backend module
│   ├── build/              # Build output (gitignored)
│   │
│   ├── cmd/leapmux/        # Unified binary entry point
│   │   ├── hub.go          # Hub mode
│   │   ├── main.go         # Subcommand routing (hub, worker, solo, dev)
│   │   ├── solo.go         # Solo/dev mode (hub + worker, default)
│   │   └── worker.go       # Worker mode
│   │
│   ├── generated/proto/    # Generated Go protobuf code (gitignored)
│   │
│   ├── hub/                # Hub public API (thin wrapper)
│   │   └── server.go       # NewServer(), Serve(), RegisterBackend(), etc.
│   │
│   ├── internal/
│   │   ├── config/         # Shared configuration loading (koanf-based)
│   │   │
│   │   ├── hub/            # Hub implementation
│   │   │   ├── agentmgr/   # Agent event broadcasting
│   │   │   ├── auth/       # Session-based authentication
│   │   │   ├── bootstrap/  # Database initialization and seeding
│   │   │   ├── channelmgr/ # E2EE channel routing and chunk validation
│   │   │   ├── config/     # Hub configuration
│   │   │   ├── db/         # Database driver, migrations, and queries
│   │   │   ├── frontend/   # Frontend asset embedding and dev proxy
│   │   │   ├── generated/  # sqlc-generated code (gitignored)
│   │   │   ├── layout/     # Workspace tiling layout management
│   │   │   ├── notifier/   # Worker notification queue (persistent delivery with retries)
│   │   │   ├── service/    # RPC service implementations (auth, workspace, channel relay)
│   │   │   ├── terminalmgr/# Terminal session management
│   │   │   ├── timeout/    # Timeout configuration
│   │   │   ├── validate/   # Input validation
│   │   │   └── workermgr/  # Worker connection registry and pending approvals
│   │   │
│   │   ├── logging/        # Structured logging and middleware
│   │   ├── metrics/        # Prometheus metrics and interceptors
│   │   ├── noise/          # Noise_NK protocol and key fingerprinting
│   │   ├── util/           # Shared utilities (id, lexorank, msgcodec, timefmt, testutil)
│   │   │
│   │   └── worker/         # Worker implementation
│   │       ├── agent/      # Claude Code process management
│   │       ├── channel/    # E2EE channel session management and dispatch
│   │       ├── config/     # Worker configuration
│   │       ├── db/         # Worker database driver, migrations, and queries
│   │       ├── filebrowser/# File system access
│   │       ├── gitutil/    # Git repository utilities
│   │       ├── hub/        # gRPC client to Hub (with auto-reconnect)
│   │       ├── service/    # Agent, terminal, file, and git service handlers
│   │       └── terminal/   # PTY session management
│   │
│   ├── solo/               # Shared solo mode startup logic
│   │
│   └── worker/             # Worker public API (thin wrapper)
│       └── runner.go       # Run(), RunConfig
│
├── desktop/                # Wails desktop application (optional)
│   ├── build/              # Build output (gitignored)
│   ├── frontend/           # Minimal loader page (redirects to embedded UI)
│   ├── platform/           # Platform-specific build resources (icons, manifests)
│   └── scripts/            # Icon generation, DMG creation
│
├── docker/                 # Dockerfile and s6-overlay service definitions
│
├── frontend/               # SolidJS web application
│   ├── src/
│   │   ├── api/            # ConnectRPC client setup
│   │   ├── components/     # UI components (chat, terminal, filebrowser, shell, etc.)
│   │   ├── context/        # Auth, Org, Workspace, and Preferences providers
│   │   ├── generated/      # Generated TypeScript protobuf code (gitignored)
│   │   ├── hooks/          # Custom hooks
│   │   ├── lib/            # Utility libraries
│   │   ├── routes/         # Route definitions
│   │   ├── stores/         # State management (agents, chat, terminals, etc.)
│   │   ├── styles/         # Global styles and themes
│   │   ├── types/          # TypeScript type definitions
│   │   └── utils/          # Shared utility functions
│   └── tests/              # Unit tests (Vitest) and E2E tests (Playwright)
│
├── icons/                  # SVG icons (light, dark, and default variants)
│
├── proto/                  # Protocol Buffer definitions
│   └── leapmux/v1/        # Service and message definitions
│
├── buf.gen.yaml            # Protocol Buffer code generation targets
├── buf.yaml                # Protocol Buffer linting configuration
├── go.work                 # Go workspace (backend + desktop modules)
├── mprocs.yaml             # Dev mode process configuration (task dev)
├── mprocs-solo.yaml        # Solo mode process configuration (task dev-solo)
├── README.md               # This file
├── Taskfile.yaml           # Build orchestration (go-task.dev)
└── versions.yaml           # Version string and tool/image versions
```

---

## Contributing

We welcome contributions to LeapMux! Here's how to get started:

### Development Workflow

1. **Fork the repository** and clone your fork
2. **Create a feature branch**: `git checkout -b feature/your-feature-name`
3. **Make your changes** following the code style guidelines
4. **Run code generation** if you modified `.proto` or `.sql` files: `task generate`
5. **Run tests**: `task test`
6. **Run linters**: `task lint`
7. **Commit your changes** with clear commit messages
8. **Push to your fork** and submit a pull request

### Code Style Guidelines

- **Go**: Follow standard Go conventions (run `gofmt`, use `golangci-lint`)
- **TypeScript/JavaScript**: Follow ESLint rules configured in the project
- **Protocol Buffers**: Use `buf lint` to validate `.proto` files

### Testing Requirements

All contributions should include:
- Unit tests for new functionality
- Integration tests for cross-component features
- E2E tests for user-facing features (when applicable)

Ensure all linters and tests pass before submitting:
```bash
task lint
task test
task test-e2e
```

### Code Generation

When you modify Protocol Buffer definitions or SQL queries:
1. Run `task generate-proto` for `.proto` changes, `task generate-sqlc` for `.sql` changes, or `task generate` for both
2. Generated code is `.gitignore`'d and should not be committed — only commit the source changes
3. Ensure tests still pass after regeneration

---

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.

This means:
- You can use, modify, and distribute the software
- There are certain limitations on competitive use
- The license will automatically convert to Apache 2.0 after a specified period

See the [LICENSE](LICENSE.md) file for full details.

---

## Project Status

**Version**: 0.0.1-dev
**Status**: Early Alpha (Active Development)

LeapMux is in active development. The API and architecture may change as we iterate toward a stable release.

---

**Built with ❤️ by the LeapMux team**
