---
title: "Glossary"
description: "Short, alphabetical definitions of the terms used throughout the LeapMux manual, each linking to the chapter that covers it in depth."
type: docs
weight: 7
---

Short, alphabetical definitions of terms used throughout the LeapMux manual. Each entry links to the chapter that covers the topic in depth.

## A

### Active client (presence)

The client connection that owns the turn-end notification sound for a workspace. The role follows your most recent input and broadcasts over your account's event stream. When an agent finishes a turn, only the active client plays the sound; if no client is active, every connected client plays it. See [Workspaces — Live sync across devices](/docs/using/workspaces/#live-sync-across-devices).

### Agent

A coding agent — one of {{< agent-provider-count >}} supported coding-assistant CLIs hosted by LeapMux on a Worker, one process per agent tab. You chat with it, inspect inline tool calls and diffs, adjust its model and reasoning effort, and resume it across restarts. See [Coding Agents](/docs/using/coding-agents/) and [Supported Agents](/docs/reference/supported-agents/).

### App

A program that requests access to an account on a LeapMux Hub. Every app has a registration on the Hub defining its display name, allowed redirect URIs, and permission ceiling. A private app is visible only to its registering user; a hub-wide app is visible to every user. The `leapmux` control CLI is a built-in app. See [App Authorization](/docs/admin/app-authorization/) and [Accounts & Connected Apps](/docs/using/accounts-and-apps/).

### App credential

One OAuth token pair held by a **connection**: a short-lived access token and a rotating refresh token. An app holds one credential per machine, labeled by its **installation name**, so revoking one laptop leaves other machines active. Revoke individual credentials under **Preferences → Apps → Connected apps**. See [Accounts & Connected Apps](/docs/using/accounts-and-apps/) and [Security & Authentication](/docs/admin/security-and-auth/).

### App registration

The Hub's record of an **app**: its display name, redirect URIs, permission ceiling, verification state, and whether it may request **step-up** elevation. The Hub evaluates the permission ceiling and elevation setting on every request, so tightening either applies immediately to existing credentials. Manage registrations under **Preferences → Apps**. See [App Authorization](/docs/admin/app-authorization/).

## C

### Channel (E2EE)

The end-to-end encrypted (E2EE) connection between your browser (or CLI) and a single Worker, multiplexed over one WebSocket and relayed — never decrypted — by the Hub. One channel is opened per Worker and shared across all agent transcripts, terminal streams, and file requests on that Worker. The client periodically rotates transport keys in-band without closing the channel, or opens a fresh handshake when a key epoch expires. See [Encryption & Data](/docs/admin/encryption-and-data/) and [Cryptography & Key Architecture](/docs/admin/cryptography/).

### Connection (app authorization)

One account's active authorization of an **app** across all machines where it runs. It starts at the **consent screen** and ends when you click **Disconnect** under **Preferences → Apps → Connected apps**, which revokes all **app credentials** under that connection at once. See [Accounts & Connected Apps](/docs/using/accounts-and-apps/).

### Consent screen

The Hub page displayed when an app requests access. It shows the app's name, warns when no administrator has verified it, and describes every requested permission in plain language (for example, *Type into your terminals, which runs any command on your machine*). Approving requires a recently proven factor. See [Accounts & Connected Apps](/docs/using/accounts-and-apps/).

### Control CLI

`leapmux` (and its short alias `lmx`): the command-line tool for driving a running Hub and its Workers from scripts or inside spawned tabs — managing workspaces, tabs, agents, terminals, layouts, files, git, and live events. External shells sign in with `leapmux auth login` or auto-discover a local `solo`, `dev`, or standalone `hub` socket; spawned tabs receive credentials automatically via `LEAPMUX_CONTROL_*` environment variables. See [CLI Guide](/docs/using/cli-guide/).

## D

### Delegation token

A short-lived credential a Worker mints for an agent or terminal running inside one of its tabs so that process can call `leapmux` without a login. It acts as the Worker owner, reaches only that owner's Workers, cannot elevate, and is revoked when the tab closes. See [Security & Authentication](/docs/admin/security-and-auth/).

### Distributed mode

Running the Hub and one or more Workers as separate processes — typically the Hub on a shared server and Workers on developer machines — instead of a single all-in-one `solo` process. Traffic between clients and Workers is end-to-end encrypted while the Hub relays ciphertext. Contrast with **solo mode**. See [Running LeapMux](/docs/admin/running-leapmux/) and [Security & Authentication](/docs/admin/security-and-auth/).

