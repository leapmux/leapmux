---
title: "Troubleshooting"
type: docs
weight: 25
---

This chapter is a problem-to-fix reference. Each entry gives the **symptom** you see, the **likely cause**, and the **fix** using real flags, labels, and messages. Entries are grouped by area. Use your browser's find (Ctrl/Cmd+F) to jump to a symptom.

> **Tip:** Most "nothing works" problems trace back to one of three things: the worker isn't online, you're bound to the wrong listen address, or first-run setup wasn't completed. Start there.

## Workers won't connect or stay offline

A worker (`leapmux worker`) dials *out* to the hub and holds a single bidirectional stream open. Its online/offline state is computed live from whether that stream is currently connected — there is no separate "approval" queue. For the full worker lifecycle, see [Managing Workers](/docs/19-managing-workers/).

### A worker exits immediately with "worker is unregistered"

**Symptom**
The worker process prints and exits:

```
worker is unregistered: pass --registration-key <key> from the hub UI
```

**Cause**
The worker has no saved credentials (no `state.json` in its data dir) and you started it without a `--registration-key`. Bare workers cannot self-register — registration is single-shot and gated entirely by possessing a valid key.

**Fix**
Mint a key in the hub UI: open the sidebar **Workers** section, click the **+** (Register worker) button, and copy the generated command. It already includes the right hub URL and key:

```bash
leapmux worker --hub https://hub.example.com --registration-key <key>
```

The key is only valid while the **Register worker** dialog stays open (5-minute TTL, auto-extended while open). If you close the dialog, the key is destroyed — reopen it to mint a fresh one. See [Managing Workers](/docs/19-managing-workers/) for minting keys via the UI, email, or the admin CLI.

### "registration rejected" / "registration key invalid or already consumed"

**Symptom**

```
registration rejected: ... registration key invalid or already consumed
```

**Cause**
Registration keys are **consumed atomically** on first use and live only 5 minutes. This error means the key was already used by another worker, was revoked, or expired. These are permanent errors — the worker does **not** retry them (unlike a transient network failure, which it does retry with backoff).

**Fix**
Mint a brand-new key from the **Register worker** dialog and run the worker with it. Never reuse a key across machines. If you mint keys via the CLI, check live keys with `leapmux admin worker reg-key list` and revoke stale ones with `leapmux admin worker reg-key revoke --id <id>` (see [Admin CLI](/docs/20-admin-cli/)).

### "worker is already registered" when you pass --registration-key

**Symptom**

```
worker is already registered; remove --registration-key or wipe local state to re-register
```

**Cause**
This worker already has saved credentials in `state.json`, and you passed `--registration-key` again. This guard exists so you don't burn a fresh key on a machine that's already configured.

**Fix**
Just run `leapmux worker --hub <url>` with **no** `--registration-key` — it reconnects with its saved credentials. If you genuinely want a clean re-registration, deregister the worker first (sidebar **Workers** row > **Deregister...**, or `leapmux admin worker deregister --id <id>`), delete the worker's `state.json` from its data dir, then register again with a new key.

### Worker process runs but never appears online

**Symptom**
The worker process is alive and logging reconnection attempts, but it never shows a connected status dot in the sidebar, or the **Worker** dropdown in the New agent/terminal dialogs shows **"No workers online"**.

**Cause and fix** — work through these in order:

