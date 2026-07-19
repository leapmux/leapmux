---
title: "Concepts & Architecture"
description: "How LeapMux is built: the Hub, Worker, and Frontend, the solo and distributed deployment shapes, and the org, workspace, tile, tab, and worktree model."
type: docs
weight: 2
---

This chapter is the map of the territory. It explains the three pieces LeapMux is built from, the two shapes you can deploy it in, and the objects you will work with every day — organizations, workspaces, layouts, tiles, tabs, worktrees, Workers, and the encrypted channels that connect them. Read it once and the rest of the manual will make sense. Every concept here links to the chapter that covers it in depth, so use this as a hub: skim it, then jump.

## The three components

LeapMux is a single Go binary, `leapmux`, that can play three roles. In a running system you always have all three present — what changes between deployment shapes is whether they live in one process or several.

- **Frontend** — the SolidJS web app that renders the workspace UI: the tiling layout, agent chats, terminals, and the file browser. It runs in your browser or inside the native desktop app. The Frontend is where you click, type, and read; it holds no agent state of its own.
- **Hub** — a Go service that handles login, organization and workspace management, and Worker registration, and **relays** encrypted Frontend↔Worker traffic. It owns the central database (pluggable: SQLite by default, or PostgreSQL, MySQL, CockroachDB, YugabyteDB, TiDB). The Hub is a coordinator and a relay, not a place where your code or conversations live.
- **Worker** — a Go process that actually runs your coding agents, PTY/terminal sessions, file browsing, and git operations. Each Worker keeps its own local SQLite database and auto-reconnects to the Hub if the connection drops. **Your agent transcripts, terminal output, and file contents live on the Worker, never on the Hub.**

The division is deliberate: the Hub knows *who is talking to whom* but never *what they say*. That property is what makes it safe to let a teammate or platform team operate the Hub while your agents run on your own machine.

> **Note:** The native desktop app is a packaged Frontend plus an embedded LeapMux binary. It can run a local Hub+Worker (solo) or connect its WebView to a remote Hub. See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/operating/running-leapmux/).

## The two deployment shapes

LeapMux runs in two shapes. They use the same components and the same end-to-end encryption; they differ in whether the pieces are co-located and whether you log in.

### Solo mode

`leapmux solo` is the simplest setup — single-user, sensible defaults, and it runs out of the box. The Hub and Worker run **in the same process**, bound to loopback only at `127.0.0.1:4327`. There is **no login** — the UI opens straight into your workspace because every request is auto-authenticated as the admin.

```text
                 LeapMux (127.0.0.1:4327)
┌───────────────────────────────────────────────────────┐
│                                                       │
│  ┌─────────────┐  in-process   ┌──────────────────┐   │
│  │     Hub     │◄─────────────►│     Worker       │   │
│  │  (no auth)  │               │  ┌────────────┐  │   │
│  │  + SQLite   │               │  │   Agents   │  │   │
│  │             │               │  │ (multiple) │  │   │
│  └─────────────┘               │  └────────────┘  │   │
│         ▲                      │  + SQLite        │   │
│         │ ConnectRPC + WS      └──────────────────┘   │
└─────────┼─────────────────────────────────────────────┘
          ▼
   ┌───────────────┐
   │   Frontend    │
   │  (Browser /   │
   │  Desktop App) │
   └───────────────┘
```

Solo mode is ideal for one person on one machine. Because it auto-authenticates and binds loopback, the security model **reduces to local trust**: any local process that can reach the port can drive the Worker. The end-to-end encryption between Frontend and Worker still operates inside the process, but it offers no protection against an attacker who is already on your machine.

> **Warning:** If you point solo mode at a non-loopback address, it logs a warning that anyone who can reach the port gets full admin access without credentials, and recommends restricting access (firewall, Tailscale/WireGuard, SSH tunnel) or running `leapmux hub` for real authentication. The desktop app avoids the issue entirely by listening on a Unix socket / named pipe instead of TCP.

### Distributed mode

For multi-user and remote setups, run `leapmux hub` and `leapmux worker` as separate processes — often on different machines. The Hub requires real login (sign-up, passwords, sessions, OAuth, API tokens) and relays end-to-end encrypted traffic between Frontends and Workers. **Workers initiate outbound connections to the Hub**, so they can sit behind a NAT or firewall with no inbound ports open.

```text
┌────────────────┐              ┌─────────────────┐            ┌──────────────────┐
│                │  ConnectRPC  │                 │    gRPC    │  Worker 1        │
│   Frontend     │◄────────────►│       Hub       │◄──────────►│  ┌────────────┐  │
│  (Browser /    │  WebSocket   │     (Relay)     │            │  │   Agents   │  │
│  Desktop App)  │              │                 │            │  │ (multiple) │  │
│                │              │    Go Service   │            │  └────────────┘  │
└────────────────┘              │  + Database     │            │  + SQLite        │
                                │   (SQLite,      │            └──────────────────┘
                                │    PostgreSQL,  │                      ⋮
                                │    MySQL, ...)  │            ┌──────────────────┐
                                │                 │            │  Worker N        │
                                └─────────────────┘            │  + Agents/SQLite │
                                                               └──────────────────┘
```

