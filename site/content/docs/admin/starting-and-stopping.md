---
title: "Starting and Stopping"
description: "Start and stop LeapMux as a service of your operating system: choose login or boot startup, check its status, change its settings, and remove it."
type: docs
weight: 2
---

Start LeapMux with `leapmux <mode> start`, and stop it with `leapmux <mode> stop`. This is the recommended way to run every mode outside a container. `start` runs LeapMux in the background as a **service**, so it keeps running after you close the terminal or log out, and it starts again after a restart of the computer. A service is an entry in the service manager of your operating system:

- launchd on macOS
- systemd on Linux
- Task Scheduler or the Service Control Manager on Windows

LeapMux creates and removes the service for you. This page applies to every mode: `solo`, `hub`, `worker`, and `dev`. The examples use `solo`. Register a standalone Worker before you start it; see [Register a Worker](/docs/admin/managing-workers/#register-a-worker).

{{< callout >}}
In a container, the container is the service, so use `leapmux <mode> run` there. It runs LeapMux in the foreground, without a service, until the process stops. The LeapMux image runs it for you, and Docker restarts the container. Do not use `start` or `enable` inside a container. See [Running under Docker](/docs/admin/running-leapmux/#running-under-docker).
{{< /callout >}}

## The commands

| Command | What it does |
| --- | --- |
| `leapmux <mode> start` | Creates the service when necessary, starts it, and waits until LeapMux is ready. |
| `leapmux <mode> stop` | Stops the instance and waits until its shutdown completes. The service remains, so LeapMux starts again at the next login or boot. |
| `leapmux <mode> restart` | Stops the instance, then starts it as a service. |
| `leapmux <mode> status` | Reports whether the instance runs, whether it is ready, and why it failed. |
| `leapmux <mode> enable` | Creates the service for automatic startup, but does not start LeapMux now. It prints the commands that start the instance and report its status. |
| `leapmux <mode> disable` | Removes the service. The instance must be stopped. |
| `leapmux <mode> run` | Runs in the foreground until the process stops, and never creates a service. Use it in a container. |

## Start LeapMux

```bash
leapmux solo start
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

Press Enter for login startup, or type `2` for boot startup. [Choose login or boot startup](#choose-login-or-boot-startup) compares the two.

`start` then shows what it will configure, and asks for your consent. This example comes from macOS:

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

Type `y` or `yes` to create the service. Any other answer changes nothing. When boot startup needs administrator rights, the preview says so, and the operating system asks for them only after you agree.

`start` returns when LeapMux is ready to accept connections. Readiness depends on the mode:

- A standalone Worker is ready before it connects to its Hub. `status` reports the connection separately.
- A solo instance is ready after its bundled Worker registers.
- A new `dev` instance is ready as soon as it serves the `/setup` page. Its bundled Worker starts after you create the first administrator there.

A later `start` reuses the saved answers and asks nothing, unless you change the configuration.

To create the service without starting LeapMux now, run `enable` instead of `start`. It asks the same questions, then prints the exact commands for this instance:

```text
Configured. Hub starts automatically at login.

Commands for this instance:
  Start:  leapmux hub start --config /home/alice/.config/leapmux/hub/hub.yaml --data-dir /home/alice/.config/leapmux/hub
  Status: leapmux hub status --config /home/alice/.config/leapmux/hub/hub.yaml --data-dir /home/alice/.config/leapmux/hub
```

Both commands select the instance by its configuration file and data directory, so they work from any directory. They use the syntax of a POSIX shell such as bash or zsh, and of PowerShell on Windows. Use them instead of `systemctl`, `launchctl`, or the Windows service tools: `start` waits until LeapMux is ready, and `status` explains a failure. On Linux, LeapMux reloads systemd itself, so you never run `systemctl daemon-reload`.

## Choose login or boot startup

| | Login startup | Boot startup |
| --- | --- | --- |
| Starts | When you log in | When the computer starts |
| Keeps running after you log out | No | Yes |
| Runs as | Your account | Your account |
| Administrator rights to create the service | Not needed on macOS and Linux | Needed: `sudo`, or User Account Control on Windows |
| macOS | LaunchAgent | LaunchDaemon |
| Linux | systemd user service | systemd system service |
| Windows | Scheduled task | Windows service |

Choose login startup for a laptop or a desktop that you use yourself. Choose boot startup for a server, or for any machine where LeapMux must run while nobody is logged in.

**Login startup** stops after your last login session ends. A locked screen does not stop it. A remote session that disconnects but stays signed in does not stop it either.

On Linux, login startup follows the graphical desktop session. A session over SSH does not count. On a server without a desktop, `start` refuses login startup and tells you to choose boot startup.

**Boot startup** still runs LeapMux as your account, never as `root` or `SYSTEM`. LeapMux needs the administrator rights only to create the service.

On **Windows**, both kinds need your account password, because Windows starts LeapMux while you are not at the keyboard. LeapMux reads the password without showing it and does not save it. Windows stores it for the service. Windows can also ask for administrator rights to grant your account the right to run the task or the service.

After you change your Windows password, the service cannot start. Run `leapmux <mode> enable` again, and type the new password when it asks.

### Service names

Each service name ends with a 16-digit hexadecimal ID. LeapMux derives the ID from the operating-system account, the mode, and the data directory. So each instance has its own service, and every command for one instance finds the same service. `start` and `enable` print the exact name in their preview, after `Definition:`.

| Platform | Login startup | Boot startup |
| --- | --- | --- |
| macOS | LaunchAgent `com.leapmux.<mode>.<id>`, in `~/Library/LaunchAgents/` | LaunchDaemon `com.leapmux.<mode>.<id>`, in `/Library/LaunchDaemons/` |
| Linux | User unit `leapmux-<mode>-<id>.service`, in `~/.config/systemd/user/` | System unit `leapmux-<mode>-<id>.service`, in `/etc/systemd/system/` |
| Windows | Scheduled task `leapmux-<mode>-<id>`, in the root folder of Task Scheduler | Windows service `leapmux-<mode>-<id>` |

Use the name to find the service in the tools of your operating system, such as `launchctl`, `systemctl`, Task Scheduler, or Services. Manage the service with `leapmux <mode>` commands, though: they keep the saved settings and the service in step.

## Check the status

```bash
leapmux solo status
```

`status` reports these facts:

- The process state: starting, running, stopping, stopped, failed, or unresponsive
- Whether the service matches the saved configuration
- Whether LeapMux is ready
- For a Worker, its registration and its connection to the Hub, separately
- The latest start: when it began and ended, its exit code, and the reason it failed
- The full path of the log of that start

`status` exits with `0` when the instance runs, `3` when it is stopped, and `1` when the command fails or the instance does not answer. A Worker that runs but has no connection to its Hub exits with `0`. So does a `dev` instance that waits for `/setup`.

`status --json` prints the same report as one JSON object for scripts. The [CLI Reference](/docs/reference/cli-reference/#status---json) lists its fields.

Each start writes its own log under `<data-dir>/.lifecycle/logs/`, and LeapMux keeps the logs of the last ten starts. When `start` fails or times out, it prints the end of the log and the full path of the log.

## Stop, restart, and remove the service

```bash
leapmux solo stop       # Stop now. The service starts LeapMux again at the next login or boot.
leapmux solo restart    # Stop, then start again.
leapmux solo disable    # Remove the service. Run stop first.
```

`disable` keeps the data directory: the databases, the Worker credentials, and the logs.

To remove LeapMux from a machine completely, run `stop`, then `disable`, then delete the data directory.

{{< callout type="warning" >}}
Deleting the data directory deletes the data of that instance permanently: a Hub's database and its encryption keys, or a Worker's credentials and its agent and terminal history. Deregister a standalone Worker before you delete its data directory, so that the Hub forgets it. See [Deregistering a Worker](/docs/admin/managing-workers/#deregistering-a-worker).
{{< /callout >}}

## Change the configuration of a service

The service saves these settings when `start`, `restart`, or `enable` creates or changes it:

- The runtime flags that you give to that command, such as `--listen` or `--log-level`
- The path of the configuration file, not its contents
- The `PATH` of the shell that runs the command, so that the service finds the same agent programs as your shell
- The path of an environment file, when you give one

To change a setting, use the place that holds it:

- **A setting in the configuration file:** edit the file, then run `restart`.
- **A saved flag:** run `restart` with the new flag, such as `leapmux hub restart --listen :4400`. A flag that you give replaces its saved value, and the other saved flags remain. For a flag that you can repeat, such as `--listen`, the values that you give replace the whole saved list.
- **Login or boot startup:** run `restart --startup=boot` or `restart --startup=login`.

When the configuration changes, `restart` shows the new configuration and asks for your consent again.

### Environment variables

The service does not inherit the environment of the shell that runs `start`. It keeps only `PATH`. A `LEAPMUX_*` variable that you export in your shell does not reach the service, so put such a setting in the configuration file instead.

A Worker starts each agent through your login shell by default (the `use_login_shell` setting). So the variables that your shell profile sets, such as an API key, still reach the agents.

To give the service other variables, write them to a YAML file and pass its path:

```yaml
# /home/alice/.config/leapmux/worker/environment.yaml
HTTPS_PROXY: "http://proxy.example.com:3128"
GIT_SSH_COMMAND: "ssh -i /home/alice/.ssh/leapmux"
```

```bash
leapmux worker restart --environment-file /home/alice/.config/leapmux/worker/environment.yaml
```

The file holds one YAML map, and each key and each value is a string. These rules apply:

- LeapMux reads the file each time the service starts, so an edit to the file applies at the next `restart`.
- A `PATH` in the file replaces the saved `PATH`.
- The file cannot set `HOME`, `USER`, `LOGNAME`, `USERNAME`, `USERPROFILE`, `HOMEDRIVE`, or `HOMEPATH`. LeapMux sets those for your account.
- On Windows, variable names ignore case, and each name may appear only once.
- To stop using the file, pass an empty value: `--environment-file=`.

The service definition holds the path of the file, not its values. Protect the file like any other secret when it holds one.

## Script the setup

A script cannot answer the prompts, so it passes the answers as flags:

```bash
leapmux worker start --startup=boot --yes
```

`--yes` accepts the preview. It does not choose between login and boot startup, so a first `start` needs `--startup` as well. Without a terminal, `start` and `enable` do not prompt. They fail and state the flags that they need.

`--timeout` sets how long a command waits. The defaults are 60 seconds for `start` and `restart`, 30 seconds for `stop`, and 3 seconds for `status`. The time that you spend at a prompt does not count. A timeout ends the wait, not the operation: LeapMux can still be starting or stopping. Run `status` before you try again.

## One instance for each data directory

The data directory identifies an instance. Give every command for one instance the same `--data-dir` or `--config`. A command with another data directory addresses another instance.

- A Hub and a Worker need separate data directories.
- A solo or dev instance owns its data directory and the `hub` and `worker` directories inside it.

## Move an instance to another directory

You can move a data directory, or restore it from a backup at another path. The first command at the new path adopts the directory, when the same operating-system account runs the same mode there. A solo or dev data directory moves as a whole, with its `hub` and `worker` directories. The service settings of the old path do not apply at the new path, so the next `start` asks the startup questions again.

1. If the instance runs as a service at the old path on this machine, stop it and remove its service there:

   ```bash
   leapmux hub stop --data-dir /srv/leapmux/old
   leapmux hub disable --data-dir /srv/leapmux/old
   ```

2. Move the directory, or restore the backup at the new path.
3. Start the instance at the new path:

   ```bash
   leapmux hub start --data-dir /srv/leapmux/new
   ```

{{< callout type="warning" >}}
Move a Worker's data directory; do not copy it and run both copies. Both copies hold the same Worker identity and the same credentials.
{{< /callout >}}

## When something goes wrong

[Troubleshooting](/docs/reference/troubleshooting/#background-service) lists the common failures and their fixes. Start with `leapmux <mode> status`, which states the reason for the last failure and the path of its log.