| Cause | How to confirm | Fix |
|---|---|---|
| Wrong hub URL | Worker logs show repeated dial failures to the wrong host/port | Set `--hub` to the hub's reachable URL. Default is `http://127.0.0.1:4327`; behind a reverse proxy use the public `https://` URL. The worker accepts `http[s]://...`, `unix:<socket-path>` (Unix only), or `npipe:<pipe-name>` (Windows only). |
| Hub not actually listening on a reachable address | `curl http://hub-host:4327/` from the worker machine | Make sure the hub binds an interface the worker can reach. The hub default is `:4327` (all interfaces); `solo` mode defaults to `127.0.0.1:4327` (loopback only — unreachable from another machine). See [Running LeapMux](/docs/17-running-leapmux/). |
| Firewall / NAT between worker and hub | Network tools on the worker host | The worker connects **outbound** (NAT-friendly), so the worker needs outbound access to the hub's port, not an inbound port. Open egress to the hub. |
| Registered to a different hub / owner | `leapmux admin worker list` on the hub | A worker belongs to the hub and user that minted its key. Re-register against the correct hub if it's pointed at the wrong one. |
| Worker was deregistered server-side | Worker logs show `Unauthenticated` on reconnect, then exits | When the hub deletes a worker, the worker clears its local state and exits on next connect. Register it again from the UI. |

> **Note:** A worker reconnects automatically with exponential backoff (1s up to 180s between attempts). If you just restarted the hub, give the worker up to ~3 minutes to reconnect, or restart the worker to retry immediately.

### Worker is "online" to the hub but the sidebar shows it disconnected

**Symptom**
The worker is registered and online, but its sidebar status dot is grey/disconnected and you can't open content on it.

**Cause**
The sidebar status reflects whether **your browser** has a live end-to-end-encrypted channel to that worker — which is distinct from the hub's worker-online flag. If you haven't opened anything on the worker yet (or the channel was torn down), the frontend has no open channel.

**Fix**
Open an agent or terminal on the worker, or click the refresh button in the **Worker** selector. The channel opens on demand. If the worker is genuinely offline at the hub, opening a channel fails with **"worker is offline"** (`CodeUnavailable`) — bring the worker process back up.

## "Worker public key changed" / handshake rejected (TOFU pin mismatch)

LeapMux pins each worker's composite static key on first connection (trust-on-first-use). A later handshake whose key doesn't match is rejected, so a compromised hub cannot silently swap a worker underneath you. See [Security & Threat Model](/docs/23-security-and-threat-model/).

### The browser shows the "Worker public key changed" dialog

**Symptom**
A dialog titled **"Worker public key changed"** appears, stating the public key for the worker has changed since the last connection, and showing an **Expected:** and **Actual:** 4-word fingerprint. The agent/terminal won't open until you choose.

**Cause**
The worker's pinned key (stored in your browser's local storage for 1 year) no longer matches the key the worker is now presenting. Legitimate causes: the worker's `state.json` was deleted/recreated (which regenerates its keypair), the data dir moved, or you reinstalled. The malicious cause this protects against: someone substituted a different worker.

