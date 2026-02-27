<p align="center">
  <img src="icons/leapmux-icon.svg" alt="LeapMux" width="128" height="128">
</p>

# LeapMux

**AI Coding Agent Multiplexer**

LeapMux is a platform for running and managing multiple Claude Code instances through a centralized web interface.

[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)
![Version](https://img.shields.io/badge/version-0.0.1--dev-orange.svg)

---

## Table of Contents

- [Overview](#overview)
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

## Overview

LeapMux is an **AI coding agent multiplexer** that enables developers to run multiple Claude Code instances simultaneously from a single web interface. It provides a centralized hub for managing AI coding agents across different workspaces and backends.

### What is LeapMux?

LeapMux acts as a multiplexer for Claude Code instances, allowing you to:
- Run multiple AI coding agents in parallel across different workspaces
- Access remote development backends through a web interface
- Share workspaces and backends with team members
- Interact with terminals and browse files on remote systems
- Manage agent lifecycles, messages, and permissions centrally

### Key Features

- **Multi-Agent Workspaces**: Run multiple Claude Code instances simultaneously
- **Terminal Access**: Interactive PTY sessions with real-time I/O streaming
- **File Browser**: Browse and read files on remote backends
- **Workspace Sharing**: Collaborate by sharing workspaces with specific users or organizations
- **Backend Management**: Register and manage multiple development backends
- **Real-Time Communication**: Bidirectional streaming for messages and events
- **Permission Management**: Control requests for agent permission prompts
- **Message Delivery**: Reliable message delivery with retry and error handling

---

## Architecture

LeapMux uses a three-tier architecture with a centralized hub mediating all communication between the frontend and workers:

```
┌─────────────────┐  ConnectRPC  ┌─────────────────┐                 ┌───────────────────┐
│                 │  WebSocket   │                 │   gRPC (bidi)   │  Worker 1         │
│    Frontend     │◄────────────►│       Hub       │◄───────────────►│  ┌─────────────┐  │
│    (Browser)    │              │    (Central)    │                 │  │ Claude Code │  │
│                 │              │                 │                 │  │ (multiple)  │  │
│    SolidJS      │              │   Go Service    │                 │  └─────────────┘  │
│    Web App      │              │   + Database    │                 └───────────────────┘
│                 │              │                 │                           ⋮
└─────────────────┘              └─────────────────┘                 ┌───────────────────┐
                                          │                          │  Worker N         │
                                          ▼                          │  ┌─────────────┐  │
                                  ┌───────────────┐                  │  │ Claude Code │  │
                                  │    SQLite     │                  │  │ (multiple)  │  │
                                  │               │                  │  └─────────────┘  │
                                  └───────────────┘                  └───────────────────┘
```

LeapMux is built as a single Go binary (`leapmux`) that can run in three modes:
- **`leapmux hub`** — Runs only the Hub (central service)
- **`leapmux worker`** — Runs only a Worker (connects to a remote Hub)
- **`leapmux`** (no subcommand) — Runs Hub + Worker together (standalone mode)
- **`leapmux version`** — Prints the version and exits

### Components

**Frontend (SolidJS)**
- Web application providing the user interface
- Communicates with Hub via ConnectRPC and WebSocket (for event streaming)
- Manages UI state for workspaces, agents, terminals, and file browsing
- Real-time message streaming and chat interface

**Hub (Go)**
- Central orchestration service that routes all traffic
- Manages user authentication, workspaces, and worker registration
- Stores persistent data in SQLite
- Handles bidirectional streaming for real-time events
- No direct Frontend-Worker communication (all traffic goes through Hub)

**Worker (Go)**
- Wraps Claude Code instances and provides system access
- Manages PTY sessions for terminal access
- Provides file system browsing capabilities
- Communicates with Hub via standard gRPC (over TCP or Unix domain socket)
- Auto-reconnects to Hub on disconnection

### Communication

- **Frontend → Hub**: ConnectRPC (gRPC-compatible) for RPCs, WebSocket for event streaming
- **Worker → Hub**: Standard gRPC with bidirectional streaming (over TCP or Unix domain socket).
  - Workers initiate outbound connections to the Hub, so they can run behind NATs, without requiring inbound port access.
  - For local workers on the same machine, connect via Unix domain socket using `unix:<socket-path>` as the Hub URL.
- **Message Format**: Protocol Buffers (defined in `/proto/leapmux/v1/`)

---

## Prerequisites

Before you begin, ensure you have the following installed:

- **Go** 1.25.7 or later
- **Bun** (latest version) - JavaScript runtime and package manager
- **Task** - Task runner (replaces Make)
- **buf** CLI - Protocol Buffer code generation ([authentication](https://buf.build/docs/bsr/authentication/) recommended to avoid rate-limit errors)
- **sqlc** - Type-safe SQL code generation
- **golangci-lint** - Go linter
- **yq** - YAML processor (used to read `versions.yaml`)
- **SQLite** (usually pre-installed on most systems)
- **Docker** - Required for building Docker images (on macOS, [Rancher Desktop](https://rancherdesktop.io/) is recommended)
- **mprocs** (optional, for easier multi-process development)

### macOS

Install [Bun](https://bun.sh/) by following the instructions at https://bun.sh/.

Install the remaining dependencies with [Homebrew](https://brew.sh/):

```bash
brew install buf go go-task golangci-lint mprocs sqlc yq
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
- **Backend** — Runs Hub + Worker together in standalone mode (with `-dev-frontend` flag to proxy to the frontend dev server)
- **Frontend** — Bun dev server for the SolidJS web application

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
```

The `leapmux` binary is output to the project root.

### Testing

Run all tests:
```bash
task test
```

Run specific test suites:
```bash
task test-backend       # Go unit tests (hub + worker)
task test-frontend      # Frontend unit tests (Vitest)
task test-integration   # Integration tests
task test-e2e           # End-to-end tests (Playwright)
```

Run specific tests by passing arguments after `--`:
```bash
# Go tests: -run <regex> <packages>
task test-backend -- -run TestMyFunction ./internal/hub/...
task test-integration -- -run TestMyFunction ./internal/hub/...

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
Always run `task generate-sqlc` after modifying `.sql` files in `/internal/hub/db/queries/`.

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

The image uses a multi-stage build (buf, Bun, Go) and runs with [s6-overlay](https://github.com/just-containers/s6-overlay) for process supervision. The Hub listens on port 4327 and data is stored in the `/data` volume.

```bash
docker run -p 4327:4327 -v leapmux-data:/data leapmux:latest
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
- **[ConnectRPC](https://connectrpc.com/)** (@connectrpc/connect-web) - RPC client for browser
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

- **[Git](https://git-scm.com/)** - Repository info and worktree management
- **[Go](https://go.dev/)** - Primary language
- **[gRPC](https://grpc.io/)** - Communication with Hub

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
├── cmd/leapmux/            # Unified binary entry point
│   ├── hub.go              # Hub mode
│   ├── main.go             # Subcommand routing (hub, worker, standalone)
│   ├── standalone.go       # Standalone mode (hub + worker, default)
│   └── worker.go           # Worker mode
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
├── generated/proto/        # Generated Go protobuf code (gitignored)
│
├── hub/                    # Hub public API (thin wrapper)
│   └── server.go           # NewServer(), Serve(), RegisterBackend(), etc.
│
├── icons/                  # SVG icons (light, dark, and default variants)
│
├── internal/
│   ├── hub/                # Hub implementation
│   │   ├── agentmgr/       # Agent message routing and event broadcasting
│   │   ├── auth/           # Session-based authentication
│   │   ├── bootstrap/      # Database initialization and seeding
│   │   ├── config/         # Hub configuration
│   │   ├── db/             # Database driver, migrations, and queries
│   │   ├── email/          # Email sending
│   │   ├── frontend/       # Frontend asset embedding and dev proxy
│   │   ├── generated/      # sqlc-generated code (gitignored)
│   │   ├── id/             # Unique ID generation
│   │   ├── layout/         # Workspace tiling layout management
│   │   ├── lexorank/       # LexoRank ordering for sections
│   │   ├── msgcodec/       # Message compression (zstd)
│   │   ├── notifier/       # Worker notification queue (persistent delivery with retries)
│   │   ├── service/        # RPC service implementations
│   │   ├── terminalmgr/    # Terminal session management
│   │   ├── timeout/        # Timeout configuration
│   │   ├── validate/       # Input validation
│   │   └── workermgr/      # Worker connection registry and pending approvals
│   │
│   ├── logging/            # Structured logging and middleware
│   ├── metrics/            # Prometheus metrics and interceptors
│   ├── util/               # Shared utilities (timefmt, sanitize, testutil)
│   │
│   └── worker/             # Worker implementation
│       ├── agent/          # Claude Code process management
│       ├── config/         # Worker configuration
│       ├── filebrowser/    # File system access
│       ├── gitutil/        # Git repository utilities
│       ├── hub/            # gRPC client to Hub (with auto-reconnect)
│       └── terminal/       # PTY session management
│
├── proto/                  # Protocol Buffer definitions
│   └── leapmux/v1/         # Service and message definitions
│
├── worker/                 # Worker public API (thin wrapper)
│   └── runner.go           # Run(), RunConfig
│
├── buf.gen.yaml            # Protocol Buffer code generation targets
├── buf.yaml                # Protocol Buffer linting configuration
├── mprocs.yaml             # Development process configuration
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
task test-integration
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
