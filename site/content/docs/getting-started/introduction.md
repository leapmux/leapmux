---
title: "Introduction"
description: "Learn what LeapMux does and which coding agents it supports. Find the chapters that explain its features and administration."
type: docs
weight: 1
---

This chapter introduces LeapMux. It explains the problem that LeapMux solves and the people who use it. It lists supported coding agents and the parts of this manual.

## The problem

A terminal works well for one or two coding agents. With three or four agents, you can lose track of each agent's branch. For example, one agent can refactor while another writes tests and a third fixes a build failure. Agents can overwrite each other's work in a shared worktree. A `tmux` crash or machine reboot requires you to relaunch each agent with `--resume`. You must restore the layout manually also.

LeapMux lets you choose each agent's git worktree and branch. Sessions stay attached across restarts. Arrange the agents in one workspace with tiles or floating windows.

## What LeapMux is

LeapMux runs several coding agents and shell terminals in one workspace. Choose each agent's git worktree and branch. Use tiles or floating windows on a local or remote machine. Agent sessions stay attached across restarts. LeapMux encrypts traffic to your agents from end to end. Use LeapMux in a browser or as a native desktop app.

The single `leapmux` program supports two deployment types:

- **Solo mode** (`leapmux solo`) starts with one command and one account.
  The desktop app uses local inter-process communication (IPC) without credentials.
  A browser that connects through TCP sets the first password.
- A **distributed** deployment uses a central **Hub** that several people can sign in to.
  The agents run on separate machines.

The native desktop app supports both deployment types.

Your work stays on the machine that runs the agents. The Hub never stores these contents:

- Agent transcripts.
- Terminal output.
- File contents.

The Hub relays encrypted traffic that it cannot read. See [Concepts & Architecture](/docs/getting-started/concepts/) for the components and deployment types. That chapter explains the trust boundaries also.

## Who it is for

LeapMux serves two groups that can overlap:

- **Developers** can give each coding agent its own branch and worktree.
  Sessions survive crashes and reboots.
  Arrange the agents in one layout on your own machine or a more powerful remote machine.
- **Administrators** can host LeapMux for a team with a central Hub.
  The Hub authenticates users and supports several database types.
  Agents run on separate machines that need no open inbound ports.

## Supported coding agents

LeapMux supports {{< agent-provider-count >}} coding-agent providers. Most providers supply a native command-line interface (CLI). ZCode uses its desktop application instead. LeapMux lists a provider only when it detects its installation on the machine that will run the agent.

LeapMux uses the same chat interface for every provider. The available features depend on each provider's native CLI. The [feature matrix](/docs/using/coding-agents/#feature-matrix) states the exact support and limits. The Goals & To-dos sidebar appears when the CLI reports task tools or a session goal.

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
| ZCode | its desktop application |
| Codewhale | `codewhale` |
| Kimi Code | `kimi` |
| MiMo Code | `mimo` |
| Qwen Code | `qwen` |
| Oh My Pi | `omp` |
| Grok Build | `grok` |
| Kiro | `kiro-cli-chat` |
| Amp | `amp` |
| Cline | `cline` |
| CodeBuddy Code | `codebuddy` or `cbc` |
| Junie | `junie` |
| Letta Code | `letta` |
| Dirac | `dirac` |
| Qoder CLI | `qodercli` |
| Factory Droid | `droid` |
| Fast Agent | `fast-agent` |
| Command Code | `command-code`, `commandcode`, or `cmdc` |
| DeepSeek Harness | `dsh` |
| Gemini CLI | `gemini` |

Each provider exposes its native settings. These include models and permission modes, plus reasoning effort where the CLI supports it. Change settings during a session through the composer's status bar or **[+]** menu. A new agent starts with the provider's configured defaults. See [Coding Agents](/docs/using/coding-agents/) for chat and session controls.

## Key features

LeapMux supplies these features also:

- **Git worktree management** — Open an agent or terminal in a new or existing worktree.
  Create or switch branches when you open it.
  LeapMux checks uncommitted changes when you close a worktree.
  See [Worktrees & Branches](/docs/using/worktrees-and-branches/).
- **File browser** — Read the file tree and current git status on a local or remote machine.
  Filter staged changes, unstaged changes, or all changes.
  Read inline diffs.
  See [File Browser](/docs/using/file-browser/).
- **Integrated terminals** — Run full pseudo-terminal (PTY) shell sessions beside your agents in the same layout.
  Sessions persist across reconnects.
  See [Terminals](/docs/using/terminals/).