**Fix**
- If you expected this change (you wiped the worker's state, reinstalled, etc.), verify the **Actual** fingerprint matches the new worker out-of-band, then click **Accept**. The pin is overwritten with the new key.
- If you did **not** expect it, click **Reject** (closing the dialog also counts as Reject) and investigate before reconnecting. The channel is not opened.

> **Warning:** Accepting overwrites the pinned key permanently. Only accept after confirming the new fingerprint really belongs to your worker.

### A `leapmux remote` or worker-to-worker connection aborts with "key mismatch"

**Symptom**
A CLI or cross-worker connection fails with:

```
worker <id> key mismatch — <hint>
```

**Cause**
Non-browser clients also pin worker keys TOFU, but they cannot pop a dialog, so a mismatch aborts the connection. The hint tells you exactly how to clear the pin. There are two separate pin stores:

- The **`leapmux remote` CLI** keeps pins per hub host.
- A **worker** keeps cross-worker pins for sibling workers.

**Fix**
Clear the stale pin so the next connect re-pins the new key, then reconnect.

For the remote CLI:

```bash
leapmux remote worker pins list                       # see all pinned workers
leapmux remote worker pins remove --worker-id <id>    # clear one pin
```

For a worker's cross-worker pins (runs entirely against local files — no worker process needs to be running):

```bash
leapmux worker cross-worker-pins list                              # see all pins (JSON)
leapmux worker cross-worker-pins remove --target-worker-id <id>    # clear one pin
```

There is no UI for clearing key pins — pin removal is CLI-only. See [Remote Control CLI](/docs/16-remote-control-cli/) and [Managing Workers](/docs/19-managing-workers/).

## Ports, listen address, and reaching the UI

### Port 4327 already in use

**Symptom**
The hub/solo/dev process fails to start because something is already bound to `4327` (the default TCP port for `hub`, `dev`, and `solo`).

**Cause**
Another process — often a previously launched LeapMux instance — already holds the port.

**Fix**
Either stop the conflicting process or change the listen address with `--listen`:

```bash
# Bind a different port
leapmux hub --listen :4400

# Solo on a different loopback port
leapmux solo --listen 127.0.0.1:4400
```

In Docker, the container always listens on `4327` internally; remap the host side of the port publish instead:

```bash
docker run -p 4400:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

See [Running LeapMux](/docs/17-running-leapmux/) and [Configuration](/docs/18-configuration/).

### Can't reach the UI / connection refused from another machine

**Symptom**
The browser can't connect to the hub at all (connection refused / timeout) from a different host than the one running LeapMux.

**Cause**
`solo` mode binds **`127.0.0.1:4327`** (loopback only) by default — it is unreachable from other machines on purpose, because solo mode auto-authenticates every request as the admin with no credentials.

**Fix**
- For local single-user use, browse to `http://127.0.0.1:4327` on the same machine.
- To serve other machines, do **not** simply rebind solo to all interfaces. Either:
  - Run `leapmux hub` (or `dev`), which use real authentication and bind `:4327` (all interfaces) by default; or
  - If you must expose solo, restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel). Solo emits this warning when it binds a non-loopback address:

    > solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, so anyone who can reach this port has full admin access without credentials. Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication.

See [Security & Threat Model](/docs/23-security-and-threat-model/) and [Running LeapMux](/docs/17-running-leapmux/).

### Blank page or the UI won't load in development

**Symptom**
The page is blank, or assets fail to load, when you're running LeapMux from source.

**Cause**
In a development setup the hub reverse-proxies to a separate frontend dev server. If that dev server isn't running (or `--dev-frontend` points at the wrong URL), there's nothing to serve the UI.

**Fix**
- Use the provided dev task that starts both processes together: `task dev` (or `task dev-solo`). See [Installation](/docs/03-installation/) and [Running LeapMux](/docs/17-running-leapmux/).
- If you wire it up manually, make sure the frontend dev server is up and `--dev-frontend` points at it.
- A standalone release binary or Docker image already bundles the built frontend and does **not** need `--dev-frontend`.

### Cookies/login don't stick behind a reverse proxy

**Symptom**
You're fronting the hub with TLS via a reverse proxy, but login won't persist or the UI behaves oddly with redirects.

**Cause**
The hub does not terminate TLS itself, and it needs to know its external URL and that it should issue secure cookies. Without that, the derived base URL and cookie scheme can be wrong.

**Fix**
Set both:

```yaml
# hub.yaml
public_url: https://hub.example.com   # scheme + host only; no path/query
secure_cookies: true
```

`public_url` must be scheme + host only — **sub-path mounting** (e.g. `https://example.com/leapmux`) is explicitly rejected. `secure_cookies` has no CLI flag; set it in the config file (or via `LEAPMUX_HUB_SECURE_COOKIES`). See [Configuration](/docs/18-configuration/) and [Running LeapMux](/docs/17-running-leapmux/).

## Can't log in or sign up

For the full account model, see [Accounts & Authentication](/docs/05-accounts-and-authentication/).

### Redirected to /setup, or you can't log in because no account exists

**Symptom**
Visiting the hub redirects you to **/setup** with the heading **"Welcome to LeapMux"**, or you can't log in because no account exists.

