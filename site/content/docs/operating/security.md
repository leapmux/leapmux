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

The key consequence: control-plane data (accounts, org/workspace records, layout, Worker registration) reaches the Hub in a form it can read, while everything you actually do inside an agent or terminal travels inside an encrypted channel the Hub merely forwards.

> **Note:** "End-to-end" here means the two ends are your browser (the Frontend) and the Worker daemon. The Hub is the middle. See [Concepts & Architecture](/docs/getting-started/concepts/) for how these components fit together and [Running LeapMux](/docs/operating/running-leapmux/) for how to launch each one.

## What the Hub can and cannot see

The two columns below are the heart of the threat model. Treat the left column as data you are entrusting to whoever runs the Hub, and the right column as data that never leaves your encrypted channel.

| The Hub **can** see | The Hub **cannot** see |
|---------------------|------------------------|
| Account metadata: user names, emails, password hashes, OAuth tokens, session tokens | Agent chat transcripts, tool-call arguments, or tool outputs |
| Organization and workspace records | Terminal I/O, shell history, or PTY state |
| Workspace **titles**, tab positions, and tiling layout geometry | File contents, diffs, or git status |
| Worker registration data: Worker ID, composite public keys, online status, last-seen time | Worker hostname, OS, or filesystem paths (sent only inside the encrypted channel) |
| Per-message transport metadata: channel ID, correlation ID, ciphertext size, timing | Any plaintext of Frontend↔Worker traffic |

> **Warning:** **Traffic analysis is in scope.** The Hub observes message timing, sizes, and which channel correlates to which Worker. It cannot read content, but it can infer activity patterns — when you are working, how much you are typing, which Worker is busy. If that metadata is itself sensitive in your environment, treat the Hub host accordingly.

A few specifics worth internalizing:

- **Workspace titles are visible, agent content is not.** Name your workspaces with that in mind. Tab positions and tiling geometry are layout metadata the Hub stores so your arrangement can sync across devices (see [Device Sync & Presence](/docs/using/collaboration/)).
- **Worker public keys are visible; private keys never leave the Worker.** The Worker registers only its public composite key with the Hub. Its private halves stay in the Worker's local state.
- **Agent and terminal state live only in the Worker's local SQLite database.** It is never uploaded to the Hub. See [Encryption & Data](/docs/operating/encryption-and-data/) for where that data lives and how to back it up.
- **The Worker tells the Hub nothing about the machine** — no hostname, OS, or path field exists in anything it registers or heartbeats. One thing does leak a hostname, though, and it is a different component: logging in with `leapmux remote` registers a device name so you can recognize the device later, defaulting to `user@host` from the machine's hostname and username, and the Hub stores it against the API token. That is the CLI's own machine, which is often the same box. Pass `--device-name` at login to choose the label yourself.

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
- The Worker signs a transcript covering the handshake hash plus the ML-KEM material with its SLH-DSA private key. If the signature does not verify, the Frontend aborts with `noise-hybrid: SLH-DSA signature verification failed` and zeroes its handshake state — a Hub that altered the exchange cannot complete the handshake.
- Both the classical and post-quantum shared secrets are mixed into the final transport keys, so an attacker would have to break *both* X25519 and ML-KEM to recover the session.

### Transport hardening

The encrypted channel is not a fire-and-forget tunnel; it has built-in limits that bound the damage from desync, replay, and resource-exhaustion attempts:

| Property | Value | Effect |
|----------|-------|--------|
| Max plaintext per message | 65,519 bytes | Larger payloads are chunked |
| Nonce exhaustion | — | Past a soft threshold the Frontend re-handshakes the channel; past a hard ceiling, both encryption and decryption refuse outright |
| Channel max age | 1 hour | The Frontend closes and re-handshakes channels older than this |
| Decrypt failure | — | Treated as unrecoverable: both sides close the channel |

The Hub enforces resource limits **without decrypting**: it caps the reassembled message size (17 MiB) and allows only one in-flight chunked message per channel and direction, so a peer cannot exhaust Hub memory through the opaque relay. The Worker also fast-rejects a duplicate channel ID *before* running the (expensive) post-quantum handshake, so a peer cannot amplify Worker CPU by replaying open requests.

Rekey and max-age are **Frontend** behaviors specifically, and both are checked lazily — when the Frontend reuses a pooled channel, not on a background timer. An idle channel is rotated at next use rather than on schedule.

