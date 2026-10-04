---
title: "FAQ"
description: "Short answers to the questions asked most often about LeapMux, each linking to the chapter that covers the topic in full."
type: docs
weight: 3
---

Short answers to the questions people ask most often about LeapMux. Each one links to the chapter where the topic is covered in full.

## Do I need a server, or can I just run it locally?

You can run everything locally. The `leapmux solo` command starts a Hub and a Worker on `127.0.0.1:4327`. A TCP browser sets the first `solo` password before it opens the app. The desktop app listens only on a local socket and needs no credential. Add another address under **Preferences → Administration → Network access** to reach the same Hub from another machine.

Use a separate Hub for any of these requirements:

- Multiple users.
- Remote Workers.
- Sign-in.

Run `leapmux hub` for the central relay and authentication. Connect one or more `leapmux worker` processes to it.

See [Running LeapMux](/docs/admin/running-leapmux/) for the run modes and [Concepts & Architecture](/docs/getting-started/concepts/) for solo vs. distributed.

## Is solo mode multi-user?

No. Solo mode is single-user by design. It has one account, `solo`. Local IPC uses that account without a credential. A passwordless TCP caller can only set the first password, but the first successful caller claims the account.

{{< callout type="warning" >}}
If you bind solo mode to a non-loopback address before setup, another machine can claim the account. LeapMux logs a warning. Set the password first and use a firewall, VPN, or SSH tunnel. Use `leapmux hub` for multi-user service.
{{< /callout >}}

For real multi-user setups see [Accounts & Authentication](/docs/using/accounts/) and [Managing Workers](/docs/admin/managing-workers/).

## Where is my data stored?

The **Worker** stores work data in its local SQLite database:

- Agent transcripts.
- Terminal output.
- File and git state.

The Hub never stores these contents. It stores account records and Worker public keys. Its workspace metadata includes titles and tab positions. It includes tiling geometry also.

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

No. LeapMux encrypts all Frontend-to-Worker traffic from end to end. The Hub authenticates connections and forwards opaque ciphertext. It never holds the session keys. The endpoints do not trust the Hub with plaintext.

The Hub can see connection metadata:

- Channel IDs.
- Ciphertext sizes.
- Timing.

Traffic analysis is part of the threat model. The Hub sees account and workspace records, plus Worker public keys also. The Worker's hostname and filesystem paths travel inside the encrypted application stream. The relay cannot read them.

| The Hub can see | The Hub cannot see |
|---|---|
| Connection metadata (channel IDs, ciphertext sizes, timing), account and workspace records, Worker public keys | Agent transcripts, tool-call arguments and outputs, terminal I/O, file contents, diffs, git status |

See [Security & Threat Model](/docs/admin/security/) for the authoritative scope of what the Hub does and does not see.

{{< callout type="info" >}}
In solo mode the Hub and Worker run in the same process. The end-to-end encryption protocol still applies. It does not protect against a local attacker who can reach the loopback port. Solo mode requires trust in the local host.
{{< /callout >}}

## Which coding agents are supported?

LeapMux supports {{< agent-provider-count >}} agent providers. Each provider uses the same chat interface. The available features depend on the provider's native command-line interface (CLI).

{{< agent-logos >}}

A provider appears in the picker only when LeapMux detects its installation on the Worker. For example, the Worker needs `claude` to offer Claude Code. ZCode supplies no CLI executable, so LeapMux detects its desktop installation instead.

See the [feature matrix](/docs/using/coding-agents/#feature-matrix) for each provider's exact support and limits.

## Can Workers run behind a NAT or firewall?

Yes. The **Worker always initiates its connection to the Hub**. It needs outbound access only. It can run behind network address translation (NAT) or a firewall without an inbound port. Set its `--hub` URL to your Hub. Use `https://` when the Hub uses TLS. The Worker reconnects automatically after a disconnection.

Local Workers can instead use a Unix domain socket (`unix:<path>`) or Windows named pipe (`npipe:<name>`).

See [Managing Workers](/docs/admin/managing-workers/) and [Configuration](/docs/admin/configuration/).

## Can I use PostgreSQL or MySQL instead of SQLite?

Yes, for the **Hub**. Select its storage backend with `storage.type`:

- `sqlite`, the default.
- `postgres`.
- `mysql`.
- `cockroachdb`.
- `yugabytedb`.
- `tidb`.

The Postgres-compatible and MySQL-compatible backends reuse their respective drivers. See [Configuration](/docs/admin/configuration/) for each backend's driver. Each external backend needs a `dsn`:

```yaml
storage:
  type: postgres
  postgres:
    dsn: "postgres://user:password@db.example.com:5432/leapmux?sslmode=disable"
```

Migrations run automatically when the store opens. **Workers always use SQLite locally**. You cannot change that database type. Storage settings use nested keys. Set them through the YAML configuration file or CLI flags. Simple environment variables cannot set these nested keys.

See [Configuration](/docs/admin/configuration/) and [Encryption & Data](/docs/admin/encryption-and-data/).

## How do multiple agents avoid clobbering each other?

Use **git worktrees**. When you open an agent or terminal, LeapMux can create a separate worktree and branch for it. Agents in separate worktrees use separate working trees and branches. For example, one agent can refactor while another writes tests. A third can fix a build failure in its own worktree.

The sidebar groups tabs by repository and branch, so you know which agent owns which branch. When you close the last tab of a worktree that has uncommitted changes, LeapMux asks you to confirm.

See [Worktrees & Branches](/docs/using/worktrees-and-branches/).

## Do my sessions survive a restart or reboot?

Agent sessions do. The Worker's local SQLite database keeps agent state. Sessions return when the Worker or machine restarts and reconnects to the Hub. You do not need to relaunch each agent manually.

Open the **New agent** dialog and use **Resume an existing session** to reopen a prior session. The list combines LeapMux's records with the CLI's native records for that directory. LeapMux resumes the provider's native session. For example, Claude Code uses `--resume`.

A shell process cannot survive a Worker restart. LeapMux keeps each terminal's last screen. Its tab returns at the same position after the Worker restarts. Press **Enter** to restart the shell in the same working directory. A temporary disconnection keeps the live shell attached if the Worker process continues to run.

See [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## What's the difference between the browser and the desktop app?

They are the same SolidJS Frontend. The difference is packaging:

- **Browser** — open `http://<host>:4327` against a running Hub, dev, or solo instance.
- **Desktop app** — a native Tauri app with the Frontend in an embedded WebView.
  Solo mode listens only on a local socket without a TCP port.
  The app can connect to a remote Hub also.

The same end-to-end encryption applies either way. Pick the desktop app for a self-contained local setup; use the browser when connecting to a shared Hub.

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