**Cause**
This is a fresh multi-user hub with no users yet. The first person to register at **/setup** becomes the admin. Until that's done, there's nothing to log into.

**Fix**
Complete the **/setup** form (Username, Display Name, Email, Password). The first user is always created as an admin. Afterward, the **/setup** route redirects to **/login**.

### "Sign-up disabled" when trying to create an account

**Symptom**
Visiting **/signup** shows a page titled **"Sign-up disabled"** with the message **"New account registration is not currently available."**

**Cause**
Public sign-up is gated by `--signup-enabled`, which defaults to **false**. The first-admin **/setup** flow still works even when sign-up is disabled; only public self-registration is blocked.

**Fix**
- To allow self-service sign-up, start the hub with `--signup-enabled` (or set `signup_enabled: true` in `hub.yaml`).
- Otherwise have an admin create the account with `leapmux admin user create` (see [Admin CLI](/docs/20-admin-cli/)).

### "invalid credentials" on login

**Symptom**
Login fails with **"invalid credentials"** even when you think the username/password is right.

**Cause**
For security, the hub returns the identical **"invalid credentials"** error for both an unknown username and a wrong password — there's no way to tell which from the message. Usernames are lowercase slugs; passwords are 8-128 characters.

**Fix**
Double-check the exact username (lowercase, hyphens, no spaces). If you've lost the password, have an admin reset it with `leapmux admin user reset-password` (see [Admin CLI](/docs/20-admin-cli/)). Note: solo mode has no login at all — if you expected a login page in solo mode, you won't get one; it auto-authenticates.

### Blocked everywhere with "email verification required"

**Symptom**
After signing up you're stuck — almost every action returns **"email verification required"**, and you land on the **"Verify your email"** page.

**Cause**
The hub runs with `--email-verification-required`, so non-admin users with an unverified email may only verify, log out, or change their email until they verify. Verification uses a 6-character code (display form `XXX-XXX`) that expires in 30 minutes with a 5-attempt budget.

**Fix**
Enter the code from the verification email, or click the link in it. If you didn't receive it, use **"Resend code"** (60-second cooldown between resends). Email features require SMTP to be configured on the hub — if the operator hasn't set `--smtp-host`, verification emails can't be sent at all (and the hub would have refused to start with `email_verification_required` set without SMTP). See [Configuration](/docs/18-configuration/).

### OAuth sign-in fails or the provider isn't shown

**Symptom**
OAuth buttons don't appear on the login page, or clicking one ends in an error such as **"OAuth provider did not return an email address; ensure the 'email' scope is granted"** or **"sign-up is disabled; no existing account linked to this identity"**.

**Cause and fix:**

| Symptom | Cause | Fix |
|---|---|---|
| No OAuth buttons at all | No enabled OAuth provider configured | Add one with `leapmux admin oauth-provider add` (see [Authentication Providers](/docs/21-authentication-providers/)). |
| "did not return an email address" | The provider config is missing the email scope | Ensure the `email`/`user:email` scope is granted; reconfigure the provider's `--scopes`. |
| Stuck on "Complete Sign Up" then rejected | New OAuth user but sign-up is disabled | Enable `--signup-enabled`, or link the OAuth identity to an existing account by signing in and verifying the matching email. |
| "This signup link is invalid or has expired." on the **Complete Sign Up** page | The pending OAuth signup expired (5-minute window) or the `?token=` link was reused/already completed | Start the OAuth sign-in over from the login page to mint a fresh pending signup, then pick a username promptly. (A blank **Complete Sign Up** page that says **"Missing signup token."** means you opened the URL without its `?token=`; restart from the login page.) |
| OAuth user logs in but can't unlink | It's their only login method | Set a password first in the **Profile** dialog, then unlink. |

Operators configuring providers should also confirm the OIDC issuer is reachable and `--public-url` is set so redirect/login URLs are built correctly. See [Authentication Providers](/docs/21-authentication-providers/).

