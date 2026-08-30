---
title: "Introduction"
description: "What LeapMux is, the problem it solves, who it is for, the coding agents it supports, and how this manual is organized."
type: docs
weight: 1
---

Welcome to the LeapMux user manual. This chapter explains what LeapMux is, the problem it solves, who it is for, the coding agents it supports, and how the rest of this manual is organized.

## The problem

A terminal works fine for one or two coding agents side by side. At three or four — one refactoring, one writing tests, one debugging a failing build — shell tabs are no longer enough. You lose track of which agent owns which branch. The agents overwrite each other's worktree. A stray `tmux` crash or dev-box reboot means re-launching each agent with `--resume` and rebuilding your layout by hand.

LeapMux exists to fix exactly that. It gives every agent its own git worktree and branch, keeps sessions attached across restarts, and lays everything out in one workspace you can tile, float, and rearrange.

## What LeapMux is

LeapMux is a workspace for running several coding agents and shell terminals at once, each in a git worktree and branch you pick, tiled or floating, on a local or remote machine. Agent sessions stay attached across restarts. Traffic to your agents is end-to-end encrypted. LeapMux runs in your browser or as a native desktop app.

It is a single program (`leapmux`) you can run two ways. The simplest is **solo mode** (`leapmux solo`): one command on your own machine, with no login and nothing to configure — ideal for a laptop or dev box. For a team, a **distributed** setup runs a central **Hub** that several people sign in to, while the agents themselves run on one or more separate machines — including remote boxes behind a NAT or firewall. The native desktop app can do either.

Whichever way you run it, your work — agent transcripts, terminal output, and file contents — stays on the machine that runs the agents, never on the Hub. The Hub only relays encrypted traffic it cannot read. For exactly how the pieces fit together — the components, the two deployment shapes, and the trust boundaries — see [Concepts & Architecture](/docs/getting-started/concepts/).

## Who it is for

LeapMux is built for two overlapping audiences:

- **Developers** who run multiple coding agents and want each on its own branch and worktree, with sessions that survive crashes and reboots, in a single tiled or floating layout — on their own machine or against a beefier remote box.
- **Administrators** who want to host LeapMux for a team: a central Hub with real authentication, pluggable database storage, and agents that run on separate machines needing no inbound ports open.

## Supported coding agents

LeapMux supports nine coding-agent providers. A provider only appears in the agent picker if its CLI is detected on the machine that will run your agents, so the list you see depends on which CLIs are installed.

Every provider is first-class: you get a consistent core experience — chat, streamed tool calls, permission prompts, and session resume — with all of them. The plan/todo sidebar appears for agents whose CLI emits task/todo tools. What you can do varies only by what each agent's own CLI offers.

| Agent | Detected binary |
|-------|-----------------|
| Claude Code | `claude` |
| Codex | `codex` |
| Cursor | `cursor-agent` |
| GitHub Copilot | `copilot` |
| Kilo | `kilo` |
| OpenCode | `opencode` |
| Goose | `goose` |
| Pi | `pi` |
| Reasonix | `reasonix` |

Each provider exposes its own settings: models and permission modes, plus effort/reasoning levels where the CLI supports them. You change them mid-session from the composer's status-bar chips or its **[+]** menu. A new agent starts on the provider's defaults. For how to open an agent, chat, answer permission prompts, switch models, and resume a session, see [Coding Agents](/docs/using/coding-agents/).

## Key features

Beyond running many agents at once, LeapMux gives you:

- **Git worktree management** — Open each agent or terminal in a new or existing worktree, and create or switch branches at open time, with dirty-worktree protection when you close. See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
- **Git-aware file browser** — A file tree with near-real-time git status, staged / unstaged / change filters, and inline diffs, even on a remote machine. See [File Browser](/docs/using/file-browser/).
- **Integrated terminals** — Full PTY shell sessions living alongside your agents in the same tiling or floating layout, and persistent across reconnects. See [Terminals](/docs/using/terminals/).
- **NAT-friendly remote machines** — The machines that run your agents always dial out to connect, so they can run behind firewalls and NATs with no inbound ports open. See [Managing Workers](/docs/admin/managing-workers/).
- **Pluggable storage** — Back the Hub with SQLite (the default), PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB. See [Configuration](/docs/admin/configuration/).
- **End-to-end encryption** — Traffic between your browser and your agents is encrypted with a hybrid post-quantum handshake, and the machine you connect to is pinned on first connection (trust on first use). See [Security & Threat Model](/docs/admin/security/) and [Encryption & Data](/docs/admin/encryption-and-data/).
- **Persistent sessions** — Agent sessions resume across restarts and reconnects, with no manual `--resume`. Terminals keep their live shell across reconnects and browser refreshes. A Worker restart ends the shell; the terminal shows its last screen and restarts when you press Enter. See [Terminals](/docs/using/terminals/).
- **Browser and desktop** — Use LeapMux in any modern browser, or install the native desktop app for macOS, Linux, or Windows. See [Installation](/docs/getting-started/installation/).
- **Live layout sync** — Your tabs and tiling geometry stay in sync in near real time across your own devices. See [Device Sync](/docs/using/device-sync/).

## How this manual is organized

The manual is grouped into a few broad parts. Start at the top if you are new; jump straight to a part if you already know what you need.

- **Getting started** — [Introduction](/docs/getting-started/introduction/) (this page), [Concepts & Architecture](/docs/getting-started/concepts/), which explains how LeapMux is built and the workspace / tile / tab vocabulary the rest of the manual uses, then [Installation](/docs/getting-started/installation/) and [Quick Start](/docs/getting-started/quick-start/), which get LeapMux running and walk you through your first agent session.
- **Using LeapMux** — the daily workflow in the order you meet it: [Workspaces](/docs/using/workspaces/), [Tabs & Layout](/docs/using/tabs-and-layout/), [Coding Agents](/docs/using/coding-agents/), [Worktrees & Branches](/docs/using/worktrees-and-branches/), [Terminals](/docs/using/terminals/), [Device Sync](/docs/using/device-sync/), and [File Browser](/docs/using/file-browser/); then [Control CLI](/docs/using/control-cli/) for driving a running instance (or scripting an agent) with `leapmux control`, and your account and how the app is tuned: [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/), [Settings & Preferences](/docs/using/settings/), [Accounts & Authentication](/docs/using/accounts/), and [Connected Apps](/docs/using/connected-apps/).
- **Administration** — [Running LeapMux](/docs/admin/running-leapmux/), [Configuration](/docs/admin/configuration/), [Managing Workers](/docs/admin/managing-workers/), [Sign-in Providers](/docs/admin/sign-in-providers/), [App Authorization](/docs/admin/app-authorization/), which covers the OAuth apps that ask your Hub for access to accounts, [Encryption & Data](/docs/admin/encryption-and-data/), [Security & Threat Model](/docs/admin/security/), [Admin CLI](/docs/admin/admin-cli/) for Hub administration, and [Recovery](/docs/admin/recover/), the offline break-glass tree.
- **Reference** — [CLI Reference](/docs/reference/cli-reference/), [Troubleshooting](/docs/reference/troubleshooting/), [FAQ](/docs/reference/faq/), [Glossary](/docs/reference/glossary/), [OAuth API](/docs/reference/oauth-api/), and [Legal](/docs/reference/legal/).

You can always return to the [manual home](/docs/) for the full table of contents.

## Issues and contributions

LeapMux is licensed under the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**. The source is available, but the project does **not** accept code contributions yet.

The reason is the license's built-in conversion to Apache 2.0: for that relicensing to happen cleanly, the maintainers must hold the rights to every line of code. Without a **Contributor License Agreement (CLA)** in place, accepting outside contributions now would make that switch very difficult — LeapMux would need the consent of every past contributor. Once a CLA is ready, the project expects to open up to external contributions.

What the maintainers do welcome in the meantime is **issues**. If you hit a bug or want a feature, open an issue at the project's GitHub repository, preferably with a plan generated by a frontier model. The maintainers will follow up.

> **Tip:** For the full license terms — including how and when FSL-1.1-ALv2 converts to Apache 2.0 — see [Legal](/docs/reference/legal/).
