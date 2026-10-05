<div align="center">
  <img src="icons/leapmux-icon.svg" alt="LeapMux" width="128" height="128">
</div>

# LeapMux

[![Docs](https://img.shields.io/badge/docs-leapmux.dev-0d9488)](https://leapmux.dev/)
[![Release](https://img.shields.io/github/v/release/leapmux/leapmux?include_prereleases&label=release)](https://github.com/leapmux/leapmux/releases)
[![Container](https://img.shields.io/badge/container-ghcr.io%2Fleapmux%2Fleapmux-2496ED?logo=docker&logoColor=white)](https://github.com/leapmux/leapmux/pkgs/container/leapmux)
[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue.svg)](LICENSE.md)
[![llms.txt](https://img.shields.io/badge/llms.txt-available-0d9488)](https://leapmux.dev/llms.txt)


Shell tabs work well for one or two coding agents.
With three or four agents, you can lose track of which agent owns each branch.
Agents can overwrite each other's work in a shared working tree.
A tmux crash or machine restart requires you to resume each agent and restore the layout manually.

LeapMux runs several coding agents and shell terminals in one workspace.
Choose each agent's git worktree and branch.
Arrange the views in tiles or floating windows on a local or remote machine.
Sessions stay attached across restarts.
LeapMux encrypts Frontend↔Worker traffic from end to end.
Use LeapMux in a browser or as a native desktop app.

## Supported Agents

<p>
  <a href="https://claude.com/product/claude-code"><img src="icons/agents/claude-code.svg" width="64" height="64" alt="Claude Code" title="Claude Code"></a>&nbsp;
  <a href="https://openai.com/codex/"><img src="icons/agents/codex.svg" width="64" height="64" alt="Codex" title="Codex"></a>&nbsp;
  <a href="https://cursor.com/cli"><img src="icons/agents/cursor.svg" width="64" height="64" alt="Cursor" title="Cursor"></a>&nbsp;
  <a href="https://github.com/features/copilot/cli"><img src="icons/agents/github-copilot.svg" width="64" height="64" alt="GitHub Copilot" title="GitHub Copilot"></a>&nbsp;
  <a href="https://opencode.ai/"><img src="icons/agents/opencode.svg" width="64" height="64" alt="OpenCode" title="OpenCode"></a>&nbsp;
  <a href="https://pi.dev/"><img src="icons/agents/pi.svg" width="64" height="64" alt="Pi" title="Pi"></a>&nbsp;
  <a href="https://kilo.ai/cli"><img src="icons/agents/kilo.svg" width="64" height="64" alt="Kilo" title="Kilo"></a>&nbsp;
  <a href="https://block.github.io/goose/"><img src="icons/agents/goose.svg" width="64" height="64" alt="Goose" title="Goose"></a>&nbsp;
  <a href="https://github.com/esengine/DeepSeek-Reasonix"><img src="icons/agents/reasonix.svg" width="64" height="64" alt="Reasonix" title="Reasonix"></a>&nbsp;
  <a href="https://zcode.z.ai/"><img src="icons/agents/zcode.svg" width="64" height="64" alt="ZCode" title="ZCode"></a>&nbsp;
  <a href="https://codewhale.net/en/product"><img src="icons/agents/codewhale.svg" width="64" height="64" alt="Codewhale" title="Codewhale"></a>&nbsp;
  <a href="https://moonshotai.github.io/kimi-code/en/"><img src="icons/agents/kimi-code.svg" width="64" height="64" alt="Kimi Code" title="Kimi Code"></a>&nbsp;
  <a href="https://mimo.xiaomi.com/coder"><img src="icons/agents/mimo-code.svg" width="64" height="64" alt="MiMo Code" title="MiMo Code"></a>&nbsp;
  <a href="https://qwenlm.github.io/qwen-code-docs/en/users/overview"><img src="icons/agents/qwen-code.svg" width="64" height="64" alt="Qwen Code" title="Qwen Code"></a>&nbsp;
  <a href="https://omp.sh"><img src="icons/agents/oh-my-pi.svg" width="64" height="64" alt="Oh My Pi" title="Oh My Pi"></a>&nbsp;
  <a href="https://x.ai/cli"><img src="icons/agents/grok-build.svg" width="64" height="64" alt="Grok Build" title="Grok Build"></a>&nbsp;
  <a href="https://kiro.dev/cli/"><img src="icons/agents/kiro.svg" width="64" height="64" alt="Kiro" title="Kiro"></a>&nbsp;
  <a href="https://ampcode.com/"><img src="icons/agents/amp.svg" width="64" height="64" alt="Amp" title="Amp"></a>&nbsp;
  <a href="https://cline.bot/cli"><img src="icons/agents/cline.svg" width="64" height="64" alt="Cline" title="Cline"></a>&nbsp;
  <a href="https://codebuddy.ai"><img src="icons/agents/codebuddy.svg" width="64" height="64" alt="CodeBuddy Code" title="CodeBuddy Code"></a>&nbsp;
  <a href="https://www.jetbrains.com/junie/"><img src="icons/agents/junie.svg" width="64" height="64" alt="Junie" title="Junie"></a>&nbsp;
  <a href="https://docs.letta.com/letta-code"><img src="icons/agents/letta.svg" width="64" height="64" alt="Letta Code" title="Letta Code"></a>&nbsp;
  <a href="https://dirac.run"><img src="icons/agents/dirac.svg" width="64" height="64" alt="Dirac" title="Dirac"></a>&nbsp;
  <a href="https://qoder.com"><img src="icons/agents/qoder.svg" width="64" height="64" alt="Qoder CLI" title="Qoder CLI"></a>&nbsp;
  <a href="https://docs.factory.ai/"><img src="icons/agents/droid.svg" width="64" height="64" alt="Factory Droid" title="Factory Droid"></a>&nbsp;
  <a href="https://fast-agent.ai"><img src="icons/agents/fast-agent.svg" width="64" height="64" alt="Fast Agent" title="Fast Agent"></a>&nbsp;
  <a href="https://commandcode.ai/"><img src="icons/agents/command-code.svg" width="64" height="64" alt="Command Code" title="Command Code"></a>&nbsp;
  <a href="https://github.com/deepseek-ai/deepseek-harness"><img src="icons/agents/deepseek-harness.svg" width="64" height="64" alt="DeepSeek Harness" title="DeepSeek Harness"></a>&nbsp;
  <a href="https://geminicli.com/"><img src="icons/agents/gemini-cli.svg" width="64" height="64" alt="Gemini CLI" title="Gemini CLI"></a>
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

LeapMux includes one Go binary (`leapmux`), a SolidJS frontend, and a Tauri desktop shell.
These parts form three components:

- **Frontend** — A SolidJS web app that renders the workspace UI. The desktop app embeds the same frontend. Its views include:
  - A tiling layout.
  - Agents.
  - Terminals.
  - A file browser.
- **Hub** — A Go service that manages login and workspaces. It registers workers and supplies an authenticated **relay** for encrypted Frontend↔Worker traffic. Its storage supports:
  - SQLite, the default.
  - PostgreSQL.
  - MySQL.
  - CockroachDB.
  - YugabyteDB.
  - TiDB.
- **Worker** — A Go process that keeps its own SQLite database. It connects **outbound** to the Hub, so it can operate behind a NAT. It runs:
  - Agents.
  - PTYs.
  - File browsing.
  - Git operations.

The binary runs in several modes:

| Command | Description |
|---------|-------------|
| `leapmux solo` | Hub + Worker, single-user; local IPC needs no credential and TCP starts with password setup |
| `leapmux hub` | Central service only (auth, relay, database) |
| `leapmux worker` | Connects to a remote Hub |
| `leapmux dev` | Hub + Worker on all interfaces, login required |
| `leapmux control admin` | Online, authenticated CLI for users, workers, OAuth, settings, and tokens |
| `leapmux recover` | Offline break-glass CLI: first-admin bootstrap, password reset, encryption keys, database |

The connections use these protocols:

- Frontend↔Hub uses ConnectRPC.
- Frontend↔Worker uses hybrid post-quantum Noise_NK over one WebSocket that the Hub relays.
- Worker↔Hub uses gRPC.

The Hub routes traffic but cannot read Frontend↔Worker content.
The wire format uses Protocol Buffers in [`/proto/leapmux/v1/`](proto/leapmux/v1/).
Each constant that multiple languages share lives once in [`/contracts/`](contracts/).
The generators produce that constant for Go, TypeScript, and Rust.

LeapMux supports exactly one active Hub process per database.
The Hub holds a runtime lease in the database.
It refuses to serve when another live Hub owns that lease.
A replacement can take over after the owner releases the lease or the lease expires.
Administrative CLI processes can access the database while the Hub runs.

For the full architecture, deployment modes, and threat model, see the **[Concepts](https://leapmux.dev/docs/getting-started/concepts/)** and **[Security & Threat Model](https://leapmux.dev/docs/admin/security/)** chapters at [leapmux.dev](https://leapmux.dev).

## Prerequisites

This README describes how to build and develop LeapMux from source.
To use LeapMux, download a desktop app or server build from the [releases page](https://github.com/leapmux/leapmux/releases).
Read the user documentation at [leapmux.dev](https://leapmux.dev).

Before you begin, ensure you have the following installed:

- **Go** 1.27.1 or later
- **Node.js** 24 or later
- **Bun** 1.4.0 or later - JavaScript runtime and package manager
- **Task** - Task runner (replaces Make)
- **buf** CLI - Protocol Buffer code generation ([authentication](https://buf.build/docs/bsr/authentication/) recommended to avoid rate-limit errors)
- **protobuf** (`protoc`) - Protocol Buffer compiler (required by Tauri's `prost-build`)
- **SQLite** (usually pre-installed on most systems)
- **Docker** - Required for building Docker images (on macOS, [Rancher Desktop](https://rancherdesktop.io/) is recommended)
- **dekit** - Multi-process runner (required for `task dev`, `task dev-solo`, and `task dev-desktop`)
- **Rust toolchain** - For the Tauri desktop app (built by `task build`)
- **Tauri desktop prerequisites** - WebView/system packages required by Tauri on your platform

The Go build tools live as `tool` dependencies in `backend/go.mod`:

- `sqlc`
- `golangci-lint`
- `gotestsum`

Task runs each tool through `go tool <name>`.
You do not need to install these tools separately.

Install dekit with its [official installation instructions](https://github.com/pvolok/dekit#install) for your operating system.

### macOS

Install [Bun](https://bun.sh/) by following the instructions at https://bun.sh/.

Install the remaining dependencies with [Homebrew](https://brew.sh/):

```bash
brew install buf go go-task node protobuf rust
```

To build Docker images, install [Rancher Desktop](https://rancherdesktop.io/) or another Docker-compatible runtime separately.
Docker Desktop and OrbStack work also.

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

For desktop app builds, install these dependencies:

- The [Tauri prerequisites for Arch Linux](https://v2.tauri.app/start/prerequisites/#linux).
- GStreamer, which `bundleMediaFramework` includes in the AppImage.
- `dpkg`, whose `dpkg-deb` command builds the `.deb` bundle. Arch does not install `dpkg` by default.

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
winget install --id SUSE.RancherDesktop --source winget  # or any other Docker-compatible runtime (e.g. Docker.DockerDesktop, Podman.Podman)
winget install --id Rustlang.Rust.MSVC --source winget
winget install --id Google.Protobuf --source winget
winget install --id Microsoft.VisualStudio.BuildTools --source winget  # or Microsoft.VisualStudio.2022.Community if you prefer the full IDE
```

After `Microsoft.VisualStudio.BuildTools` installs, open the Visual Studio Installer.
Enable the **"Desktop development with C++"** workload in the installation.
Winget installs the bootstrapper but does not select a workload automatically.

For the remaining Tauri Windows prerequisites (WebView2, etc.), see the [Tauri Windows prerequisites](https://v2.tauri.app/start/prerequisites/#windows).

## Quick Start

Get LeapMux running locally:

```bash
# 1. Clone the repository
git clone https://github.com/leapmux/leapmux.git
cd leapmux

# 2. Generate code and download assets (protobuf, contracts, sqlc, and spinner JSON — not checked into git)
task generate

# 3. Start all services (requires dekit)
task dev
```

After all services start, open this URL in your browser:

```
http://localhost:4327
```

Each `dev` target generates code and builds its prerequisites.
It then runs `dekit mprocs` to start the processes concurrently:

| Command | Processes | Description |
|---------|-----------|-------------|
| `task dev` | Go backend (`leapmux dev`) + Bun frontend dev server | Full-featured dev mode on all interfaces, login required |
| `task dev-solo` | Go backend (`leapmux solo`) + Bun frontend dev server | Localhost-only, single-user; the browser sets the first `solo` password |
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

The build writes the `leapmux` binary to the repository root.
Tauri writes desktop bundles under `desktop/rust/target/`.
The build copies these final artifacts to the repository root also:

- macOS: `.dmg`
- Linux: `.AppImage` and `.deb`
- Windows: `.msi` and `.exe`

### Testing

Run all tests except end-to-end (E2E) tests:
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
task test-e2e -- tests/e2e/pi/text-attachments.spec.ts
task test-e2e -- --grep "repaints the app"
```

### Coding agent coverage

The source checklist lives beside the provider specs under `frontend/tests/e2e/feature-matrix/`.
Two JSON files define the coverage:

- [features.json](frontend/tests/e2e/feature-matrix/features.json) holds stable feature IDs and precise descriptions.
  It holds display labels and website publication choices also.
  It lists the feature groups, and each published feature names its group.
  The website shows the groups in that order, and the published features of a group stand together.
- [checklist.json](frontend/tests/e2e/feature-matrix/checklist.json) holds each provider and feature combination.
  Each entry supplies its distinct spec path and its support state.
  It records source verification and browser results separately.
  Each entry holds two notes. The user note is one short paragraph with no implementation detail and no raw HTML.
  It keeps upstream issue links, and each link is an `https://` link.
  The detail note holds the evidence for the maintainers.
  The website shows the user note only.

A support state is one of three values:

- `supported`: LeapMux delivers the feature with this agent.
- `agent-limit`: the native protocol of the tested agent version cannot carry the feature.
- `leapmux-limit`: the native protocol carries the feature, and LeapMux does not use it yet.

A limited cell needs a detail note. A limited cell of a published feature needs a user note also.
A hidden feature has no user note, because the website does not show it.
`task validate-json` requires each cell to name its own spec and to mark that spec as passed.

Feature IDs use kebab-case and match the spec filenames.
For example, Pi's `text-attachments` entry points to `frontend/tests/e2e/pi/text-attachments.spec.ts`.
A pending browser result does not mean that the feature is unsupported.
Mark a browser entry as passed only after its complete spec passes.
Unsupported features need native refusal or limitation assertions also.

Run the affected provider specs with the examples below.
Run `task test-scripts` to check the source matrix and script behavior.
Run `task site` to generate the published feature matrix from these same files.
The site includes published features and their descriptions.
The checklist includes additional provider tests that the site does not publish.

### E2E execution

The E2E launcher splits the selected files into isolated Playwright processes.
The default process count is the smaller of four and the available CPU capacity.
The selected file count limits the actual shard count also.
Each process runs one Playwright worker and shares its fixtures across its own tests.
Each process owns these resources:

- A LeapMux server and a mock model server.
- A browser context and a shared tab.
- Private provider configuration and databases.
- Private native process records and binary copies.
- Private reports and test artifacts.

The launcher builds the backend once before any shard starts.
It uses the same binary for every shard.
No E2E test sends a request to a real model.

Use `--workers` to select the number of isolated processes:

```bash
task test-e2e -- --workers=2
task test-e2e -- --workers=50% tests/e2e/pi/
task test-e2e -- --workers=1 tests/e2e/pi/
```

The launcher consumes this option.
Every child still uses `--workers=1` and zero retries.
The launcher rejects `--fully-parallel` because tests within a shard share mutable fixtures.

Interactive modes use serial execution.
Custom configurations and manually selected shards use serial execution also.
Invocation-wide deadlines and failure limits retain serial execution.
Snapshot updates and source updates use serial execution also.

Serial and parallel runs retain artifacts under `frontend/test-results/runs/<run-id>/`.
An explicit `--output=<directory>` changes the root to `<directory>/runs/<run-id>/`.
Each run retains its complete console log and test artifacts.
The default configuration retains native reports also.
Serial runs preserve the caller's reporter options and explicit report destinations.
For parallel runs, the launcher merges native blob reports and verifies every selected test case.
The parallel merge preserves each case's project and repeat count also.
Use `--reporter` to select reporters for the combined parallel result.
Each parallel shard retains its internal list, blob, and JSON reports.

For parallel runs, the launcher prints the combined JSON report path.
Set `PLAYWRIGHT_JSON_OUTPUT_FILE` to select that report's destination.
Alternatively, set `PLAYWRIGHT_JSON_OUTPUT_DIR` and `PLAYWRIGHT_JSON_OUTPUT_NAME`.
The explicit file value takes precedence.
Failed runs retain reports and attachments after native process cleanup.
Later serial or parallel runs preserve these artifacts.

Use `--last-failed` to run the failed cases from the combined result:

```bash
task test-e2e -- --workers=2 tests/e2e/pi/
task test-e2e -- --last-failed
```

The launcher stores the combined native selection at the output root's `.last-run.json`.
A complete passing run clears the prior failed selection.
A parallel run that selects no test leaves the saved selection unchanged.
The last-failed run uses parallel execution.
Each shard reads a private copy of the selection through `--last-failed-file`.
Only the merged result replaces the caller's `.last-run.json`.
The launcher refuses an absent or malformed selection, because a rerun of the failures must never become a complete run.
An explicit `--last-failed-file` or `PLAYWRIGHT_LAST_RUN_OUTPUT_FILE` selects another state file.
If the preceding run used `--output=<directory>`, use the same option for its last-failed run.

Use `--failed-files` to run every file that holds a test without a complete clean result:

```bash
task test-e2e -- --failed-files
task test-e2e -- --failed-files-from=test-results/runs/<run-id>/report.json
```

This option reruns complete files, not single cases.
A complete file is the unit of acceptance for a matrix cell.
The launcher reads the last combined report, `.last-run-report.json` under the output root.
Each parallel run saves that report after its coverage check, also a run that selects only some tests.
A serial run saves no report.
The launcher refuses a report that is older than the last-run state, because a later run replaced the state.
`--failed-files-from` reads an explicit combined report instead.
It resolves a relative path against the `frontend` directory, as `--output` does.
The option selects its own files.
Do not combine it with `--last-failed`, `--test-list`, `--only-changed`, or file arguments.
Do not combine it with `--grep`, `--grep-invert`, `--test-list-invert`, or `--shard`, because they select fewer tests of a file.
The launcher refuses an absent or malformed report.

The launcher balances the shards by the measured duration of each file.
Each parallel run records the duration of every file in `.file-durations.json` under the output root.
The next run assigns the files to the shards longest first, from that history.
Without a usable history, the launcher uses the native `--shard=i/N` split.
Use `--balance=off` to select the native split.

Use `--pass-with-no-tests` to accept an empty selection.
Without it, a rerun fails before the build when the failed selection lists no test.

### Linting

Run all linters:
```bash
task lint
```

Run specific linters:
```bash
task lint-versions   # Check documented tool versions against versions.env
task validate-json   # Validate every project-written JSON file against its schema
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

Regenerate all generated code and downloaded assets (Protocol Buffers, contracts, sqlc, and spinner JSON):
```bash
task generate
```

You can also run each generator individually:
```bash
task generate-proto      # Generate Protocol Buffer code (Go and TypeScript)
task generate-contracts  # Generate Go/TS/Rust constants from contracts/*.json (the single source for every cross-language value)
task generate-sqlc       # Generate type-safe SQL code (hub and worker)
task generate-spinners   # Download spinner verb JSON files from awesome-claude-spinners
```

Task uses checksums to skip generation when source files stay the same.
Run `task --force generate` to force generation.

Run `task generate-proto` after you modify `.proto` files in `/proto/leapmux/v1/`.
Run `task generate-sqlc` after you modify SQL queries in these directories:

- `/backend/internal/hub/store/*/db/queries/`
- `/backend/internal/worker/db/queries/`

Run `task generate-contracts` after you modify `contracts/*.json`.
A new proto enum value, such as AgentProvider or Scope, fails generation until its contract entry exists.

### Preparation

Prepare every module for builds (code generation, frontend install, asset generation, icon generation, and embedding the frontend into the backend):
```bash
task prepare
```

You can also run each step individually:
```bash
task prepare-frontend   # Generate proto/contracts/spinners, run bun install, generate icons, copy NOTICE.html
task prepare-backend    # Generate proto/contracts/sqlc, build the frontend, and embed it into the backend
task prepare-desktop    # Generate proto/contracts, build the frontend, prepare the backend, and generate desktop icons
```

Build targets run their required preparation steps automatically.
You can run `task build` without a separate `task prepare` command.

### Third-Party License Notice

Generate `NOTICE.md` and `NOTICE.html` with all third-party dependency licenses:
```bash
task generate-notice
```

Run this command after you change dependencies.
Regular build targets do not run it.
The task fails if a dependency lacks a license file.
It fails also if a vendored override's license identifier differs from the upstream package.

### Cleaning

Remove all build artifacts and generated code:
```bash
task clean
```

Clean a specific module:
```bash
task clean-backend    # Remove leapmux binaries and generated/ directories
task clean-frontend   # Remove .output, .vinxi, node_modules, and generated/ directories
task clean-desktop    # Remove desktop binaries, bundles, the generated contracts module, and Rust target/
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

The default build targets `linux/amd64` and `linux/arm64`.
You can override the platform and tag:

```bash
task docker-build-alpine PLATFORM=linux/amd64 TAG=leapmux:dev
```

The image uses a multi-stage build (buf, Bun, Go). Tool and base image versions are centralized in `versions.env` at the repository root.

### Tool versions

`versions.env` is the single source of truth for every toolchain and base-image version.
These consumers read it directly:

- `Taskfile.yaml` loads it through `dotenv:`.
- The CI workflows write it to `$GITHUB_ENV`.
- `docker/Dockerfile` reads it as build arguments.

This README and the documentation site cannot use variables for tool versions.
The `go` directive in each `go.mod` has the same restriction.
Task generates these values from `versions.env`:

```bash
task sync-versions   # rewrite those copies from versions.env
task lint-versions   # fail if one has drifted (runs as part of task lint)
```

After you edit `versions.env`, run `task sync-versions`.
Add a claim to `CLAIMS` in `scripts/sync-versions.mjs` when another location states a version.
A claim whose pattern matches nothing fails the build.
This check detects a changed sentence that no longer receives version updates.

### Documentation site

The site at [leapmux.dev](https://leapmux.dev) uses [Hugo](https://gohugo.io/) and [Hextra](https://imfing.github.io/hextra/).
Its source lives under `site/`.
Hugo lives as a `go tool` dependency in `site/go.mod`.
You do not need to install Hugo separately.

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

- **[CIRCL](https://github.com/cloudflare/circl)** - SLH-DSA post-quantum signatures for E2EE channel handling
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
- **[dekit](https://github.com/pvolok/dekit)** - Multi-process runner for development
- **[Task](https://taskfile.dev/)** - Build orchestration with checksum-based caching

## Project Structure

```
leapmux/
├── backend/             # Go backend: the unified `leapmux` binary (hub + worker)
│   ├── cmd/leapmux/     # Entry point, subcommand routing, recover + control trees
│   └── internal/
│       ├── hub/         # Hub: auth, channel relay, pluggable store, keystore, OAuth
│       └── worker/      # Worker: agents, terminals, file browser, git, E2EE channel
│           └── agent/   # Agent runtime: the neutral API, and one package per provider in providers/
├── contracts/           # Cross-language constant contracts (JSON + sibling JSON Schema), generated into Go/TS/Rust
├── desktop/             # Tauri v2 desktop app (Rust shell + Go sidecar)
├── docker/              # Dockerfile and s6-overlay service definitions
├── frontend/            # SolidJS web app
│   └── src/components/  # UI: chat (with provider plugins), terminal, files, shell
├── icons/               # App and agent-provider SVG icons
├── proto/leapmux/v1/    # Protocol Buffer service and message definitions
├── scripts/             # Build scripts: contracts/proto generation, JSON-schema validation, NOTICE, icons
├── site/                # Hugo + Hextra documentation site (leapmux.dev)
│   └── content/docs/    # The user manual
├── testdata/            # Cross-language conformance corpora (JSON + JSON Schema), replayed by the Go and TS suites
├── go.work              # Go workspace (backend + desktop/go)
├── Taskfile.yaml        # Build orchestration (go-task.dev)
└── versions.env         # Version string and tool/image versions
```

## Contributing

We do not accept code contributions yet because of the license requirements.
LeapMux uses FSL-1.1-ALv2, which converts to Apache 2.0 over time.
That conversion requires us to hold the rights to every line of code.
Without a Contributor License Agreement (CLA), external contributions would require us to obtain each contributor's consent before the conversion.
Obtaining consent from every past contributor would be very difficult.
We expect to accept external contributions after we prepare a CLA.

Create issues in the meantime.
Include a plan from a frontier model if possible.
We will review those issues.

## License

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.

This means:

- You can use, modify, and distribute the software
- There are certain limitations on competitive use
- The license automatically converts to Apache 2.0 two years after each release is first made available

See the [LICENSE](LICENSE.md) file for full details.

## Disclaimer

All product names, logos, and trademarks belong to their respective owners.
LeapMux has no affiliation, endorsement, or sponsorship from these companies or any other third party:

- Alibaba Cloud
- Amazon Web Services
- Amp Frontier Corporation
- Anomaly
- Anthropic
- Anysphere
- Apple
- Block
- Cline Bot Inc.
- Cognition
- Don Ho
- Earendil
- Factory
- GitHub
- Google
- JetBrains
- Kilo Code
- Letta, Inc.
- Microsoft
- Moonshot AI
- OpenAI
- Stencil Labs
- Sublime HQ
- xAI
- Xiaomi
- Z.ai
- Zed Industries

Coding agent icons indicate compatibility only.
Editor and IDE icons serve the same purpose.
LeapMux reproduces these icons for identification only.