One Hub coordinates one-or-more Workers. Each Worker is a separate machine (a dev box, a build server, your laptop) running `leapmux worker`. You pick which Worker hosts each tab, so you can keep an agent's filesystem and git operations local to the machine that holds the repo.

There is also `leapmux dev` — the same Hub+Worker-in-one-process runner as solo, but it binds all interfaces (`:4327`), requires real password auth, and bootstraps the first admin through the `/setup` flow. It is meant for development, not production.

| Aspect | Solo (`leapmux solo`) | Distributed (`leapmux hub` + `leapmux worker`) |
|---|---|---|
| Processes | One (Hub + Worker together) | Two or more (one Hub, one-or-more Workers) |
| Default listen | `127.0.0.1:4327` (loopback) | Hub `:4327` (all interfaces); Workers dial out |
| Login | None — auto-admin | Real auth (password, OAuth, sessions) |
| Workers | One local, in-process | One-or-more, local or remote, NAT-friendly |
| Multiple user accounts | No | Yes |
| Trust model | Local-host trust | Hub is an authenticated relay |

The default TCP port everywhere is **4327**. For run modes, ports, data directories, Docker, and reverse-proxy setup, see [Running LeapMux](/docs/operating/running-leapmux/) and [Configuration](/docs/operating/configuration/). For the full command surface, see the [CLI Reference](/docs/reference/cli-reference/).

## The object hierarchy

Everything you see in the UI fits into one nested structure. From the outside in:

```text
Organization
└── Workspace
    ├── Layout tree (tiles: leaf / split / grid)
    │   └── Tabs (agent / terminal / file)
    └── Floating windows (their own tile trees)
        └── Tabs (agent / terminal / file)

Each tab is hosted on a Worker, optionally bound to a worktree/branch.
```

### Organization

An **organization** (org) is the top-level tenant. Every account has exactly **one**: a personal org created automatically with the account and deleted with it, whose slug is your username. It scopes the URLs — the whole app lives under the prefix `/o/{username}` — and the event stream your workspaces sync over. An org is not a team: it has no members or roles beyond you, and there is nothing to create, join, or switch between. Renaming your username renames the org (and the URL prefix) with it. See [Accounts & Authentication](/docs/using/accounts/).

### Workspace

A **workspace** is the unit of work and the top-level container you actually spend time in. It owns a tiling layout of tabs and lives at `/o/{username}/workspace/{id}`. The left sidebar groups your workspaces into sections — "In progress", any custom sections you create, and "Archived". Each workspace has a single **owner** (its creator), and workspace access is strictly owner-only: you see exactly the workspaces you own. See [Workspaces](/docs/using/workspaces/).

### Layout, tiles, and floating windows

Inside a workspace, the center panel is a recursive **tiling layout** — a tree of tiles you arrange to fit your work.

- A **tile (leaf)** is a single rectangular pane. It shows a tab bar plus the content of its active tab.
- A **split** divides a tile into two side-by-side panes (a vertical divider) or two stacked panes (a horizontal divider), with a draggable handle between them.
- A **grid** turns a tile into a rows×cols arrangement of cells.
- A **floating window** lifts a tab out of the layout into a movable, resizable, opacity-adjustable overlay. A floating window has its own internal tile tree and can itself be split or gridded.

You can split, grid, resize, pop tabs out and back in, and drag tabs between tiles or even between workspaces. See [Tabs & Layout](/docs/using/tabs-and-layout/) for the full set of controls, the responsive behavior, and the close dialogs.

### Tab

A **tab** is one piece of content, and there are exactly three kinds:

- **Agent** — a coding-agent chat session (Claude Code, Codex, and other supported agents).
- **Terminal** — a PTY/shell session.
- **File** — a file viewer / diff.

Every tab lives in a tile, carries an ordering position, and — this is the key architectural fact — **is hosted on a specific Worker** and may be **bound to a git worktree and branch**. The Worker is where the agent process or shell actually runs; the worktree/branch determines which checkout it operates on. See [Coding Agents](/docs/using/coding-agents/), [Terminals](/docs/using/terminals/), and [File Browser](/docs/using/file-browser/) for each tab type, and [Worktrees & Branches](/docs/using/worktrees-and-branches/) for the git binding.

## Workers: where work actually runs

A **Worker** is a machine (or, in solo mode, an in-process component) that runs your agents, terminals, file browsing, and git operations. When you open a tab you pick which Worker hosts it, so an agent's filesystem access and git commands happen on the machine that holds the repo — your laptop, a remote dev box, a build server.

Key properties:

