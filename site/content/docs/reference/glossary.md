---
title: "Glossary"
description: "Short, alphabetical definitions of the terms used throughout the LeapMux manual, each linking to the chapter that covers it in depth."
type: docs
weight: 4
---

Short definitions of the terms you will meet throughout the LeapMux manual. Each entry links to the chapter that covers the term in depth. Terms are listed alphabetically.

## A

### Active client (presence)

The single client connection that "owns" the turn-end notification sound for a workspace at any moment. The slot is not a fixed assignment: it follows wherever you last typed, going to the client that most recently received your input (and stays empty if two clients tie or none has reported input yet). When an agent finishes a turn, only the client that currently holds the slot plays the ding — so opening the same workspace on a laptop and a phone does not double-ding. The active client is broadcast over the per-org events stream and is not the same thing as the layout sync. See [Device Sync & Presence](/docs/using/collaboration/).

### Agent

A coding agent: a CLI assistant (Claude Code, Codex, Cursor, GitHub Copilot, OpenCode, Pi, Kilo, Goose, Reasonix) that LeapMux launches and hosts on a Worker, one process per agent tab. You chat with it, watch its tool calls render inline, set its model and effort, and resume it across restarts. See [Coding Agents](/docs/using/coding-agents/).

## C

### Channel (E2EE)

The end-to-end-encrypted connection between your browser (or CLI) and a single Worker, multiplexed over one WebSocket and relayed — but never decrypted — by the Hub. One channel is opened per Worker and reused: all of that Worker's agent transcripts, terminal streams, and file requests share the same channel, kept separate by correlation IDs. The channel is periodically re-handshaked to refresh keys. See [Encryption & Data](/docs/operating/encryption-and-data/) and [Security & Threat Model](/docs/operating/security/).

## D

### Distributed mode

Running LeapMux with the Hub and one or more Workers as separate processes — typically the Hub on a shared server and Workers on your dev machines — instead of the single all-in-one `solo` process. In this mode the Hub is treated as an authenticated relay that cannot read content, which is what makes it safe for a teammate or platform team to operate. Contrast with **solo mode**. See [Running LeapMux](/docs/operating/running-leapmux/) and [Security & Threat Model](/docs/operating/security/).

## E

### Effort (reasoning level)

A per-agent setting that controls how much reasoning the agent applies. The available tiers are model-dependent — each model advertises its own supported set — and the default is `auto`, which lets the CLI pick. Common tiers are `low`, `medium`, and `high`, with some models adding `minimal`, `xhigh`, or `max` (Claude models, for example, expose tiers up to `max` and an `ultracode` tier; Codex models offer `auto`, `minimal`, `low`, `medium`, `high`, `xhigh`, and `none`). You can set effort when you open an agent and change it later from the agent's settings panel. See [Coding Agents](/docs/using/coding-agents/).

## F

### Floating window

A tab popped out of the tiled layout into a movable, resizable, opacity-adjustable overlay that floats on top of the main layout. A floating window is itself a small tiling tree you can split and grid (with no depth limit), and you can pop the tab back into the main layout. The default keyboard shortcut to pop out/in is `Cmd/Ctrl + Shift + O`. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### Frontend

The SolidJS web app — running in your browser or embedded in the native desktop app — that renders the tiling UI: the layout, agent chats, terminals, and the file browser. It is where you click, type, and read, and it holds no agent state of its own; everything you care about lives on the **Worker**. One of the three core components alongside the **Hub** and the **Worker**. See [Concepts & Architecture](/docs/getting-started/concepts/).

## G

### Grid

A tile turned into a fixed `rows × cols` matrix of panes (up to 20 × 20), with draggable resize handles between rows and columns. Making a grid moves the tile's existing tabs into the top-left cell. The grid's close button lives on its top-right anchor cell and closes the whole grid. Contrast with a **split**, which divides a tile into just two panes. See [Tabs & Layout](/docs/using/tabs-and-layout/).

## H

### Hub

The central service (`leapmux hub`) that authenticates users, stores accounts, organizations, workspaces, and layout geometry, mints Worker registration keys, and relays encrypted traffic between Frontends and Workers. The Hub is an **authenticated relay, not a trusted peer**: it routes opaque ciphertext and sees metadata (who talks to whom, message sizes, timing) but never the plaintext of agent transcripts, terminal I/O, or file contents. Its default listen address is `:4327`. See [Concepts & Architecture](/docs/getting-started/concepts/) and [Running LeapMux](/docs/operating/running-leapmux/).

## L

### LexoRank

The string-based ordering scheme LeapMux uses to position tabs (and other ordered items) without renumbering everything when you reorder or insert. Each tab carries a LexoRank `position` string; a new tab gets a rank computed from its neighbours, so drag-to-reorder and "insert before/after" need only a single update. It is an implementation detail you rarely see directly. See [Tabs & Layout](/docs/using/tabs-and-layout/).

## N

### Noise_NK

The Noise-protocol handshake pattern behind the E2EE channel: the Worker has a known static key the Frontend verifies. LeapMux extends it into a hybrid post-quantum handshake (see **Post-quantum encryption**). See [Security & Threat Model](/docs/operating/security/).

## O

### Organization

