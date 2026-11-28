---
title: "Agents & Terminals"
description: "Fixes for coding-agent discovery, provider startup failures, setting-triggered restarts, shell detection, exited terminals, and in-tab leapmux CLI errors."
type: docs
weight: 3
---

Fixes for coding-agent providers, mid-session restarts, terminal shells, and running `leapmux` inside spawned tabs. For usage guides, see [Coding Agents](/docs/using/coding-agents/) and [Terminals](/docs/using/terminals/).

## Coding agents

### Agent provider missing from picker

**Symptom**
The **Agent Provider** selector shows **"No agents available"**, or an installed CLI (such as Claude Code, Codex, or Cursor) is missing.

**Cause**
The selected Worker's shell must find the provider executable on `PATH` or through a profile alias or function. Discovery runs from a neutral directory so results apply across the whole Worker; relative `PATH` entries work only inside one checkout.

**Fix**
- Install the provider CLI on the Worker, or define a forwarding alias/function in a profile loaded by the Worker's shell.
- Click **Refresh available providers** in the dialog (or run `leapmux agent providers`).
- Ensure any wrapper script runs the provider in the foreground, forwards all arguments and standard streams, preserves its exit status, and leaves stdin untouched.

{{< details title="Supported shells and login-shell behavior" closed="true" >}}
- **Login shell default:** The Worker launches discovery and agents in login mode by default when the configured shell supports it. To disable login flags, set `use_login_shell: false` in `worker.yaml` / `solo.yaml`, or pass `--use-login-shell=false` when starting or restarting the service. Windows PowerShell 5.1 loads profiles without a login flag.
- **Supported shells:** Bash, Zsh, Fish, POSIX sh, Dash, BusyBox ash, posh, Ksh 93, mksh, Csh, Tcsh, Elvish, Nushell, Xonsh, Yash, Windows PowerShell 5.1, and PowerShell 7.
- **Shell notes:** Use a current Nushell release for agent commands. Elvish loads its profile without starting its interactive editor. On Windows, if the Worker defaults to Command Prompt, set its default shell (`LEAPMUX_DEFAULT_SHELL`) to PowerShell or Bash.
{{< /details >}}

### ZCode missing from picker

**Symptom**
Other providers appear, but **ZCode** does not.

**Cause**
LeapMux looks for a `zcode` command in the Worker's shell, or for the ZCode desktop app's `zcode.cjs` alongside a Node interpreter that supports `node:sqlite`.

**Fix**
Install the ZCode desktop app or put `zcode` on the Worker's `PATH`. For custom locations, set `LEAPMUX_ZCODE_SCRIPT` to the path to `zcode.cjs` and, if needed, `LEAPMUX_ZCODE_NODE` to the Node executable path.

### ZCode reports not configured

**Symptom**
Opening a ZCode agent fails immediately before any message is sent.

**Cause**
ZCode has no enabled model provider with an API key in `~/.zcode/v2/config.json`.

**Fix**
Sign in to the ZCode desktop application on the Worker machine and enable a model provider. LeapMux reads `~/.zcode/v2/config.json` read-only and never modifies it.

### Agent fails to start

**Symptom**
The chat pane shows a centered error from the Worker reporting that the provider failed to start.

**Cause**
The agent subprocess could not launch, failed authentication, used an invalid working directory, or exceeded the startup timeout (default `5m`).

**Fix**
- Read the exact error message in the pane — it comes directly from the Worker.
- On the Worker host, run the provider CLI directly (for example `claude --version`) to verify installation and login.
- If startup is legitimately slow, increase the timeout:
  - Standalone Worker: set `agent_startup_timeout: 10m` in `worker.yaml` and run `leapmux daemon worker restart`, or run `leapmux daemon worker restart --agent-startup-timeout 10m`.
  - Embedded Worker (`solo` / `dev`): run `leapmux admin settings set timeouts '{"agent_startup_seconds":600}'`. See [Configuration Reference](/docs/admin/configuration-reference/).

### Cline agent fails to start

**Symptom**
A Cline agent fails to start and reports that Cline's `providers.json` does not match Cline's expected format.

**Cause**
Cline silently ignores a malformed `providers.json` and falls back to a built-in provider endpoint you did not choose, so LeapMux refuses to launch it.

**Fix**
Run `cline auth` on the Worker host to regenerate `providers.json`, then reopen the agent.

### Setting change restarts the agent

**Symptom**
Changing model, effort, or permission mode from a composer chip or the **[+]** menu restarts the agent subprocess.

**Cause**
Most settings apply **live**, while settings that map to process launch flags require restarting the provider subprocess. Which settings require a restart depends on the provider — for example, Claude Code restarts when switching effort to **Auto** (removing its `--effort` flag), and providers such as Reasonix fix their model or mode at launch. Changing to a model that does not support the current effort also resets effort to **Auto**.

**Fix**
Wait for the agent to finish restarting and reattaching to its session. If the restart fails, follow [Agent fails to start](#agent-fails-to-start).

## Terminals and `leapmux` CLI

### CLI inside a tab cannot find the Hub

**Symptom**
Running `leapmux` inside a LeapMux terminal or agent prints `error: not_logged_in: no hub selected`.

**Cause**
Inside a spawned tab, `leapmux` connects through the Worker-injected `LEAPMUX_CONTROL_SOCK` and `LEAPMUX_CONTROL_TOKEN` environment variables. If a sub-shell cleared the environment or the Worker failed to bind its local control endpoint, those variables are missing.

{{< details title="Control-endpoint socket path and namespace rules" closed="true" >}}
- **Unix:** The socket lives under `<data-dir>/run/control` when the path fits the OS socket path limit; deeper data directories fall back to a private per-account namespace under `/tmp`.
- **Unix refusals:** The Worker refuses insecure directory permissions, conflicting namespace ownership, or a fallback path that still exceeds the platform limit.
- **Windows:** Uses a named pipe independent of the Worker data directory path.
{{< /details >}}

**Fix**
- **Inside a LeapMux tab:** check `env | grep LEAPMUX_CONTROL`. If absent, open a fresh tab or inspect `leapmux daemon worker logs` for a control-endpoint startup error (on Unix, fix directory permissions or choose a shorter Worker `--data-dir`).
- **Outside LeapMux (your own shell):** sign in once:

```bash
leapmux auth login --hub https://hub.example.com
```

Add `--device-code` on headless, SSH, or container machines. Check your active identity with `leapmux whoami` and `leapmux auth status`. See [CLI Guide](/docs/using/cli-guide/).

### Terminal exited and offers a restart

**Symptom**
A terminal tab stops accepting input and displays a bracketed notice showing the shell's exit code (or Worker disconnect) and prompting to press **Enter**.

**Cause**
The shell exited (`exit`, crash, or Worker restart). LeapMux preserves the last screen and scrollback until you restart it.

**Fix**
Ensure the Worker is online, then press **Enter** inside the terminal to launch a new shell in the same working directory.

### Expected shell missing in New terminal

**Symptom**
The **Shell** dropdown in **New terminal** omits a shell you installed or shows **"No shells available"**.

**Cause**
The Worker caches available shells at startup from `LEAPMUX_DEFAULT_SHELL`, `SHELL`, platform defaults, and `PATH` lookups (`sh`, `bash`, `zsh`, `fish`, `nu`/`nushell`, `elvish`, `pwsh`, `powershell`).

**Fix**
Install the shell on the Worker's `PATH`, restart the Worker (`leapmux daemon worker restart`), and reopen the dialog. To pin a specific default shell, set `LEAPMUX_DEFAULT_SHELL` in the Worker's environment.
