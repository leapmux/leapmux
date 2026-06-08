---
title: "FAQ"
type: docs
weight: 3
---

Short answers to the questions people ask most often about LeapMux. Each one links to the chapter where the topic is covered in full.

## Do I need a server, or can I just run it locally?

You can run everything locally. The `leapmux solo` command starts a Hub and a Worker in a single process, bound to `127.0.0.1:4327` (loopback only), with no login required — it auto-authenticates every request as the admin. The desktop app does the same thing, listening only on a local socket so it opens no TCP port at all.

You only need a separate Hub when you want multiple users, remote Workers, or sign-in. In that case run `leapmux hub` (central relay, real authentication) and connect one or more `leapmux worker` processes to it.

See [Running LeapMux](/docs/operating/running-leapmux/) for the run modes and [Concepts & Architecture](/docs/getting-started/concepts/) for solo vs. distributed.

## Is solo mode multi-user?

No. Solo mode is single-user by design. Every request is auto-authenticated as the admin without credentials, so it offers no protection against another process that can reach the port. Workspace sharing is disabled in solo mode (the share dialog is hidden, and the backend rejects sharing with `workspace sharing is not available in solo mode`).

> **Warning:** If you ever bind solo mode to a non-loopback address, anyone who can reach the port has full admin access with no password. LeapMux logs a warning when this happens. For multi-user or networked use, run `leapmux hub` instead, or place solo behind a firewall, VPN (Tailscale/WireGuard), or SSH tunnel.

For real multi-user setups see [Organizations & Members](/docs/using/organizations/) and [Managing Workers](/docs/operating/managing-workers/).

## Where is my data stored?

Agent transcripts, terminal output, and file/git state live only in the **Worker's** local SQLite database — never on the Hub. The Hub stores accounts, organizations, workspace metadata (titles, tab positions, tiling geometry), and Worker public keys.

Default locations:

| Mode | Config + data directory |
|------|-------------------------|
| Solo | `~/.config/leapmux/solo/` (split into `hub/` and `worker/` subdirectories) |
| Dev | `~/.config/leapmux/dev/` |
| Hub | `~/.config/leapmux/hub/` (DB `hub.db`, key ring `encryption.key`) |
| Worker | `~/.config/leapmux/worker/` (DB `worker.db`, state `state.json`) |
| Docker | `/data/<mode>/` inside the `/data` volume |

See [Configuration](/docs/operating/configuration/) and [Encryption & Data](/docs/operating/encryption-and-data/).

## Can the Hub read my code, chats, or terminal output?

No — all Frontend-to-Worker traffic is end-to-end encrypted, and the Hub is an **authenticated relay, not a trusted peer**: it forwards opaque ciphertext and never holds the session keys.

The Hub **can** see connection metadata — channel IDs, ciphertext sizes, and timing (traffic analysis is in scope) — plus account, organization, and workspace records and Worker public keys. The Hub **cannot** see agent transcripts, tool-call arguments or outputs, terminal I/O, file contents, diffs, or git status. Even the Worker's hostname and filesystem paths travel inside the encrypted application stream, so they are not exposed to the relay. See [Security & Threat Model](/docs/operating/security/) for the authoritative scope of what the Hub does and does not see.

> **Note:** In solo mode the Hub and Worker run in the same process, so the E2EE protocol is still in effect but provides no protection against a local attacker who can reach the loopback port. The threat model there reduces to local-host trust.

## Which coding agents are supported?

LeapMux supports nine agent providers: **Claude Code**, **Codex**, **Gemini CLI**, **Cursor**, **GitHub Copilot**, **Kilo**, **OpenCode**, **Goose**, and **Pi**. All nine are first-class, and LeapMux gives each the same core surface where the underlying CLI supports it: chat, tool calls, permission prompts, plan tracking, and session resume.

A provider only appears in the picker when its CLI binary is detected on the Worker (LeapMux probes the shell for the binary — `command -v` on POSIX shells, `Get-Command` on PowerShell, `which` on nushell and csh). So if `claude` or `codex` isn't installed on the machine running the Worker, that provider won't show up.

For what each provider can do, see [Coding Agents](/docs/using/coding-agents/).

## Can Workers run behind a NAT or firewall?

Yes. The **Worker always initiates the connection to the Hub**, so it never needs an inbound port — it works behind NAT or a firewall with only outbound access. Set the Worker's `--hub` URL to your Hub (over `https://` for a TLS-fronted Hub) and it dials out and stays connected, auto-reconnecting on disconnection.

