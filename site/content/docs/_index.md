---
title: User Manual
type: docs
weight: 1
toc: false
---

Welcome to the LeapMux user manual. LeapMux is a workspace for running several
coding agents and shell terminals at once — each in a git worktree and branch you
pick, tiled or floating, on a local or remote machine. Sessions stay attached
across restarts, and Frontend↔Worker traffic is end-to-end encrypted.

This manual covers both **using** LeapMux (the in-app experience) and **running**
it (installation, configuration, and administration). Use the sidebar to browse
every chapter, or start with the cards below.

## Start here

{{< cards >}}
  {{< card link="/docs/01-introduction/" title="Introduction" icon="book-open" subtitle="What LeapMux is, the problems it solves, and the agents it supports." >}}
  {{< card link="/docs/02-concepts/" title="Concepts & Architecture" icon="template" subtitle="Hub, Worker, and Frontend; solo vs distributed; the workspace model." >}}
  {{< card link="/docs/04-quick-start/" title="Quick Start" icon="sparkles" subtitle="Launch LeapMux and open your first coding agent in minutes." >}}
{{< /cards >}}

## Using LeapMux

{{< cards >}}
  {{< card link="/docs/09-coding-agents/" title="Coding Agents" icon="cube" subtitle="Open agents, pick models, chat, and answer permission prompts." >}}
  {{< card link="/docs/10-worktrees-and-branches/" title="Worktrees & Branches" icon="code" subtitle="Run each agent in its own git worktree and branch." >}}
  {{< card link="/docs/08-tabs-and-layout/" title="Tabs & Layout" icon="view-grid" subtitle="Tabs, tiling, splits, grids, and floating windows." >}}
  {{< card link="/docs/11-terminals/" title="Terminals" icon="terminal" subtitle="Open shell terminals alongside your agents." >}}
  {{< card link="/docs/12-file-browser/" title="File Browser" icon="folder-tree" subtitle="Browse files with live git status and inline diffs." >}}
  {{< card link="/docs/15-keyboard-shortcuts/" title="Keyboard Shortcuts" icon="adjustments" subtitle="The full default keybindings and how to customize them." >}}
{{< /cards >}}

## Running & administering

{{< cards >}}
  {{< card link="/docs/17-running-leapmux/" title="Running LeapMux" icon="server" subtitle="Run modes, ports, data directories, and Docker deployment." >}}
  {{< card link="/docs/18-configuration/" title="Configuration" icon="cog" subtitle="Every configuration key, environment variable, and storage backend." >}}
  {{< card link="/docs/20-admin-cli/" title="Admin CLI" icon="terminal" subtitle="Manage orgs, users, workers, OAuth, keys, and the database." >}}
  {{< card link="/docs/23-security-and-threat-model/" title="Security & Threat Model" icon="shield-check" subtitle="Trust boundaries, end-to-end encryption, and worker pinning." >}}
{{< /cards >}}

## Reference

{{< cards >}}
  {{< card link="/docs/24-cli-reference/" title="CLI Reference" icon="terminal" subtitle="A consolidated command-line cheat-sheet." >}}
  {{< card link="/docs/25-troubleshooting/" title="Troubleshooting" icon="puzzle" subtitle="Symptoms, causes, and fixes for common problems." >}}
  {{< card link="/docs/26-faq/" title="FAQ" icon="annotation" subtitle="Quick answers to common questions." >}}
  {{< card link="/docs/27-glossary/" title="Glossary" icon="collection" subtitle="Definitions of LeapMux terms." >}}
  {{< card link="/docs/28-legal/" title="Legal" icon="scale" subtitle="License, trademarks, third-party notices, and privacy." >}}
{{< /cards >}}
