---
title: "Workers & Network"
description: "Fixes for Worker registration, offline Workers, TOFU public-key mismatches, reverse proxies, WebSockets, client IP forwarding, and cookies."
type: docs
weight: 2
---

Fixes for Worker registration, Hub connectivity, trust-on-first-use (TOFU) key mismatches, and reverse-proxy networking.

## Workers won't connect or stay offline

A Worker always dials *outbound* to the Hub over one bidirectional stream. Its online status reflects whether that stream is connected; there is no separate approval queue. See [Managing Workers](/docs/admin/managing-workers/).

### Worker is unregistered

**Symptom**
`leapmux daemon worker start`, `restart`, or `run` fails with ``worker is unregistered: run `leapmux daemon worker register` first``.

**Cause**
The Worker's `--data-dir` contains no saved credentials, or the command used a different `--data-dir` / `--config` than the registration command.

**Fix**
Register the Worker once with `leapmux daemon worker register --hub <url> --registration-key <key>`, or pass the `--data-dir` / `--config` that holds the registration.

### Registration key invalid or already used

**Symptom**
`leapmux daemon worker register` fails immediately because the Hub rejected the registration key.

**Cause**
Registration keys are single-use (consumed atomically) and expire after 5 minutes. The key expired, was revoked, or was already used by another Worker.

**Fix**
Generate a fresh key in the Hub's **Register worker** dialog, abandon the refused `pending-registration.json` attempt (see [Managing Workers](/docs/admin/managing-workers/)), and run `leapmux daemon worker register` again. To inspect or revoke active CLI-minted keys, use `leapmux admin worker reg-key list` and `leapmux admin worker reg-key revoke <reg-key>` (see [Admin CLI](/docs/admin/admin-cli/)).

### Pending registration already exists

**Symptom**
`leapmux daemon worker register` fails and reports a pending registration for a Hub.

**Cause**
An earlier registration attempt with another key or Hub did not finish, and the Hub may already have committed it.

**Fix**
If the earlier command may have succeeded, re-run that exact command to finish it. Otherwise abandon the pending registration as documented in [Managing Workers](/docs/admin/managing-workers/), then run `register` with the new key.

### Worker is already registered

**Symptom**
`leapmux daemon worker register` refuses because the Worker is already registered.

**Cause**
`state.json` already exists in `--data-dir`. LeapMux refuses to avoid wasting a fresh key on an already registered Worker.

**Fix**
Start the Worker with `leapmux daemon worker start`. To move it to another Hub or register a fresh identity, stop it, run `leapmux daemon worker deregister`, and then register again.

### Worker command rejects `--hub` or `--registration-key`

**Symptom**
`start`, `restart`, or `run` rejects `--hub` or `--registration-key`.

**Cause**
Only `leapmux daemon worker register` accepts those flags. Runtime commands always connect to the Hub URL saved in `state.json`.

**Fix**
Remove the flag. To change the Hub URL, deregister the Worker and register it against the new Hub URL.

### Worker runs but stays offline

**Symptom**
The Worker process is running, but never shows an online dot in the sidebar or the **Worker** dropdown says **"No workers online"**.

**Cause and fix** — check in order:

| Cause | How to verify | Fix |
|---|---|---|
| Wrong Hub URL | Check `leapmux daemon worker status` and `logs` | Deregister and re-register with the correct `--hub` URL (use the public `https://` URL behind a reverse proxy; local Workers accept `unix:<path>` or `npipe:<name>`). |
| Hub unreachable from Worker host | Run `curl https://hub.example.com/` from the Worker host | Bind the Hub to a reachable interface (`:4327` for `hub`/`dev`; `solo` defaults to `127.0.0.1:4327`). |
| Outbound firewall / NAT block | Test outbound TCP/TLS to the Hub port | Workers need only outbound access to the Hub; allow egress to the Hub port. |
| Registered to a different Hub or account | Run `leapmux admin worker list` on the Hub | Workers belong to the Hub and account that minted their registration key. Re-register with a key minted by the right account on the right Hub. |
| Worker was deregistered or deleted | Check Worker logs for deletion notice | A standalone Worker clears its local state and exits. Register it again with a fresh key. |

A Worker reconnects automatically with exponential backoff after transient outages or Hub restarts. Restart the Worker with `leapmux daemon worker restart` to reconnect immediately.

### Repeated HTTP 401 or 403 on reconnect

**Symptom**
The Worker stays offline and logs `HTTP 401` (or `HTTP 403` behind a proxy) on every reconnect attempt. After five `HTTP 401` refusals in a row, the Worker logs a remediation hint.

**Cause**
Either the Hub has no record of the Worker's token (deleted more than 7 days ago, registered to another Hub, or restored from an older database backup), or a proxy in front of the Hub is returning `401` / `403`. The Worker keeps `state.json` so a proxy mistake or backup rollback does not destroy its identity.

**Fix**
Check the saved Hub URL with `leapmux daemon worker status` and verify your reverse proxy does not intercept `/ws/worker`. If the Hub genuinely no longer knows the Worker, stop the Worker, delete `<data-dir>/state.json`, and register it again.

### Hub refused Worker public keys

**Symptom**
The Worker logs that the Hub refused its public keys (or that its registered keypair is missing or unusable) and stops.