## E

### Effort (reasoning level)

A per-agent setting that controls how much reasoning the model applies. Available tiers depend on the model; for managed providers, the default is `auto` (letting the CLI decide). Change effort mid-session from the composer's Effort chip or **[+]** menu. See [Coding Agents](/docs/using/coding-agents/).

## F

### Floating window

A tab popped out of the main tiled layout into a movable, resizable overlay with adjustable opacity. A floating window contains its own tiling tree (supporting splits and grids with no depth limit) and can be popped back into the main layout with `⌘⇧O` / `Ctrl+Shift+O`. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### Frontend

The web interface — running in your browser or bundled inside LeapMux Desktop — that renders workspaces, tiling layouts, agent chats, terminals, and the file browser. Workers hold authoritative agent and terminal state while the browser caches rendered messages locally in IndexedDB. One of the three core components alongside the **Hub** and **Worker**. See [Concepts & Topologies](/docs/getting-started/concepts/).

## G

### Grid

A tile divided into a fixed `rows × cols` matrix of panes (up to 20 × 20) with draggable resize handles between rows and columns. Creating a grid moves existing tabs into the top-left cell; the grid close button lives on the top-right cell and closes the entire grid. Contrast with a **split** (two panes). See [Tabs & Layout](/docs/using/tabs-and-layout/).

## H

### Hub

The central service (`leapmux daemon hub start`) that authenticates users, stores accounts, workspaces, and tiling layouts, issues Worker registration keys, and relays end-to-end encrypted traffic between Frontends and Workers. Its default listen port is `4327`. See [Concepts & Topologies](/docs/getting-started/concepts/) and [Running LeapMux](/docs/admin/running-leapmux/).

## I

### Input queue

The Worker-owned queue that holds every input sent to an agent — messages, attachments, slash commands, plan executions, and permission answers. It survives page reloads, reconnects, and Worker restarts, and stays synchronized across all devices. You can reorder, edit, delete, schedule, pause, retry, and **steer** queued items. See [Coding Agents](/docs/using/coding-agents/).

### Installation name

The per-machine label attached to an **app credential** — for example, *Alice's MacBook*. Because an app holds one credential per machine, the installation name lets you distinguish machines in **Preferences → Apps → Connected apps**. See [Accounts & Connected Apps](/docs/using/accounts-and-apps/).

## P

### Permission (scope)

A specific capability an app may request, such as `file:read` or `terminal:write`. A grant only ever **narrows** what the owning account can already do — `admin:users` on a non-admin account grants no admin access. Both the Hub and the Worker enforce scopes. See [App Authorization](/docs/admin/app-authorization/).

### Permission mode

A per-agent setting that controls when the agent asks for approval before acting — such as Claude Code Auto Mode, Goose Smart Approve, or a provider's bypass mode. Change it from the composer's mode chip or **[+]** menu, which also offers **Smart permissions** and **Bypass permissions** shortcuts where supported. See [Coding Agents](/docs/using/coding-agents/).

### Post-quantum encryption

The default E2EE mode (`post-quantum`), which uses a hybrid handshake combining classical and post-quantum cryptography so channels remain protected even if one family is later broken. The alternative `classic` mode omits post-quantum forward secrecy while still verifying the Worker's registered identity. Configure the mode on a Worker with `--encryption-mode`. See [Cryptography & Key Architecture](/docs/admin/cryptography/).

### Presence

See **Active client (presence)**.

### PTY

A pseudo-terminal — the operating-system primitive backing each LeapMux terminal tab. Every PTY runs a real shell on the Worker; its I/O stays encrypted end-to-end and never reaches the Hub in plaintext. See [Terminals](/docs/using/terminals/).

## Q

### Quake-mode terminal

