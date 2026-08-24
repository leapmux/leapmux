---
title: "Security & Threat Model"
description: "LeapMux trust model and end-to-end encryption: how browser-to-agent traffic is protected, how Worker identity is pinned, and the steps to operate it safely."
type: docs
weight: 7
---

This chapter is the security reference for security-conscious users and operators. It describes the trust model LeapMux assumes, the end-to-end encryption (E2EE) that protects Frontend↔Worker traffic, how Worker identity is pinned, what changes in solo mode, and the concrete steps you should take to operate LeapMux safely.

If you only remember one thing: **LeapMux treats the Hub as an authenticated relay, not a trusted peer.** The Hub routes opaque ciphertext between your browser and your Workers. It sees who is talking to whom, but never what they say.

**The end-to-end encrypted relay — the tunnel passes through the Hub but is opaque to it:**

```text
       Noise_NK end-to-end encrypted tunnel (opaque to the Hub)
       encrypted: chat, tools, terminal I/O, files
       ┌───────────────────────────────────────────────┐
       ▼                                               ▼
┌─────────────┐        ┌─────────────┐        ┌──────────────────┐
│  Frontend   │ cipher │     Hub     │ cipher │      Worker      │
│  (Browser / │◄──────►│  (relay     │◄──────►│    (daemon,      │
│  Desktop)   │  text  │   only)     │  text  │   holds keys)    │
└─────────────┘        └──────┬──────┘        └──────────────────┘
                              │
                              ▼
              sees: ciphertext + metadata
              (channel id, sizes, timing)
```

## The trust model

LeapMux is built around a single, deliberate trust boundary. In distributed mode the Hub may be operated by a teammate, a platform team, or a hosting provider — someone other than you. The design assumes the Hub host could be curious or even compromised, and limits the blast radius accordingly.

There are three protocol paths, each with a different security posture:

| Path | Protocol | Encryption |
|------|----------|------------|
| Frontend → Hub | ConnectRPC (gRPC-compatible) — login, workspace management, Worker registration | TLS in front of the Hub (your responsibility as operator) |
| Frontend → Worker | Hybrid post-quantum Noise_NK. The handshake rides Hub-relayed RPCs; the encrypted traffic that follows is multiplexed over a single relayed WebSocket | End-to-end encrypted; the Hub cannot decrypt |
| Worker → Hub | ConnectRPC over the gRPC protocol, bidirectional streaming; the Worker always dials out (NAT-friendly, no inbound ports) | TLS in front of the Hub; channel payloads ride inside the E2EE tunnel |

The key consequence: control-plane data (accounts, workspace records, layout, Worker registration) reaches the Hub in a form it can read, while everything you actually do inside an agent or terminal travels inside an encrypted channel the Hub merely forwards.

> **Note:** "End-to-end" here means the two ends are your browser (the Frontend) and the Worker daemon. The Hub is the middle. See [Concepts & Architecture](/docs/getting-started/concepts/) for how these components fit together and [Running LeapMux](/docs/operating/running-leapmux/) for how to launch each one.

## What the Hub can and cannot see

The two columns below are the heart of the threat model. Treat the left column as data you are entrusting to whoever runs the Hub, and the right column as data that never leaves your encrypted channel.

| The Hub **can** see | The Hub **cannot** see |
|---------------------|------------------------|
| Account metadata: user names, emails, password hashes, OAuth tokens, session tokens, passkey credential metadata (friendly names, credential IDs, encrypted public keys) | Agent chat transcripts, tool-call arguments, or tool outputs |
| Account and workspace records | Terminal I/O, shell history, or PTY state |
| Workspace **titles**, tab positions, and tiling layout geometry | File contents, diffs, or git status |
| Worker registration data: Worker ID, composite public keys, online status, last-seen time | Worker hostname, OS, or filesystem paths (sent only inside the encrypted channel) |
| Per-message transport metadata: channel ID, correlation ID, ciphertext size, timing | Any plaintext of Frontend↔Worker traffic |

