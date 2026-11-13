---
title: "Troubleshooting"
description: "A symptom-to-fix reference for LeapMux: common problems with their likely cause and the exact fix, grouped by area, with the real flags and settings."
type: docs
weight: 2
---

This chapter is a problem-to-fix reference. Each entry gives the **symptom** you see, the **likely cause**, and the **fix** with the real flags and settings. Entries are grouped by area. Use your browser's find (Ctrl/Cmd+F) to jump to a symptom.

{{< callout >}}
Most "nothing works" problems trace back to one of three things: the Worker isn't online, you're bound to the wrong listen address, or nobody completed first-run setup. Start there.
{{< /callout >}}

## Background service

These entries cover an instance that `leapmux <mode> start` runs as a service. Start with `leapmux <mode> status`, with the same `--data-dir` or `--config` as the instance. It states the process state, the reason for the last failure, and the path of its log. For how the service works, see [Starting and Stopping](/docs/admin/starting-and-stopping/).

### `start` reports that the startup wait expired

**Symptom**
`start` or `restart` prints a line that begins with `Startup wait expired`, and says that startup may continue.

**Cause**
LeapMux did not become ready within `--timeout`, which is 60 seconds by default. The service keeps starting. A first start that migrates a large database, or an external database that answers slowly, can take longer.

**Fix**
Run `status` until it reports that the instance runs or failed. A failed start states its reason and the path of its log. For a slow first start, pass a longer `--timeout`.

### Login startup fails on a Linux server

**Symptom**
`start` fails, and the error tells you to choose boot startup for headless use, or says that login startup requires an active desktop session.

**Cause**
On Linux, login startup follows the graphical desktop session. A session over SSH is not one.

**Fix**
Choose boot startup, which runs while nobody is logged in:

```bash
leapmux hub start --startup=boot
```

Boot startup asks for `sudo` to create the service. LeapMux still runs as your account.

### A script fails because setup is noninteractive

**Symptom**
`start` or `enable` fails in a script, and the error says that noninteractive setup requires `--startup=login or --startup=boot and --yes`.

**Cause**
The first `start` asks when LeapMux should start, then asks for consent. A script has no terminal to answer.

**Fix**
Pass both answers as flags: `leapmux worker start --startup=boot --yes`.

### The service does not start after a Windows password change

**Symptom**
`start` or `status` reports that Windows rejected the account's password because it is wrong or has expired.

**Cause**
Windows keeps a copy of your account password to start the service, and that copy is out of date.

**Fix**
Run `leapmux <mode> enable`, and type the new password when it asks.

### `status` reports that the service definition is missing or differs

**Symptom**
`status` reports one of these:

```text
The native definition is missing. Run enable to repair the saved configuration.
The native definition differs from the saved configuration. Run enable to repair it.
```

**Cause**
Something outside LeapMux removed or changed the service entry, such as a cleanup tool or a manual edit.

**Fix**
Run `leapmux <mode> enable`. It writes the entry again from the saved configuration.

### `stop` reports that the shutdown could not be confirmed

**Symptom**
`stop` prints `Shutdown could not be confirmed.`

**Cause**
The instance did not finish its shutdown within `--timeout`, which is 3 minutes by default. It is still closing its databases and stopping its agents.

**Fix**
Run `status` until it reports that the instance stopped. Pass a longer `--timeout` to wait longer.

### A command refuses because the instance runs

**Symptom**
`start` says that the instance is already running. `disable` or `worker deregister` says that the instance or the Worker is running.

**Cause**
`start` starts only a stopped instance. `disable` and `worker deregister` change what a running instance depends on.

**Fix**
Use `restart` to restart a running instance. Run `stop` before `disable` or `worker deregister`.

### A command says that the data directory belongs to another instance

**Symptom**
The error says that the data directory belongs to a `hub`, `worker`, `solo`, or `dev` instance, and tells you to choose a separate data directory.

**Cause**
A data directory belongs to one instance: one mode and one operating-system account. These cause the conflict:

- Two modes use the same directory, such as a Hub and a Worker, or a Hub and the `hub` directory inside a solo data directory.
- Another account uses the directory.
- A `hub` or `worker` directory moved out of its solo or dev data directory, without the rest of it.

