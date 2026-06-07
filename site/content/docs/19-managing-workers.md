---
title: "Managing Workers"
type: docs
weight: 19
---

A **Worker** is the long-running daemon (`leapmux worker`) that hosts your coding agents, terminals, and file access. It runs on whatever machine holds the code you want to work on — your laptop, a beefy desktop, a remote build box — and connects *out* to a Hub. This chapter covers how a Worker reaches a Hub, how you register and approve one, how the UI shows Worker status, how Trust-On-First-Use (TOFU) key pinning protects you from a malicious Hub, and how you pick which Worker hosts a new tab.

If you only ever run `leapmux solo`, a Worker is started for you in-process and most of this chapter is informational — see [Running LeapMux](/docs/17-running-leapmux/) for the run modes and [Concepts & Architecture](/docs/02-concepts/) for how the Hub, Worker, and Frontend fit together.

## How a Worker reaches a Hub

The Worker always **dials the Hub** — it opens a single outbound bidirectional gRPC stream and keeps it alive. Nothing ever connects *to* the Worker, so:

- The Worker needs **no inbound port** and works fine behind NAT, a home router, or a corporate firewall.
- Only the Hub needs a reachable address (see [Running LeapMux](/docs/17-running-leapmux/) and [Configuration](/docs/18-configuration/)).

You tell the Worker where its Hub is with the `--hub` flag:

```bash
leapmux worker --hub https://hub.example.com --registration-key <key>
```

The default is `http://127.0.0.1:4327` (a Hub on the same machine). The `--hub` value accepts three URL forms:

| Form | Example | When to use |
| --- | --- | --- |
| `http://` / `https://` | `https://hub.example.com` | Remote or networked Hub (the common case). |
| `unix:<socket-path>` | `unix:/run/leapmux/hub.sock` | Hub on the same machine, over a Unix domain socket. **Unix/macOS only.** |
| `npipe:<pipe-name>` | `npipe:leapmux-hub` | Hub on the same machine, over a Windows named pipe. **Windows only.** |

