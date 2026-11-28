# LeapMux Agent Guide

Use this guide when a human asks you to explain LeapMux, pick a deployment mode, install it, launch their first parallel coding-agent session, or diagnose a setup problem.

## Check whether you are already inside LeapMux

Check whether `LEAPMUX_CONTROL_SOCK` is set in your environment before you follow this guide:

- **If `LEAPMUX_CONTROL_SOCK` is set**, you are already running inside a LeapMux tab. Do not walk the user through installation. Use the `leapmux` command-line interface (CLI) directly and read `https://leapmux.dev/skills/leapmux/SKILL.md` (or run `leapmux guide` and `leapmux skill show leapmux`) to control tabs, agents, terminals, and worktrees.
- **If `LEAPMUX_CONTROL_SOCK` is unset**, you are helping the user from outside LeapMux. Continue with this guide.

This document differs from `https://leapmux.dev/skills/leapmux/SKILL.md`. That skill teaches an agent *inside* LeapMux how to operate the control CLI. This guide teaches an external coding agent how to help a human get started with LeapMux.

## What LeapMux is

LeapMux runs multiple coding-agent CLIs (Claude Code, Codex, OpenCode, Cursor, Gemini CLI, and more — see `https://leapmux.dev/docs/reference/supported-agents/`) alongside shell terminals. Each agent can run in its own git worktree and branch, sessions persist across restarts, and Frontend-to-Worker traffic is end-to-end encrypted.

LeapMux has three roles:

- **Frontend:** The native desktop app or web browser interface.
- **Hub:** The central coordinator and encrypted relay (default port `4327`). It stores workspaces, layouts, and user accounts, not your code or transcripts.
- **Worker:** The process on the machine that holds your repositories. It runs coding agents, pseudo-terminal (PTY) shells, git operations, and file browsing, and keeps transcripts in its local SQLite database. Workers open outbound connections to the Hub, so they need zero open inbound ports.

## Choose a deployment mode

Ask the user where their repositories live and whether other people or devices need access, then pick one mode (full comparison in `https://leapmux.dev/docs/getting-started/concepts/`):

| Mode | Best for | How it runs | Auth & network |
| --- | --- | --- | --- |
| **Solo Desktop** | One developer on macOS, Linux, or Windows | Bundled Hub + Worker over local inter-process communication (IPC) | No login on local IPC; no TCP port opened by default |
| **Solo Daemon** | One developer using a browser, CLI, or persistent local service | `leapmux up` (`leapmux daemon solo start`) / `leapmux down` | Listens on `127.0.0.1:4327`; first browser visit sets the `solo` password |
| **Distributed (Hub + Workers)** | Remote devboxes behind firewalls/NAT, or multiple users | `leapmux daemon hub start` (or Docker `LEAPMUX_MODE=hub`) plus `leapmux daemon worker` on each repo host | Full authentication on Hub; Workers dial out using a registration key |

For an all-in-one container reachable over the network, use `LEAPMUX_MODE=dev` rather than `solo`, because `solo` listens on `127.0.0.1:4327` by default. Place an HTTPS reverse proxy in front of any network-reachable Hub.

## Install LeapMux

Prerequisites: install at least one supported coding-agent CLI (for example `claude` or `codex`) and confirm that the Worker's shell `PATH` or profile resolves it.

- **Desktop app:** Download from [GitHub Releases](https://github.com/leapmux/leapmux/releases) (`.dmg` on macOS arm64, `.deb` or `.AppImage` on Linux amd64/arm64, `.msi` on Windows amd64). On macOS, grant **Full Disk Access** when prompted in Solo mode. Note that Linux `.AppImage` bundles no `leapmux` CLI; recommend `.deb` or the standalone binary if the user wants the CLI or Agent Skills.
- **Standalone binary:** Download the `leapmux` release archive for your platform from [GitHub Releases](https://github.com/leapmux/leapmux/releases), extract it, and place `leapmux` on `PATH`. Use `leapmux up` to start solo mode and `leapmux down` to stop it.
- **Docker:** Pull `ghcr.io/leapmux/leapmux:latest`, set `-e LEAPMUX_MODE=hub` (or `dev` / `worker`), expose `-p 4327:4327` for `hub`/`dev`, and mount a persistent volume at `-v leapmux-data:/data`.

## Run the first parallel agent session

Guide the user through these steps:

1. **Start LeapMux:** Launch **LeapMux Desktop** and select **Solo**, or run `leapmux up` (same as `leapmux daemon solo start`) and open `http://127.0.0.1:4327`.
2. **Create a workspace:** Open the **`⋯` actions menu** on **In progress** in the left sidebar and choose **New workspace...**. Pick the Worker, an installed coding-agent provider, and the repository directory.
3. **Isolate in a git worktree:** Under **Git options**, select **Create new worktree** so the agent runs on its own branch and linked worktree. Creating the workspace automatically opens its first agent.
4. **Add a second parallel agent:** Click **`+`** on the tab bar, choose **New agent...**, pick the same repository with **Create new worktree** on a second branch, and split the tile so both agents sit side by side.
5. **Adjust settings mid-session:** Change the model, reasoning effort, or provider-specific mode (such as **Permission Mode** in Claude Code) from the composer's status-bar chips or **`[+]`** menu.
6. **Install Agent Skills (optional):** Run `leapmux skill install --all` (uses `--target auto`: installs to `~/.agents/skills` and automatically includes `~/.claude/skills` when `~/.claude` exists, plus existing tool-specific directories) or enable skills in **Preferences → Chat & Composer → Agent skills**.

## Diagnose problems safely

Always confirm which machine has the problem (**Client**, **Hub**, or **Worker**) before checking paths or logs. Start with read-only diagnostics (and consult `https://leapmux.dev/docs/reference/troubleshooting/` and `https://leapmux.dev/docs/using/cli-guide/`):

```sh
leapmux version
leapmux context show
leapmux whoami --json
leapmux doctor
leapmux daemon solo status   # or hub / worker / dev
leapmux worker list
leapmux agent providers
```

Safety rules:

- Never modify config files, change credentials (`leapmux auth`), or restart a Hub or Worker without explicit user approval.
- Warn the user before restarting a Worker that hosts active agent sessions.
- Redact tokens, cookies, private keys, recovery codes, and internal hostnames before sharing logs or drafting bug reports.

## Ground every answer — never hallucinate

- **Never invent CLI commands, flags, config keys, or environment variables.**
- Verify command syntax locally with `leapmux <command> --help`, `leapmux commands <prefix> --json`, or `leapmux guide <topic>`.
- Fetch the compact documentation index at `https://leapmux.dev/llms-small.txt` (or the full index at `https://leapmux.dev/llms.txt`) and read only the specific `/docs/.../index.md` pages needed for the user's question (for example `/docs/getting-started/concepts/`, `/docs/using/cli-guide/`, `/docs/reference/supported-agents/`, and `/docs/reference/troubleshooting/`).
- Give the user direct, actionable steps instead of telling them to read the manual themselves.
