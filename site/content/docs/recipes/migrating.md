---
title: "Migrating from tmux, Orca, or Conductor"
description: "Map your existing tmux, Orca, or Conductor workflow to LeapMux workspaces, git worktrees, persistent terminals, remote Workers, and CLI control."
type: docs
weight: 5
---

If you already run multiple coding agents in `tmux`, Orca, or Conductor, the same workflows map directly to LeapMux. LeapMux focuses on self-hosted end-to-end encryption behind network address translation (NAT) with zero inbound ports, native desktop and browser access across Linux, Windows, and macOS, {{< agent-provider-count >}} providers with an end-to-end verified feature matrix, and a built-in control command-line interface (CLI) with Agent Skills.

## Concept mapping

| Concept | `tmux` | Orca | Conductor | LeapMux |
| --- | --- | --- | --- | --- |
| Project / task grouping | Session | Workspace / repo group | Workspace | **Workspace** (left sidebar, grouped under **In progress** and **Archived**) |
| Split panes & windows | Windows + split panes | Agent panes / tabs | Workspace panes | **Tiling layout** (splits, grids, and floating windows, synced across devices) |
| Branch isolation | Manual `git worktree add` | Built-in worktrees | Built-in worktrees | **Git options → Create new worktree** per agent or terminal tab, with dirty-worktree protection on close |
| Shell beside an agent | Adjacent `tmux` pane | Integrated terminal | Integrated terminal | **Terminal tab** tiled beside your agent, plus a per-directory **Quake terminal** |
| Remote execution | Secure Shell (SSH) + remote `tmux` | SSH, remote, and cloud runtimes | Cloud Firecracker microVMs | **Self-hosted outbound Worker** (`leapmux daemon worker`) — zero inbound ports, end-to-end encrypted |
| Platforms | Any Unix terminal | Desktop | macOS desktop + cloud | Native desktop app + web browser on **Linux, Windows, and macOS** |
| Scripting & automation | `tmux send-keys` + `capture-pane` | `orca` CLI and Bots | HTTP / MCP API | **Built-in `leapmux` CLI** (`agent send --wait`, `terminal wait --change`, `events --checkpoint`) + **Agent Skills** |
| Agent diversity | Any CLI typed into a pane | 27 agents | Focused provider set | {{< agent-provider-count >}} agent CLIs with unified chat and diffs; check the [Supported Agents matrix](/docs/reference/supported-agents/) for per-provider support of permission banners, model switching, and plan approval |

## Three quick adjustments

1. **Let LeapMux create your worktrees.** Instead of running `git worktree add` in a shell before starting an agent, pick **Create new worktree** in the **New workspace** or **New agent** dialog (or pass `--worktree <branch>` to `leapmux agent open`). LeapMux groups tabs by repository and branch in the sidebar and prompts before deleting uncommitted work on close.
2. **Resume existing CLI sessions without starting over.** Already have an active Claude Code or Codex conversation started inside `tmux`? Open **New agent...**, pick the same directory and provider, and select that conversation under **Resume an existing session**.
3. **Replace `tmux send-keys` scripts with `leapmux` control commands.** Inside a LeapMux terminal or agent tab, `leapmux` (and its short alias `lmx` on macOS and Linux) is automatically placed on `PATH` and authenticated to the local Worker — except when running the Linux `.AppImage` desktop package (which bundles no CLI; use `.deb` or the standalone binary) or when tab remote control is disabled on the Worker. Use `leapmux terminal attach <id>` when you want an interactive terminal view from your native shell.

## Let your coding agent help you migrate

Paste this prompt into your current coding agent to translate your `tmux` layouts, worktree scripts, or existing workflow into LeapMux:

{{< copy-prompt label="Ask your coding agent to migrate your workflow" >}}
Read https://leapmux.dev/agent-guide.md and inspect my current git repositories, installed coding-agent CLIs, and any tmux or worktree scripts in this workspace. Recommend a LeapMux setup (Solo vs. remote Workers), help me install it, and map my current workflow into LeapMux workspaces, worktrees, and skills.
{{< /copy-prompt >}}

## Next steps

- [Quick Start](/docs/getting-started/quick-start/) — launch your first two parallel agents in under five minutes.
- [Supported Agents](/docs/reference/supported-agents/) — compare per-provider support across all {{< agent-provider-count >}} coding agents.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — learn split, tab, and workspace shortcuts.
- [CLI Guide](/docs/using/cli-guide/) — replace shell wrappers with structured `leapmux` commands.
