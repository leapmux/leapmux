---
title: "Introduction"
description: "What LeapMux is, when to use it, what it is not, how to pick a starting path, and how this manual is organized."
type: docs
weight: 1
---

A plain terminal works for one or two coding agents. At three or four — one refactoring, one writing tests, one chasing a build failure — shell tabs break down. Agents overwrite each other's files, you lose track of which tab owns which branch, and a reboot forces you to rebuild your layout and resume each session by hand.

LeapMux solves that problem. It runs {{< agent-provider-count >}} coding-agent command-line interfaces (CLIs) — including Claude Code, Codex, OpenCode, Cursor, Gemini CLI, and more — alongside full pseudo-terminal (PTY) shells and a git-aware file browser. Each agent can run in its own linked git worktree and branch, sessions persist across restarts, layouts sync across your devices, and traffic between your app and your agents is end-to-end encrypted. See [Coding Agents](/docs/using/coding-agents/#feature-matrix) for the complete provider roster and feature matrix.

## When to use LeapMux

Use LeapMux when you want to:

- Run multiple coding agents on the same repository without branch or file collisions.
- Keep agent transcripts, terminal shells, and tiled layouts intact across disconnects and reboots.
- Drive agents on remote Linux or GPU devboxes behind firewalls and network address translation (NAT) without opening inbound ports.
- Script or orchestrate agents and terminals from another agent or continuous integration (CI) without polling screens.

## What LeapMux is not

- **Not a model provider or API proxy.** You bring your own installed agent CLIs and provider credentials on the machine that runs your code.
- **Not a shared multiplayer IDE.** Workspaces are strictly owner-only. Multiple users can share a central Hub, but nobody else sees your workspaces or sessions.
- **Not a cloud code host.** Your repositories, transcripts, and terminal output stay on the Worker machine that runs your agents. While the Hub only relays encrypted traffic, it cannot read that content. A compromised or malicious Hub still has authority over identity and browser Frontend code; see [Security & Threat Model](/docs/admin/security/).

## Choose your path

- **Solo on one machine:** Install the desktop app or standalone binary in [Installation](/docs/getting-started/installation/), then run your first parallel session in [Quick Start](/docs/getting-started/quick-start/).
- **Remote devboxes behind NAT:** Compare topologies in [Ways to Run LeapMux](/docs/getting-started/ways-to-run/), then follow [Connect a Remote Devbox](/docs/recipes/remote-devbox/).
- **Coming from `tmux`, Orca, or Conductor:** Map your existing habits in [Migrating to LeapMux](/docs/recipes/migrating/).

## Or let your coding agent guide you

Already have a coding agent open? Paste this prompt to let it inspect your environment and guide your setup:

{{< copy-prompt label="Ask your coding agent to set up LeapMux" >}}
Read https://leapmux.dev/agent-guide.md and help me choose the right LeapMux setup mode for my machine, install it, and start my first parallel coding-agent session.
{{< /copy-prompt >}}

## How this manual is organized

- **Getting Started** — [Introduction](/docs/getting-started/introduction/), [Installation](/docs/getting-started/installation/), [Quick Start](/docs/getting-started/quick-start/), [Ways to Run LeapMux](/docs/getting-started/ways-to-run/), and [Concepts & Architecture](/docs/getting-started/concepts/).
- **Workflow Recipes** — Ready-to-run playbooks for [Parallel Agents](/docs/recipes/parallel-agents/), [Second-Opinion Reviews](/docs/recipes/second-opinion-review/), [Remote Devboxes](/docs/recipes/remote-devbox/), [CLI Automation](/docs/recipes/cli-automation/), and [Migrating](/docs/recipes/migrating/).
- **Using LeapMux** — Daily workflows: [Workspaces](/docs/using/workspaces/), [Tabs & Layout](/docs/using/tabs-and-layout/), [Coding Agents](/docs/using/coding-agents/), [Worktrees & Branches](/docs/using/worktrees-and-branches/), [Terminals](/docs/using/terminals/), [Device Sync](/docs/using/device-sync/), [File Browser](/docs/using/file-browser/), [Control CLI](/docs/using/control-cli/), [Agent Skills](/docs/using/agent-skills/), [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/), [Settings & Preferences](/docs/using/settings/), [Accounts & Authentication](/docs/using/accounts/), and [Connected Apps](/docs/using/connected-apps/).
- **Administration** — Operating a deployment: [Running LeapMux](/docs/admin/running-leapmux/), [Starting and Stopping](/docs/admin/starting-and-stopping/), [Reverse Proxy](/docs/admin/reverse-proxy/), [Configuration](/docs/admin/configuration/), [Managing Workers](/docs/admin/managing-workers/), [Sign-in Providers](/docs/admin/sign-in-providers/), [App Authorization](/docs/admin/app-authorization/), [Encryption & Data](/docs/admin/encryption-and-data/), [Security & Threat Model](/docs/admin/security/), [Cryptography](/docs/admin/cryptography/), [Admin CLI](/docs/admin/admin-cli/), and [Recovery](/docs/admin/recover/).
- **Reference** — [CLI Reference](/docs/reference/cli-reference/), the topic-split [Control Command Reference](/docs/reference/commands/), [Troubleshooting](/docs/reference/troubleshooting/), [FAQ](/docs/reference/faq/), [Glossary](/docs/reference/glossary/), [OAuth API](/docs/reference/oauth-api/), and [Legal](/docs/reference/legal/) (including the Functional Source License, Version 1.1, Apache 2.0 Future License terms).