The top-level container ("org") that owns a user's workspaces, identified by a globally-unique GitHub-style slug. Every account has exactly one: a **personal** org created automatically with the account, named after the username, and renamed with it. The whole app lives under the URL prefix `/o/{username}`. See [Concepts & Architecture](/docs/getting-started/concepts/).

## P

### Post-quantum encryption

The default E2EE mode (`post-quantum`), a hybrid handshake combining classical and post-quantum cryptography so the channel stays secure even if one algorithm is later broken: **X25519 + ML-KEM-1024 (FIPS 203)** for key exchange, **SLH-DSA-SHAKE-256f (FIPS 205)** for Worker static-key authentication, and ChaCha20-Poly1305 + BLAKE2b for transport. The alternative `classic` mode is X25519-only. Set it on the Worker with `--encryption-mode`. See [Encryption & Data](/docs/operating/encryption-and-data/) and [Security & Threat Model](/docs/operating/security/).

### Presence

See **Active client (presence)**.

### PTY

A pseudo-terminal — the operating-system primitive behind a terminal tab. Each LeapMux terminal is a PTY running a real shell on the Worker, and its I/O and shell state live only on the Worker (the Hub never sees them). A PTY session stays attached across reconnects; across a Worker restart the shell cannot survive, but its last screen is preserved so the terminal can be restarted where it left off. See [Terminals](/docs/using/terminals/).

## R

### Registration key

A short-lived, single-use secret (5-minute TTL) that authorizes a Worker to join a Hub. You mint one from the Hub's "Register worker" dialog (or via the admin CLI), then run the Worker with `--registration-key`. Presenting a valid key immediately creates an active Worker — there is no separate approval queue; the gate is possessing the key. See [Managing Workers](/docs/operating/managing-workers/).

### Remote CLI

`leapmux remote`: a JSON-emitting command-line surface for driving a running Hub from a script or from inside an agent — creating workspaces and tabs, sending agent and terminal input, mutating the tile layout, inspecting files and git, and watching events. External users authorize it with `leapmux remote auth login`; agents are handed credentials automatically through `LEAPMUX_REMOTE_*` environment variables. See [Remote Control CLI](/docs/operating/remote-control-cli/).

## S

### Session (agent session)

The persistent state of a running coding agent — its conversation history and process — kept in the Worker's local database so the agent survives browser reloads, Worker reconnects, and restarts. Reopening the agent's tab reattaches to the same session. (Distinct from a user **login session** in [Accounts & Authentication](/docs/using/accounts/).) See [Coding Agents](/docs/using/coding-agents/).

### Solo mode

The single-user, all-in-one mode (`leapmux solo`) that runs a Hub and a Worker in one process on `127.0.0.1:4327` with **no authentication** — every request is auto-authenticated as the admin. It is the easiest way to run LeapMux locally, but its security reduces to local-host trust: any local process that can reach the port has full access. Binding it to a non-loopback address triggers a warning. Contrast with **distributed mode**. See [Running LeapMux](/docs/operating/running-leapmux/) and [Security & Threat Model](/docs/operating/security/).

## T

### Tab

A single piece of content inside a tile. There are three kinds: **Agent** (a coding-agent chat), **Terminal** (a shell/PTY), and **File** (a file viewer or diff). You open tabs from the tab bar, rename non-file tabs by double-clicking, close them with the X or a middle-click, and drag them between tiles and workspaces. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### Tile (leaf / split / grid)

A rectangular pane in the workspace's recursive tiling layout. A **leaf** tile is a single pane that shows a tab bar plus its active tab's content. A leaf can become a **split** (two side-by-side or stacked panes) or a **grid** (a `rows × cols` matrix); both nest, with draggable resize handles between panes and a depth cap of 3 in the main layout. The workspace's last remaining tile cannot be closed. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### TOFU (trust on first use)

The pinning scheme that protects Worker identity. On the first connection to a Worker the Frontend records the Worker's composite static public key, and rejects any later handshake whose key differs — so a compromised Hub cannot silently swap a Worker underneath you. A mismatch raises the **"Worker public key changed"** dialog showing 4-word fingerprints, where you must explicitly Accept or Reject. CLI and cross-Worker connections pin the same way. See [Managing Workers](/docs/operating/managing-workers/) and [Security & Threat Model](/docs/operating/security/).

## W

### Worker

A long-running daemon (`leapmux worker`) that runs on a developer machine and hosts agents, terminals, and file access for a workspace. A Worker dials the Hub outbound (so it works behind NAT with no inbound ports), registers with a registration key, and keeps agent and terminal state in its own local database. Its online/offline status reflects whether it currently holds a live connection to the Hub. See [Managing Workers](/docs/operating/managing-workers/) and [Running LeapMux](/docs/operating/running-leapmux/).

### Workspace

A named container for one tiling layout of tabs, owned by its creator and living in that user's personal organization. Workspaces appear in the sidebar tree and persist their layout (CRDT-synced through the Hub); you see exactly the workspaces you own. Each workspace's tabs run agents and terminals on a Worker you pick. See [Workspaces](/docs/using/workspaces/).

### Worktree

A separate working directory of a git repository, on its own branch, that an agent or terminal runs in — so several agents can work the same repo on different branches without clobbering each other's files. You can open a tab in a new or existing worktree and create, switch, or push branches at open time; closing the last tab tied to a worktree raises a "Close last tab" dialog with dirty-worktree protection. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