The Go clients (`leapmux remote`, cross-worker connections, and the desktop app's tunnels) do not rotate themselves. They do not need to, because the Hub bounds a channel from the outside: **every channel is armed to expire with the credential that opened it**. A CLI access token and a Worker-minted delegation token both live one hour, so those channels are torn down and re-established at least that often no matter what the client does — the same ceiling the Frontend applies to itself, enforced by the side that actually knows when the credential dies.

The desktop app's tunnel channel is the one exception worth naming: it is authorized by a session that slides on use, so it can stay open for days. It deliberately does not rotate, because a single channel multiplexes every live TCP connection through that Worker — re-handshaking would reset your port-forward or SOCKS5 session, and unlike the Frontend's cursor-resumable event streams, a relayed TCP connection cannot be resumed. The nonce ceiling is the backstop, and it is roughly 128 TiB of tunnelled traffic away on one uninterrupted channel; if it were ever reached, the channel fails closed and re-opens rather than reusing a nonce.

> **Note:** Protocol internals, not operational knobs: the soft rekey threshold is a nonce counter of 2³¹ − 1 and the hard ceiling — at which the session refuses to encrypt *or* decrypt — is 2³² − 1, and the Hub additionally rejects interleaved chunks — a chunk for a second correlation id while one is still in progress on that channel and direction — which is what holds it to a single in-flight chunked message there. These bounds matter for the protocol but not for day-to-day operation.

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

Teardown is immediate when the Hub handling the request is also the one holding the channel — logout, password change, and in-process token revocation land at once. Admin CLI operations (account deletion, force-logout) run in a separate process, so they reach the Hub through a durable revocation ledger that every Hub replays. That is what makes revocation work across a multi-Hub deployment, at the cost of a brief propagation delay rather than a synchronous kill.

See [Admin CLI](/docs/operating/admin-cli/) for token revocation and [Remote Control CLI](/docs/operating/remote-control-cli/) for how delegation tokens are used.

### What a delegation token can reach

A delegation token is minted by a Worker for the agent running in one of its tabs, and it always carries the identity of the **workspace owner** — the same user the Worker is registered to, since workspace access is owner-only. The token is bounded on two independent axes — both re-checked when a channel opens, not just when the token was minted:

- **Workspace.** The token is pinned to the single workspace it was minted for, and the pin is re-verified at open time, so a workspace deleted since the mint is caught at use.
- **Worker.** The token may open a channel back to the Worker that minted it, and to the owner's other Workers. It cannot be aimed at a third party's machine.

The machine-scoped Worker RPCs — filesystem, git, tunnels, and system info — are **owner-only**: the Worker serves them solely to the user it is registered to. Their reach is the host rather than a workspace (a path is normalized and traversal-blocked, but not confined to a root), which is the owner's own access by definition and nobody else's. A delegation token carries the owner's identity and may therefore call them — that is the ordinary `leapmux remote` case.

> **Note:** These bounds are defence in depth. The Hub authorizes a channel to a Worker only for that Worker's own owner, so every tab on a Worker belongs to its owner and every delegation token carries that owner's identity in the first place. The Worker enforces the bounds anyway rather than trusting the Hub to have gotten it right.

## Worker identity and TOFU pinning

Each Worker has a persistent **composite static keypair** (X25519 + ML-KEM-1024 + SLH-DSA-SHAKE-256f), generated on first run and stored in the Worker's local state. The Hub stores only the public halves.

The Frontend pins this identity **TOFU** ("trust on first use"). On the first connection to a Worker, the browser records the Worker's composite public key. On every later connection it compares the key the Hub hands over against the pinned one:

- **First use** — no pin exists, so the handshake proceeds and the key is recorded once it succeeds.
- **Match** — the connection proceeds silently.
- **Mismatch** — the Frontend stops and asks you to decide. Reject once and that Worker is refused for the rest of the browser session without prompting again; reload to be asked afresh.

This is what defeats a compromised Hub. Because the Hub is the party that tells your browser the Worker's key, a malicious Hub might try to substitute its own key and impersonate the Worker. TOFU pinning catches that: the substituted key won't match the pin, and you get an explicit prompt instead of a silent man-in-the-middle.

### The "Worker public key changed" dialog

When a mismatch occurs, the Frontend shows a dialog titled **"Worker public key changed"**:

> The public key for worker `<workerId>` has changed since the last connection. This could indicate a legitimate key rotation or a potential security issue.

It displays an **Expected:** fingerprint and an **Actual:** fingerprint, and warns: *"If you did not expect this change, reject the connection and verify the worker's identity before accepting."* Two buttons are offered — **Reject** and **Accept** (the Accept button is styled as a danger action). Dismissing the dialog counts as Reject. If the confirmation UI is not available for any reason, the transport defaults to reject (fail-closed).

> **Tip:** The fingerprints are **4 dash-joined English words** derived from a hash of the Worker's composite public key (for example, `deep-idea-obey-tack`). Every word is drawn from a fixed 256-word list and is exactly four letters, so a fingerprint is always the same shape and easy to read aloud. The wordlist is identical across the browser and the Worker, so you can read the fingerprint over a trusted out-of-band channel (a phone call, an in-person check) and confirm it matches before accepting a changed key.

### When to accept and when to reject

- **Accept** only if you *expected* the change — for example, you deliberately re-generated the Worker's identity, or you wiped and re-registered the Worker. Verify the fingerprint out-of-band first.
- **Reject** if the change is unexpected. A surprise key change on a Worker you didn't touch is exactly the signal TOFU pinning exists to surface.

In the browser, the pin is kept for one year and refreshed on use. Pin management from the browser UI is limited; for the non-browser clients there are dedicated CLI pin stores covered in [Managing Workers](/docs/operating/managing-workers/):

- Worker-to-Worker (cross-worker) pins, cleared with `leapmux worker cross-worker-pins remove --target-worker-id=<id>`.
- `leapmux remote` CLI pins, cleared with `leapmux remote worker pins remove --worker-id=<id>`.

Both follow the same rule: first contact auto-pins, any later mismatch aborts the connection until you explicitly clear the pin.

## Solo mode: a reduced threat model

Solo mode collapses the trust boundary on purpose. It runs the Hub and the Worker **in the same process**, by default on `127.0.0.1:4327`, with **no authentication** — every request is auto-authenticated as the admin. Any local process that can reach the port can drive the Worker. (This applies to solo mode only. Dev mode uses real password authentication, which is why the warning below never fires for it.)

So in solo mode the threat model reduces to **local-host trust**. The E2EE channel, the composite keypair, and TOFU pinning all still operate end-to-end inside the single process, but that protocol-level separation offers **no protection against a local attacker** who can reach the loopback port.

> **Warning:** If you point solo mode at a non-loopback address, LeapMux warns you at startup:
>
> > solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, so anyone who can reach this port has full admin access without credentials. Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication.
>
> Heed it. If you need authentication, run `leapmux hub` (distributed mode) instead of exposing solo mode. See [Running LeapMux](/docs/operating/running-leapmux/) for the differences between run modes.

The bundled Worker that solo and dev modes auto-register is created in-process and flagged as auto-registered; it deliberately bypasses the registration-key flow, since presenting a bearer token to a local in-process RPC would be security theatre.

The **desktop app** avoids the exposure differently: it always starts its in-process Hub with the TCP listener disabled, reaching it over a local Unix socket (named pipe on Windows) instead. There is no `--no-tcp` flag or setting — it is how the desktop app is built, and `leapmux solo` on the command line does not do it. So the desktop app opens no loopback port for its Hub, and the non-loopback warning above cannot apply to it. Tunnels you create yourself still bind a loopback TCP port, by design.

## At-rest encryption (separate from E2EE)

Distinct from the channel E2EE above, the Hub encrypts a small set of stored secrets **at rest** using a versioned XChaCha20-Poly1305 key ring kept in an `encryption.key` file (mode `0600`, default `<DataDir>/encryption.key`, auto-generated on first run). Exactly three things are encrypted, all of them OAuth secrets: the OAuth provider client secrets, per-user OAuth access/refresh tokens, and the access/refresh tokens held for a pending signup. If the Hub's database is exfiltrated without the key file, those stay unreadable.

Be clear on what this does **not** cover, since "encrypted at rest" invites over-reading. It is not the Frontend↔Worker channel keys, and it does not touch agent or terminal content (which never reaches the Hub at all). Other credentials in the database are protected by their storage form rather than this key: passwords are Argon2id hashes and API/delegation token secrets are HMAC hashes — neither is reversible, so neither needs encrypting. Worker auth tokens, registration keys, and session tokens are stored **as-is**.

The key ring is managed with `leapmux admin encryption-key rotate | remove | reencrypt | rotate-pepper`. The full keystore, key-rotation runbook, database backends, and backup/restore guidance live in [Encryption & Data](/docs/operating/encryption-and-data/).

> **Warning:** The `encryption.key` file holds more than the encryption key ring. It also carries the **token pepper** — the HMAC key for every API and delegation token secret. Two consequences follow. The file and the database are a matched pair that must be backed up together: without the key file, the encrypted columns are permanently undecryptable. And `rotate-pepper` invalidates every API and delegation token at once — it takes effect on the next Hub restart, since a running Hub holds the pepper in memory from startup. Sessions, Worker auth tokens, and registration keys do not use the pepper and survive a rotation. Losing the file is therefore not only an OAuth-data loss; it takes every issued token with it.

## Recommendations for operators

If you run a Hub for a team, the security of the deployment rests largely on the host and a few files. Concrete steps:

1. **Protect the Hub host.** It can read all control-plane data — accounts, org/workspace records, layout, Worker registration metadata — and it sees transport metadata for every channel (traffic analysis is in scope). Treat it as a sensitive service: minimal access, patched OS, monitored.
2. **Terminate TLS in front of the Hub.** The Frontend↔Hub and Worker↔Hub legs are not E2EE; they rely on transport TLS. Put the Hub behind a reverse proxy with valid certificates. See [Running LeapMux](/docs/operating/running-leapmux/).
3. **Guard the `encryption.key` file like a top-grade secret.** It is base64 key material in a plain text file at mode `0600` — there is no master password, KMS, or HSM wrapping, so filesystem permissions are the only thing protecting it. It holds both the encryption key ring and the token pepper, so whoever reads it can decrypt the OAuth columns *and* forge the hash of any API or delegation token. Back it up with the database, store both encrypted, and restrict access.
4. **Rotate encryption keys deliberately.** Use `rotate` → restart → `reencrypt`, and never `remove` an old version before re-encryption has migrated every row. The exact runbook is in [Encryption & Data](/docs/operating/encryption-and-data/).
5. **Never expose solo mode beyond loopback** for real use. If you bound it to a non-loopback address, you exposed unauthenticated admin access. Run `leapmux hub` for authenticated multi-user deployments, and firewall or tunnel any non-loopback access. See [Configuration](/docs/operating/configuration/) for listen addresses.
6. **Mint registration keys carefully.** A valid registration key immediately produces an active Worker — there is no separate approval queue, so possession of a live key *is* the gate. Keys are single-use, expire 5 minutes after issue, and the UI dialog destroys the key when closed. Note the 5 minutes is per issuance, not a hard lifetime: an open registration dialog auto-extends its key as expiry approaches, so a key stays live as long as the dialog is open. Treat them as one-time secrets, deliver them over a trusted channel, and close the dialog when you are done. See [Managing Workers](/docs/operating/managing-workers/).
7. **Teach users to take the key-change dialog seriously.** The "Worker public key changed" prompt is the user-facing line of defense against a Hub swapping a Worker. Users should reject unexpected changes and verify the 4-word fingerprint out-of-band before ever accepting.
8. **Revoke credentials when needed, and know it tears down channels.** Logout, password changes, account deletion, force-logout, and token revocation all force-close the affected user's open channels — a password change spares only the session it was made from. Use the [Admin CLI](/docs/operating/admin-cli/) for these operations.

## Recommendations for security-conscious users

- **Verify a Worker's fingerprint on first connect** if you can, via a trusted out-of-band channel, before you start trusting that pin.
- **Reject, don't reflexively accept,** when the "Worker public key changed" dialog appears unexpectedly.
- **Remember the Hub sees workspace titles and activity metadata.** Don't put secrets in workspace names, and recall that *when* and *how much* you work is observable even though the content is not.

## Quick reference

The facts an operator looks up most often. The full crypto primitives are in the [Primitives](#primitives) table above; identity pinning and encryption modes are covered in their own sections.

| Property | Value |
|----------|-------|
| Noise protocol label | `Noise_NK_25519_ChaChaPoly_BLAKE2b` |
| Worker encryption mode flag | `--encryption-mode classic` \| `post-quantum` (default `post-quantum`) |
| Solo mode default bind | `127.0.0.1:4327`, no authentication (local trust only) |
| At-rest secret key file | `encryption.key` (mode `0600`, default `<DataDir>/encryption.key`) |

See also: [Managing Workers](/docs/operating/managing-workers/) · [Encryption & Data](/docs/operating/encryption-and-data/) · [Authentication Providers](/docs/operating/authentication-providers/) · [Accounts & Authentication](/docs/using/accounts/) · [Running LeapMux](/docs/operating/running-leapmux/).
