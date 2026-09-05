---
title: "Glossary"
description: "Short, alphabetical definitions of the terms used throughout the LeapMux manual, each linking to the chapter that covers it in depth."
type: docs
weight: 4
---

Short definitions of the terms you will meet throughout the LeapMux manual. Each entry links to the chapter that covers the term in depth. Terms are listed alphabetically.

## A

### Active client (presence)

The client connection that owns the turn-end notification sound for a workspace. The role follows your most recent input and is broadcast on the per-user events stream. When an agent finishes a turn, only that client plays the sound; if no client is active, every client plays it. See [Device Sync](/docs/using/device-sync/).

### Agent

A coding agent: a CLI assistant (Claude Code, Codex, Cursor, GitHub Copilot, OpenCode, Pi, Kilo, Goose, Reasonix, ZCode) that LeapMux launches and hosts on a Worker, one process per agent tab. You chat with it, watch its tool calls render inline, set its model and effort, and resume it across restarts. See [Coding Agents](/docs/using/coding-agents/).

### App

A program that asks a LeapMux Hub for access to an account on it. Every app is a registration the Hub holds — a name, the addresses an authorization may return to, and a ceiling on the permissions it may ask for. A private app is visible to its registering user alone; a hub-wide app is visible to everybody. The control CLI is an app, registered like any other. See [App Authorization](/docs/admin/app-authorization/) and [Connected Apps](/docs/using/connected-apps/).

### App credential

