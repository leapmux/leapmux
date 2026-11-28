---
title: "Introduction"
description: "What LeapMux does, when to use it, what it is not, how to pick a starting path, and how this manual is organized."
type: docs
weight: 1
---

A plain terminal works for one or two coding agents. At three or four — one refactoring, one writing tests, and one chasing a build failure — shell tabs break down. Agents overwrite each other's files, you lose track of which tab owns which branch, and a reboot forces you to rebuild your layout and resume each session by hand.

LeapMux solves that problem. It runs {{< agent-provider-count >}} coding-agent command-line interfaces (CLIs) — including Claude Code, Codex, OpenCode, Cursor, and Gemini CLI — alongside full pseudo-terminal (PTY) shells and a git-aware file browser. Each agent runs in its own linked git worktree and branch, sessions persist across restarts, layouts sync across your devices, and traffic between your app and your agents is end-to-end encrypted. See [Supported Agents](/docs/reference/supported-agents/) for the complete provider roster and feature matrix.

## When to use LeapMux

Use LeapMux when you want to:

- Run multiple coding agents on the same repository without branch or file collisions.
- Keep agent transcripts, terminal shells, and tiled layouts intact across disconnects and reboots.
- Drive agents on remote Linux or GPU devboxes behind firewalls and network address translation (NAT) without opening inbound ports.
- Script or orchestrate agents and terminals from another agent or CI without polling screens.

## What LeapMux is not

- **Not a model provider or API proxy.** You bring your own installed agent CLIs and provider credentials on the machine that runs your code.
- **Not a shared multiplayer IDE.** Workspaces are strictly owner-only. Multiple users can share a central Hub, but nobody else sees your workspaces or sessions.
- **Not a cloud code host.** Your repositories, transcripts, and terminal output stay on the Worker machine that runs your agents. The Hub relays encrypted traffic and cannot read agent chats, terminal output, or files; a compromised Hub could only serve modified browser client code unless you run the desktop app or pin trusted origins. See [Security & Authentication](/docs/admin/security-and-auth/).

## Choose your path

- **Solo on one machine:** Install the desktop app or standalone binary in [Installation](/docs/getting-started/installation/), then run your first parallel session in [Quick Start](/docs/getting-started/quick-start/).
- **Remote devboxes behind NAT:** Compare topologies in [Concepts & Topologies](/docs/getting-started/concepts/), then follow [Connect a Remote Devbox Behind NAT](/docs/recipes/remote-devbox/).
- **Coming from `tmux`, Orca, or Conductor:** Map your existing habits in [Migrating from tmux, Orca, or Conductor](/docs/recipes/migrating/).

## Or let your coding agent guide you

Already have a coding agent open? Paste this prompt to let it inspect your environment and guide your setup:

{{< copy-prompt label="Ask your coding agent to set up LeapMux" >}}
Read https://leapmux.dev/agent-guide.md and help me choose the right LeapMux setup mode for my machine, install it, and start my first parallel coding-agent session.
{{< /copy-prompt >}}

## How this manual is organized

- **Getting Started** — [Introduction](/docs/getting-started/introduction/), [Installation](/docs/getting-started/installation/), [Quick Start](/docs/getting-started/quick-start/), and [Concepts & Topologies](/docs/getting-started/concepts/).
- **Workflow Recipes** — [Run Parallel Coding Agents](/docs/recipes/parallel-agents/), [Get a Second-Opinion Review](/docs/recipes/second-opinion-review/), [Connect a Remote Devbox Behind NAT](/docs/recipes/remote-devbox/), [Automate Agents & Terminals Without Polling](/docs/recipes/cli-automation/), and [Migrating from tmux, Orca, or Conductor](/docs/recipes/migrating/).
- **Using LeapMux** — [Workspaces](/docs/using/workspaces/), [Tabs & Layout](/docs/using/tabs-and-layout/), [Coding Agents](/docs/using/coding-agents/), [Worktrees & Branches](/docs/using/worktrees-and-branches/), [Terminals](/docs/using/terminals/), [File Browser](/docs/using/file-browser/), [CLI Guide](/docs/using/cli-guide/), [Agent Skills](/docs/using/agent-skills/), [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/), [Settings & Preferences](/docs/using/settings/), and [Accounts & Connected Apps](/docs/using/accounts-and-apps/).
- **Administration** — [Production Checklist](/docs/admin/production-checklist/), [Running LeapMux](/docs/admin/running-leapmux/), [Starting and Stopping](/docs/admin/starting-and-stopping/), [Reverse Proxy](/docs/admin/reverse-proxy/), [Configuration Guide](/docs/admin/configuration/), [Configuration Reference](/docs/admin/configuration-reference/), [Managing Workers](/docs/admin/managing-workers/), [Sign-in Providers](/docs/admin/sign-in-providers/), [App Authorization](/docs/admin/app-authorization/), [Encryption & Data](/docs/admin/encryption-and-data/), [Security & Authentication](/docs/admin/security-and-auth/), [Cryptography & Key Architecture](/docs/admin/cryptography/), [Admin CLI](/docs/admin/admin-cli/), and [Recovery](/docs/admin/recover/).
- **Reference** — [Supported Agents](/docs/reference/supported-agents/), [CLI Reference](/docs/reference/cli-reference/), the topic-split [Control Command Reference](/docs/reference/commands/), the topic-split [Troubleshooting](/docs/reference/troubleshooting/) guides, [FAQ](/docs/reference/faq/), [Glossary](/docs/reference/glossary/), [OAuth API](/docs/reference/oauth-api/), and [Legal](/docs/reference/legal/).
