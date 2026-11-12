---
title: "FAQ"
description: "Short answers to the questions asked most often about LeapMux, each linking to the chapter that covers the topic in full."
type: docs
weight: 3
---

Short answers to the questions people ask most often about LeapMux. Each one links to the chapter where the topic is covered in full.

## Do I need a server, or can I just run it locally?

You can run everything locally. Solo mode (`leapmux solo`) runs a Hub and a Worker on `127.0.0.1:4327`. A TCP browser sets the first `solo` password before it opens the app. The desktop app listens only on a local socket and needs no credential. Add another address under **Preferences → Administration → Network access** to reach the same Hub from another machine.

You only need a separate Hub when you want multiple users, remote Workers, or sign-in. In that case run a Hub (`leapmux hub`: central relay, real authentication) and connect one or more Workers (`leapmux worker`) to it.

See [Running LeapMux](/docs/admin/running-leapmux/) for the run modes and [Concepts & Architecture](/docs/getting-started/concepts/) for solo vs. distributed.

## Is solo mode multi-user?

No. Solo mode is single-user by design. It has one account, `solo`. Local IPC uses that account without a credential. A passwordless TCP caller can only set the first password, but the first successful caller claims the account.

{{< callout type="warning" >}}
If you bind solo mode to a non-loopback address before setup, another machine can claim the account. LeapMux logs a warning. Set the password first and use a firewall, VPN, or SSH tunnel. Use hub mode (`leapmux hub`) for multi-user service.
{{< /callout >}}

For real multi-user setups see [Accounts & Authentication](/docs/using/accounts/) and [Managing Workers](/docs/admin/managing-workers/).

## Where is my data stored?

The Worker stores agent transcripts, terminal output, and file/git state in its local SQLite database. The Hub stores accounts and workspace metadata (titles, tab positions, tiling geometry). It also stores each tab's type and hosting Worker, plus a file tab's display mode, view mode, and diff base. The public keys each Worker registered with are stored on the Hub too. They are public material, and the Hub pins them so a later connection cannot present other keys.

The browser caches rendered message text and code for seven days after last use in IndexedDB. That cache is not keyed to the account, and signing out does not clear it.

Default locations:

| Mode | Config + data directory |
|------|-------------------------|
| Solo | `~/.config/leapmux/solo/` (split into `hub/` and `worker/` subdirectories) |
| Dev | `~/.config/leapmux/dev/` |
| Hub | `~/.config/leapmux/hub/` (DB `hub.db`, key ring `encryption.key`) |
| Worker | `~/.config/leapmux/worker/` (DB `worker.db`, state `state.json`) |
| Docker | `/data/<mode>/` inside the `/data` volume |

See [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).

## Can the Hub read my code, chats, or terminal output?

A Hub that only observes cannot read that content: Frontend-to-Worker traffic is end-to-end encrypted. The Hub is trusted for identity and code. A Hub that acts can open its own channel as you or serve modified Frontend code.

The Hub **can** see connection metadata — channel IDs, ciphertext sizes, and timing (traffic analysis is in scope) — plus account and workspace records and Worker public keys. Even the Worker's hostname and filesystem paths travel inside the encrypted application stream, so they are not exposed to the relay.

| The Hub can see | The Hub cannot see |
|---|---|
| Connection metadata (channel IDs, ciphertext sizes, timing), account and workspace records, Worker public keys | Agent transcripts, tool-call arguments and outputs, terminal I/O, file contents, diffs |

A file tab's diff base tells the Hub that the file had staged or unstaged changes.

See [Security & Threat Model](/docs/admin/security/) for the authoritative scope of what the Hub does and does not see.

{{< callout type="info" >}}
In solo mode the Hub and Worker run in the same process. Once `solo` has a password, a loopback TCP caller must sign in. Before that, the first successful caller can claim the account by setting its password. The desktop app's local socket admits only your operating-system account.
{{< /callout >}}

## Which coding agents are supported?