A drop-down shell overlay scoped to one working directory, toggled with ``Ctrl+` `` (or `Ctrl` plus the key under `Esc`) from any tab. One shell is shared per `(Worker, working directory)` across all tabs in that directory and across your devices, while panel visibility stays per-device. It closes when you exit the shell or close the last tab in that directory. See [Terminals](/docs/using/terminals/).

## R

### Registration key

A short-lived, single-use secret (5-minute lifetime) minted in the Hub's **Register worker** dialog and passed to `leapmux daemon worker register --registration-key`. Possessing a valid key authorizes the Worker immediately with no separate approval queue. See [Managing Workers](/docs/admin/managing-workers/).

## S

### Session (agent session)

The persistent conversation history and runtime state of a coding agent stored in the Worker's local database so it survives browser reloads, reconnects, and Worker restarts. Distinct from a user login session in [Accounts & Connected Apps](/docs/using/accounts-and-apps/). See [Coding Agents](/docs/using/coding-agents/).

### Service

An OS background service entry (launchd on macOS, systemd on Linux, or Task Scheduler / Service Control Manager on Windows) created by `leapmux daemon <mode> start` or `enable` to run LeapMux automatically at login or boot. See [Starting and Stopping](/docs/admin/starting-and-stopping/).

### Solo mode

The single-user mode (`leapmux up` or `leapmux daemon solo start`) that runs a Hub and a Worker in one process for one account (`solo`). Local IPC requires no credential, while TCP requires first-password setup and login thereafter. Contrast with **distributed mode**. See [Running LeapMux](/docs/admin/running-leapmux/) and [Security & Authentication](/docs/admin/security-and-auth/).

### Steering

Delivering the **input queue**'s first waiting item into an agent's currently running turn rather than waiting for the turn to end. Trigger steering with the queue row's **Steer** button or by pressing `⌘Enter` / `Ctrl+Enter` on an empty composer while the provider supports mid-turn steering. See [Coding Agents](/docs/using/coding-agents/).

### Step-up

A verification ceremony that proves a factor (password or passkey) and opens an elevation window of {{< duration elevation-window >}} on an existing session or app credential without issuing a new token:

- Browsers verify directly on the Hub.
- Remote-Hub Desktop sessions and browser-issued CLI credentials use a local loopback ceremony (`GET /oauth/authorize?step_up=...`).
- Device-code and admin-issued credentials call `POST /oauth/step-up` and approve on `/oauth/device`.

See [Security & Authentication](/docs/admin/security-and-auth/).

## T

### Tab

One piece of content inside a tile. LeapMux has four tab types:

- **Agent** — a coding-agent chat.
- **Terminal** — an interactive shell (PTY).
- **File** — a file viewer or git diff.
- **Image** — an image returned by a tool call, opened for zoom and pan.

Open Agent, Terminal, and File tabs from the tab bar; open Image tabs from chat tool results. Double-click non-file tabs to rename them, close tabs with **X** or middle-click, and drag tabs between tiles and workspaces. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### Tile (leaf / split / grid)

A rectangular pane in a workspace's recursive tiling layout. A **leaf** tile shows a tab bar and its active tab, and can turn into a **split** (two panes) or a **grid** (`rows × cols`), nested up to depth 3 in the main layout. The last remaining tile in a workspace cannot be closed. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### TOFU (trust on first use)

The key-pinning mechanism protecting Worker identity. On first connection to a Worker, the client stores the Worker's static public key and rejects any later handshake presenting a different key:

- **Browser:** opens the **"Worker public key changed"** dialog showing 4-word fingerprints so you can **Reject** or **Accept** after out-of-band verification.
- **CLI and cross-Worker:** abort automatically until you inspect and clear the stale pin.
- **Desktop tunnels:** verify through the account's browser pin and stop with both fingerprints if a key changes on reconnect.

See [Managing Workers](/docs/admin/managing-workers/) and [Security & Authentication](/docs/admin/security-and-auth/).

## W

### Worker

A long-running daemon (`leapmux daemon worker start`) on a developer machine that hosts coding agents, terminals, and file access. Workers connect outbound to a Hub (requiring no inbound ports), register with a single-use registration key, and keep agent and terminal state in their own local SQLite database. See [Managing Workers](/docs/admin/managing-workers/) and [Running LeapMux](/docs/admin/running-leapmux/).

### Workspace

A container with its own title and tiling layout of tabs, owned privately by its creator. Workspaces appear in the sidebar tree and keep their layout live-synced across devices through the Hub. See [Workspaces](/docs/using/workspaces/).

### Worktree

A separate working directory of a git repository checked out to its own branch so multiple agents can edit the same repository in parallel without overwriting each other's files. Closing the last tab tied to a worktree checks for uncommitted changes before offering cleanup. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