The two local-IPC forms exist for co-located setups (notably the desktop app's local-only mode, where the Hub never opens a TCP port at all). On a remote Hub you will always use `http://` or `https://`.

> **Tip:** Every Worker flag has an environment-variable equivalent with the prefix `LEAPMUX_WORKER_` — for example `LEAPMUX_WORKER_HUB` and `LEAPMUX_WORKER_REGISTRATION_KEY`. This is handy for containers and `systemd` units. See [Configuration](/docs/18-configuration/) for the full precedence rules.

### Where the Worker keeps its state

On first run the Worker generates a persistent composite keypair (its long-term identity — see [TOFU key pinning](#tofu-key-pinning) below) and, after registering, stores its credentials. Both live under the Worker's data directory (`--data-dir`). `--data-dir` defaults to `.`, which is resolved relative to the config file's directory if a config file is present, or to `~/.config/leapmux/worker` when there is no config file. So out of the box the Worker keeps its state in `~/.config/leapmux/worker`:

- `state.json` — the Worker ID, auth token, and keypair (file mode `0600`).
- `worker.db` — the Worker's local SQLite database (agent and terminal state).

The auth token in `state.json` is what lets the Worker reconnect after a restart without re-registering. Keep this file private; it is the Worker's credential.

## Registration: the "approval" model

Before a Worker can connect, it must be **registered** with the Hub. LeapMux has no separate "pending approval" queue where an admin clicks *Approve* after a Worker dials in. Instead, **the approval *is* the registration key**: anyone who hands a Worker a valid registration key has already approved it. Presenting a valid key in the registration handshake atomically consumes the key and creates a live, active Worker in a single step.

This keeps the model simple: to authorize a new machine, you mint a key and run the Worker with it.

**Registration is one outbound step — there is no separate approve action:**

```text
   ┌──────────────────┐   1. mint key   ┌──────────────────┐
   │  Admin / User    │────────────────►│       Hub        │
   │  (Register       │                 │  (reachable      │
   │   worker dialog) │                 │   address)       │
   └──────────────────┘                 └──────────────────┘
          │                                    ▲
          │ 2. hand off                        │ 3. dial OUT with key
          │    --registration-key              │    (outbound,
          ▼                                    │     NAT-friendly)
   ┌──────────────────┐                        │
   │     Worker       │────────────────────────┘
   │  (behind NAT /   │
   │   firewall)      │   4. Hub validates + consumes the key atomically
   └──────────────────┘      → Worker is registered + active (online)
```

### Minting a registration key in the UI

This is the everyday path for users.

1. In the sidebar, find the **Workers** section header and click the **+** button (titled *Register worker*).
2. The **Register worker** dialog opens and immediately mints a key, showing a ready-to-run command:

   ```bash
   leapmux worker --hub https://hub.example.com --registration-key <key>
   ```

   The `--hub` value is filled in with the address your Hub advertises (falling back to the URL in your browser's address bar).
3. Copy the command with **Copy command** and run it on the machine where the Worker should live.

The dialog explains the key's lifetime in plain terms: *"This registration key is only valid while this dialog stays open. If you close the dialog, the key is destroyed and you'll need to start over."* Keys are short-lived (5 minutes), but the dialog auto-extends the key as long as it stays open, so you have time to switch machines and paste the command.

> **Note:** If your Hub has email configured and your account email is **verified**, the dialog also offers **Send email**, which mails the command to your verified address — useful when the target machine is elsewhere. The button is hidden in solo mode and on Hubs without SMTP, and disabled until your email is verified. See [Accounts & Authentication](/docs/05-accounts-and-authentication/) for email verification.

### Running the Worker with the key

On the target machine, run the generated command. What happens next:

- **First run, no key** → the Worker refuses to start: `worker is unregistered: pass --registration-key <key> from the hub UI`. A bare Worker cannot self-register; it always needs a key.
- **First run, valid key** → the Worker registers, saves its credentials to `state.json`, logs `credentials saved`, and connects. It is now active.
- **Already registered, but a key was passed anyway** → the Worker refuses: `worker is already registered; remove --registration-key or wipe local state to re-register`. This protects you from accidentally burning a fresh key on a machine that is already set up. After the first successful run, drop `--registration-key` from your command, env, or config — the saved token handles reconnection.

The `--registration-key` value is **never written to disk**; only the resulting auth token is persisted.

### Why a registration might be rejected

The Worker distinguishes *transient* from *permanent* failures:

- **Hub unreachable** (network error) → retried automatically with exponential backoff. Leave the Worker running and it will connect once the Hub is reachable.
- **Invalid, expired, or already-consumed key** → rejected immediately, no retry: `registration rejected: ...` with the underlying reason `registration key invalid or already consumed`. Mint a fresh key and try again.

Because keys are one-shot and expire in 5 minutes, a key that worked a moment ago will not work twice; reopen the **Register worker** dialog for a new one.

### Auto-registered Workers (solo and dev)

In **solo** and **dev** modes the launcher auto-registers a co-located Worker in-process, bypassing the key flow entirely (presenting a bearer token to a local same-process RPC would add nothing). These Workers are flagged *auto-registered* and **cannot be deregistered** — re-registration on the next launch would just undo it. See [Running LeapMux](/docs/17-running-leapmux/).

## Worker status: online and offline

A Worker's **online** status is computed live — it reflects whether the Hub currently holds an open stream to that Worker, not a stored flag. The moment a Worker's connection drops, it shows offline; the moment it reconnects, it shows online again. The Hub records `last_seen_at` on connect and on every heartbeat.

Under the hood the Worker checks its connection every 2 seconds and sends an idle heartbeat only after 5 seconds of silence (no other message sent in that window); the Hub treats a Worker that has gone silent for 10 seconds as disconnected. During normal traffic the regular messages keep the link alive, so no extra heartbeats are sent.

### In the sidebar

Each Worker appears as a row in the sidebar **Workers** section showing its name (or an em-dash `—` if the name hasn't been fetched yet) and a status dot. The dot reflects whether your browser currently has a live end-to-end-encrypted channel open to that Worker — *connected* or *disconnected*. When you have no Workers, the section reads **No workers**.

Right-click a Worker row (or open its context menu) to:

- View and copy its details — **Name**, **Version** (with commit hash), and **OS** (with architecture) are always shown when the Worker's info is available; a **Built at** row appears only when the Worker reports a build time. Clicking the info block copies the full metadata as JSON to your clipboard.
- Manage [tunnels](#tunnels-desktop-app) (when available and you own the Worker).
- **Deregister...** the Worker — shown only for Workers that are *not* auto-registered.

## Selecting which Worker hosts a new tab

When you open a new agent, terminal, or workspace, the dialog includes a **Worker** dropdown (with a **Refresh workers** button next to the label). This is where you choose the machine that will host the new tab.

Key behaviors:

- The dropdown lists **only online Workers**. If none are online, it shows a single, unselectable option: **No workers online**.
- Each option is labeled `name (version, os, arch)` — for example `build-box (1.4.0, linux, arm64)` — falling back to the raw Worker ID if the Worker's details haven't been fetched yet.
- When something is online and nothing is selected, the dialog preselects a sensible default (your last-used Worker if it's online, otherwise the first online Worker).
- Some dialogs are locked to a single Worker and show no dropdown at all — for example *Change branch* / *Delete branch*, which must act on the Worker that already hosts the worktree.

Worker details are cached for about a minute, so reopening a dialog is instant; click **Refresh workers** to force a re-list (for example, right after a new Worker comes online).

To open an agent or terminal on a chosen Worker, see [Coding Agents](/docs/09-coding-agents/) and [Terminals](/docs/11-terminals/). To script the same thing, `leapmux remote tab open --worker-id <id> ...` — see [Remote Control CLI](/docs/16-remote-control-cli/).

## Auto-reconnect

A registered Worker reconnects on its own. If the connection drops — Hub restart, network blip, laptop sleep — the Worker retries with exponential backoff:

| Property | Value |
| --- | --- |
| Initial retry interval | 1 second |
| Maximum interval | 180 seconds |
| Backoff multiplier | 2.0 |
| Jitter | ±20% |
| Backoff reset | After a connection survives ≥ 30 seconds |

So a Worker that briefly loses its link reconnects within seconds, while a Worker that can't reach a down Hub backs off to at most ~3 minutes between attempts. Once a connection holds for at least 30 seconds, the backoff resets, so the *next* outage starts fresh from 1 second again.

When the Hub shuts down gracefully it tells the Worker how long to wait before reconnecting; the Worker honors that requested delay once, then resumes normal backoff.

> **Note:** Auto-reconnect handles transient failures, **not** revocation. If the Hub rejects the Worker's token as unauthenticated on reconnect — which happens when the Worker has been deregistered or deleted — the Worker clears its local state and exits instead of retrying. Re-registering it requires a fresh registration key.

## Deregistering a Worker

Deregistering tells the Hub to forget a Worker and tear down everything running on it.

**From the UI:** choose **Deregister...** from a Worker's context menu. The **Deregister worker** dialog warns: *"This will terminate all active workspaces and terminals on this worker. This action cannot be undone."* Confirm to proceed. Auto-registered (bundled local) Workers cannot be deregistered and don't show the option; attempting it returns `the bundled local worker cannot be deregistered`.

**From the admin CLI** (operators):

```bash
leapmux admin worker deregister --id <worker-id>
```

On deregistration the Worker acknowledges the request, shuts down gracefully, clears its local credentials, and exits. See [Admin CLI](/docs/20-admin-cli/) for the full operator surface.

## Tunnels (desktop app)

A **tunnel** lets you reach a network service that is reachable from a Worker — for example a dev server, a database, or any TCP endpoint on the Worker's machine or its private network — from your local machine, all riding the Worker's existing end-to-end-encrypted channel. The Hub still only relays ciphertext; it cannot see the tunneled traffic any more than it can see your agent or terminal content.

Tunnels are a **desktop-app feature**. They are available in both solo and distributed desktop modes, but not in a plain browser (the browser cannot open the local listening sockets a tunnel needs). You manage them from a Worker's sidebar context menu, where — **when tunnels are available and you own the Worker** — two extra items appear:

- **Add tunnel...** — open a new tunnel to the selected Worker.
- **Delete all tunnels...** — tear down every tunnel to that Worker.

### The two tunnel types

| Type | What it does |
| --- | --- |
| **Port-forward** (`port_forward`) | Binds a local port and forwards every connection to a single address-and-port reachable *from the Worker*. Use this to reach one specific service — e.g. forward `127.0.0.1:8080` on your machine to a dev server the Worker can see. |
| **SOCKS5** (`socks5`) | Runs a SOCKS5 proxy locally; connections through it are dialed *from the Worker*, so they resolve and route as if they originated on the Worker's machine. Use this to reach many destinations through the Worker without one tunnel per service. |

### Fields and defaults

A tunnel's configuration has these fields:

| Field | Applies to | Required? | Default |
| --- | --- | --- | --- |
| `workerId` | both | required | — (the Worker you opened the menu on) |
| `targetAddr` | port-forward | required | — |
| `targetPort` | port-forward | required | — (must be 1–65535) |
| `bindAddr` | both | optional | `127.0.0.1` |
| `bindPort` | both | optional | `targetPort` (port-forward) or `1080` (SOCKS5); must be 1–65535 |

So a port-forward needs a `targetAddr` and `targetPort` (the service the Worker should reach) and, by default, binds the same port locally on loopback. A SOCKS5 tunnel needs no target — it binds `127.0.0.1:1080` by default and dials each destination from the Worker. In both cases you can override `bindAddr`/`bindPort` if the default port is taken or you want to listen on a different interface. The dialog reports the IP and port the tunnel actually bound to once it is running.

## TOFU key pinning

LeapMux end-to-end encrypts all Frontend↔Worker traffic and treats the Hub as an **authenticated relay, not a trusted peer** — it routes ciphertext but can never read it. The piece that stops a *malicious or compromised Hub* from quietly swapping a real Worker for an impostor is **TOFU (Trust-On-First-Use) static-key pinning**: each client records a Worker's composite public key on first contact and rejects any later connection whose key differs. [Security & Threat Model](/docs/23-security-and-threat-model/) covers *why* this defeats a compromised Hub, the composite keypair, and the fingerprint scheme in full; this section is the operational reference for the three pin stores and how to reset a pin in each.

**TOFU pinning in the browser — first contact pins the key, a later mismatch is rejected:**

```text
First connection (trust on first use):

   ┌────────────┐   sees key K1    ┌──────────────────┐
   │  Frontend  │◄────────────────►│      Worker      │
   │  (browser) │   via Hub relay  │  static key = K1 │
   └────────────┘                  └──────────────────┘
         │ pins K1
         ▼
   ┌───────────────────────────┐
   │ leapmux:key-pins = { K1 } │
   └───────────────────────────┘

Later connection (key changed):

   ┌────────────┐   sees key K2    ┌──────────────────┐
   │  Frontend  │   ✗  REJECTED    │      Worker      │
   │  (browser) │◄─ ─ ─ ─ ─ ─ ─ ─ ─│  static key = K2 │
   └────────────┘                  └──────────────────┘
         │ pinned K1 ≠ K2
         ▼
   ┌─────────────────────────────────────┐
   │ "Worker public key changed" dialog  │
   │ Expected: <K1 fp>   Actual: <K2 fp> │
   └─────────────────────────────────────┘
```

There are three independent pin stores, depending on *who* is connecting:

| Client | Pin store | How to reset a pin |
| --- | --- | --- |
| **Your browser** (Frontend) | `localStorage` (`leapmux:key-pins`, kept ~1 year) | Accept the in-app *Worker public key changed* dialog, or clear the key from browser storage. |
| **A Worker → a sibling Worker** (cross-worker channels) | `<data_dir>/cross_worker_pins.json` on the Worker | `leapmux worker cross-worker-pins remove --target-worker-id=<id>` |
| **The `leapmux remote` CLI** | `<config-dir>/<hub-host>/pins.json` | `leapmux remote worker pins remove --worker-id=<id>` |

### What a mismatch looks like in the browser

When a Worker's key has changed since you last connected, the Frontend stops and shows a dialog titled **Worker public key changed**. It displays the **Expected:** and **Actual:** fingerprints (short, human-comparable four-word strings) and asks you to **Reject** or **Accept** the changed key. Accept only if you *expected* the change — for example, you deliberately wiped a Worker's `state.json` and re-registered it, regenerating its keypair — and verify the new fingerprint out-of-band first. See [Security & Threat Model](/docs/23-security-and-threat-model/) for the full dialog text, the fingerprint scheme, and the accept-vs-reject reasoning.

> **Warning:** A key mismatch you did not cause is a serious signal. Do not click **Accept** to "make it work." When in doubt, **Reject** and confirm the Worker's fingerprint directly with whoever runs it.

### What a mismatch looks like on the CLI / a sibling Worker

For non-browser clients there is no dialog — agents and scripts can't answer interactive prompts — so a mismatch **aborts the connection** with an error of the form `worker <id> key mismatch — <hint>`. The hint names the exact command to clear the pin so the next connect re-pins the new key:

```bash
# A leapmux remote agent or script hit a pin mismatch:
leapmux remote worker pins remove --worker-id=<id>

# One Worker connecting to a sibling Worker hit a pin mismatch:
leapmux worker cross-worker-pins remove --target-worker-id=<id>
```

Both pin-management commands run entirely against local pin files — no Worker process needs to be running to manage them. For the full `leapmux remote worker pins list|show|remove` reference (and the required `--hub` flag), see [Remote Control CLI](/docs/16-remote-control-cli/).

> **Note:** There is no UI for browsing or pre-clearing browser pins; in the browser, a pin is only reset by accepting the mismatch dialog (or by clearing browser storage). For a deeper look at the handshake, fingerprints, and the full trust model, see [Security & Threat Model](/docs/23-security-and-threat-model/).

## Operator reference: admin worker commands

Operators manage all Workers on a Hub (not just their own) with `leapmux admin worker`. These act directly on the Hub's database and need no running Hub. Highlights:

| Command | Purpose |
| --- | --- |
| `leapmux admin worker list` | List Workers. Filters: `--user-id`, `--username`, `--status` (`active`, `deregistering`, `deleted`, `all`; default `active`). |
| `leapmux admin worker get --id <id>` | Show one Worker's details and access grants (includes soft-deleted Workers for auditing). |
| `leapmux admin worker deregister --id <id>` | Force-deregister a Worker. |
| `leapmux admin worker reg-key list` | List live registration keys (`--include-expired` to include revoked/expired). |
| `leapmux admin worker reg-key revoke --id <id>` | Revoke a registration key. |
| `leapmux admin worker reg-key purge-expired` | Hard-delete all expired or revoked keys. |

> **Note:** The admin CLI deliberately has **no** `reg-key create` — registration keys are minted only by an authenticated user (via the **Register worker** dialog), which is itself the authorization step. The admin CLI lists, revokes, and purges keys but does not issue them.

For the complete operator surface — including encryption keys, sessions, and tokens — see [Admin CLI](/docs/20-admin-cli/).

## Encryption mode

A Worker runs in one of two encryption modes, set with `--encryption-mode`:

- `post-quantum` (the default) — the hybrid post-quantum handshake, advertising all three static keys.
- `classic` — X25519-only.

Leave this at the default unless you have a specific reason to change it; both ends must agree, and the Hub tracks the mode the Worker declares. The cryptographic details are covered in [Security & Threat Model](/docs/23-security-and-threat-model/).

## See also

- [Running LeapMux](/docs/17-running-leapmux/) — run modes, ports, data directories, the bundled Worker.
- [Configuration](/docs/18-configuration/) — Worker flags, env vars, and config-file precedence.
- [Security & Threat Model](/docs/23-security-and-threat-model/) — the E2EE protocol, TOFU pinning, and what the Hub can and cannot see.
- [Remote Control CLI](/docs/16-remote-control-cli/) — `leapmux remote worker` and `worker pins` for scripting and pin management.
- [Admin CLI](/docs/20-admin-cli/) — the full `leapmux admin worker` and registration-key reference.
</content>
</invoke>