Local Workers can instead use a Unix domain socket (`unix:<path>`) or Windows named pipe (`npipe:<name>`).

See [Managing Workers](/docs/operating/managing-workers/) and [Configuration](/docs/operating/configuration/).

## Can I use PostgreSQL or MySQL instead of SQLite?

Yes — for the **Hub**. The Hub supports six storage backends, selected with `storage.type`: `sqlite` (default), `postgres`, `mysql`, `cockroachdb`, `yugabytedb`, and `tidb`. The Postgres- and MySQL-compatible backends reuse their respective drivers — see [Configuration](/docs/operating/configuration/) for which driver each one uses. Each external backend needs a `dsn`:

```yaml
storage:
  type: postgres
  postgres:
    dsn: "postgres://user:password@db.example.com:5432/leapmux?sslmode=disable"
```

Migrations run automatically when the store opens. **Workers always use SQLite locally** — that's not configurable. Note that storage settings are nested keys, so set them in the YAML config file (or via CLI flags), not via simple environment variables.

See [Configuration](/docs/operating/configuration/) and [Encryption & Data](/docs/operating/encryption-and-data/).

## How do multiple agents avoid clobbering each other?

Through **git worktrees**. When you open an agent (or terminal), you can have LeapMux create a dedicated worktree and branch for it, so each agent works in its own checkout. When each agent gets its own worktree, they never touch the same working tree or branch — one agent refactoring, another on tests, and a third chasing a build failure stay fully isolated.

The sidebar groups tabs by repository and branch so you always know which agent owns which branch, and LeapMux protects you against losing uncommitted work in a dirty worktree.

See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

## Do my sessions survive a restart or reboot?

Agent sessions do. Agent state persists in the Worker's local SQLite database, so when the Worker process or the machine comes back and reconnects to the Hub, your agent sessions are still there — no need to relaunch each agent by hand. You can also resume a prior agent session by entering its session ID in the **New agent** dialog (it resumes the underlying agent session — Claude Code's `--resume` flag, or the equivalent resume method for other providers).

Terminals are a partial exception: a shell process cannot outlive a Worker restart. LeapMux persists each terminal's last screen, so after the Worker comes back the terminal tab reappears exactly where it left off — but its shell has exited, and you press **Enter** to restart it in the same working directory. (A transient disconnect, where the Worker process itself never went down, keeps the live shell attached.)

See [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## What's the difference between the browser and the desktop app?

They are the same SolidJS Frontend. The difference is packaging:

- **Browser** — open `http://<host>:4327` against a running Hub, dev, or solo instance.
- **Desktop app** — a native Tauri app with the Frontend in an embedded WebView. It can run solo mode entirely on your machine (listening only on a local socket, no TCP port) or connect to a remote Hub.

The same end-to-end encryption applies either way. Pick the desktop app for a self-contained local setup; use the browser when connecting to a shared Hub.

See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/operating/running-leapmux/).

## How do I update LeapMux?

| Distribution | How to update |
|--------------|---------------|
| Desktop app | Download and install the newer artifact from the [Releases page](https://github.com/leapmux/leapmux/releases) |
| CLI binary | Replace the `leapmux` binary from the newer server tarball/zip |
| Docker | Pull a newer tag (`:latest`, a pinned `:<version>`, or `:<major>`) and recreate the container against the same `/data` volume |

Database migrations run automatically on startup, so no manual migration command is required.

See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/operating/running-leapmux/).

## Is it free? What's the license?

LeapMux is source-available under the **Functional Source License, Version 1.1, with an Apache 2.0 future grant** (FSL-1.1-ALv2), Copyright Event Loop, Inc. In short, you may use, modify, and redistribute it for any **Permitted Purpose** — including your own internal use, non-commercial education, and non-commercial research — but not for a **Competing Use** (making it available to others in a commercial product or service that substitutes for, or offers substantially similar functionality to, LeapMux). Each version converts to Apache 2.0 on a future date — see [Legal](/docs/reference/legal/) for the conversion date and the full terms.

> **Note:** This FAQ summarizes the license for convenience and is not legal advice. The `LICENSE.md` file in the repository is the authoritative text.

## More questions?

If your problem isn't answered here, see [Troubleshooting](/docs/reference/troubleshooting/) for problem-to-fix entries, or the [Glossary](/docs/reference/glossary/) for term definitions.

Still have a question, or found a bug? [Open a GitHub issue](https://github.com/leapmux/leapmux/issues) — the maintainers welcome questions and bug reports (for feature requests, a plan generated by a frontier model is appreciated).
