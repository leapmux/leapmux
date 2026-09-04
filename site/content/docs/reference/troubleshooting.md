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

## Workers won't connect or stay offline

A Worker (`leapmux worker`) dials *out* to the Hub and holds a single bidirectional stream open. Its online/offline state is computed live from whether that stream is currently connected — there is no separate "approval" queue. For the full Worker lifecycle, see [Managing Workers](/docs/admin/managing-workers/).

### A Worker exits at once and reports that it is unregistered

**Symptom**
The Worker process prints an error and exits. The error says that the Worker is unregistered, and it tells you to pass a registration key from the hub UI.

**Cause**
The Worker has no saved credentials (no `state.json` in its data dir) and you started it without a `--registration-key`. Bare Workers cannot self-register — registration is single-shot and controlled entirely by possessing a valid key.

**Fix**
Mint a key in the Hub UI: open the sidebar **Workers** section, click the **+** (Register worker) button, and copy the generated command. It already includes the right Hub URL and key:

```bash
leapmux worker --hub https://hub.example.com --registration-key <key>
```

The key is only valid while the **Register worker** dialog stays open (5-minute TTL, auto-extended while open). If you close the dialog, the key is destroyed — reopen it to mint a fresh one. See [Managing Workers](/docs/admin/managing-workers/) for minting keys via the UI or email, and for listing or revoking keys from the CLI.

### The Hub rejects the registration because the key is invalid or already used

**Symptom**
The Worker prints an error and exits. The error says that the Hub rejected the registration because the key is invalid or already consumed.

**Cause**
Registration keys are **consumed atomically** on first use and live only 5 minutes. This error means the key was already used by another Worker, was revoked, or expired. These are permanent errors — the Worker does **not** retry them (unlike a transient network failure, which it does retry with backoff).

**Fix**
Mint a brand-new key from the **Register worker** dialog and run the Worker with it. Never reuse a key across machines. If you mint keys via the CLI, check live keys with `leapmux control admin worker reg-key list` and revoke stale ones with `leapmux control admin worker reg-key revoke --id <id>` (see [Control CLI](/docs/using/control-cli/)).

### The Worker refuses `--registration-key` because it is already registered

**Symptom**
The Worker prints an error and exits. The error says that the Worker is already registered, and it tells you to remove `--registration-key` or to wipe the local state.

**Cause**
This Worker already has saved credentials in `state.json`, and you passed `--registration-key` again. This guard exists so you do not waste a fresh key on a machine that's already configured.

**Fix**
Just run `leapmux worker --hub <url>` with **no** `--registration-key` — it reconnects with its saved credentials. If you genuinely want a clean re-registration, deregister the Worker first (sidebar **Workers** row > **Deregister...**, or `leapmux control admin worker deregister --id <id>`), delete the Worker's `state.json` from its data dir, then register again with a new key.

### Worker process runs but never appears online

**Symptom**
The Worker process is alive and logging reconnection attempts, but it never shows a connected status dot in the sidebar, or the **Worker** dropdown in the New agent/terminal dialogs shows **"No workers online"**.

**Cause and fix** — work through these in order:

| Cause | How to confirm | Fix |
|---|---|---|
| Wrong Hub URL | Worker logs show repeated dial failures to the wrong host/port | Set `--hub` to the Hub's reachable URL. Default is `http://127.0.0.1:4327`; behind a reverse proxy use the public `https://` URL. The Worker accepts `http[s]://...`, `unix:<socket-path>` (Unix only), or `npipe:<pipe-name>` (Windows only). |
| Hub not actually listening on a reachable address | `curl http://hub-host:4327/` from the Worker machine | Make sure the Hub binds an interface the Worker can reach. The Hub default is `:4327` (all interfaces); solo mode defaults to `127.0.0.1:4327` (loopback only — unreachable from another machine). See [Running LeapMux](/docs/admin/running-leapmux/). |
| Firewall / NAT between Worker and Hub | Network tools on the Worker host | The Worker connects **outbound** (NAT-friendly), so the Worker needs outbound access to the Hub's port, not an inbound port. Open egress to the Hub. |
| Registered to a different Hub / owner | `leapmux control admin worker list` on the Hub | A Worker belongs to the Hub and user that minted its key. Re-register against the correct Hub if it's pointed at the wrong one. |
| Worker was deregistered server-side | Worker logs show `Unauthenticated` on reconnect, then exits | When the Hub deletes a Worker, the Worker clears its local state and exits on next connect. Register it again from the UI. |

{{< callout type="info" >}}
A Worker reconnects automatically with exponential backoff (1s up to 180s between attempts). If you just restarted the Hub, give the Worker up to ~3 minutes to reconnect, or restart the Worker to retry immediately.
{{< /callout >}}

### Worker is "online" to the Hub but the sidebar shows it disconnected