- **Remote machines** — Workers always initiate their connections.
  They can run behind firewalls or network address translation (NAT) without open inbound ports.
  See [Managing Workers](/docs/admin/managing-workers/).
- **Database storage** — Use SQLite by default, or choose an external database.
  The Hub supports PostgreSQL and MySQL.
  It supports CockroachDB, YugabyteDB, and TiDB also.
  See [Configuration](/docs/admin/configuration/).
- **End-to-end encryption** — A hybrid post-quantum handshake protects traffic between the browser and your agents.
  LeapMux pins the machine's key on your first connection, which implements trust on first use.
  See [Security & Threat Model](/docs/admin/security/) and [Encryption & Data](/docs/admin/encryption-and-data/).
- **Persistent sessions** — Agent sessions resume across restarts and reconnects without a manual `--resume`.
  Terminals keep their live shell across reconnects and browser refreshes.
  A Worker restart ends the shell.
  The terminal shows its last screen.
  Press Enter to restart the shell.
  See [Terminals](/docs/using/terminals/).
- **Browser and desktop** — Use LeapMux in any modern browser, or install the native desktop app for macOS, Linux, or Windows. See [Installation](/docs/getting-started/installation/).
- **Live layout sync** — LeapMux synchronizes your tabs and tiling geometry across your own devices in near real time.
  See [Device Sync](/docs/using/device-sync/).

## How this manual is organized

The manual has four parts. Start with the first part if you are new. Open a specific part if you know which feature you need.

- **Getting started**
  - [Introduction](/docs/getting-started/introduction/) is this page.
  - [Concepts & Architecture](/docs/getting-started/concepts/) explains LeapMux and its workspace terminology.
  - [Installation](/docs/getting-started/installation/) installs LeapMux.
  - [Quick Start](/docs/getting-started/quick-start/) explains your first agent session.
- **Using LeapMux** follows the order of a typical work session.
  - [Workspaces](/docs/using/workspaces/).
  - [Tabs & Layout](/docs/using/tabs-and-layout/).
  - [Coding Agents](/docs/using/coding-agents/).
  - [Worktrees & Branches](/docs/using/worktrees-and-branches/).
  - [Terminals](/docs/using/terminals/).
  - [Device Sync](/docs/using/device-sync/).
  - [File Browser](/docs/using/file-browser/).
  - [Control CLI](/docs/using/control-cli/) controls a running instance or agent from a script with `leapmux control`.
  - [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/).
  - [Settings & Preferences](/docs/using/settings/).
  - [Accounts & Authentication](/docs/using/accounts/).
  - [Connected Apps](/docs/using/connected-apps/).
- **Administration**
  - [Running LeapMux](/docs/admin/running-leapmux/).
  - [Configuration](/docs/admin/configuration/).
  - [Managing Workers](/docs/admin/managing-workers/).
  - [Sign-in Providers](/docs/admin/sign-in-providers/).
  - [App Authorization](/docs/admin/app-authorization/) explains OAuth apps that request access to Hub accounts.
  - [Encryption & Data](/docs/admin/encryption-and-data/).
  - [Security & Threat Model](/docs/admin/security/).
  - [Admin CLI](/docs/admin/admin-cli/) explains Hub administration.
  - [Recovery](/docs/admin/recover/) explains offline recovery.
- **Reference**
  - [CLI Reference](/docs/reference/cli-reference/).
  - [Troubleshooting](/docs/reference/troubleshooting/).
  - [FAQ](/docs/reference/faq/).
  - [Glossary](/docs/reference/glossary/).
  - [OAuth API](/docs/reference/oauth-api/).
  - [Legal](/docs/reference/legal/).

You can always return to the [manual home](/docs/) for the full table of contents.

## Issues and contributions

LeapMux uses the **Functional Source License, Version 1.1, Apache 2.0 Future License (FSL-1.1-ALv2)**.
The source is available, but the project does **not** accept code contributions yet.

The license converts to Apache 2.0 after its specified period.
That conversion requires the maintainers to hold the rights to every line of code.
Without a **Contributor License Agreement (CLA)**, external contributions would require consent from every past contributor before conversion.
Obtaining all that consent would be very difficult.
The project expects to accept external contributions after it prepares a CLA.

Create an **issue** in the project's GitHub repository if you find a bug or want a feature.
Include a plan from a frontier model if possible.
The maintainers will review the issue.

{{< callout >}}
For the full license terms — including how and when FSL-1.1-ALv2 converts to Apache 2.0 — see [Legal](/docs/reference/legal/).
{{< /callout >}}