A directory that moved as a whole, or a backup restored at another path, is not a conflict: the first command at the new path adopts it. See [Move an instance to another directory](/docs/admin/starting-and-stopping/#move-an-instance-to-another-directory).

**Fix**
Give each instance its own data directory, and pass that instance's `--data-dir` or `--config` to every command. Run each instance as the account that created it.

## Workers won't connect or stay offline

A Worker dials *out* to the Hub and holds a single bidirectional stream open. Its online/offline state is computed live from whether that stream is currently connected — there is no separate "approval" queue. For the full Worker lifecycle, see [Managing Workers](/docs/admin/managing-workers/).

### A Worker command reports that the Worker is unregistered

**Symptom**
`leapmux worker run`, `start`, or `restart` fails with ``worker is unregistered: run `leapmux worker register` first``.

**Cause**
The data directory holds no Worker credentials. Only `leapmux worker register` creates them. A command with another `--data-dir` or `--config` than the registration reads another data directory.

**Fix**
[Register the Worker](/docs/admin/managing-workers/#register-a-worker), or pass the `--data-dir` or `--config` that you registered it with.

### The Hub rejects the registration because the key is invalid or already used

**Symptom**
`leapmux worker register` fails at once, and the error says that the Hub rejected the registration because the key is invalid or already consumed.

**Cause**
Registration keys are **consumed atomically** on first use and live only 5 minutes. This error means the key was already used by another Worker, was revoked, or expired. These are permanent errors — the command does **not** retry them (unlike a transient network failure, which it does retry with backoff).

**Fix**
Generate a new key in the **Register worker** dialog. The refused attempt stays in `pending-registration.json` and blocks a new key, so [abandon it](/docs/admin/managing-workers/#abandon-an-attempt) first. Then run `worker register` with the new key. Never reuse a key across machines. If you mint keys via the CLI, check live keys with `leapmux control admin worker reg-key list` and revoke stale ones with `leapmux control admin worker reg-key revoke --id <id>` (see [Control CLI](/docs/using/control-cli/)).

### `worker register` reports a pending registration

**Symptom**
`leapmux worker register` fails, and the error says that a pending registration for a Hub already exists.

**Cause**
An earlier `worker register` with another key or another Hub did not finish, and the Hub may have accepted it.

**Fix**
If the earlier command could have succeeded, run that same command again to complete it. Otherwise, [abandon the attempt](/docs/admin/managing-workers/#abandon-an-attempt), then run the new command.

### `worker register` reports that the Worker is already registered

**Symptom**
`leapmux worker register` fails, and the error says that this worker is already registered with a Hub.

**Cause**
The data directory already holds credentials. The command refuses, so that it does not use up a fresh key on a machine that is already set up.

**Fix**
Start the Worker with `leapmux worker start`. To register it with another Hub or as a new Worker, see [Register a Worker again](/docs/admin/managing-workers/#register-a-worker-again).

### A Worker command refuses `--hub` or `--registration-key`

**Symptom**
The error says that `--hub` or `--registration-key` belongs to `leapmux worker register`.

**Cause**
Only `worker register` takes these flags. Every other Worker command connects to the Hub URL that the registration saved.

**Fix**
Remove the flag. To connect the Worker to another Hub, [register it again](/docs/admin/managing-workers/#register-a-worker-again).

### Worker process runs but never appears online

**Symptom**
The Worker process is alive and logging reconnection attempts, but it never shows a connected status dot in the sidebar, or the **Worker** dropdown in the New agent/terminal dialogs shows **"No workers online"**.

**Cause and fix** — work through these in order:

| Cause | How to confirm | Fix |
|---|---|---|
| Wrong Hub URL | Worker logs show repeated dial failures to the wrong host/port. `leapmux worker status` shows the Hub URL that the Worker uses. | The Worker connects to the URL that `worker register` saved. To change it, [register the Worker again](/docs/admin/managing-workers/#register-a-worker-again) with the right `--hub`. Behind a reverse proxy, use the public `https://` URL. The Worker accepts `http[s]://...`, `unix:<socket-path>` (Unix only), or `npipe:<pipe-name>` (Windows only). |
| Hub not actually listening on a reachable address | `curl http://hub-host:4327/` from the Worker machine | Make sure the Hub binds an interface the Worker can reach. The Hub default is `:4327` (all interfaces); solo mode defaults to `127.0.0.1:4327` (loopback only — unreachable from another machine). See [Running LeapMux](/docs/admin/running-leapmux/). |
| Firewall / NAT between Worker and Hub | Network tools on the Worker host | The Worker connects **outbound** (NAT-friendly), so the Worker needs outbound access to the Hub's port, not an inbound port. Open egress to the Hub. |
| Registered to a different Hub / owner | `leapmux control admin worker list` on the Hub | A Worker belongs to the Hub and user that minted its key. The Worker keeps its identity, because a Hub that does not know its token makes the Worker retry instead of deleting its state. If the Worker was registered with another Hub or by another user, [register it again](/docs/admin/managing-workers/#register-a-worker-again) with a key that the right user minted on the right Hub. |
| Worker was deregistered or deleted | The Worker logs that it received a deregistration notification, or that the Hub reports it was deleted. A standalone Worker then exits, and an auto-registered Worker stops reconnecting. | A standalone Worker also clears its local state. Register it again with a fresh registration key from the UI. The launcher of an auto-registered Worker registers a fresh Worker at the next launch. |

{{< callout type="info" >}}
A Worker reconnects automatically with exponential backoff: 1 s, doubling to 180 s, with ±20% jitter, so one wait reaches at most about 3.6 minutes. If you just restarted the Hub, give the Worker up to that long to reconnect, or restart the Worker to retry immediately.
{{< /callout >}}

### A Worker cannot register or stay connected through a reverse proxy

**Symptom**
A Worker registers but never appears online, or it keeps reconnecting, and the Hub answers the web app normally.

**Cause and fix**
The proxy must forward the `/ws/worker` WebSocket upgrade and keep the connection open. [Reverse Proxy](/docs/admin/reverse-proxy/#what-the-proxy-must-carry) gives the requirements and verified configurations for nginx, Caddy, Traefik, HAProxy, and Apache httpd. These messages were recorded with upgrade forwarding disabled and with a five-second inactivity timeout:

| The Worker logs | Cause | Fix |
|---|---|---|
| `the hub refused the worker stream: HTTP 426 Upgrade Required: WebSocket protocol violation: Connection header "" does not contain Upgrade` | The proxy does not forward the WebSocket upgrade for `/ws/worker`. | Forward WebSocket upgrades on `/ws/`, including the Worker's route. |
| `disconnected from hub, reconnecting...` with `receive: failed to get reader: failed to read frame header: EOF`, at a fixed idle interval | The proxy's inactivity timeout is shorter than the ten-second ping interval. | Use an inactivity timeout of one minute or longer. |

### A Worker keeps retrying, and each attempt receives HTTP 401 or 403

**Symptom**
The Worker never comes online, and each reconnection attempt logs `HTTP 401`, or `HTTP 403` when a proxy refuses it.

**Cause** — either the Hub has no record of the Worker's token, or something in front of the Hub refuses it:

- The Worker was deleted more than 7 days ago, so the Hub hard-deleted its row.
- The Worker was registered with another Hub.
- The Worker registered after the backup that a restored Hub database came from.
- A proxy in front of the Hub answers 401 or 403 by itself.

The Worker keeps its `state.json` for these errors, because a proxy or a Hub restored from an old backup must not destroy its identity.

After five `HTTP 401` refusals in a row, the Worker logs one error that states the remedy, and it keeps retrying:

```text
the hub does not know this worker's token. Check the saved Hub endpoint and any proxy in front of the Hub. If the hub deleted this worker more than 7 days ago, stop it, delete state.json, and register it again.
```

**Fix**
`leapmux worker status` shows the Hub URL that the Worker uses. Check that URL and the proxy. If neither is wrong, the Hub no longer knows this Worker, so `worker deregister` cannot complete. Stop the Worker, delete its `state.json`, and register it again.

### A Worker stops because the Hub refused its public keys

**Symptom**
The Worker logs that the Hub refused its public keys and stops. A standalone Worker exits, and its error tells you to run `leapmux worker deregister`, then `leapmux worker register` with a new registration key.

**Cause**
Its saved keys differ from the keys it registered with. The Hub pins the keys a Worker registers with and refuses a hello that presents others, so this cannot be fixed by reconnecting.

**Fix**
Run `leapmux worker stop`, then `leapmux worker deregister`, then [register the Worker again](/docs/admin/managing-workers/#register-a-worker-again) with a new key. `stop` matters when the Worker runs as a service, and it does nothing when the Worker already exited. For an auto-registered Worker (solo mode, dev mode, or the desktop app), restart the app instead: at launch, the launcher finds that the saved keys differ from the pinned keys, retires the old Worker, and registers a fresh one. The fresh Worker has a new Worker ID, and the old Worker's tabs do not move to it.

### A registered Worker exits at start because its keys are missing or unusable

**Symptom**
The error says that the Worker state is registered but its keypair is missing or unusable, and tells you to run `leapmux worker deregister`, then `leapmux worker register` with a new registration key.

**Cause**
A registered `state.json` with a missing or broken keypair.

**Fix**
The same as the entry above.

### Worker is "online" to the Hub but the sidebar shows it disconnected

**Symptom**
The Worker is registered and online, but its sidebar status dot is grey/disconnected and you can't open content on it.

**Cause**
The sidebar status reflects whether **your browser** has a live end-to-end-encrypted channel to that Worker — which is distinct from the Hub's Worker-online flag. If you haven't opened anything on the Worker yet (or the channel was torn down), the Frontend has no open channel.

**Fix**
Open an agent or terminal on the Worker — that's what opens the channel; it opens on demand. The refresh button in the **Worker** selector only re-fetches the Worker list/status, which can clear a stale "offline" display, but it does not open the content channel by itself. If the Worker is genuinely offline at the Hub, opening a channel fails with the status `CodeUnavailable` and an error that says the Worker is offline — bring the Worker process back up.

## "Worker public key changed" / handshake rejected (TOFU pin mismatch)

LeapMux remembers each Worker's public key on first connection (trust-on-first-use) and warns if it later changes. A mismatch detects a Hub substituting a Worker key in a channel your browser opens. It does not stop a Hub opening its own channel or serving modified Frontend code. See [Security & Threat Model](/docs/admin/security/).

### The browser shows the "Worker public key changed" dialog

**Symptom**
A dialog titled **"Worker public key changed"** appears, stating that the Worker's public key differs from the key pinned for it at an earlier connection, and showing an **Expected:** and **Actual:** 4-word fingerprint. The agent/terminal won't open until you choose.

**Cause**
A Worker cannot change its keys under the same Worker ID, because the Hub pins the keys it registered with. So one of the two keys did not come from this Worker: either someone substituted a different Worker, or the Hub handed over a key that is not the pinned one.

**Fix**
- Click **Reject** (closing the dialog also counts as Reject) and investigate before reconnecting. The channel is not opened.
- Click **Accept** only after you confirm, out of band, that the **Actual** fingerprint matches the one the Worker logs when it starts.

A Worker whose `state.json` was wiped registers with a new Worker ID, so it shows no dialog at all.

{{< callout type="warning" >}}
Accepting overwrites the pinned key permanently. Only accept after confirming the new fingerprint really belongs to your Worker.
{{< /callout >}}

### A `leapmux control` or Worker-to-Worker connection aborts on a key mismatch

**Symptom**
A CLI or cross-worker connection fails. The error identifies the Worker whose key no longer matches, states the pinned and the presented fingerprints, and its hint states the command that clears the pin.

**Cause**
Non-browser clients also pin Worker keys TOFU, but they cannot pop a dialog, so a mismatch aborts the connection. A Worker keeps its keys while it stays registered, so a mismatch means that one of the two keys did not come from this Worker. Clear the pin only after the presented fingerprint matches the one the Worker logs when it starts. There are two separate pin stores:

- The **`leapmux control` CLI** keeps pins per Hub host.
- A **Worker** keeps cross-worker pins for sibling Workers.

**Fix**
Once the presented fingerprint matches the one the Worker logs when it starts, clear the stale pin so the next connect pins the presented key, then reconnect.

For the control CLI:

```bash
leapmux control worker pins list                       # see all pinned workers
leapmux control worker pins remove --worker-id <id>    # clear one pin
```

For a Worker's cross-worker pins (runs entirely against local files — no Worker process needs to run):

```bash
leapmux worker cross-worker-pins list                              # see all pins (JSON)
leapmux worker cross-worker-pins remove --target-worker-id <id>    # clear one pin
```

The browser clears its own pins under **Preferences → Advanced → Trusted worker keys**. The control CLI's pins and a Worker's cross-worker pins have no UI; clear them with the commands above. See [Control CLI](/docs/using/control-cli/) and [Managing Workers](/docs/admin/managing-workers/).

## Ports, listen address, and reaching the UI

### Port 4327 already in use

**Symptom**
The hub/solo/dev process fails to start because something is already bound to `4327` (the default TCP port for `hub`, `dev`, and `solo`).

**Cause**
Another process — often a previously launched LeapMux instance — already holds the port.

**Fix**
Either stop the conflicting process, or restart LeapMux on another port. The service saves the new port:

```bash
leapmux hub restart --listen :4400
```

For solo mode, pass a loopback address such as `--listen 127.0.0.1:4400`, so that it stays unreachable from other machines.

In Docker, the container always listens on `4327` internally; remap the host side of the port publish instead:

```bash
docker run -p 4400:4327 -e LEAPMUX_MODE=dev -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/).

### Can't reach the UI / connection refused from another machine

**Symptom**
The browser can't connect to the Hub at all (connection refused / timeout) from a different host than the one running LeapMux.

**Cause**
Solo mode binds **`127.0.0.1:4327`** by default, so another machine cannot reach it. A TCP browser on the same machine completes the restricted first-password setup.

**Fix**
- For local single-user use, browse to `http://127.0.0.1:4327` on the same machine.
- To serve other machines, do **not** simply rebind solo to all interfaces. Either:
  - Use hub mode or dev mode, which use real authentication and bind `:4327` (all interfaces) by default; or
  - To expose solo mode, set the `solo` password first and add the address in **Preferences → Administration → Network access**. Every TCP address then asks for that password. Restrict access with a firewall, VPN, or SSH tunnel. Solo mode warns on a non-loopback address because another machine can win first-password setup before the password exists. See [Security & Threat Model](/docs/admin/security/).

See [Running LeapMux](/docs/admin/running-leapmux/) for binding and listen-address options.

### Sessions or rate limits show the reverse proxy address

**Symptom**
Session records show the proxy IP, or all users behind the proxy share one address-keyed rate limit.

**Cause**
The direct proxy peer does not match `trusted_proxy_ranges`, or the preferred forwarding header is malformed. LeapMux ignores forwarding headers from untrusted peers. If `Forwarded` exists, LeapMux does not fall back to X-Forwarded headers.

**Fix**
- Add the direct proxy address or range under **Preferences → Administration → Network access → Trusted reverse proxies**.
- Configure the proxy to remove client-supplied forwarding headers or append only verified values.
- Correct or remove a malformed `Forwarded` header before you rely on `X-Forwarded-For`.
- Use a manual range when a bundled `cloudflare` or `cloudfront` snapshot does not contain the current proxy address.

See [Trusted reverse proxies](/docs/admin/configuration/#trusted-reverse-proxies) for selector forms and security requirements.

### Blank page or the UI won't load in development

**Symptom**
The page is blank, or assets fail to load, when you're running LeapMux from source.

**Cause**
In a development setup the Hub reverse-proxies to a separate Frontend dev server. If that dev server isn't running (or `--dev-frontend` points at the wrong URL), there's nothing to serve the UI.

**Fix**
- Use the provided dev task that starts both processes together: `task dev` (or `task dev-solo`). See [Installation](/docs/getting-started/installation/) and [Running LeapMux](/docs/admin/running-leapmux/).
- If you wire it up manually, make sure the Frontend dev server is up and `--dev-frontend` points at it.
- A standalone release binary or Docker image already bundles the built Frontend and does **not** need `--dev-frontend`.

### Cookies/login don't stick behind a reverse proxy

**Symptom**
You're fronting the Hub with TLS via a reverse proxy, but login won't persist or the UI behaves oddly with redirects.

**Cause**
The Hub does not terminate TLS itself, and it needs to know its external URL and that it should issue secure cookies. Without that, the derived base URL and cookie scheme can be wrong.

**Fix**
Set both — they are database settings, and both are hot (a running Hub applies them within ~30 seconds):

```bash
leapmux control admin settings set public_url "https://hub.example.com"   # scheme + host only; no path/query
leapmux control admin settings set secure_cookies true
```

`public_url` must be scheme + host only — **sub-path mounting** (e.g. `https://example.com/leapmux`) is explicitly rejected. Enabling `secure_cookies` changes the cookie name and signs every current session out — do it once, at setup. See [Configuration](/docs/admin/configuration/) and [Reverse Proxy](/docs/admin/reverse-proxy/).

## Can't log in or sign up

For the full account model, see [Accounts & Authentication](/docs/using/accounts/).

### Redirected to /setup, or you can't log in because no account exists

**Symptom**
Visiting the Hub redirects you to **/setup** with the heading **"Welcome to LeapMux"**, or you can't log in because no account exists.

**Cause**
This is a fresh multi-user Hub with no users yet. The first person to register at **/setup** becomes the admin. Until that's done, there's nothing to log into.

**Fix**
Complete the **/setup** form (Username, Display Name, Email, Password). The first user is always created as an admin. Afterward, the **/setup** route redirects to **/login**.

### "Sign-up disabled" when trying to create an account

**Symptom**
Visiting **/signup** shows a page titled **"Sign-up disabled"**, which states that new account registration is not available.

**Cause**
Public sign-up is controlled by the `signup_enabled` setting, which defaults to **false**. The first-admin **/setup** flow works even when sign-up is disabled; only public self-registration is blocked.

**Fix**
- To allow self-service sign-up: `leapmux control admin settings set signup_enabled true`.
- Otherwise have an admin create the account with `leapmux control admin user create` (see [Control CLI](/docs/using/control-cli/)).

### Login is refused although the username and the password look right

**Symptom**
Sign-in is refused, and the form says only that the credentials are invalid.

**Cause**
For security, the Hub returns the identical error for both an unknown username and a wrong password — there's no way to tell which from the message. Usernames are lowercase slugs; passwords are 8-128 printable ASCII characters, and a password may hold spaces.

**Fix**
Double-check the exact username (lowercase, hyphens, no spaces). If you've lost the password, have an admin reset it with `leapmux control admin user reset-password` (see [User passwords](/docs/admin/admin-cli/#user-passwords)), which runs over RPC against the live Hub. When the Hub is stopped, `leapmux recover password reset` does the same work offline (see [Recovery](/docs/admin/recover/)); that command opens the Hub's database directly, so run it on the Hub host with the Hub stopped.

Both reset commands end every session and revoke the account's API tokens and existing delegation tokens. They also delete every passkey. Registered Workers keep their credentials and can mint new delegation tokens. After a suspected compromise, review the Worker list and explicitly deregister each unknown Worker. See [Managing Workers](/docs/admin/managing-workers/).

Solo mode has one account, `solo`, and it asks for a password only once you set one; set and change it in **Preferences → Account → Password**.

### Passkey sign-in fails or the authenticator never appears

**Symptom**
Choosing **Passkey** on the login or signup form does nothing useful, the browser shows a WebAuthn error, or the Hub refuses the ceremony.

**Cause and fix**

| Symptom | Cause | Fix |
|---|---|---|
| The **Passkey** option is still on the login page but **disabled**, and its reason identifies an insecure page; **Add passkey** carries the same reason | The page is plain HTTP away from `localhost`, so the browser exposes no WebAuthn API at all | Serve the Hub over HTTPS, or open it at a `localhost` address. This is the browser's rule, not the Hub's: no value of `public_url` changes it. A Hub published at a plain-HTTP LAN address hits exactly this. |
| The login page offers **no Passkey option at all**, and **Add passkey** is disabled with a reason that identifies the address | The page origin is not one the Hub serves, so no ceremony can run there. The forms drop the option rather than disabling it, because this refusal is the same for every visitor | Set `public_url` to the exact origin users open in the browser (`leapmux control admin settings set public_url https://hub.example.com`), or open the Hub at the address it already publishes. `localhost` and `127.0.0.1` count as different origins, and so do two ports of one host. A change made in **Preferences → Administration** reaches the option at once; you need no page reload. |
| The **Passkey** option is disabled and its reason identifies the browser | This browser has no WebAuthn support at all, whatever the page is served over | Use a browser that supports passkeys. This is the browser's rule too, so no Hub setting changes it. The option stays visible and disabled, exactly as the insecure-page case does. |
| "origin header is required" / ceremony fails immediately | The browser request omitted `Origin` | Use a normal browser navigation to the Hub UI; do not strip `Origin` in a reverse proxy for `/leapmux.v1.*` RPCs. |
| A passkey-only account cannot verify its identity for any account change | The page cannot run a passkey ceremony at all | The **Verify your identity** form states which reason applies and what to do. Fix that one: serve the Hub over HTTPS, or set `public_url` as in the rows above. An account with a passkey and no password has no other way to verify: elevation does not fall back to a linked provider. See [Session elevation](/docs/admin/security/#session-elevation). |
| Passkey login works but app access is blocked | SMTP is configured and the email is still unverified | Complete `/verify-email` (or use **Resend code**). Passkey login does not skip email verification. |
| After account recovery, passkey login fails | Expected: recovery clears all passkeys | Sign in with the new password, then add passkeys again under **Preferences → Account → Passkeys**. |
| The passkey is gone with the device, or the provider account is lost, and there is no password | The account holds no usable sign-in method | Recover it with **Can't sign in?** on the login page (or `/recover-account`): the emailed link lets you set a password regardless of how the account used to sign in. Requires a verified email and SMTP; otherwise an admin resets the password (see above). |

Self-service account recovery (`/recover-account`) also clears every passkey on the account when it completes — the same rule as admin and offline password reset.

### Almost every action is refused until you verify your email

**Symptom**
After signing up you're stuck — almost every action is refused because your email is unverified, and you land on the **"Verify your email"** page.

**Cause**
The hub has SMTP configured, so non-admin users with an unverified email may only verify, sign out, or change their email until they verify. Verification uses a 6-character code (display form `XXX-XXX`) that expires in 30 minutes with a 5-attempt budget.

**Fix**
Enter the code from the verification email, or click the link in it. If you didn't receive it, use **Resend code** (60-second cooldown between resends). Email features require SMTP to be configured on the Hub — if the administrator hasn't configured SMTP (`leapmux control admin settings set smtp …`), verification is off and this requirement does not apply. See [Configuration](/docs/admin/configuration/).

### OAuth sign-in fails or the provider isn't shown

**Symptom**
OAuth buttons don't appear on the login page, or clicking one ends in an error. Two errors are common: the provider returned no email address, or sign-up is disabled and no existing account is linked to that identity.

**Cause and fix:**

| Symptom | Cause | Fix |
|---|---|---|
| No OAuth buttons at all | No enabled OAuth provider configured | Add one with `leapmux control admin idp add` (see [Sign-in Providers](/docs/admin/sign-in-providers/)). |
| The error says the provider returned no email address | The provider config does not have the email scope | Grant the `email`/`user:email` scope; reconfigure the provider's `--scopes`. |
| Stuck on "Complete Sign Up" then rejected | New OAuth user but sign-up is disabled | `leapmux control admin settings set signup_enabled true`, or link the OAuth identity to an existing account by signing in and verifying the matching email. |
| The **Complete Sign Up** page says the signup link is invalid or expired | The pending OAuth signup expired or the `?token=` link was reused | Start the OAuth sign-in over from the login page (see note below). |
| OAuth user logs in but can't unlink | It's their only login method | Set a password first under **Preferences → Account → Password**, then unlink. |

{{< callout type="info" >}}
An invalid or expired signup link means the pending OAuth signup ran past its 5-minute window, or the `?token=` link was reused or already completed. Start the OAuth sign-in over from the login page to mint a fresh pending signup, then pick a username promptly. A blank **Complete Sign Up** page that reports a missing signup token means you opened the URL without its `?token=` — restart from the login page.
{{< /callout >}}

Administrators configuring providers should also confirm the OIDC issuer is reachable and the `public_url` setting is set so redirect/login URLs are built correctly. See [Sign-in Providers](/docs/admin/sign-in-providers/).

## An app can't get in

For how apps are registered and what they may ask for, see [App Authorization](/docs/admin/app-authorization/); for the wire contract, [OAuth API](/docs/reference/oauth-api/).

### The consent page never appears, and the app waits

**Symptom**
`leapmux control auth login` opens a browser and nothing happens, or a third-party app hangs after you clicked its sign-in button.

**Cause and fix:**

| Symptom | Cause | Fix |
|---|---|---|
| The browser lands on a "not found" page | The address the app sent you to is not one the Hub serves | Check `public_url`. The app builds the authorization URL from the Hub's own metadata document, so a wrong `public_url` sends the browser somewhere the Hub does not answer. |
| The page asks you to verify, then returns to the same prompt | The session did not elevate | Prove a factor. If the account has neither a password nor a passkey, it has nothing to prove -- set a password first. See [Session elevation](/docs/admin/security/#session-elevation). |
| `invalid_client` | The `client_id` matches no app this account may authorize | Another account's private app is invisible, so the Hub answers exactly as it does for one that does not exist. Ask the app's owner to register it hub-wide, or register your own. |
| `invalid_request` about `redirect_uri` | The address the app sent does not match one it registered | Compare them exactly. Only a **loopback** address ignores the port; everything else is literal. |
| The app never gets a callback after you clicked **Deny** | Nothing is wrong | A refusal returns `error=access_denied` to the app, which is the app's cue to stop. If it keeps waiting, the app is not reading its own callback. |

### An app is refused a permission it thought it had

**Symptom**
An authorized app gets `permission_denied` that states the permission, such as *this app was not granted the terminal:write permission*.

**Cause and fix:**

The credential holds what the consent screen granted, not what the app asked for. Open **Preferences → Apps → Connected apps** and read the row: the permissions listed there are exactly what it can do. Authorize the app again to grant more; a refresh can only ever narrow a grant, never widen one.

Two refusals are permanent whatever you grant:

- **The account's own sign-in settings.** Adding a passkey, changing the recovery address, unlinking a sign-in provider, and managing another app's credential are outside every grant. No consent screen offers them.
- **An admin permission on an ordinary account.** A grant subtracts from what the owner can do and never adds, so `admin:users` on a non-administrator reaches nothing.

### A sensitive change is refused although the app is authorized

**Symptom**
The app reports that verification is required, and running the step-up ceremony is itself refused.

**Cause and fix:**

An app is refused the step-up ceremony unless its owner allowed it. An administrator turns it on per app:

```bash
leapmux control admin app allow-elevation --client-id <client_id>
```

Turning it off closes every open window on the next request, so a credential that worked a moment ago stops immediately. That is the intended behavior, not a stale cache.

### `invalid_grant`, and the app must be authorized again

**Symptom**
A credential that worked yesterday is refused with `invalid_grant`.

**Cause and fix:**

| Description | Cause | Fix |
|---|---|---|
| *token revoked* | Somebody disconnected the app, an administrator retired it, or the owner's password was reset | Authorize the app again. |
| *refresh reuse detected; token revoked* | A retired refresh token was presented after the grace window | The credential leaked, or two copies of the app share one credential file. Authorize again, and give each machine its own. |
| *this credential reached its maximum lifetime* | {{< duration absolute-cap >}} passed since the consent that created it | Authorize the app again. Nothing renews past this. |
| *code expired or already consumed* | The authorization code was exchanged twice, or more than {{< duration authorization-code-ttl >}} passed | Start the flow again. A code presented twice also revokes the credential the first exchange minted, which is deliberate. |

### Dynamic registration is refused

**Symptom**
`POST /oauth/register` answers `403`, or a client library reports that the Hub does not support registration.

**Cause and fix:**

RFC 7591 open registration is off by default. While it is off, `registration_endpoint` is absent from the metadata document, which is what a conformant library reports. An administrator turns it on:

```bash
leapmux control admin settings set open_app_registration true
```

Consider registering the app yourself instead. An anonymous caller who can create a registration can create one that appears on a consent screen, which is why the default is off.

## Agents won't start

For how agents work, see [Coding Agents](/docs/using/coding-agents/).

### The agent provider isn't in the picker / "No agents available"

**Symptom**
The **Agent Provider** selector shows **"No agents available"**, or the provider you want (e.g. Codex, Cursor, Pi) is missing from the list.

**Cause**
A provider only appears if **its CLI binary is detected on the Worker**. The Worker probes the shell for each provider's binary (`claude`, `codex`, `cursor-agent`, `copilot`, `kilo`, `opencode`, `goose`, `pi`, `reasonix`, `codewhale`, `kimi`, `mimo`, `qwen`, `omp`, `grok`, `kiro-cli-chat`, `amp`, `cline`) and lists only the ones it finds on `PATH`. ZCode is the exception: it ships no command, so the Worker looks for its desktop installation instead — see the next entry.

**Fix**
Install the agent's own CLI on the **Worker** machine (not where the browser runs) and make sure it's on the Worker's `PATH`. Then click the refresh button (**"Refresh available providers"**) in the selector, or reopen the dialog.

### ZCode isn't in the picker

**Symptom**
Every other installed provider appears, but **ZCode** does not.

**Cause**
ZCode ships no command, so the `PATH` probe cannot find it. LeapMux looks for a `zcode` command first, then for the desktop application's `zcode.cjs`, and it accepts that script only with an interpreter that provides `node:sqlite` — ZCode keeps its session store there. A Worker with an older `node` and no ZCode installation therefore reports nothing.

**Fix**
Install the ZCode desktop application on the **Worker** machine, or install Node 24 or newer and put a `zcode` wrapper on the Worker's `PATH`. For an installation in a place LeapMux does not know, set `LEAPMUX_ZCODE_SCRIPT` to the `zcode.cjs` — and `LEAPMUX_ZCODE_NODE` to the interpreter as well, if the one on `PATH` cannot run it.

### A ZCode agent reports that ZCode is not configured

**Symptom**
Opening a ZCode agent fails immediately, before any message is sent.

**Fix**
Sign in to the ZCode desktop application on the Worker machine and enable a model provider in it. LeapMux reads the credentials and the model list from `~/.zcode/v2/config.json` and hands them to the app-server; without an enabled provider that carries an API key, every turn would fail on the model request instead. LeapMux only reads that file — it never writes it, so nothing you do in LeapMux changes the ZCode application's own settings.

### The agent fails to start

**Symptom**
The chat pane shows a centered error. Its title identifies the provider that failed to start, and the pane carries an error message from the Worker.

**Cause**
The agent subprocess couldn't be launched or didn't complete its startup handshake. Common reasons: the CLI binary isn't actually runnable on the Worker (wrong version, broken install, missing auth), the working directory is invalid, or startup exceeded the timeout (`--agent-startup-timeout`, default **5m**).

**Fix**
- Read the error text shown in the pane — it comes straight from the Worker and usually identifies the cause.
- On the Worker, run the agent's CLI directly (e.g. `claude --version`) to confirm it works and is authenticated.
- If startup is legitimately slow, raise the timeout: set `agent_startup_timeout: 10m` in `worker.yaml` and run `leapmux worker restart`, or run `leapmux worker restart --agent-startup-timeout 10m`, which saves the flag. The setting exists on the standalone Worker only; in solo/dev the embedded worker reads the timeout from the `timeouts` setting: `leapmux control admin settings set timeouts '{"agent_startup_seconds":600}'`. See [Configuration](/docs/admin/configuration/).
- Reopen the agent once the underlying CLI issue is fixed.

### The Cline agent does not start

**Symptom**
A Cline agent fails to start, and the error states that Cline's provider settings do not match Cline's format.

**Cause**
Cline ignores a `providers.json` that does not match its own format, and then uses the built-in endpoint of the provider, which you did not choose. LeapMux does not start the agent in that case.

**Fix**
On the Worker, run `cline auth` to write the file again. Then open the agent again.

### A setting change seems to "reset" the agent

**Symptom**
Changing a setting from a composer chip or the **[+]** menu restarts the agent.

**Cause**
Most settings changes apply **live**. Some settings control process launch arguments and require a restart.

An effort change to **Auto** also requires a restart because LeapMux must remove the `--effort` argument. A model change keeps the effort when the new model offers it. If the new model does not offer the effort, the change resets the effort to Auto. Reasonix also fixes its model at process launch.

**Fix**
Wait for the agent to start again. If the start fails, use the startup error guidance above.

## Docker

For the full Docker setup, see [Running LeapMux](/docs/admin/running-leapmux/) and [Installation](/docs/getting-started/installation/).

### Container exits immediately

**Symptom**
The container starts and dies right away. It prints an error that says `LEAPMUX_MODE` must be one of `hub`, `worker`, `dev`, or `solo`.

**Cause**
The image's supervisor requires the `LEAPMUX_MODE` environment variable to choose a run mode. Without it (or with an invalid value) it exits 1.

**Fix**
Pass a valid mode:

```bash
docker run -p 4327:4327 -e LEAPMUX_MODE=hub -v leapmux-data:/data ghcr.io/leapmux/leapmux:latest
```

{{< callout type="info" >}}
Use `LEAPMUX_MODE=dev` (not `solo`) for an all-in-one container reachable from your host. `solo` defaults to loopback-only inside the container, so its port isn't reachable from outside unless you override `listen` to `:4327` in `/data/solo/solo.yaml`. `dev` binds all interfaces by default.
{{< /callout >}}

### A Worker container exits because it is unregistered

**Symptom**
A `LEAPMUX_MODE=worker` container exits, and its log says ``worker is unregistered: run `leapmux worker register` first``.

**Cause**
The container runs `leapmux worker run`, which never registers. The volume holds no credentials yet, or the registration used another directory than `/data/worker`.

**Fix**
Run the registration container from [Running a Worker container](/docs/admin/running-leapmux/#running-a-worker-container) once, with the same volume and `--data-dir /data/worker`. Then start the Worker container again. To see the registration and the Hub connection of a running container, run `docker exec <container> /usr/local/bin/leapmux worker status --data-dir /data/worker`.

### Non-SQLite storage configured via env vars is ignored

**Symptom**
You set something like `LEAPMUX_HUB_STORAGE_TYPE=postgres` but the Hub still uses SQLite.

**Cause**
Nested storage settings (`storage.type`, `storage.postgres.dsn`, ...) can't be set via `LEAPMUX_*_STORAGE_*` env vars due to how those keys are mapped.

**Fix**
Configure storage in the YAML config file (`/data/hub/hub.yaml` in Docker) or via the dedicated CLI flags (`--storage-type`, `--storage-postgres-dsn`, ...):

```yaml
# hub.yaml
storage:
  type: postgres
  postgres:
    dsn: postgres://user:password@db:5432/leapmux?sslmode=disable
```

See [Configuration](/docs/admin/configuration/).

## Data and persistence

### Data disappears between restarts

**Symptom**
Workspaces, accounts, agents, or terminals vanish after restarting the container or process.

**Cause**
The Hub's state lives in its data dir (SQLite DB at `<data_dir>/hub.db`, encryption key ring at `<data_dir>/encryption.key`); the Worker keeps `state.json` and `worker.db`. If the data dir isn't persisted, every restart starts fresh. A Worker that registered with a Hub whose data was lost keeps retrying, because the Hub has no record of its token; stop it, delete its `state.json`, and register it again.

**Fix**
- **Docker:** mount a volume at `/data`. Without `-v ...:/data`, state is lost when the container is removed. State lands under `/data/<mode>/`.
- **CLI:** point `--data-dir` at a stable directory, or rely on the default under `~/.config/leapmux/<mode>/`. Don't run from a temp directory whose relative `data-dir` resolves somewhere transient.
- **Upgrades:** pull a newer image/binary and recreate against the **same** data dir/volume. Migrations run automatically on startup; no manual migration command is needed.

{{< callout type="warning" >}}
Back up the Hub's `encryption.key` together with its database. Losing the file makes encrypted secrets unrecoverable. Its token pepper also becomes unavailable, signing out every session and invalidating every Worker auth token. See [Encryption & Data](/docs/admin/encryption-and-data/).
{{< /callout >}}

### Encryption-key errors after restoring a backup

**Symptom**
After restoring the database, OAuth or token-backed features fail to decrypt. Users may also be signed out, and the Hub may refuse Worker tokens.

**Cause**
You restored `hub.db` but not the matching `encryption.key`, or restored a key from a different point in time. The two must be in sync.

**Fix**
Restore the `encryption.key` from the same backup as the database. If the matching file is lost, the new pepper invalidates every session and Worker token. Solo mode and the desktop app reissue their own Worker's token. For each standalone Worker:

1. Remove it in the Hub's Worker list.
2. Stop it.
3. Delete its `state.json`.
4. Register it with a new key. It receives a new Worker ID.

For planned key rotation, use `leapmux recover encryption-key rotate` then `reencrypt` — and follow the on-screen instruction to restart the Hub between the two. See [Encryption & Data](/docs/admin/encryption-and-data/).

## Terminals and `leapmux control`

For terminal behavior, see [Terminals](/docs/using/terminals/); for the CLI, see [Control CLI](/docs/using/control-cli/).

### `leapmux control` inside a terminal/agent says it can't find the Hub

**Symptom**
Running `leapmux control ...` from inside a LeapMux terminal or agent fails. The error says that it found no `--hub` flag and no `LEAPMUX_HUB` or `LEAPMUX_CONTROL_SOCK` environment variable.

**Cause**
`leapmux control` resolves its target from `LEAPMUX_CONTROL_SOCK` (+ `LEAPMUX_CONTROL_TOKEN`) when spawned inside a LeapMux terminal/agent, or from `--hub`/`LEAPMUX_HUB` plus saved login credentials otherwise. Those `LEAPMUX_CONTROL_*` env vars are injected automatically for every terminal and agent spawn — but if they're missing, the command can't locate the Hub. The Worker may have failed to start the control endpoint. On Unix, the socket lives in `<data-dir>/run/control` when its full path fits the platform limit. A deeper data directory uses a private namespace under the canonical system `/tmp` directory. The namespace derives from the account and the canonical Worker data directory. The Worker refuses an insecure directory, a conflicting namespace identity, a namespace held by another process, or a fallback path that still exceeds the platform limit. It never changes an insecure directory's permissions or switches locations to hide an ownership refusal. On Windows, the endpoint is a named pipe whose name does not depend on the Worker data directory. The Worker log states the actual error.

**Fix**
- **Inside a LeapMux terminal/agent:** read the Worker log for a control-endpoint error. On Unix, correct the directory ownership and permissions, or choose a shorter Worker data directory. On Windows, inspect the named-pipe startup error in the Worker log. The `LEAPMUX_CONTROL_*` vars should already be present. Confirm with `env | grep LEAPMUX_CONTROL`. If they're absent, you likely spawned a sub-shell that stripped the environment, or the remote-IPC server wasn't available at spawn — open a fresh terminal tab. (There is **no** "remote-enabled" checkbox to toggle; it's wired up automatically.)
- **From your own shell (not inside LeapMux):** authenticate first:

  ```bash
  leapmux control auth login --hub https://hub.example.com
  ```

  For headless/SSH/container shells where a browser can't open, add `--device-code` to use the device-code flow. Check your identity with `leapmux control whoami` and `leapmux control auth status`.

### A terminal stops accepting input and offers a restart

**Symptom**
The terminal stops accepting input and shows a notice in square brackets. The notice reports that the shell process exited, with its exit code, or that the Worker disconnected. It also tells you that Enter restarts the shell.

**Cause**
The shell process exited (you typed `exit`, the shell crashed, or the Worker connection dropped). The tab persists so its scrollback isn't lost.

**Fix**
Press **Enter** — that's the only key an exited terminal acts on; it restarts the shell in the same working directory. The new prompt appears below the preserved buffer. A faded/struck-through tab label means the terminal is DISCONNECTED or EXITED. If the Worker itself is down, bring it back online first, then press Enter.

### The expected shell isn't offered in "New terminal"

**Symptom**
The **Shell** dropdown in the **New terminal** dialog doesn't list the shell you want, or shows **"No shells available"**.

**Cause**
The shell list is probed **on the Worker**. The Worker resolves a default shell (from `LEAPMUX_DEFAULT_SHELL`, then `SHELL`, then platform detection) and probes a fixed known-shells set (`sh`, `bash`, `zsh`, `fish`, `pwsh`, `powershell`) via `PATH`. Only shells found on the Worker appear.

**Fix**
Install the shell on the Worker and ensure it's on the Worker's `PATH`, then reopen the dialog (the list is per-Worker and refetched when you change Worker). To force a specific default, set `LEAPMUX_DEFAULT_SHELL` in the Worker's environment (a bare name like `zsh` or an absolute path).

## Still stuck?

If none of these match:

- Restart the Worker with `--log-level debug` to get verbose connection logs.
- Verify versions match expectations with `leapmux version` and the Worker info shown in the sidebar **Workers** row context menu.
- Check the [FAQ](/docs/reference/faq/) for common questions, the [Glossary](/docs/reference/glossary/) for terminology, and the [CLI Reference](/docs/reference/cli-reference/) for exact flags.
- For security-sensitive symptoms (unexpected key changes, unknown Workers), read [Security & Threat Model](/docs/admin/security/) before accepting anything.
- Still blocked, or think you've hit a bug? [Open a GitHub issue](https://github.com/leapmux/leapmux/issues). Include your `leapmux version`, the run mode, and any relevant `--log-level debug` output so the maintainers can follow up.