**Symptom**
The Worker is registered and online, but its sidebar status dot is grey/disconnected and you can't open content on it.

**Cause**
The sidebar status reflects whether **your browser** has a live end-to-end-encrypted channel to that Worker — which is distinct from the Hub's Worker-online flag. If you haven't opened anything on the Worker yet (or the channel was torn down), the Frontend has no open channel.

**Fix**
Open an agent or terminal on the Worker — that's what opens the channel; it opens on demand. The refresh button in the **Worker** selector only re-fetches the Worker list/status, which can clear a stale "offline" display, but it does not open the content channel by itself. If the Worker is genuinely offline at the Hub, opening a channel fails with the status `CodeUnavailable` and an error that says the Worker is offline — bring the Worker process back up.

## "Worker public key changed" / handshake rejected (TOFU pin mismatch)

LeapMux remembers each Worker's public key on first connection (trust-on-first-use) and warns if it later changes. A later handshake whose key doesn't match is rejected, so a compromised Hub cannot silently swap a Worker underneath you. See [Security & Threat Model](/docs/admin/security/).

### The browser shows the "Worker public key changed" dialog

**Symptom**
A dialog titled **"Worker public key changed"** appears, stating that the Worker's public key differs from the one at the last connection, and showing an **Expected:** and **Actual:** 4-word fingerprint. The agent/terminal won't open until you choose.

**Cause**
The Worker's remembered key no longer matches the key the Worker is now presenting. Legitimate causes: the Worker's `state.json` was deleted/recreated (which regenerates its keypair), the data dir moved, or you reinstalled. The malicious cause this protects against: someone substituted a different Worker.