One token pair a **connection** holds: an access token that renews itself and a refresh token behind it. An app holds one per machine it runs on, labelled by its **installation name**, so signing one laptop out leaves the rest working. A credential is **minted** when the app is authorized and **revoked** from its own row in **Preferences → Apps → Connected apps**. See [Connected Apps](/docs/using/connected-apps/#ending-an-apps-access) and [App credentials](/docs/admin/security/#app-credentials).

### App registration

The Hub's record of one **app**: its name, its redirect addresses, the permission ceiling it may ask for, whether an administrator vouched for it, and whether it may run the **step-up** ceremony. The ceiling and the step-up flag are read at every request rather than only at the consent, so removing either takes it from the credentials the app already holds. It is **registered** and **deleted** from **Preferences → Apps**, and every write to it needs a recently proven factor. See [App Authorization](/docs/admin/app-authorization/).

## C

### Channel (E2EE)

The end-to-end-encrypted connection between your browser (or CLI) and a single Worker, multiplexed over one WebSocket and relayed — but never decrypted — by the Hub. One channel is opened per Worker and reused: all of that Worker's agent transcripts, terminal streams, and file requests share the same channel, kept separate by correlation IDs. The channel is periodically re-handshaked to refresh keys. See [Encryption & Data](/docs/admin/encryption-and-data/) and [Security & Threat Model](/docs/admin/security/).

### Connection (app authorization)

One account's live authorization of one **app**, across every machine that app runs on. It begins at the **consent screen** and ends with **Disconnect** in **Preferences → Apps → Connected apps**, which retires every **app credential** the connection holds at once. Not to be confused with **active client (presence)**, which is a connected client, not an authorization. See [Connected Apps](/docs/using/connected-apps/).

### Consent screen

The page a Hub renders when an app asks for access. It states the app's name, warns when no administrator vouched for it, and lists every permission as a sentence a person can act on -- *Type into your terminals, which runs any command on your machine*, never `terminal:write`. Approving needs a recently proven factor. See [Connected Apps](/docs/using/connected-apps/#authorizing-an-app).

## D

### Delegation token

A short-lived credential a Worker mints for the agent or terminal running in one of its tabs, so it can call `leapmux control` with no login. It carries the Worker owner's identity, reaches only that owner's Workers, holds no elevation, and is revoked when the tab closes. See [What a delegation token can reach](/docs/admin/security/#what-a-delegation-token-can-reach).

### Distributed mode

Running LeapMux with the Hub and one or more Workers as separate processes — the Hub on a shared server and Workers on your dev machines — instead of the single all-in-one `solo` process. In this mode the Hub is treated as an authenticated relay that cannot read content, which is what makes it safe for a teammate or platform team to operate. Contrast with **solo mode**. See [Running LeapMux](/docs/admin/running-leapmux/) and [Security & Threat Model](/docs/admin/security/).

## E

### Effort (reasoning level)

A per-agent setting that controls how much reasoning the agent applies. The available tiers are model-dependent — each model advertises its own supported set — and for the providers LeapMux manages the default is `auto`, which lets the CLI pick. You change effort after opening the agent, from the composer's Effort chip or its **[+]** menu. See [Coding Agents](/docs/using/coding-agents/).

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

The central service (`leapmux hub`) that authenticates users, stores accounts, workspaces, and layout geometry, mints Worker registration keys, and relays encrypted traffic between Frontends and Workers. The Hub is an **authenticated relay, not a trusted peer**: it routes opaque ciphertext and sees metadata (who talks to whom, message sizes, timing) but never the plaintext of agent transcripts, terminal I/O, or file contents. Its default listen address is `:4327`. See [Concepts & Architecture](/docs/getting-started/concepts/) and [Running LeapMux](/docs/admin/running-leapmux/).

## I

### Installation name

The per-machine label of one **app credential** — for example *trustin's MacBook*. An app holds one credential per machine it runs on; the installation name tells two rows of the same app apart. See [Connected Apps](/docs/using/connected-apps/).

## L

### LexoRank

The string-based ordering scheme LeapMux uses to position tabs (and other ordered items) without renumbering everything when you reorder or insert. Each tab carries a LexoRank `position` string; a new tab gets a rank computed from its neighbours, so drag-to-reorder and "insert before/after" need only a single update. It is an implementation detail you rarely see directly. See [Tabs & Layout](/docs/using/tabs-and-layout/).

## N

### Noise_NK

The Noise-protocol handshake pattern behind the E2EE channel: the Worker has a known static key the Frontend verifies. LeapMux extends it into a hybrid post-quantum handshake (see **Post-quantum encryption**). See [Security & Threat Model](/docs/admin/security/).

## P

### Permission (scope)

One named thing an app may do, such as `file:read` or `terminal:write`. A grant is a set of them, and it only ever **subtracts** from what its owner can already do — an app granted `admin:users` on an ordinary account administers nothing. The Hub enforces a grant at its own boundary and the Worker enforces it again inside the encrypted channel. See [App Authorization](/docs/admin/app-authorization/#permissions).

### Permission mode

A per-agent setting that controls when the agent asks before it acts. Examples include Claude Code Auto Mode, Goose Smart Approve, and each provider's bypass mode. Change it from the composer's mode chip or its **[+]** menu. That menu also offers the Smart permissions and Bypass permissions shortcuts, which select these values for you when the session supports them. A provider can carry a separate permission option group beside this axis, such as GitHub Copilot Assisted Approval. See [Coding Agents](/docs/using/coding-agents/).

### Post-quantum encryption

The default E2EE mode (`post-quantum`): a hybrid handshake that combines classical and post-quantum cryptography, so the channel stays secure if one algorithm is later broken. The alternative `classic` mode is X25519-only. Set it on the Worker with `--encryption-mode`. See [Encryption & Data](/docs/admin/encryption-and-data/) and [Security & Threat Model](/docs/admin/security/).

### Presence

See **Active client (presence)**.

### PTY

A pseudo-terminal — the operating-system primitive behind a terminal tab. Each LeapMux terminal is a PTY running a real shell on the Worker, and its I/O and shell state live only on the Worker (the Hub never sees them). A PTY session stays attached across reconnects; across a Worker restart the shell cannot survive, but its last screen is preserved so the terminal can be restarted where it left off. See [Terminals](/docs/using/terminals/).

## R

### Registration key

A short-lived, single-use secret (5-minute TTL) that authorizes a Worker to join a Hub. You mint one from the Hub's "Register worker" dialog, then run the Worker with `--registration-key`. Presenting a valid key immediately creates an active Worker — there is no separate approval queue; the gate is possessing the key. See [Managing Workers](/docs/admin/managing-workers/).

### Control CLI

`leapmux control`: a JSON-emitting command-line surface for driving a running Hub from a script or from inside an agent — creating workspaces and tabs, sending agent and terminal input, mutating the tile layout, inspecting files and git, and watching events. External users authorize it with `leapmux control auth login`; agents are handed credentials automatically through `LEAPMUX_CONTROL_*` environment variables. See [Control CLI](/docs/using/control-cli/).

## S

### Session (agent session)

The persistent state of a running coding agent — its conversation history and process — kept in the Worker's local database so the agent survives browser reloads, Worker reconnects, and restarts. Reopening the agent's tab reattaches to the same session. (Distinct from a user **login session** in [Accounts & Authentication](/docs/using/accounts/).) See [Coding Agents](/docs/using/coding-agents/).

### Solo mode

The single-user mode (`leapmux solo`) that runs a Hub and a Worker in one process. Its account, `solo`, starts without a password. Local IPC uses the account without a credential. TCP exposes only first-password setup until one caller claims the account. After setup, every TCP address requires sign-in. Binding a passwordless solo hub to a non-loopback address triggers a warning. Contrast with **distributed mode**. See [Running LeapMux](/docs/admin/running-leapmux/) and [Security & Threat Model](/docs/admin/security/).

### Step-up

The ceremony that proves a factor for a credential rather than for a browser session. An app that needs one is refused with a marker, opens `/oauth/step-up`, and the account holder approves in a browser; the credential then holds a window for {{< duration elevation-window >}}. It issues nothing new. An app is refused this ceremony unless its owner allowed it per app. See [Session elevation](/docs/admin/security/#session-elevation).

## T

### Tab

A single piece of content inside a tile. There are three kinds: **Agent** (a coding-agent chat), **Terminal** (a shell/PTY), and **File** (a file viewer or diff). You open tabs from the tab bar, rename non-file tabs by double-clicking, close them with the X or a middle-click, and drag them between tiles and workspaces. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### Tile (leaf / split / grid)

A rectangular pane in the workspace's recursive tiling layout. A **leaf** tile is a single pane that shows a tab bar plus its active tab's content. A leaf can become a **split** (two side-by-side or stacked panes) or a **grid** (a `rows × cols` matrix); both nest, with draggable resize handles between panes and a depth cap of 3 in the main layout. The workspace's last remaining tile cannot be closed. See [Tabs & Layout](/docs/using/tabs-and-layout/).

### TOFU (trust on first use)

The pinning scheme that protects Worker identity. On the first connection to a Worker the Frontend records the Worker's composite static public key, and rejects any later handshake whose key differs — so a compromised Hub cannot silently swap a Worker underneath you. A mismatch raises the **"Worker public key changed"** dialog showing 4-word fingerprints, where you must explicitly Accept or Reject. CLI and cross-Worker connections pin the same way. See [Managing Workers](/docs/admin/managing-workers/) and [Security & Threat Model](/docs/admin/security/).

## W

### Worker

A long-running daemon (`leapmux worker`) that runs on a developer machine and hosts agents, terminals, and file access for a workspace. A Worker dials the Hub outbound (so it works behind NAT with no inbound ports), registers with a registration key, and keeps agent and terminal state in its own local database. Its online/offline status reflects whether it currently holds a live connection to the Hub. See [Managing Workers](/docs/admin/managing-workers/) and [Running LeapMux](/docs/admin/running-leapmux/).

### Workspace

A named container for one tiling layout of tabs, owned by its creator. Workspaces appear in the sidebar tree and persist their layout (CRDT-synced through the Hub); you see exactly the workspaces you own, with no sharing. See [Workspaces](/docs/using/workspaces/).

### Worktree

A separate working directory of a git repository, on its own branch, that an agent or terminal runs in — so several agents can work the same repo on different branches without clobbering each other's files. You can open a tab in a new or existing worktree and create, switch, or push branches at open time; closing the last tab tied to a worktree raises a "Close last tab" dialog with dirty-worktree protection. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