**Cause**
The Hub pins every Worker's public keys at registration and rejects any connection presenting different keys.

**Fix**
- **Standalone Worker:** run `leapmux daemon worker stop`, then `leapmux daemon worker deregister`, then register again with a new key.
- **Auto-registered Worker (`solo`, `dev`, Desktop app):** restart the app. At startup it detects the mismatch, retires the old Worker, and registers a fresh one (with a new Worker ID).

### Sidebar shows Worker disconnected

**Symptom**
The Worker is online on the Hub, but its sidebar indicator is disconnected.

**Cause**
The sidebar status reflects whether **your browser** currently holds a live end-to-end encrypted (E2EE) channel to that Worker, which opens on demand.

**Fix**
Open an agent or terminal on the Worker to open the E2EE channel. Use the refresh button in the **Worker** selector to clear a stale offline display. If the Worker is genuinely offline at the Hub, opening a channel fails with an error reporting that the Worker is offline — bring the Worker process back online.

## Worker public key changed (TOFU pin mismatch)

LeapMux pins each Worker's static public key on first connection (trust-on-first-use). A mismatch alerts you if a Hub substitutes a Worker key on a channel your client opens. See [Security & Authentication](/docs/admin/security-and-auth/).

### Browser shows "Worker public key changed"

{{< screenshot src="worker-key-mismatch" alt="LeapMux Worker public key changed dialog showing the Worker ID, Expected and Actual four-word fingerprints, and Reject and Accept buttons" caption="Reject the connection and verify the Actual fingerprint against the Worker's startup log before clicking Accept." width="608" height="518" >}}

**Symptom**
A dialog titled **"Worker public key changed"** displays **Expected** and **Actual** 4-word fingerprints and blocks opening tabs on that Worker.

**Cause**
A registered Worker cannot change its keys under the same Worker ID, so one of the two keys did not come from that Worker. (Wiping `state.json` registers a new Worker ID and does not trigger this dialog.)

**Fix**
- Click **Reject** (or close the dialog) and investigate before connecting.
- Click **Accept** only after verifying out of band that the **Actual** fingerprint matches the fingerprint logged by your Worker at startup.

{{< callout type="warning" >}}
Clicking **Accept** overwrites the pinned key permanently. Confirm the fingerprint on the Worker host first.
{{< /callout >}}

### CLI or cross-Worker connection aborts on key mismatch

**Symptom**
A `leapmux` CLI command or cross-Worker operation aborts with pinned and presented fingerprints.

**Cause**
Non-browser clients enforce TOFU pinning without an interactive dialog. The CLI keeps pins per Hub host, and each Worker keeps cross-Worker pins for sibling Workers.

**Fix**
After verifying out of band that the presented fingerprint matches the Worker's startup log, clear the stale pin and reconnect:

```bash
# Control CLI pin store
leapmux worker pins list
leapmux worker pins remove <worker>

# Worker cross-Worker pin store (local files only; Worker need not run)
leapmux daemon worker cross-worker-pins list
leapmux daemon worker cross-worker-pins remove <worker-id>
```

Browser pins can be managed under **Preferences → Advanced → Trusted worker keys**.

## Reverse proxies, WebSockets, and tunnels

For verified nginx, Caddy, Traefik, HAProxy, and Apache configurations, see [Reverse Proxy](/docs/admin/reverse-proxy/).

### Worker fails through a reverse proxy

**Symptom**
The web app works, but a Worker fails to stay online or reconnects on a fixed idle interval.

| Worker log | Cause | Fix |
|---|---|---|
| `HTTP 426 Upgrade Required: WebSocket protocol violation` | The proxy strips WebSocket upgrade headers on `/ws/worker`. | Forward `Upgrade` and `Connection` headers on `/ws/`. |
| `disconnected from hub, reconnecting...` (`EOF`) at a fixed idle interval | Proxy idle timeout is shorter than the 10-second ping interval. | Raise the proxy read/idle timeout to at least 60 seconds. |

### Sessions show reverse proxy IP

**Symptom**
Session records show the proxy IP address, or all users behind the proxy share one rate-limit bucket.

**Cause**
The proxy's direct peer address is not listed in `trusted_proxy_ranges`, or the proxy sends a malformed `Forwarded` header (LeapMux does not fall back to `X-Forwarded-For` when `Forwarded` is present).

**Fix**
Add the proxy's address or CIDR range under **Preferences → Administration → Network access → Trusted reverse proxies**, strip client-supplied forwarding headers at the proxy, and fix or remove any malformed `Forwarded` header. See [Configuration Reference](/docs/admin/configuration-reference/).

### Login cookies do not stick behind a proxy

**Symptom**
Signing in succeeds briefly or redirects unexpectedly when the Hub sits behind TLS termination.

**Cause**
The Hub does not terminate TLS directly and needs its external origin (`public_url`) and `secure_cookies` enabled.

**Fix**
Set both Hub settings (changes apply within ~30 seconds without restarting):

```bash
leapmux admin settings set public_url "https://hub.example.com"
leapmux admin settings set secure_cookies true
```

`public_url` must be scheme and host only (sub-path mounting such as `https://example.com/leapmux` is rejected). Enabling `secure_cookies` changes the cookie name and signs out existing sessions, so set it once during initial setup.
