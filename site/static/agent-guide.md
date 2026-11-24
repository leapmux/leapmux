# LeapMux Agent Guide

Use this guide when a human asks you to explain LeapMux, pick a deployment mode, install it, launch their first parallel coding-agent session, or diagnose a setup problem.

## Check whether you are already inside LeapMux

Check whether `LEAPMUX_CONTROL_SOCK` is set in your environment before you follow this guide:

- **If `LEAPMUX_CONTROL_SOCK` is set**, you are already running inside a LeapMux tab. Do not walk the user through installation. Use the `leapmux` CLI directly and read `skills/leapmux/SKILL.md` (or run `leapmux guide` and `leapmux skill show leapmux`) to control tabs, agents, terminals, and worktrees.
- **If `LEAPMUX_CONTROL_SOCK` is unset**, you are helping the user from outside LeapMux. Continue with this guide.

This document is different from `skills/leapmux/SKILL.md`. `skills/leapmux/SKILL.md` teaches an agent *inside* LeapMux how to operate the control CLI. This guide teaches an external coding agent how to help a human get started with LeapMux.

## What LeapMux is

LeapMux is a workspace for running multiple coding-agent CLIs (Claude Code, Codex, OpenCode, Cursor, Gemini CLI, and more) and shell terminals side by side. Each agent can run in its own git worktree and branch, sessions persist across restarts, and Frontend-to-Worker traffic is end-to-end encrypted.

LeapMux has three roles:

- **Frontend:** The native desktop app or web browser interface.
- **Hub:** The central coordinator and encrypted relay (default port `4327`). It stores workspaces, layouts, and user accounts, not your code or transcripts.
- **Worker:** The process on the machine that holds your repositories. It runs coding agents, terminals, git operations, and file browsing, and keeps transcripts in its local SQLite database. Workers open outbound connections to the Hub, so they need no inbound ports open.

## Choose a deployment mode

Ask the user where their repositories live and whether other people or devices need access, then pick one mode:

| Mode | Best for | How it runs | Auth & network |
| --- | --- | --- | --- |
| **Solo desktop app** | One developer on macOS, Linux, or Windows | Bundled Hub + Worker over local inter-process communication (IPC) | No login on local IPC; no network port opened by default |
| **Solo CLI + browser** | One developer who prefers the `leapmux` binary in a browser | `leapmux daemon solo start` | Listens on `127.0.0.1:4327`; browser sets the `solo` password on first visit |
| **Distributed (Hub + Workers)** | Remote dev boxes, build servers behind NAT/firewalls, or multiple users | `leapmux daemon hub start` (or Docker `LEAPMUX_MODE=hub`) plus `leapmux daemon worker` on each repo host | Full authentication on Hub; Workers dial out to Hub using a registration key |

For an all-in-one container accessible over the network, use `LEAPMUX_MODE=dev` rather than `solo`, because `solo` binds `127.0.0.1:4327` by default. Put a reverse proxy in front of a Hub for HTTPS.

## Install LeapMux

Prerequisites: install at least one supported coding-agent CLI (for example `claude` or `codex`) and ensure the Worker's shell `PATH` or profile resolves it.

- **Desktop app:** Download from [GitHub Releases](https://github.com/leapmux/leapmux/releases) (`.dmg` on macOS arm64, `.deb` or `.AppImage` on Linux amd64/arm64, `.msi` on Windows amd64). On macOS, grant **Full Disk Access** when prompted in Solo mode.
- **Standalone binary:** Download the `leapmux` release archive for your platform from [GitHub Releases](https://github.com/leapmux/leapmux/releases), extract it, and place `leapmux` on `PATH`.
- **Docker:** Pull `ghcr.io/leapmux/leapmux:latest`, set `-e LEAPMUX_MODE=hub` (or `dev` / `worker`), expose `-p 4327:4327` for `hub`/`dev`, and mount a persistent volume at `-v leapmux-data:/data`.

## Run the first parallel agent session

Guide the user through these steps:

1. **Start LeapMux:** Launch **LeapMux Desktop** and select **Solo**, or run `leapmux daemon solo start` and open `http://127.0.0.1:4327`.
2. **Create a workspace:** Click **+** in the **In progress** sidebar section. Choose the Worker, an installed coding-agent provider, and the repository directory.
3. **Isolate in a git worktree:** Under **Git options**, select **Create new worktree** so the agent works on its own branch and linked worktree.
4. **Add a second parallel agent:** Open another agent tab from the tab bar's add menu, pick the same repository with **Create new worktree** on a second branch, and split the tile so both agents run side by side.
5. **Adjust settings mid-session:** Change the model, reasoning effort, or permission mode from the composer's status bar chips or `[+]` menu.
6. **Install Agent Skills (optional):** Run `leapmux skill install --all` (or enable skills in **Preferences → Chat & Composer → Agent skills**) so agents inside LeapMux learn parallel delegation, cross-provider review, council, and handoff playbooks.

## Diagnose problems safely

Always confirm which machine has the problem (**Client**, **Hub**, or **Worker**) before checking paths or logs. Start with read-only diagnostics:

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
- Verify command syntax locally with `leapmux <command> --help` or `leapmux guide <topic>`.
- Fetch the compact documentation index at `https://leapmux.dev/llms-small.txt` (or the full index at `https://leapmux.dev/llms.txt`) and read only the specific `/docs/.../index.md` pages needed for the user's question.
- Give the user direct, actionable steps rather than telling them to read the manual themselves.