- Workers keep their own **local SQLite database**. Agent and terminal state lives there, not in the Hub, so your work survives reconnects and restarts (see [Persistence](#persistence) below).
- Workers **dial out** to the Hub and re-establish the connection automatically after a drop, so they work behind NATs and firewalls without inbound ports.
- In distributed mode a Worker must be **registered** before it can connect: an authenticated user mints a registration key in the Hub UI and passes it to `leapmux worker --registration-key <key>` on first run. Once registered, the Worker saves its credentials and reconnects on its own.

For registering, approving, pinning, and selecting Workers, see [Managing Workers](/docs/operating/managing-workers/).

> **Note:** A tab can only be used while its Worker is online. If the hosting Worker is offline, opening a channel to it fails with "worker is offline" until it reconnects.

## End-to-end encrypted channels

All Frontend↔Worker traffic is **end-to-end encrypted**. The Hub relays opaque ciphertext over a single WebSocket; it never sees the plaintext.

Conceptually:

- When the Frontend needs to talk to a Worker, it opens an **encrypted channel** to that Worker, multiplexed through the Hub. The Hub routes the ciphertext but cannot decrypt it.
- The encryption is a hybrid post-quantum handshake (classical + post-quantum key exchange, with authenticated encryption). A **classic** (non-PQ) mode is also available; the default is **post-quantum**. The specific algorithms are listed in [Security & Threat Model](/docs/operating/security/).
- After the handshake the Frontend proves its identity to the Worker (so a channel is bound to the authenticated user). The Worker refuses any request before this verification.

So the Hub sees *who talks to whom* plus the control-plane metadata it needs to route — but never the *content*: agent transcripts, tool calls, terminal I/O, and file contents all travel inside the encrypted channel. This is what "the Hub is an **authenticated relay, not a trusted peer**" means in practice, and it is load-bearing in distributed mode, where the Hub may be operated by someone other than you. For the authoritative breakdown of exactly what the Hub can and cannot see, see [Security & Threat Model](/docs/operating/security/).

### Worker identity is pinned (TOFU)

Each Worker has a persistent static keypair. The Frontend pins that key **trust-on-first-use (TOFU)**: it records the Worker's key on first connection and rejects any later handshake whose key doesn't match. If the key changes, you get a **"Worker public key changed"** dialog showing 4-word fingerprints for the expected and actual keys, and you must explicitly **Accept** or **Reject**. A compromised Hub therefore cannot silently swap a Worker underneath you. (Closing the dialog counts as Reject — it fails closed.)

For the full protocol, threat model, and trust boundaries, see [Security & Threat Model](/docs/operating/security/). For operator-side encryption at rest (a separate keystore protecting Hub-stored secrets like OAuth tokens), see [Encryption & Data](/docs/operating/encryption-and-data/).

## Persistence

LeapMux is built to survive restarts:

- **Agent sessions** live in the Worker's local database. A Worker reboot or a dropped connection does not destroy them — they re-attach and resume when the Worker reconnects to the Hub. See [Coding Agents](/docs/using/coding-agents/).
- **Terminals** ride out a dropped connection the same way, re-attaching when the Worker reconnects. A Worker reboot, though, ends the live shell: LeapMux preserves the terminal's last screen, so the tab returns showing where it left off and restarts on demand. See [Terminals](/docs/using/terminals/).
- **Workspaces, layouts, tabs, and floating windows** are stored centrally (their structure, not their content) so the arrangement you left is the arrangement you return to, across reloads and across devices.

You don't have to re-launch agents with `--resume` or rebuild your tiling by hand after a crash; LeapMux reattaches and redraws.

## Presence and device sync

A workspace's **layout** — the tiling tree, tabs, splits, grids, floating windows, and lifecycle (created / renamed / deleted) — stays synchronized across every client that has it open: your own browser tabs, windows, and devices. Changes appear within roughly a frame to a network round-trip, with no setup step.

All of those clients are yours, and any of them can edit the layout — open, move, rename, or close things — with the rest following along live. You only ever see the workspaces you own.

The one presence signal LeapMux exposes is a per-workspace "active client", used solely so that only the client you are actually looking at plays the agent turn-end notification sound. There are **no avatars, "who is viewing" badges, remote cursors, or typing indicators.**

See [Device Sync & Presence](/docs/using/collaboration/) for exactly what does and doesn't sync, and [Settings & Preferences](/docs/using/settings/) for the turn-end sound preference.

## Putting it together

A typical mental walkthrough:

1. You sign in to a **Hub** (or just launch **solo** and skip login). You land in your **personal organization**.
2. You open or create a **workspace**.
3. Inside it you arrange a **layout** of **tiles** — splits, grids, maybe a **floating window**.
4. In each tile you open **tabs**: an agent here, a terminal there, a file viewer alongside. Each tab is hosted on a **Worker** you choose, and an agent or terminal tab can be bound to a git **worktree/branch**.
5. The Frontend talks to each Worker over an **end-to-end encrypted channel** relayed by the Hub — which routes the bytes but can't read them.
6. Everything keeps running on the Workers when you close the tab or reload the page, and your layout follows you across your devices.

From here, jump to whichever piece you need: get LeapMux running in [Installation](/docs/getting-started/installation/) and [Quick Start](/docs/getting-started/quick-start/), or dig into any concept via its dedicated chapter linked above. New terms are collected in the [Glossary](/docs/reference/glossary/).
