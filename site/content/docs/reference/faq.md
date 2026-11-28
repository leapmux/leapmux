---
title: "FAQ"
description: "Short answers to the questions asked most often about LeapMux, each linking to the chapter that covers the topic in full."
type: docs
weight: 6
---

Short answers to the questions people ask most often about LeapMux. Each answer links to the chapter that covers the topic in full.

## Do I need a server, or can I just run it locally?

You can run everything locally. Solo mode (`leapmux up` or `leapmux daemon solo start`) runs a Hub and a Worker on `127.0.0.1:4327`. A TCP browser sets the first `solo` password before opening the app, while the Desktop app connects over a local socket and needs no credential. Add another address under **Preferences → Administration → Network access** to reach the same Hub from another machine.

You only need a separate Hub when you want multiple users, remote Workers, or full sign-in. In that case, run a Hub (`leapmux daemon hub start`) and connect one or more standalone Workers (`leapmux daemon worker start`) to it.

See [Running LeapMux](/docs/admin/running-leapmux/) for run modes and [Concepts & Topologies](/docs/getting-started/concepts/) for solo vs. distributed deployments.

## Is solo mode multi-user?

No. Solo mode is single-user by design, with one account (`solo`). Local inter-process communication (IPC) uses that account without a credential, while TCP callers complete first-password setup before any TCP session can sign in.

{{< callout type="warning" >}}
If you bind solo mode to a non-loopback address before setting a password, another machine can claim the account. LeapMux logs a warning. Set the password first and restrict access with a firewall, VPN, or SSH tunnel. Use hub mode (`leapmux daemon hub start`) for multi-user deployments.
{{< /callout >}}

For multi-user setups, see [Accounts & Connected Apps](/docs/using/accounts-and-apps/) and [Managing Workers](/docs/admin/managing-workers/).

## Where is my data stored?

The Worker stores agent transcripts, terminal output, and file/git state in its local SQLite database. The Hub stores accounts, workspace layout metadata (titles, tab positions, tiling geometry, and hosting Workers), and each Worker's pinned public keys. The browser also caches rendered message text and code locally in IndexedDB; see [Encryption & Data](/docs/admin/encryption-and-data/) for retention and sign-out behavior.

Default locations:

| Mode | Config and data directory |
|---|---|
| Solo | `~/.config/leapmux/solo/` (split into `hub/` and `worker/` subdirectories) |
| Dev | `~/.config/leapmux/dev/` (split into `hub/` and `worker/` subdirectories) |
| Hub | `~/.config/leapmux/hub/` (`hub.db`, `encryption.key`) |
| Worker | `~/.config/leapmux/worker/` (`worker.db`, `state.json`) |
| Docker | `/data/<mode>/` inside the `/data` volume |

See [Configuration Reference](/docs/admin/configuration-reference/) and [Encryption & Data](/docs/admin/encryption-and-data/).

## Can the Hub read my code, chats, or terminal output?

An observing Hub cannot read that content: Frontend-to-Worker traffic is end-to-end encrypted (E2EE). The Hub is trusted for identity, and a web browser also trusts it for serving Frontend code (whereas the Desktop app runs its own bundled Frontend, so a remote Hub cannot serve it modified code).

The Hub can see connection metadata (channel IDs, ciphertext sizes, timing), account and workspace metadata, and Worker public keys. Worker hostnames and filesystem paths travel inside the encrypted stream and are not exposed to the relay.

| The Hub can see | The Hub cannot see |
|---|---|
| Connection metadata (channel IDs, ciphertext sizes, timing), account and workspace records, Worker public keys | Agent transcripts, tool-call arguments and outputs, terminal I/O, file contents, diffs |

In solo mode, the Hub and Worker run in one process; once `solo` has a password, loopback TCP callers must sign in, while the Desktop app's local socket admits only your operating-system account. See [Security & Authentication](/docs/admin/security-and-auth/) for the full threat model.

## Which coding agents are supported?

LeapMux supports {{< agent-provider-count >}} coding-agent providers. Every provider uses the same interface where the underlying CLI supports it: chat, streamed tool calls, permission prompts, plan tracking, and session resume. See [Supported Agents](/docs/reference/supported-agents/) for the E2E-verified capability matrix.

{{< agent-logos >}}

The selected Worker's shell determines which providers appear in the picker by checking `PATH` and shell profiles (ZCode can also be detected from its desktop installation). For day-to-day agent workflows, see [Coding Agents](/docs/using/coding-agents/).