## Agents won't start

For how agents work, see [Coding Agents](/docs/09-coding-agents/).

### The agent provider isn't in the picker / "No agents available"

**Symptom**
The **Agent Provider** selector shows **"No agents available"**, or the provider you want (e.g. Codex, Gemini, Pi) is missing from the list.

**Cause**
A provider only appears if **its CLI binary is detected on the worker**. The worker probes the shell for each provider's binary (`claude`, `codex`, `gemini`, `cursor-agent`, `copilot`, `kilo`, `opencode`, `goose`, `pi`) and lists only the ones it finds on `PATH`.

**Fix**
Install the agent's own CLI on the **worker** machine (not where the browser runs) and make sure it's on the worker's `PATH`. Then click the refresh button (**"Refresh available providers"**) in the selector, or reopen the dialog. Note: Pi only ever shows when the `pi` binary is actually detected.

### The agent shows "failed to start"

**Symptom**
The chat pane shows a centered error titled **"&lt;Provider&gt; failed to start"** (e.g. "Claude Code failed to start") with an error message from the worker.

**Cause**
The agent subprocess couldn't be launched or didn't complete its startup handshake. Common reasons: the CLI binary isn't actually runnable on the worker (wrong version, broken install, missing auth), the working directory is invalid, or startup exceeded the timeout (`--agent-startup-timeout-seconds`, default **300**).

**Fix**
- Read the error text shown in the pane — it comes straight from the worker and usually names the cause.
- On the worker, run the agent's CLI directly (e.g. `claude --version`) to confirm it works and is authenticated.
- If startup is legitimately slow, raise the timeout: `leapmux worker --agent-startup-timeout-seconds 600` (or the equivalent key in config). This flag exists on the worker and on `hub`/`solo`/`dev` modes. See [Configuration](/docs/18-configuration/).
- Reopen the agent once the underlying CLI issue is fixed.

### A model, effort, or permission-mode change seems to "reset" the agent

**Symptom**
Changing the model or effort in the in-chat settings dropdown restarts the agent.

