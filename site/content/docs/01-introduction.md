---
title: "Introduction"
type: docs
weight: 1
---

Welcome to the LeapMux user manual. This chapter explains what LeapMux is, the problem it solves, who it is for, the coding agents it supports, and how the rest of this manual is organized.

## The problem

A terminal works fine for one or two coding agents side by side. At three or four — one refactoring, one writing tests, one chasing a failing build — shell tabs stop helping. You lose track of which agent owns which branch, the agents clobber each other's working tree, and a stray `tmux` crash or dev-box reboot means re-launching each agent with `--resume` and rebuilding your layout by hand.

LeapMux exists to fix exactly that. It gives every agent its own git worktree and branch, keeps sessions attached across restarts, and lays everything out in one workspace you can tile, float, and rearrange.

## What LeapMux is

LeapMux is a workspace for running several coding agents and shell terminals at once, each in a git worktree and branch you pick, tiled or floating, on a local or remote machine. Sessions stay attached across restarts, and all Frontend-to-Worker traffic is end-to-end encrypted. It runs in your browser or as a native desktop app.

Under the hood, LeapMux is a single Go binary (`leapmux`) that can play three roles:

- A **Hub** that handles login, workspace management, and Worker registration, and relays encrypted traffic between your Frontend and your Workers.
- A **Worker** that runs the agent processes, terminals, file browsing, and git operations on a given machine, keeping its own local SQLite database.
- A **Frontend** — the SolidJS web app (also embedded in the desktop app) that renders the tiling layout, agents, terminals, and file browser.

You run it in one of two deployment shapes. **Solo mode** (`leapmux solo`) packs the Hub and Worker into one process on `127.0.0.1:4327` for a zero-config, single-user, no-login setup — ideal for your own laptop or dev box. **Distributed mode** runs `leapmux hub` and one or more `leapmux worker` processes separately, so Workers can live on different machines, behind NATs, and multiple people can share workspaces. The native desktop app can do either: run solo mode locally, or connect to a remote Hub.

A defining property of distributed mode is that the Hub is an **authenticated relay, not a trusted peer**. It routes opaque ciphertext between the Frontend and Workers and can see connection metadata (who is talking to whom, message sizes, timing) and account records, but it cannot read agent transcripts, tool calls, terminal I/O, file contents, or even a Worker's hostname. The full trust boundaries are covered in [Security & Threat Model](/docs/23-security-and-threat-model/).

> **Note:** Solo mode auto-authenticates every request as the admin and binds to loopback by default. That collapses the trust model down to local-host trust, which is exactly what you want on your own machine. If you bind solo mode to a non-loopback address, LeapMux warns you, because anyone who can reach the port gets full admin access without credentials. For multi-user or exposed setups, run `leapmux hub` instead. See [Running LeapMux](/docs/17-running-leapmux/).

## Who it is for

LeapMux is built for two overlapping audiences:

- **Developers** who run multiple coding agents and want each on its own branch and worktree, with sessions that survive crashes and reboots, in a single tiled or floating layout — on their own machine or against a beefier remote box.
- **Operators** who want to host LeapMux for a team: a central Hub with real authentication, role-based organizations, pluggable database storage, and NAT-friendly Workers that need no inbound ports.

## Supported coding agents

LeapMux supports nine coding-agent providers. A provider only appears in the agent picker if its command-line tool is detected on the Worker, so the list you actually see depends on which CLIs are installed.

Every provider is first-class: you get the same core experience — chat, streamed tool calls, permission prompts, the plan/todo sidebar, and session resume — with all of them. What you can do varies only by what each agent's own CLI offers.

| Agent | Detected binary |
|-------|-----------------|
| Claude Code | `claude` |
| Codex | `codex` |
| Gemini CLI | `gemini` |
| Cursor | `cursor-agent` |
| GitHub Copilot | `copilot` |
| Kilo | `kilo` |
| OpenCode | `opencode` |
| Goose | `goose` |
| Pi | `pi` |

Each provider exposes its own models, effort levels, permission modes, and other settings, which you change mid-session from an in-chat settings dropdown. For example, Claude Code defaults to the `opus[1m]` model and Codex to `gpt-5.4`. For how to open an agent, chat, answer permission prompts, switch models, and resume a session, see [Coding Agents](/docs/09-coding-agents/).

## Key features

Beyond running many agents at once, LeapMux gives you:

- **Git worktree management** — Open each agent or terminal in a new or existing worktree, and create or switch branches at open time, with dirty-worktree protection when you close. See [Worktrees & Branches](/docs/10-worktrees-and-branches/).
- **Git-aware file browser** — A file tree with real-time git status, staged / unstaged / change filters, and inline diffs, even on a remote Worker. See [File Browser](/docs/12-file-browser/).
- **Integrated terminals** — Full PTY shell sessions living alongside your agents in the same tiling or floating layout, on the same Worker, and persistent across reconnects. See [Terminals](/docs/11-terminals/).
- **NAT-friendly Workers** — Workers always initiate outbound connections to the Hub, so they can run behind firewalls and NATs with no inbound ports open. See [Managing Workers](/docs/19-managing-workers/).
- **Multi-org with RBAC** — Organizations with Owner / Admin / Member roles; workspaces shared per user or per org member. See [Organizations & Members](/docs/06-organizations-and-members/).
- **Pluggable Hub storage** — SQLite (the default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB for the Hub; Workers always use a local SQLite database. See [Configuration](/docs/18-configuration/).
- **End-to-end encryption** — Frontend-to-Worker traffic is encrypted with a hybrid post-quantum handshake; Worker identity is pinned on first connection (trust on first use). See [Security & Threat Model](/docs/23-security-and-threat-model/) and [Encryption & Data](/docs/22-encryption-and-data/).
- **Persistent sessions** — Agent and terminal state live in the Worker's local database and stay attached across Hub restarts, Worker reconnects, and browser refreshes — no manual `--resume` juggling.
- **Browser and desktop** — Use LeapMux in any browser pointed at the Hub, or install the native desktop app for macOS, Linux, or Windows. See [Installation](/docs/03-installation/).
- **Live layout collaboration** — Workspace tabs and tiling geometry sync in real time across everyone with access, so a shared workspace stays in sync. See [Collaboration & Presence](/docs/13-collaboration-and-presence/).

## How this manual is organized

The manual is grouped into a few broad parts. Start at the top if you are new; jump straight to a part if you already know what you need.

- **Getting started** — [Installation](/docs/03-installation/) and [Quick Start](/docs/04-quick-start/) get LeapMux running and walk you through your first agent session. [Concepts & Architecture](/docs/02-concepts/) explains the Hub / Worker / Frontend model and the org / workspace / tile / tab vocabulary the rest of the manual uses.
- **Using LeapMux day to day** — [Accounts & Authentication](/docs/05-accounts-and-authentication/), [Organizations & Members](/docs/06-organizations-and-members/), [Workspaces](/docs/07-workspaces/), [Tabs & Layout](/docs/08-tabs-and-layout/), [Coding Agents](/docs/09-coding-agents/), [Worktrees & Branches](/docs/10-worktrees-and-branches/), [Terminals](/docs/11-terminals/), [File Browser](/docs/12-file-browser/), [Collaboration & Presence](/docs/13-collaboration-and-presence/), [Settings & Preferences](/docs/14-settings-and-preferences/), and [Keyboard Shortcuts](/docs/15-keyboard-shortcuts/).
- **Automation** — [Remote Control CLI](/docs/16-remote-control-cli/) covers driving a running Hub (or scripting an agent) with `leapmux remote`.
- **Running and operating LeapMux** — [Running LeapMux](/docs/17-running-leapmux/), [Configuration](/docs/18-configuration/), [Managing Workers](/docs/19-managing-workers/), [Admin CLI](/docs/20-admin-cli/), [Authentication Providers](/docs/21-authentication-providers/), [Encryption & Data](/docs/22-encryption-and-data/), and [Security & Threat Model](/docs/23-security-and-threat-model/).
- **Reference** — [CLI Reference](/docs/24-cli-reference/), [Troubleshooting](/docs/25-troubleshooting/), [FAQ](/docs/26-faq/), and [Glossary](/docs/27-glossary/).

You can always return to the [manual home](/docs/) for the full table of contents.

## Issues and contributions

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**. The source is available, but the project does **not** accept code contributions yet.

The reason is the license's built-in conversion to Apache 2.0: for that relicensing to happen cleanly, the maintainers must hold the rights to every line of code. Without a **Contributor License Agreement (CLA)** in place, accepting outside contributions now would make that switch extremely hard — it would mean tracking down every past contributor for their consent. Once a CLA is ready, the project expects to open up to external contributions.

What the maintainers do welcome in the meantime is **issues**. If you hit a bug or want a feature, please open an issue at the project's GitHub repository — preferably with a plan generated by a frontier model attached — and the maintainers will follow up.

> **Tip:** The FSL-1.1-ALv2 license automatically converts to Apache 2.0 two years after each release is first made available. The full terms live in the `LICENSE.md` file at the repository root.