> **Warning:** **Traffic analysis is in scope.** The Hub observes message timing, sizes, and which channel correlates to which Worker. It cannot read content, but it can infer activity patterns — when you are working, how much you are typing, which Worker is busy. If that metadata is itself sensitive in your environment, treat the Hub host accordingly.

A few specifics worth internalizing:

- **Workspace titles are visible, agent content is not.** Name your workspaces with that in mind. Tab positions and tiling geometry are layout metadata the Hub stores so your arrangement can sync across devices (see [Device Sync & Presence](/docs/using/collaboration/)).
- **Worker public keys are visible; private keys never leave the Worker.** The Worker registers only its public composite key with the Hub. Its private halves stay in the Worker's local state.
- **Agent and terminal state live only in the Worker's local SQLite database.** It is never uploaded to the Hub. This includes agent and subagent transcripts, to-do lists, and the background-task registry. See [Encryption & Data](/docs/operating/encryption-and-data/) for where that data lives and how to back it up.
- **The Worker tells the Hub nothing about the machine** — no hostname, OS, or path field exists in anything it registers or heartbeats. A different component does send one: `leapmux control` login registers a device name against the API token so you can recognize the device later, defaulting to `user@host` — often the same machine the Worker runs on. Pass `--device-name` at login to choose the label yourself.

## The E2EE protocol

Frontend↔Worker traffic is protected by a **hybrid post-quantum Noise_NK handshake**. "Hybrid" means it combines a classical algorithm with a post-quantum one for each security property, so that — in the protocol's own words — *security is maintained even if either the classical or PQ algorithm is broken*.

The channel is established in two stages, over two different transports. The Frontend first fetches the Worker's keys and encryption mode, then completes the Noise handshake through Hub-relayed unary RPCs — the Hub forwards each opaque handshake message to the Worker and the reply back. Only once a session exists does the Frontend attach it to the **shared WebSocket**, over which every channel's encrypted traffic is multiplexed. The WebSocket is therefore not the transport the handshake runs on.

Alongside that ciphertext, the same WebSocket carries the Hub's own control frames on a reserved `_hub` channel — deliberately **plaintext**, since the Hub originates them and they are addressed to your browser rather than to a Worker. They signal things like "the worker list changed, re-fetch it", and carry no channel content.

### Primitives

| Role | Classical | Post-quantum |
|------|-----------|--------------|
| Key exchange | X25519 ECDH | ML-KEM-1024 (FIPS 203) |
| Worker static-key authentication | (Noise NK pre-message) | SLH-DSA-SHAKE-256f (FIPS 205) signature over the transcript |
| Transport encryption | ChaCha20-Poly1305 AEAD | — |
| Hashing / key derivation | BLAKE2b | — |

The Noise protocol label is `Noise_NK_25519_ChaChaPoly_BLAKE2b`. The `NK` pattern means the responder (the **Worker**) has a known static key that the initiator (your **Frontend**) verifies, while the initiator stays anonymous. The Frontend learns the Worker's static key out-of-band from the Hub and checks it against its pin.

That anonymity is permanent: the initiator is never authenticated at the Noise layer, before or after the handshake. Who the caller is comes from the Hub, not from the channel — see "User identity binding" below.

### Why this design defeats a curious Hub

- The ML-KEM ciphertext is bound into the handshake hash, so tampering with it makes the next message's authentication fail.
- The Worker signs a transcript covering the handshake hash plus the ML-KEM material with its SLH-DSA private key. If the signature does not verify, the Frontend aborts the handshake and zeroes its handshake state — a Hub that altered the exchange cannot complete the handshake.
- Both the classical and post-quantum shared secrets are mixed into the final transport keys, so an attacker would have to break *both* X25519 and ML-KEM to recover the session.

### Transport hardening

The encrypted channel is not a fire-and-forget tunnel; it has built-in limits that bound the damage from desync, replay, and resource-exhaustion attempts:

| Property | Value | Effect |
|----------|-------|--------|
| Max plaintext per message | 65,519 bytes | Larger payloads are chunked |
| Nonce exhaustion | — | Past a soft threshold (2³¹ − 1) the initiator requests an in-band Noise rekey; past the hard ceiling (2³² − 1), both encryption and decryption refuse outright |
| Session key max age | 1 hour | Initiators request an in-band rekey; the same channel id and multiplexed connections stay up |
| Session key hard ceiling | 70 minutes | Past this per-epoch key age (reset on each successful rekey), initiators close and re-handshake rather than serve under the old key; the margin over max age covers one refused rekey |
| Min rekey interval | 50 minutes | Age-only rekeys inside this window are rejected (10 minutes of headroom under max age); soft-nonce still bypasses |
| Decrypt failure | — | Treated as unrecoverable: both sides close the channel |

The Hub enforces resource limits **without decrypting**: it caps the reassembled message size at the negotiated payload budget plus 64 KiB of envelope headroom (default ~16.06 MiB; operators may raise the payload budget up to 64 MiB via the Hub's `max_message_size_bytes` setting) and allows only one in-flight chunked message per channel and direction, so a peer cannot exhaust Hub memory through the opaque relay. The Worker also fast-rejects a duplicate channel ID *before* running the (expensive) post-quantum handshake, so a peer cannot amplify Worker CPU by replaying open requests.

**In-band rekey** rotates the channel's transport keys without closing it. The initiator proposes fresh key material — a new classical ephemeral and, on post-quantum channels, fresh ML-KEM material — keeps sending under the current key until the peer acknowledges, and only then switches. Both sides mix fresh Diffie–Hellman *and* post-quantum entropy into the next epoch, so compromising one epoch's key does not yield the next.

The exchange travels inside the already-encrypted channel, so the current cipher authenticates it and no extra signature is needed; the Hub relays it without decrypting, as it does everything else. A refused rekey leaves both sides on their existing keys. A short key-overlap window (~10 s) lets frames a peer encrypted just before the swap still decrypt afterwards, so traffic keeps flowing across the round trip — the Frontend, `leapmux control`, cross-worker links, and the desktop app's tunnels all share this, which is why port-forwards and SOCKS sessions survive hourly rotation without a stall.

Hub credential expiry still bounds bearer-token channels from the outside: CLI access tokens and delegation tokens live one hour. Desktop tunnels authorized by a sliding session cookie can stay open for days, and rekey is what bounds their *key epoch* without resetting multiplexed TCP connections. Hard nonce exhaustion remains fail-closed. The Frontend re-checks key age the next time it uses a channel, on a one-minute idle timer, and again when the page wakes from suspend — so a frozen clock cannot hide an over-age key. A rekey refused for being too early tells the initiator how long to wait rather than leaving it to guess.

Identity drift — the page's expected user no longer matching the Hub-authenticated channel user — still closes the channel and re-handshakes; that needs a new open, not a rekey. Rekey does **not** re-run the Hub's channel authorization: revoked credentials are torn down by the Hub's revocation watcher, not by the rekey path.

### Encryption modes

A Worker can run in one of two modes via `--encryption-mode`:

| Mode | Handshake |
|------|-----------|
| `post-quantum` (default) | Hybrid X25519 + ML-KEM-1024 + SLH-DSA |
| `classic` | X25519-only Noise_NK, no PQ |

The default is `post-quantum`. The Hub reports the Worker's live mode to the Frontend so the browser uses the matching handshake. There is rarely a reason to choose `classic`; do so only if you have a specific compatibility or performance constraint and understand you are giving up post-quantum protection. For the flag's accepted values, aliases, and fail-safe resolution, see [Configuration](/docs/operating/configuration/).

### User identity binding

Noise_NK does not authenticate the initiator, so the channel's encryption layer says nothing about who the caller is. The **Hub** establishes that instead: it authenticates the `OpenChannel` request and then tells both ends the same answer — the Frontend reads it from the `OpenChannel` response, and the Worker from the `ChannelOpened` notification. Every request the Worker dispatches carries that Hub-supplied identity.

The identity therefore never travels inside the channel, and the client never asserts one. That is deliberate: an in-channel claim would be an unauthenticated string the Worker could only check against the value the Hub had already given it — it would restate the Hub's answer rather than prove anything. Binding to the Hub's answer directly leaves no window in which a channel is open but unattributed, and no claim for a stale local session to get wrong.

This does mean the Hub is trusted for *identity* (it authenticates the user and names them to the Worker), while remaining unable to read channel *content*. Worker identity is not trusted to the Hub in the same way — see TOFU pinning below.

What the channel does verify for itself is that it *works*. Before `OpenChannel` returns, the client round-trips a no-op `Ping` through the encrypted session. The handshake alone only proves the client can encrypt to the Worker's static key; the Ping proves the Worker's session decrypts and that its replies decrypt back. Channels are pooled and reused, so without that round trip a session broken in either direction — a key mismatch, a corrupted handshake, a relay that mangled a frame — would open "successfully" and be handed to every later caller until something evicted it. The Ping keeps that failure at the open, where it is attributable.

### Channels don't outlive their credential

An open Noise session cannot outlive the credential that authorized it. The Hub force-closes the affected channels on logout, password change, account deletion, admin force-logout, and revocation of an API or delegation token. Detected refresh-token reuse revokes automatically, with no operator action.

Two details are worth knowing, because both are easy to assume wrong:

- **A password change spares the session you are changing it from.** That session's channels are restamped to the new authentication generation first, so the user-wide revocation that follows tears down every *other* session's channels but not the one in your hands.
- **Routine token rotation and profile edits do not close channels.** Rotating an API token's secret keeps the token row valid, so its channels are re-armed at the new expiry rather than dropped; a profile change (an admin-role update, say) only invalidates cached user data.

Teardown is immediate when the Hub handling the request is also the one holding the channel — logout, password change, and in-process token revocation land at once. Online admin operations (account deletion, force-logout) reach the Hub through the same authenticated RPC path, and their revocations land through a durable revocation ledger that every Hub replays. That is what makes revocation work across a multi-Hub deployment, at the cost of a brief propagation delay rather than a synchronous kill.

Revoke a credential with `leapmux control admin`: `api-token revoke`, `delegation-token revoke`, `session revoke`, or `session revoke-user`. See [`admin` — hub administration over RPC](/docs/operating/control-cli/#admin--hub-administration-over-rpc) for the flags, and [Remote Control CLI](/docs/operating/control-cli/) for how delegation tokens are used.

### What a delegation token can reach

A Worker mints a delegation token for the agent running in one of its tabs. The token carries the identity of that Worker's **owner** — the single user the Worker is registered to — and is bounded to the machines it may reach: the Worker that minted it, plus that owner's other Workers. It can never be aimed at someone else's machine. The bound is re-checked every time the token opens a channel, not only when it was minted.

On those machines the token can do whatever its owner could do from a browser. That includes the Worker RPCs that act on the machine rather than on a single tab — filesystem, git, tunnels, system info. Their scope is the whole host: paths are normalized and traversal is blocked, but nothing confines them to one project directory. This is how `leapmux control` normally works. It is also the exposure to weigh before you point a prompt-injectable agent at a Worker.

Every Worker RPC that touches data is **owner-only**: a Worker serves nobody but the user it is registered to. Because that is exactly one user, "the caller owns this Worker" and "the caller owns every tab this Worker holds" are the same statement. The one exception is the liveness ping, which does no work and discloses nothing.

That equivalence is also the shape of the exposure, so it is worth stating plainly: a delegation token is bounded by **owner and machine, not by workspace or tab**. An agent running in one workspace can reach every tab its owner holds on the machines it may reach — read another agent's messages, write to another terminal, close another workspace's tab — and can submit layout changes across all of that owner's workspaces.

The machine bound does not narrow what the token sees at the **Hub**, either. It authenticates as its owner there, so it can list that owner's workspaces and tabs and resolve any of them by id — the whole inventory, not the workspace it was minted in. Treat a leaked delegation token as disclosing what the account contains, not just what one project does.

All of one user's own work is a single trust domain from an agent's point of view. If you need a stronger boundary than that, use a separate user rather than a separate workspace.

> **Note:** The Worker's check is defence in depth. The Hub already authorizes a channel to a Worker only for that Worker's own owner, so every delegation token that reaches it carries the right identity in the first place. The Worker verifies it anyway rather than trusting the Hub to have gotten it right.

## Worker identity and TOFU pinning

Each Worker has a persistent **composite static keypair** (X25519 + ML-KEM-1024 + SLH-DSA-SHAKE-256f), generated on first run and stored in the Worker's local state. The Hub stores only the public halves.

The Frontend pins this identity **TOFU** ("trust on first use"). On the first connection to a Worker, the browser records the Worker's composite public key. On every later connection it compares the key the Hub hands over against the pinned one:

- **First use** — no pin exists, so the handshake proceeds and the key is recorded once it succeeds. This is TOFU's weak point: everything afterwards is measured against whatever was pinned here, so verify the fingerprint out-of-band on first connect if you can.
- **Match** — the connection proceeds silently.
- **Mismatch** — the Frontend stops and asks you to decide. Reject once and that Worker is refused for the rest of the browser session without prompting again; reload to be asked afresh.

This is what defeats a compromised Hub. Because the Hub is the party that tells your browser the Worker's key, a malicious Hub might try to substitute its own key and impersonate the Worker. TOFU pinning catches that: the substituted key won't match the pin, and you get an explicit prompt instead of a silent man-in-the-middle.

### The "Worker public key changed" dialog

When a mismatch occurs, the Frontend shows a dialog titled **"Worker public key changed"**. The dialog identifies the Worker, and it states that the key changed since the last connection. It also states that the cause is either a legitimate key rotation or a security problem.

It displays an **Expected:** fingerprint and an **Actual:** fingerprint, and it tells you to reject the connection and to verify the Worker's identity out-of-band before you accept an unexpected change. Two buttons are offered — **Reject** and **Accept** (the Accept button is styled as a danger action). Dismissing the dialog counts as Reject. If the confirmation UI is not available for any reason, the transport defaults to reject (fail-closed).

> **Tip:** The fingerprints are **4 dash-joined English words** derived from a hash of the Worker's composite public key (for example, `deep-idea-obey-tack`). Every word is drawn from a fixed 256-word list and is exactly four letters, so a fingerprint is always the same shape and easy to read aloud. The wordlist is identical across the browser and the Worker, so you can read the fingerprint over a trusted out-of-band channel (a phone call, an in-person check) and confirm it matches before accepting a changed key.

### When to accept and when to reject

- **Accept** only if you *expected* the change — for example, you deliberately re-generated the Worker's identity, or you wiped and re-registered the Worker. Verify the fingerprint out-of-band first.
- **Reject** if the change is unexpected. A surprise key change on a Worker you didn't touch is exactly the signal TOFU pinning exists to surface.

In the browser, the pin is kept for one year and refreshed on use. Pin management from the browser UI is limited; for the non-browser clients there are dedicated CLI pin stores covered in [Managing Workers](/docs/operating/managing-workers/):

- Worker-to-Worker (cross-worker) pins, cleared with `leapmux worker cross-worker-pins remove --target-worker-id=<id>`.
- `leapmux control` CLI pins, cleared with `leapmux control worker pins remove --worker-id=<id>`.

Both follow the same rule: first contact auto-pins, any later mismatch aborts the connection until you explicitly clear the pin.

## Solo mode: a reduced threat model

Solo mode collapses the trust boundary on purpose. It runs the Hub and the Worker **in the same process**, by default on `127.0.0.1:4327`, with **no authentication** — every request is auto-authenticated as the admin. Any local process that can reach the port can drive the Worker. (This applies to solo mode only. Dev mode uses real password authentication, which is why the warning below never fires for it.)

So in solo mode the threat model reduces to **local-host trust**. The E2EE channel, the composite keypair, and TOFU pinning all still operate end-to-end inside the single process, but that protocol-level separation offers **no protection against a local attacker** who can reach the loopback port.

> **Warning:** If you point solo mode at a non-loopback address, LeapMux warns you at startup. The warning states that every request is auto-authenticated as the admin, so anyone who can reach the port has full admin access without credentials. It also tells you to restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or to run `leapmux hub` for real authentication.
>
> Heed it. If you need authentication, run `leapmux hub` (distributed mode) instead of exposing solo mode. See [Running LeapMux](/docs/operating/running-leapmux/) for the differences between run modes.

The bundled Worker that solo and dev modes auto-register is created in-process and flagged as auto-registered; it deliberately bypasses the registration-key flow, since presenting a bearer token to a local in-process RPC would be security theatre.

The **desktop app** avoids the exposure differently: it always starts its in-process Hub with the TCP listener disabled, reaching it over a local Unix socket (named pipe on Windows) instead. There is no `--no-tcp` flag or setting — it is how the desktop app is built, and `leapmux solo` on the command line does not do it. So the desktop app opens no loopback port for its Hub, and the non-loopback warning above cannot apply to it. Tunnels you create yourself still bind a loopback TCP port, by design.

## Passkey step-up

Passkey management — adding, removing, or disabling passkeys — is treated as a **privileged account change**, not a casual settings tweak. The hub enforces a step-up check before any of these mutations land:

| Your account today | Step-up required |
| --- | --- |
| Password set | Confirm your **current password**. |
| Passkey-only (no password) | Authenticate with an **existing passkey** (a short-lived reauth proof). |
| Removing your last passkey | Passkey step-up **and** setting a new password (you must retain *some* sign-in method). |
| Disabling passkey sign-in entirely | Password confirmation, or passkey step-up plus setting a password when you have none. |

Self-service **password reset** and admin **reset-password** both **delete every passkey** on the account. That is deliberate: a password reset is break-glass recovery, and leaving old passkeys registered would let someone who still holds a device sign back in without knowing the new password.

WebAuthn ceremony state (in-flight sign-up, login, and reauth handshakes) is encrypted at rest with the same keystore as passkey public keys; see [Encryption & Data](/docs/operating/encryption-and-data/).

## At-rest encryption (separate from E2EE)

Distinct from the channel E2EE above, the Hub encrypts a small set of stored secrets **at rest** using a versioned XChaCha20-Poly1305 key ring kept in an `encryption.key` file (mode `0600`, default `<DataDir>/encryption.key`, auto-generated on first run). Exactly these are encrypted:

- OAuth provider client secrets and per-user OAuth access/refresh tokens (including pending-signup tokens).
- **Passkey credential public keys** and **WebAuthn ceremony session payloads** (each row's ciphertext is bound to that row's id as additional authenticated data).

If the Hub's database is exfiltrated without the key file, those fields stay unreadable.

Be clear on what this does **not** cover, since "encrypted at rest" invites over-reading. It is not the Frontend↔Worker channel keys, and it does not touch agent or terminal content (which never reaches the Hub at all). Other credentials in the database are protected by their storage form rather than this key: passwords are Argon2id hashes and API/delegation token secrets are HMAC hashes — neither is reversible, so neither needs encrypting. Worker auth tokens, registration keys, and session tokens are stored **as-is**.

The key ring is managed with `leapmux recover encryption-key rotate | remove | reencrypt | rotate-pepper`. The full keystore, key-rotation runbook, database backends, and backup/restore guidance live in [Encryption & Data](/docs/operating/encryption-and-data/).

> **Warning:** The `encryption.key` file holds more than the encryption key ring. It also carries the **token pepper** — the HMAC key for every API and delegation token secret. Two consequences follow. The file and the database are a matched pair that must be backed up together: without the key file, the encrypted columns are permanently undecryptable. And `rotate-pepper` invalidates every API and delegation token at once — it takes effect on the next Hub restart, since a running Hub holds the pepper in memory from startup. Sessions, Worker auth tokens, and registration keys do not use the pepper and survive a rotation. Losing the file is therefore not only an OAuth-data loss; it takes every issued token with it.

## Recommendations for operators

If you run a Hub for a team, the security of the deployment rests largely on the host and a few files. Concrete steps:

1. **Protect the Hub host.** It can read all control-plane data — accounts, workspace records, layout, Worker registration metadata — and it sees transport metadata for every channel (traffic analysis is in scope). Treat it as a sensitive service: minimal access, patched OS, monitored.
2. **Terminate TLS in front of the Hub.** The Frontend↔Hub and Worker↔Hub legs are not E2EE; they rely on transport TLS. Put the Hub behind a reverse proxy with valid certificates. See [Running LeapMux](/docs/operating/running-leapmux/).
3. **Guard the `encryption.key` file like a top-grade secret.** It is base64 key material in a plain text file at mode `0600` — there is no master password, KMS, or HSM wrapping, so filesystem permissions are the only thing protecting it. It holds both the encryption key ring and the token pepper, so whoever reads it can decrypt the OAuth columns *and* forge the hash of any API or delegation token. Back it up with the database, store both encrypted, and restrict access.
4. **Rotate encryption keys deliberately.** Use `rotate` → restart → `reencrypt`, and never `remove` an old version before re-encryption has migrated every row. The exact runbook is in [Encryption & Data](/docs/operating/encryption-and-data/).
5. **Never expose solo mode beyond loopback** for real use. If you bound it to a non-loopback address, you exposed unauthenticated admin access. Run `leapmux hub` for authenticated multi-user deployments, and firewall or tunnel any non-loopback access. See [Configuration](/docs/operating/configuration/) for listen addresses.
6. **Mint registration keys carefully.** A valid registration key immediately produces an active Worker — there is no separate approval queue, so possession of a live key *is* the gate. Keys are single-use, expire 5 minutes after issue, and the UI dialog destroys the key when closed. Note the 5 minutes is per issuance, not a hard lifetime: an open registration dialog auto-extends its key as expiry approaches, so a key stays live as long as the dialog is open. Treat them as one-time secrets, deliver them over a trusted channel, and close the dialog when you are done. See [Managing Workers](/docs/operating/managing-workers/).
7. **Teach users to take the key-change dialog seriously.** The "Worker public key changed" prompt is the user-facing line of defense against a Hub swapping a Worker. Users should reject unexpected changes and verify the 4-word fingerprint out-of-band before ever accepting.
8. **Revoke credentials when needed, and know it tears down channels.** Revocation force-closes the affected user's open channels; see [Channels don't outlive their credential](#channels-dont-outlive-their-credential) for which operations do it and the two cases that behave unexpectedly. Run these operations with `leapmux control admin`: `session revoke-user`, `api-token revoke`, and `delegation-token revoke`. See [`admin` — hub administration over RPC](/docs/operating/control-cli/#admin--hub-administration-over-rpc).

## Quick reference

The facts an operator looks up most often. The full crypto primitives are in the [Primitives](#primitives) table above; identity pinning and encryption modes are covered in their own sections.

| Property | Value |
|----------|-------|
| Noise protocol label | `Noise_NK_25519_ChaChaPoly_BLAKE2b` |
| Worker encryption mode flag | `--encryption-mode classic` \| `post-quantum` (default `post-quantum`) |
| Solo mode default bind | `127.0.0.1:4327`, no authentication (local trust only) |
| At-rest secret key file | `encryption.key` (mode `0600`, default `<DataDir>/encryption.key`) |

See also: [Managing Workers](/docs/operating/managing-workers/) · [Encryption & Data](/docs/operating/encryption-and-data/) · [Authentication Providers](/docs/operating/authentication-providers/) · [Accounts & Authentication](/docs/using/accounts/) · [Running LeapMux](/docs/operating/running-leapmux/).