LeapMux supports {{< agent-provider-count >}} agent providers. All of them are first-class, and LeapMux gives each the same core surface where the underlying CLI supports it: chat, tool calls, permission prompts, plan tracking, and session resume. The [feature matrix](/docs/using/coding-agents/#feature-matrix) states each provider's exact support and limits.

{{< agent-logos >}}

A provider only appears in the picker when LeapMux detects its CLI binary on the Worker. If `claude` or `codex` is not installed on the machine that runs the Worker, that provider does not appear. ZCode is the exception to the binary rule: it ships no command, so LeapMux looks for its desktop installation instead.

For what each provider can do, see [Coding Agents](/docs/using/coding-agents/).

## Can Workers run behind a NAT or firewall?

Yes. The **Worker always initiates the connection to the Hub**, so it never needs an inbound port — it works behind NAT or a firewall with only outbound access. Set the Worker's `--hub` URL to your Hub (over `https://` for a TLS-fronted Hub) and it dials out and stays connected, auto-reconnecting on disconnection.

Local Workers can instead use a Unix domain socket (`unix:<path>`) or Windows named pipe (`npipe:<name>`).

See [Managing Workers](/docs/admin/managing-workers/) and [Configuration](/docs/admin/configuration/).

## Can I use PostgreSQL or MySQL instead of SQLite?

Yes — for the **Hub**. The Hub supports six storage backends, selected with `storage.type`: `sqlite` (default), `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, and `tidb`. The Postgres- and MySQL-compatible backends reuse their respective drivers — see [Configuration](/docs/admin/configuration/) for which driver each one uses. Each external backend needs a `dsn`:

```yaml
storage:
  type: postgres
  postgres:
    dsn: "postgres://user:password@db.example.com:5432/leapmux?sslmode=disable"
```

Migrations run automatically when the store opens. **Workers always use SQLite locally** — that's not configurable. Note that storage settings are nested keys, so set them in the YAML config file (or via CLI flags), not via simple environment variables.

See [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).

## How do multiple agents avoid clobbering each other?

Through **git worktrees**. When you open an agent or a terminal, you can have LeapMux create a dedicated worktree and branch for it. Agents in separate worktrees never touch the same working tree or branch. One agent can refactor, a second can write tests, and a third can fix a build failure, all fully isolated.

The sidebar groups tabs by repository and branch, so you know which agent owns which branch. When you close the last tab of a worktree that has uncommitted changes, LeapMux asks you to confirm.

See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

## Do my sessions survive a restart or reboot?

Agent sessions do. Agent state persists in the Worker's local SQLite database, so when the Worker process or the machine comes back and reconnects to the Hub, your agent sessions are still there — no need to relaunch each agent. You can also resume a prior agent session by picking it from the **Resume an existing session** menu in the **New agent** dialog, which lists what LeapMux and the agent CLI itself both recorded for that directory. LeapMux resumes the provider's own session — Claude Code's `--resume` flag, or the equivalent for other providers.

Terminals are a partial exception: a shell process cannot outlive a Worker restart. LeapMux persists each terminal's last screen, so after the Worker comes back the terminal tab reappears exactly where it left off — but its shell has exited, and you press **Enter** to restart it in the same working directory. (A transient disconnect, where the Worker process itself never went down, keeps the live shell attached.)

See [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## How do I keep LeapMux running after I log out or restart?

Start it with `leapmux <mode> start`, as in `leapmux solo start`. This is the recommended way to run LeapMux outside a container: `start` runs it as a service of your operating system. The first `start` asks whether LeapMux should start when you log in or when the computer starts. Choose login startup for your own laptop. Choose boot startup for a server that must run while nobody is logged in. In Docker, the container's restart policy does this instead.

See [Starting and Stopping](/docs/admin/starting-and-stopping/).

## What's the difference between the browser and the desktop app?

They are the same SolidJS Frontend. The difference is packaging:

- **Browser** — open `http://<host>:4327` against a running Hub, dev, or solo instance.
- **Desktop app** — a native Tauri app with the Frontend in an embedded WebView. It can run solo mode entirely on your machine (listening only on a local socket, no TCP port) or connect to a remote Hub.

The same end-to-end encryption applies either way. In remote-Hub mode, the desktop app loads its Frontend from the Hub. As in a browser, you trust the Hub for that code. Pick the desktop app for a self-contained local setup; use the browser when connecting to a shared Hub.

See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/admin/running-leapmux/).

## How do I update LeapMux?

| Distribution | How to update |
|--------------|---------------|
| Desktop app | Download and install the newer artifact from the [Releases page](https://github.com/leapmux/leapmux/releases) |
| CLI binary | Replace the `leapmux` binary from the newer server tarball/zip |
| Docker | Pull a newer tag (`:latest`, a pinned `:<version>`, or `:<major>`) and recreate the container against the same `/data` volume |

Database migrations run automatically on startup, so no manual migration command is required.

See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/admin/running-leapmux/).

## Is it free? What's the license?

LeapMux is source-available under the **Functional Source License, Version 1.1, with an Apache 2.0 future grant** (FSL-1.1-ALv2), Copyright Event Loop, Inc.

You may use, modify, and redistribute it for any **Permitted Purpose** — including your own internal use, non-commercial education, and non-commercial research — but not for a **Competing Use**. A Competing Use makes LeapMux available to others in a commercial product or service that substitutes for, or offers substantially similar functionality to, LeapMux. Each version converts to Apache 2.0 on a future date — see [Legal](/docs/reference/legal/) for the conversion date and the full terms.

{{< callout type="info" >}}
This FAQ summarizes the license for convenience and is not legal advice. The `LICENSE.md` file in the repository is the authoritative text.
{{< /callout >}}

## More questions?

If your problem isn't answered here, see [Troubleshooting](/docs/reference/troubleshooting/) for problem-to-fix entries, or the [Glossary](/docs/reference/glossary/) for term definitions.

Still have a question, or found a bug? [Open a GitHub issue](https://github.com/leapmux/leapmux/issues) — the maintainers welcome questions and bug reports (for feature requests, a plan generated by a frontier model is appreciated).