**Cause**
This is expected. Model and effort changes require an agent restart; the change is applied optimistically and rolled back on failure. (Only Claude Code's permission-mode change is live and does not restart.)

**Fix**
No action needed — wait for the agent to come back up. If it fails to restart, you'll see the "failed to start" error above; resolve that.

## Docker

For the full Docker setup, see [Running LeapMux](/docs/17-running-leapmux/) and [Installation](/docs/03-installation/).

### Container exits immediately

**Symptom**
The container starts and dies right away, printing:

```
error: LEAPMUX_MODE must be one of: hub, worker, dev, solo
```

**Cause**
The image's supervisor requires the `LEAPMUX_MODE` environment variable to choose a run mode. Without it (or with an invalid value) it exits 1.

**Fix**
Pass a valid mode:

```bash
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

> **Note:** Use `LEAPMUX_MODE=dev` (not `solo`) for an all-in-one container reachable from your host. `solo` defaults to loopback-only inside the container, so its port isn't reachable from outside unless you override `listen` to `:4327` in `/data/solo/solo.yaml`. `dev` binds all interfaces by default.

### A worker container can't connect

**Symptom**
A `LEAPMUX_MODE=worker` container fails with **"worker is unregistered..."** or never connects.

**Cause**
The supervisor invokes `leapmux worker -config /data/worker/worker.yaml` and passes **no** `--hub` or `--registration-key` flags. A worker container must get its hub URL and (first-run) key from config or env vars.

**Fix**
Supply them via `LEAPMUX_WORKER_*` env vars or the worker YAML:

```bash
docker run \
  -e LEAPMUX_MODE=worker \
  -e LEAPMUX_WORKER_HUB=https://hub.example.com \
  -e LEAPMUX_WORKER_REGISTRATION_KEY=<key> \
  -v leapmux-worker-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

The registration key is consumed on first run; once registered, the worker reconnects with its saved state and you can drop `LEAPMUX_WORKER_REGISTRATION_KEY`. See [Managing Workers](/docs/19-managing-workers/).

### Non-SQLite storage configured via env vars is ignored

**Symptom**
You set something like `LEAPMUX_HUB_STORAGE_TYPE=postgres` but the hub still uses SQLite.

**Cause**
The env-var layer only strips the prefix and lowercases the rest — it does **not** convert underscores into the dotted keys that nested storage settings need (`storage.type`, `storage.postgres.dsn`, ...). So storage env vars don't map cleanly.

**Fix**
Configure storage in the YAML config file (`/data/hub/hub.yaml` in Docker) or via the dedicated CLI flags (`--storage-type`, `--storage-postgres-dsn`, ...):

```yaml
# hub.yaml
storage:
  type: postgres
  postgres:
    dsn: postgres://user:password@db:5432/leapmux?sslmode=disable
```

See [Configuration](/docs/18-configuration/).

## Data and persistence

### Data disappears between restarts

**Symptom**
Workspaces, accounts, agents, or terminals vanish after restarting the container or process.

**Cause**
The hub's state lives in its data dir (SQLite DB at `<data_dir>/hub.db`, encryption key ring at `<data_dir>/encryption.key`); the worker keeps `state.json` and `worker.db`. If the data dir isn't persisted, every restart starts fresh.

**Fix**
- **Docker:** mount a volume at `/data`. Without `-v ...:/data`, state is lost when the container is removed. State lands under `/data/<mode>/`.
- **CLI:** point `--data-dir` at a stable directory, or rely on the default under `~/.config/leapmux/<mode>/`. Don't run from a temp directory whose relative `data-dir` resolves somewhere transient.
- **Upgrades:** pull a newer image/binary and recreate against the **same** data dir/volume. Migrations run automatically on startup; no manual migration command is needed.

> **Warning:** Back up the hub's `encryption.key` together with its database. The key ring encrypts stored secrets (OAuth tokens, etc.) at rest — losing it makes those secrets unrecoverable. See [Encryption & Data](/docs/22-encryption-and-data/).

### Encryption-key errors after restoring a backup

**Symptom**
After restoring the database, OAuth or token-backed features fail to decrypt.

**Cause**
You restored `hub.db` but not the matching `encryption.key`, or restored a key from a different point in time. The two must be in sync.

**Fix**
Restore the `encryption.key` from the same backup as the database. For planned key rotation, use `leapmux admin encryption-key rotate` then `reencrypt` — and follow the on-screen instruction to restart the hub between the two. See [Encryption & Data](/docs/22-encryption-and-data/).

## Sharing and collaboration

### A shared workspace appears empty

**Symptom**
Someone shared a workspace with you. You can see the tabs and layout, but agent chats, terminals, and files appear empty or won't load.

**Cause**
Sharing grants **routing permission via the hub only** — it does not hand over content. The hub never has plaintext. To read agent/terminal/file content you must open your **own** end-to-end-encrypted channel to the worker. Also, the worker that hosts the content may simply be offline.

**Fix**
- Open the agent or terminal tab to trigger your own encrypted channel to the worker. Layout geometry and titles come from the hub (so the tabs show), but content streams only over your channel.
- Confirm the hosting worker is online; if it isn't, opening a channel fails with **"worker is offline"**. Bring the worker back up.
- The first time you connect to that worker you may get the TOFU first-use pin (silent) or, if its key changed, the **"Worker public key changed"** dialog — handle it as above.

See [Collaboration & Presence](/docs/13-collaboration-and-presence/) and [Workspaces](/docs/07-workspaces/).

## Terminals and `leapmux remote`

For terminal behavior, see [Terminals](/docs/11-terminals/); for the CLI, see [Remote Control CLI](/docs/16-remote-control-cli/).

### `leapmux remote` inside a terminal/agent says it can't find the hub

**Symptom**
Running `leapmux remote ...` from inside a LeapMux terminal or agent fails with something like:

```
no --hub flag or LEAPMUX_HUB / LEAPMUX_REMOTE_SOCK env var; run `leapmux remote auth login --hub <url>` or invoke from inside an agent
```

**Cause**
`leapmux remote` resolves its target from `LEAPMUX_REMOTE_SOCK` (+ `LEAPMUX_REMOTE_TOKEN`) when spawned inside a LeapMux terminal/agent, or from `--hub`/`LEAPMUX_HUB` plus saved login credentials otherwise. Those `LEAPMUX_REMOTE_*` env vars are injected automatically for every terminal and agent spawn — but if they're missing, the command can't locate the hub.

**Fix**
- **Inside a LeapMux terminal/agent:** the `LEAPMUX_REMOTE_*` vars should already be present. Confirm with `env | grep LEAPMUX_REMOTE`. If they're absent, you likely spawned a sub-shell that stripped the environment, or the remote-IPC server wasn't available at spawn — open a fresh terminal tab. (There is **no** "remote-enabled" checkbox to toggle; it's wired up automatically.)
- **From your own shell (not inside LeapMux):** authenticate first:

  ```bash
  leapmux remote auth login --hub https://hub.example.com
  ```

  For headless/SSH/container shells where a browser can't open, add `--device-code` to use the device-code flow. Check your identity with `leapmux remote whoami` and `leapmux remote auth status`.

### A terminal shows "[Terminal process exited ... Press Enter to restart]"

**Symptom**
The terminal stops accepting input and shows a notice like:

```
[Terminal process exited (0) - Press Enter to restart]
```

or `[Worker disconnected - Press Enter to restart]`.

**Cause**
The shell process exited (you typed `exit`, the shell crashed, or the worker connection dropped). The tab persists so its scrollback isn't lost.

**Fix**
Press **Enter** — that's the only key an exited terminal acts on; it restarts the shell in the same working directory. The new prompt appears below the preserved buffer. A faded/struck-through tab label means the terminal is DISCONNECTED or EXITED. If the worker itself is down, bring it back online first, then press Enter.

### The expected shell isn't offered in "New terminal"

**Symptom**
The **Shell** dropdown in the **New terminal** dialog doesn't list the shell you want, or shows **"No shells available"**.

**Cause**
The shell list is probed **on the worker**. The worker resolves a default shell (from `LEAPMUX_DEFAULT_SHELL`, then `SHELL`, then platform detection) and probes a fixed known-shells set (`sh`, `bash`, `zsh`, `fish`, `pwsh`, `powershell`) via `PATH`. Only shells found on the worker appear.

**Fix**
Install the shell on the worker and ensure it's on the worker's `PATH`, then reopen the dialog (the list is per-worker and refetched when you change worker). To force a specific default, set `LEAPMUX_DEFAULT_SHELL` in the worker's environment (a bare name like `zsh` or an absolute path).

## Still stuck?

If none of these match:

- Restart the worker with `--log-level debug` to get verbose connection logs.
- Verify versions match expectations with `leapmux version` and the worker info shown in the sidebar **Workers** row context menu.
- Check the [FAQ](/docs/26-faq/) for common questions, the [Glossary](/docs/27-glossary/) for terminology, and the [CLI Reference](/docs/24-cli-reference/) for exact flags.
- For security-sensitive symptoms (unexpected key changes, unknown workers), read [Security & Threat Model](/docs/23-security-and-threat-model/) before accepting anything.
- Still blocked, or think you've hit a bug? [Open a GitHub issue](https://github.com/leapmux/leapmux/issues). Include your `leapmux version`, the run mode, and any relevant `--log-level debug` output so the maintainers can follow up.
