---
title: "Starting and Stopping"
description: "Start and stop LeapMux as a service of your operating system: choose login or boot startup, check status, change settings, and move or remove an instance."
type: docs
weight: 3
---

Start LeapMux with `leapmux daemon <mode> start` (or `leapmux up` for `solo`), and stop it with `leapmux daemon <mode> stop` (or `leapmux down`). This is the recommended way to run every mode outside a container. `start` installs LeapMux as a background **service** of your operating system, so it keeps running after you close the terminal or log out and restarts after a reboot:

- launchd on macOS
- systemd on Linux
- Task Scheduler or the Service Control Manager on Windows

LeapMux creates and removes the service for you. This page applies to every mode (`solo`, `hub`, `worker`, and `dev`), using `solo` in the examples. Register a standalone Worker before starting it; see [Register a Worker](/docs/admin/managing-workers/#register-a-worker).

{{< callout >}}
Inside a container, use `leapmux daemon <mode> run` in the foreground instead of `start` or `enable`. The container runtime owns the restart policy; see [Running under Docker](/docs/admin/running-leapmux/#running-under-docker).
{{< /callout >}}

## The commands

| Command | What it does |
| --- | --- |
| `leapmux daemon <mode> start` | Creates the service when needed, starts it, waits until LeapMux is ready, and prints the URL to open. (`leapmux up` is a shortcut for `solo`.) |
| `leapmux daemon <mode> stop` | Stops the instance and waits for shutdown to finish. The service remains installed for the next login or boot. (`leapmux down` is a shortcut for `solo`.) |
| `leapmux daemon <mode> restart` | Stops the instance, then starts it again as a service. |
| `leapmux daemon <mode> status` | Reports whether the instance runs, whether it is ready, and why it failed. |
| `leapmux daemon <mode> logs` | Prints or follows (`-f`, `-n`) the latest attempt log of the service. |
| `leapmux daemon <mode> enable` | Creates the service for automatic startup without starting LeapMux now. |
| `leapmux daemon <mode> disable` | Removes the service. The instance must be stopped first. |
| `leapmux daemon <mode> run` | Runs in the foreground without creating a service. Use this in containers. |

## Start LeapMux

```bash
leapmux daemon solo start
```

The first `start` asks when LeapMux should start automatically:

```text
When should LeapMux start automatically?

  1. When you log in
     Runs as your account.
     Stops after your last applicable login session ends.

  2. When the computer starts
     Runs as your account even while you are logged out.

Choose [1]:
```

Press Enter for login startup, or type `2` for boot startup. [Choose login or boot startup](#choose-login-or-boot-startup) compares the two options.

`start` then displays a preview of the service definition and asks for confirmation (macOS example):

```text
LeapMux will configure solo:
  Startup: login
  Account: alice (501)
  Mechanism: launchd LaunchAgent
  Definition: com.leapmux.solo.3f2a9c41d07b6e58
  Executable: /usr/local/bin/leapmux
  Working directory: /Users/alice
  Configuration: /Users/alice/.config/leapmux/solo/solo.yaml
  Data directory: /Users/alice/.config/leapmux/solo
  Private environment overrides: PATH

Apply this configuration? [y/N]:
```

Type `y` or `yes` to create the service. Any other answer changes nothing. When boot startup needs administrator rights, the preview states so, and the operating system prompts only after you confirm.

`start` returns once LeapMux is ready:

- A standalone Worker is ready before it connects to its Hub (`status` reports Hub connectivity separately).
- A `solo` instance is ready after its bundled Worker registers.
- A new `dev` instance is ready as soon as it serves `/setup`; its bundled Worker starts after you create the first administrator there.

When ready, `start` prints the URL (`public_url` if set, otherwise `http://localhost:<port>`) and copy-ready follow-up commands targeting that exact config file and data directory:

```text
Solo started.
Open http://localhost:4327 in your browser.

Commands for this instance:
  Status:  leapmux daemon solo status --config /Users/alice/.config/leapmux/solo/solo.yaml --data-dir /Users/alice/.config/leapmux/solo
  Stop:    leapmux daemon solo stop --config /Users/alice/.config/leapmux/solo/solo.yaml --data-dir /Users/alice/.config/leapmux/solo
  Disable: leapmux daemon solo disable --config /Users/alice/.config/leapmux/solo/solo.yaml --data-dir /Users/alice/.config/leapmux/solo
```

Later `start` runs reuse your saved answers unless you change the configuration. To register the service without starting it immediately, run `leapmux daemon <mode> enable`. Always manage the service through `leapmux daemon <mode>` rather than `systemctl`, `launchctl`, or Windows service tools directly — `leapmux` reloads systemd automatically, waits for readiness, and keeps saved settings synchronized.

## Choose login or boot startup

| Property | Login startup | Boot startup |
| --- | --- | --- |
| Starts | When you log in | When the computer boots |
| Keeps running after logout | No | Yes |
| Runs as | Your OS account | Your OS account (never `root` or `SYSTEM`) |
| Administrator rights to install | Not needed on macOS and Linux | Required (`sudo`, or User Account Control on Windows) |
| macOS | LaunchAgent | LaunchDaemon |
| Linux | systemd user service | systemd system service |
| Windows | Scheduled task | Windows service |

Choose **login startup** for a personal laptop or desktop. Choose **boot startup** for a server or any machine where LeapMux must run while nobody is logged in.

- **Login startup** stops after your last login session ends (a locked screen or detached remote session does not stop it). On Linux, login startup follows the graphical desktop session; SSH sessions do not count, so headless Linux servers require boot startup.
- **Boot startup** still runs LeapMux as your non-root OS account; administrator rights are needed only to install the service definition.
- **Windows:** both startup types ask for your account password so Windows can start LeapMux unattended. LeapMux reads the password without echoing or saving it and hands it to Windows. After you change your Windows password, re-run `leapmux daemon <mode> enable` to update the service credentials.

### Service identifiers

Each service identifier ends with a 16-digit hexadecimal ID derived from the OS account, mode, and data directory. `start` and `enable` print the exact identifier after `Definition:`.

| Platform | Login startup | Boot startup |
| --- | --- | --- |
| macOS | LaunchAgent `com.leapmux.<mode>.<id>` in `~/Library/LaunchAgents/` | LaunchDaemon `com.leapmux.<mode>.<id>` in `/Library/LaunchDaemons/` |
| Linux | User unit `leapmux-<mode>-<id>.service` in `~/.config/systemd/user/` | System unit `leapmux-<mode>-<id>.service` in `/etc/systemd/system/` |
| Windows | Scheduled task `leapmux-<mode>-<id>` in Task Scheduler root | Windows service `leapmux-<mode>-<id>` |

## Check status and logs

```bash
leapmux daemon solo status
leapmux daemon solo logs -f
```

`status` reports:

- Process state (`starting`, `running`, `stopping`, `stopped`, `failed`, or `unresponsive`)
- Whether the installed service matches the saved configuration
- Readiness and the URL to open while running
- For a Worker, registration state and Hub connectivity separately
- The latest start timestamps, exit code, failure reason, and log file path

Exit codes for `status`: `0` when running (including a running Worker waiting to reconnect or a `dev` Hub awaiting `/setup`), `8` when stopped, `1` on failure or no response, and `2` for invalid flags. Use `status --json` for scripts (`{"data": ...}`); see [CLI Reference](/docs/reference/cli-reference/#status---json).

Each start writes a log file under `<data-dir>/.lifecycle/logs/`, retaining the last ten starts. When `start` fails or times out, it prints the tail of the log and its path.

## Stop, restart, and remove the service

```bash
leapmux daemon solo stop       # Stop now; service still runs at next login or boot.
leapmux daemon solo restart    # Stop, then start again.
leapmux daemon solo disable    # Remove the service (must be stopped first).
```

`disable` keeps the data directory (databases, Worker credentials, and logs). To remove an instance completely, run `stop`, then `disable`, then delete its data directory.

{{< callout type="warning" >}}
Deleting a data directory permanently destroys that instance's data — a Hub's database and `encryption.key`, or a Worker's credentials and agent/terminal history. Deregister a standalone Worker before deleting its data directory so the Hub forgets it; see [Deregistering a Worker](/docs/admin/managing-workers/#deregistering-a-worker).
{{< /callout >}}

## Change service configuration

When `start`, `restart`, or `enable` writes a service, it saves:

- Runtime flags passed to the command (such as `--listen` or `--log-level`)
- The configuration file path (not its contents)
- Your shell's `PATH`, so the service locates the same coding-agent binaries
- The `--environment-file` path, if provided

Update each type of setting at its source:

- **Config file setting:** edit the YAML file, then run `leapmux daemon <mode> restart`.
- **Saved flag:** pass the updated flag to `restart` (for example `leapmux daemon hub restart --listen :4400`). Other saved flags stay intact; repeatable flags such as `--listen` replace the entire saved list.
- **Startup mode:** run `restart --startup=boot` or `restart --startup=login`.

When service settings change, `restart` previews the update and asks for confirmation.

### Environment variables

A managed service does not inherit variables exported in your shell — it captures only `PATH`. Put `LEAPMUX_*` daemon settings in the YAML config file instead. Because a Worker launches coding agents through your login shell by default (`use_login_shell: true`), API keys exported in your shell profile still reach agents.

To pass extra environment variables to the service itself, write a flat string-to-string YAML map and pass `--environment-file`:

```yaml
# /home/alice/.config/leapmux/worker/environment.yaml
HTTPS_PROXY: "http://proxy.example.com:3128"
GIT_SSH_COMMAND: "ssh -i /home/alice/.ssh/leapmux"
```

```bash
leapmux daemon worker restart --environment-file /home/alice/.config/leapmux/worker/environment.yaml
```

Environment file rules:

- LeapMux reads the file on each service start, so edits apply on the next `restart`.
- A `PATH` entry in the file replaces the saved `PATH`.
- The file cannot set any of the 10 account-identity variables owned by LeapMux: **`HOME`**, **`USER`**, **`LOGNAME`**, **`USERNAME`**, **`USERPROFILE`**, **`HOMEDRIVE`**, **`HOMEPATH`**, **`UID`**, **`EUID`**, or **`SUDO_USER`**.
- On Windows, variable names are matched case-insensitively and duplicates are rejected.
- Clear the file association with `--environment-file=`.

The service definition stores only the path to the environment file, not its values. Protect the file with owner-only permissions whenever it contains secrets.

## Script the setup

Non-interactive scripts cannot answer prompts, so pass `--startup` and `--yes`:

```bash
leapmux daemon worker start --startup=boot --yes
```

Without a terminal, `start` and `enable` fail immediately and list any missing flags.

`--timeout` overrides the wait limit (defaults: `60s` for `start`, `3m` for `stop`, `4m` for `restart`, `3s` for `status`). When launchd or systemd stops a service directly at logout or shutdown, it sends `SIGKILL` after 3 minutes. A CLI timeout ends only the wait, not the service operation — run `status` before retrying.

## One instance per data directory

The data directory identifies an instance. Pass the same `--data-dir` or `--config` to every command targeting that instance:

- A Hub and a standalone Worker require separate data directories.
- A `solo` or `dev` instance owns its root data directory along with its `hub/` and `worker/` subdirectories.

## Move an instance to another directory

You can move a data directory or restore a backup at a new path under the same OS account and mode. Move `solo` and `dev` directories as a whole so `hub/` and `worker/` stay together.

{{% steps %}}

### Stop and disable the old service

```bash
leapmux daemon hub stop --data-dir /srv/leapmux/old
leapmux daemon hub disable --data-dir /srv/leapmux/old
```

### Move or restore the directory

Move the data directory (or restore your backup) to the new path. Move a Worker's data directory rather than copying it — running two copies creates duplicate Workers sharing one identity and credential.

### Start the instance at the new path

```bash
leapmux daemon hub start --data-dir /srv/leapmux/new
```

The first command adopts the directory and asks the startup questions again for the new service definition.

{{% /steps %}}

## When something goes wrong

Start with `leapmux daemon <mode> status` to inspect the failure reason and log path, then check [Troubleshooting — Background service](/docs/reference/troubleshooting/install-and-desktop/#background-service).