## Can Workers run behind a NAT or firewall?

Yes. A **Worker always initiates the connection outbound to the Hub**, so it never requires an inbound port. Register the Worker once against your Hub URL (using `https://` for a TLS-fronted Hub), then start it:

```bash
leapmux daemon worker register --hub https://hub.example.com --registration-key <key>
leapmux daemon worker start
```

The Worker saves the Hub URL during registration and reconnects automatically after network interruptions. Local Workers can register against a Unix domain socket (`unix:<path>`) or Windows named pipe (`npipe:<name>`). See [Managing Workers](/docs/admin/managing-workers/).

## Can I use PostgreSQL or MySQL instead of SQLite?

Yes, for the **Hub**. The Hub supports six storage backends via `storage.type`: `sqlite` (default), `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, and `tidb`. Each external backend requires a `dsn`:

```yaml
storage:
  type: postgres
  postgres:
    dsn: "postgres://user:password@db.example.com:5432/leapmux?sslmode=disable"
```

Migrations run automatically at startup. **Workers always use SQLite locally.** Because storage settings are nested keys, set them in YAML or via `--storage-*` flags rather than environment variables. See [Configuration Reference](/docs/admin/configuration-reference/).

## How do multiple agents avoid clobbering each other?

Through **git worktrees**. When you open an agent or terminal, LeapMux can create a dedicated worktree and branch so parallel agents never touch the same working tree. The sidebar groups tabs by repository and branch, and closing the last tab in a dirty worktree prompts for confirmation. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

## Do my sessions survive a restart or reboot?

Agent sessions do. Agent transcripts and queues live in the Worker's local SQLite database, so when the Worker reconnects, your agent tabs resume automatically using each provider's native session resume. You can also reopen prior CLI sessions from **Resume an existing session** in the **New agent** dialog.

Terminals preserve their last screen and scrollback across a Worker restart, though the underlying shell process exits; press **Enter** to start a fresh shell in the same working directory. Transient network disconnects where the Worker stays running keep live shells attached. See [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## How do I keep LeapMux running after I log out or restart?

Start it with `leapmux daemon <mode> start` (or `leapmux up` for `solo`). The `start` command installs an OS service and prompts whether to run at login (best for laptops) or at boot (best for servers running while nobody is logged in). In Docker, use your container restart policy. See [Starting and Stopping](/docs/admin/starting-and-stopping/).

## What's the difference between the browser and the desktop app?

Both run the same LeapMux interface and use the same end-to-end encryption. The difference is packaging and code trust:

- **Browser** — open `http://<host>:4327` (or your `https://` reverse-proxy URL) against a running Hub. The Hub serves the web interface, so you trust the Hub for that code.
- **Desktop app** — a native installer that runs its own bundled interface locally. It can run solo mode on your machine over a local socket (with no open TCP port) or connect to a remote Hub without letting that Hub serve it code.

Choose the Desktop app for a self-contained local setup or stronger client integrity against a remote Hub; use the browser for zero-install access to a shared Hub. See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/admin/running-leapmux/).

## How do I update LeapMux?

| Distribution | How to update |
|---|---|
| Desktop app | Download and install the newer release from the [Releases page](https://github.com/leapmux/leapmux/releases) |
| CLI binary | Replace the `leapmux` binary from the newer release archive |
| Docker | Pull the newer image tag (`:latest`, `:<version>`, or `:<major>`) and recreate the container against the same `/data` volume |

Database migrations run automatically on startup.

## Is it free? What's the license?

LeapMux is source-available under the **Functional Source License, Version 1.1, with an Apache 2.0 future grant** (`FSL-1.1-ALv2`), Copyright Event Loop, Inc.

You may use, modify, and redistribute it for any **Permitted Purpose** — including internal company use, non-commercial education, and non-commercial research — but not for a **Competing Use** (offering LeapMux to others in a commercial product or service that competes with LeapMux). Each release converts automatically to Apache 2.0 two years after publication. See [Legal](/docs/reference/legal/) for details.

{{< callout type="info" >}}
This FAQ summarizes the license for convenience and is not legal advice. `LICENSE.md` in the repository is the authoritative text.
{{< /callout >}}

## More questions?

Check [Troubleshooting](/docs/reference/troubleshooting/) for symptom-to-fix guides, or the [Glossary](/docs/reference/glossary/) for term definitions. To ask a question or report a bug, [open a GitHub issue](https://github.com/leapmux/leapmux/issues).