**Fix**
- If you expected this change (you wiped the Worker's state, reinstalled, etc.), verify the **Actual** fingerprint matches the new Worker out-of-band, then click **Accept**. The pin is overwritten with the new key.
- If you did **not** expect it, click **Reject** (closing the dialog also counts as Reject) and investigate before reconnecting. The channel is not opened.

{{< callout type="warning" >}}
Accepting overwrites the pinned key permanently. Only accept after confirming the new fingerprint really belongs to your Worker.
{{< /callout >}}

### A `leapmux control` or Worker-to-Worker connection aborts on a key mismatch

**Symptom**
A CLI or cross-worker connection fails. The error identifies the Worker whose key no longer matches, and it gives a hint that tells you how to clear the pin.

**Cause**
Non-browser clients also pin Worker keys TOFU, but they cannot pop a dialog, so a mismatch aborts the connection. The hint tells you exactly how to clear the pin. There are two separate pin stores:

- The **`leapmux control` CLI** keeps pins per Hub host.
- A **Worker** keeps cross-worker pins for sibling Workers.

**Fix**
Clear the stale pin so the next connect re-pins the new key, then reconnect.

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

See [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/).

### Can't reach the UI / connection refused from another machine

**Symptom**
The browser can't connect to the Hub at all (connection refused / timeout) from a different host than the one running LeapMux.

**Cause**
Solo mode binds **`127.0.0.1:4327`** by default, so another machine cannot reach it. A TCP browser on the same machine completes the restricted first-password setup.

**Fix**
- For local single-user use, browse to `http://127.0.0.1:4327` on the same machine.
- To serve other machines, do **not** simply rebind solo to all interfaces. Either:
  - Run `leapmux hub` (or `dev`), which use real authentication and bind `:4327` (all interfaces) by default; or
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

`public_url` must be scheme + host only — **sub-path mounting** (e.g. `https://example.com/leapmux`) is explicitly rejected. Enabling `secure_cookies` changes the cookie name and signs every current session out — do it once, at setup. See [Configuration](/docs/admin/configuration/) and [Running LeapMux](/docs/admin/running-leapmux/).

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

Either way, every session, token, **and passkey** the account holds is revoked. Solo mode has one account, `solo`, and it asks for a password only once you set one; set and change it in **Preferences → Account → Password**.

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
A provider only appears if **its CLI binary is detected on the Worker**. The Worker probes the shell for each provider's binary (`claude`, `codex`, `cursor-agent`, `copilot`, `kilo`, `opencode`, `goose`, `pi`, `reasonix`) and lists only the ones it finds on `PATH`. ZCode is the exception: it ships no command, so the Worker looks for its desktop installation instead — see the next entry.

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
- If startup is legitimately slow, raise the timeout: `leapmux worker --agent-startup-timeout 10m` (or the equivalent key in config). The flag exists on the Worker only; in solo/dev the embedded worker reads the timeout from the `timeouts` setting: `leapmux control admin settings set timeouts '{"agent_startup_seconds":600}'`. See [Configuration](/docs/admin/configuration/).
- Reopen the agent once the underlying CLI issue is fixed.

### A setting change seems to "reset" the agent

**Symptom**
Changing a setting from a composer chip or the **[+]** menu restarts the agent.

**Cause**
Most settings changes apply **live**. Some settings control process launch arguments and require a restart. Copilot Assisted Approval is one example. LeapMux adds `--experimental` and `--assisted-approval` when this option is on. This option also enables all Copilot experimental features.

Some Copilot CLI versions reject Assisted Approval with Agent Client Protocol (ACP) mode. LeapMux retries a new session with the safe default switched off, and locks Assisted Approval to Off for that session. It also remembers that this CLI refuses the flag, so every later session and every restart starts with the flag off. An explicit request still fails and shows the startup error.

An effort change to **Auto** also requires a restart because LeapMux must remove the `--effort` argument. A model change can reset effort to Auto. Reasonix also fixes its model at process launch.

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

### A Worker container can't connect

**Symptom**
A `LEAPMUX_MODE=worker` container fails because the Worker is unregistered, or it never connects.

**Cause**
The container's supervisor starts the Worker with no `--hub` or `--registration-key` flags, so a Worker container must get its Hub URL and (first-run) key from config or env vars. See [Running LeapMux](/docs/admin/running-leapmux/) for how the container supervisor launches each mode.

**Fix**
Supply them via `LEAPMUX_WORKER_*` env vars or the Worker YAML:

```bash
docker run \
  -e LEAPMUX_MODE=worker \
  -e LEAPMUX_WORKER_HUB=https://hub.example.com \
  -e LEAPMUX_WORKER_REGISTRATION_KEY=<key> \
  -v leapmux-worker-data:/data \
  ghcr.io/leapmux/leapmux:latest
```

The registration key is consumed on first run; once registered, the Worker reconnects with its saved state and you can drop `LEAPMUX_WORKER_REGISTRATION_KEY`. See [Managing Workers](/docs/admin/managing-workers/).

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
The Hub's state lives in its data dir (SQLite DB at `<data_dir>/hub.db`, encryption key ring at `<data_dir>/encryption.key`); the Worker keeps `state.json` and `worker.db`. If the data dir isn't persisted, every restart starts fresh.

**Fix**
- **Docker:** mount a volume at `/data`. Without `-v ...:/data`, state is lost when the container is removed. State lands under `/data/<mode>/`.
- **CLI:** point `--data-dir` at a stable directory, or rely on the default under `~/.config/leapmux/<mode>/`. Don't run from a temp directory whose relative `data-dir` resolves somewhere transient.
- **Upgrades:** pull a newer image/binary and recreate against the **same** data dir/volume. Migrations run automatically on startup; no manual migration command is needed.

{{< callout type="warning" >}}
Back up the Hub's `encryption.key` together with its database. The key ring encrypts stored secrets (OAuth tokens, etc.) at rest — losing it makes those secrets unrecoverable. See [Encryption & Data](/docs/admin/encryption-and-data/).
{{< /callout >}}

### Encryption-key errors after restoring a backup

**Symptom**
After restoring the database, OAuth or token-backed features fail to decrypt.

**Cause**
You restored `hub.db` but not the matching `encryption.key`, or restored a key from a different point in time. The two must be in sync.

**Fix**
Restore the `encryption.key` from the same backup as the database. For planned key rotation, use `leapmux recover encryption-key rotate` then `reencrypt` — and follow the on-screen instruction to restart the Hub between the two. See [Encryption & Data](/docs/admin/encryption-and-data/).

## Terminals and `leapmux control`

For terminal behavior, see [Terminals](/docs/using/terminals/); for the CLI, see [Control CLI](/docs/using/control-cli/).

### `leapmux control` inside a terminal/agent says it can't find the Hub

**Symptom**
Running `leapmux control ...` from inside a LeapMux terminal or agent fails. The error says that it found no `--hub` flag and no `LEAPMUX_HUB` or `LEAPMUX_CONTROL_SOCK` environment variable.

**Cause**
`leapmux control` resolves its target from `LEAPMUX_CONTROL_SOCK` (+ `LEAPMUX_CONTROL_TOKEN`) when spawned inside a LeapMux terminal/agent, or from `--hub`/`LEAPMUX_HUB` plus saved login credentials otherwise. Those `LEAPMUX_CONTROL_*` env vars are injected automatically for every terminal and agent spawn — but if they're missing, the command can't locate the Hub.

**Fix**
- **Inside a LeapMux terminal/agent:** the `LEAPMUX_CONTROL_*` vars should already be present. Confirm with `env | grep LEAPMUX_CONTROL`. If they're absent, you likely spawned a sub-shell that stripped the environment, or the remote-IPC server wasn't available at spawn — open a fresh terminal tab. (There is **no** "remote-enabled" checkbox to toggle; it's wired up automatically.)
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
