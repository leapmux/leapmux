---
title: "Migrating from tmux, Orca, or Conductor"
description: "Map your existing tmux, Orca, or Conductor workflow to LeapMux workspaces, git worktrees, persistent terminals, remote Workers, and CLI control."
type: docs
weight: 5
---

If you already juggle coding agents in `tmux`, Orca, or Conductor, you do not need to throw away your mental model. Most habits map one-to-one into LeapMux — with native git worktrees, persistent agent sessions, and remote Workers built in.

## Concept mapping

| Concept | `tmux` | Orca / Conductor | LeapMux |
| --- | --- | --- | --- |
| Project / task grouping | Session | Workspace / repo group | **Workspace** (left sidebar, grouped under **In progress** and **Archived**) |
| Split panes & windows | Windows + split panes | Agent panes / tabs | **Tiling layout** (splits, grids, and floating windows, synced across devices) |
| Branch isolation | Manual `git worktree add` | Built-in worktrees | **Git options → Create new worktree** per agent or terminal tab, with dirty-worktree protection on close |
| Shell beside an agent | Adjacent `tmux` pane | Integrated terminal | **Terminal tab** tiled beside your agent, plus a per-directory **Quake terminal** |
| Surviving disconnects | `tmux attach` | Local desktop process | Agent sessions and PTY shells live in the **Worker**; layouts and transcripts return automatically on reconnect |
| Remote Linux / GPU box | `ssh` + remote `tmux` | Local machine only (or SSH wrappers) | **Outbound Worker** (`leapmux daemon worker`) — zero open inbound ports, end-to-end encrypted |
| Scripting & automation | `tmux send-keys` + `capture-pane` | Manual UI or shell wrappers | **`leapmux` CLI** (`agent send --wait`, `terminal wait --change`, `events --checkpoint`) + **Agent Skills** |
| Agent diversity | Any CLI you type into a pane | Selected agent CLIs | {{< agent-provider-count >}} first-class agent CLIs with unified chat, diffs, permission banners, and mid-session model switching |

## Three quick adjustments

1. **Let LeapMux create your worktrees.** Instead of running `git worktree add` in a shell before launching an agent, pick **Create new worktree** in the **New workspace** or **New agent** dialog (or pass `--worktree <branch>` to `leapmux agent open`). LeapMux groups tabs by repository and branch in the sidebar and prompts before deleting uncommitted work on close.
2. **Resume existing CLI sessions without starting over.** Already have an active Claude Code or Codex conversation you started inside `tmux`? Open **New agent...**, pick the same directory and provider, and select that conversation under **Resume an existing session**.
3. **Replace `tmux send-keys` scripts with `leapmux` control commands.** Inside any LeapMux terminal or agent tab, `leapmux` (and its short alias `lmx`) is already on `PATH` and authenticated to the local Worker. Use `leapmux terminal attach <id>` when you want an interactive terminal view from your native shell.

## Let your coding agent help you migrate

Paste this prompt into your current coding agent to translate your `tmux` layouts, worktree scripts, or existing agent workflow into LeapMux:

{{< copy-prompt label="Ask your coding agent to migrate your workflow" >}}
Read https://leapmux.dev/agent-guide.md and inspect my current git repositories, installed coding-agent CLIs, and any tmux or worktree scripts in this workspace. Recommend a LeapMux setup (Solo vs. remote Workers), help me install it, and map my current workflow into LeapMux workspaces, worktrees, and skills.
{{< /copy-prompt >}}

## Next steps

- [Quick Start](/docs/getting-started/quick-start/) — launch your first two parallel agents in under five minutes.
- [Keyboard Shortcuts](/docs/using/keyboard-shortcuts/) — learn split, tab, and workspace shortcuts.
- [Control CLI](/docs/using/control-cli/) — replace shell wrappers with structured `leapmux` commands.
