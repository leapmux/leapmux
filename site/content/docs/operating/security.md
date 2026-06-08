---
title: "Security & Threat Model"
description: "LeapMux trust model and end-to-end encryption: how browser-to-agent traffic is protected, how Worker identity is pinned, and the steps to operate it safely."
type: docs
weight: 7
---

This chapter is the security reference for security-conscious users and operators. It describes the trust model LeapMux assumes, the end-to-end encryption (E2EE) that protects Frontend↔Worker traffic, how Worker identity is pinned, what changes in solo mode, what sharing a workspace does and does not expose, and the concrete steps you should take to operate LeapMux safely.

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
| Frontend → Worker | Hybrid post-quantum Noise_NK, multiplexed over a single WebSocket relayed through the Hub | End-to-end encrypted; the Hub cannot decrypt |
| Worker → Hub | ConnectRPC over the gRPC protocol, bidirectional streaming; the Worker always dials out (NAT-friendly, no inbound ports) | TLS in front of the Hub; channel payloads ride inside the E2EE tunnel |

The key consequence: control-plane data (accounts, org/workspace records, layout, Worker registration) reaches the Hub in a form it can read, while everything you actually do inside an agent or terminal travels inside an encrypted channel the Hub merely forwards.

> **Note:** "End-to-end" here means the two ends are your browser (the Frontend) and the Worker daemon. The Hub is the middle. See [Concepts & Architecture](/docs/getting-started/concepts/) for how these components fit together and [Running LeapMux](/docs/operating/running-leapmux/) for how to launch each one.

## What the Hub can and cannot see

The two columns below are the heart of the threat model. Treat the left column as data you are entrusting to whoever runs the Hub, and the right column as data that never leaves your encrypted channel.

| The Hub **can** see | The Hub **cannot** see |
|---------------------|------------------------|
| Account metadata: user names, emails, password hashes, OAuth tokens, session tokens | Agent chat transcripts, tool-call arguments, or tool outputs |
| Organization, workspace, and membership records | Terminal I/O, shell history, or PTY state |
| Workspace **titles**, tab positions, and tiling layout geometry | File contents, diffs, or git status |
| Worker registration data: Worker ID, composite public keys, online status, last-seen time | Worker hostname, OS, or filesystem paths (sent only inside the encrypted channel) |
| Per-message transport metadata: channel ID, correlation ID, ciphertext size, timing | Any plaintext of Frontend↔Worker traffic |

> **Warning:** **Traffic analysis is in scope.** The Hub observes message timing, sizes, and which channel correlates to which Worker. It cannot read content, but it can infer activity patterns — when you are working, how much you are typing, which Worker is busy. If that metadata is itself sensitive in your environment, treat the Hub host accordingly.

A few specifics worth internalizing:

- **Workspace titles are visible, agent content is not.** Name your workspaces with that in mind. Tab positions and tiling geometry are layout metadata the Hub stores so your arrangement can sync across devices (see [Collaboration & Presence](/docs/using/collaboration/)).
- **Worker public keys are visible; private keys never leave the Worker.** The Worker registers only its public composite key with the Hub. Its private halves stay in the Worker's local state.
- **Agent and terminal state live only in the Worker's local SQLite database.** It is never uploaded to the Hub. See [Encryption & Data](/docs/operating/encryption-and-data/) for where that data lives and how to back it up.

## The E2EE protocol

Frontend↔Worker traffic uses a **hybrid post-quantum Noise_NK handshake**, multiplexed over a single relayed WebSocket. "Hybrid" means it combines a classical algorithm with a post-quantum one for each security property, so that — in the protocol's own words — *security is maintained even if either the classical or PQ algorithm is broken*.

### Primitives

| Role | Classical | Post-quantum |
|------|-----------|--------------|
| Key exchange | X25519 ECDH | ML-KEM-1024 (FIPS 203) |
| Worker static-key authentication | (Noise NK pre-message) | SLH-DSA-SHAKE-256f (FIPS 205) signature over the transcript |
| Transport encryption | ChaCha20-Poly1305 AEAD | — |
| Hashing / key derivation | BLAKE2b | — |

The Noise protocol label is `Noise_NK_25519_ChaChaPoly_BLAKE2b`. The `NK` pattern means the responder (the **Worker**) has a known static key that the initiator (your **Frontend**) verifies, while the initiator stays anonymous at the Noise layer. The Frontend learns the Worker's static key out-of-band from the Hub and verifies it; it then proves *its own* identity to the Worker after the handshake (see "User identity binding" below).

### Why this design defeats a curious Hub

- The ML-KEM ciphertext is bound into the handshake hash, so tampering with it makes the next message's authentication fail.
- The Worker signs a transcript covering the handshake hash plus the ML-KEM material with its SLH-DSA private key. If the signature does not verify, the Frontend aborts with `noise-hybrid: SLH-DSA signature verification failed` and zeroes its handshake state — a Hub that altered the exchange cannot complete the handshake.
- Both the classical and post-quantum shared secrets are mixed into the final transport keys, so an attacker would have to break *both* X25519 and ML-KEM to recover the session.

### Transport hardening

The encrypted channel is not a fire-and-forget tunnel; it has built-in limits that bound the damage from desync, replay, and resource-exhaustion attempts:

| Property | Value | Effect |
|----------|-------|--------|
| Max plaintext per message | 65,519 bytes | Larger payloads are chunked |
| Nonce exhaustion | — | The channel auto-rekeys (re-handshakes) well before the nonce counter is exhausted, and refuses to encrypt or decrypt once a hard ceiling is reached |
| Channel max age | 1 hour | The Frontend closes and re-handshakes channels older than this |
| Decrypt failure | — | Treated as unrecoverable: both sides close the channel |

The Hub enforces resource limits **without decrypting**: it caps the reassembled message size (16 MiB) and bounds the number of in-flight chunked messages, so a peer cannot exhaust Hub memory through the opaque relay. The Worker also fast-rejects a duplicate channel ID *before* running the (expensive) post-quantum handshake, so a peer cannot amplify Worker CPU by replaying open requests.

> **Note:** Protocol internals, not operational knobs: the soft rekey threshold is a nonce counter of 2³¹ − 1 and the hard refuse-to-encrypt ceiling is 2³² − 1, and the Hub additionally limits the in-flight chunk count per channel and direction and rejects interleaved chunks. These bounds matter for the protocol but not for day-to-day operation.

### Encryption modes

A Worker can run in one of two modes via `--encryption-mode`:

| Mode | Handshake |
|------|-----------|
| `post-quantum` (default) | Hybrid X25519 + ML-KEM-1024 + SLH-DSA |
| `classic` | X25519-only Noise_NK, no PQ |

The default is `post-quantum`. The Hub reports the Worker's live mode to the Frontend so the browser uses the matching handshake. There is rarely a reason to choose `classic`; do so only if you have a specific compatibility or performance constraint and understand you are giving up post-quantum protection. For the flag's accepted values, aliases, and fail-safe resolution, see [Configuration](/docs/operating/configuration/).

### User identity binding

Because Noise_NK does not authenticate the initiator's static key, the Frontend proves who it is *after* the handshake. The first encrypted inner message it sends is a `UserIdClaim` carrying the authenticated user ID. The Worker checks that claim against the user ID the Hub announced when the channel was opened:

- If they match, the channel is marked verified and normal requests proceed.
- If they mismatch, the Worker rejects with `user ID mismatch` and closes the channel.
- Any request that arrives before verification is refused with `user ID not verified`.

This binds the encrypted channel to the authenticated user even though the encryption layer itself leaves the initiator anonymous.

### Channels don't outlive their credential

The Hub force-closes a user's open channels when the credential that authorized them is revoked — a password change, account deletion, admin force-logout, or a revoked API/delegation token. An open Noise session cannot survive the bearer that authorized it. Delegation-token channels are additionally pinned to a single workspace, re-verified at open time, so a stolen delegation bearer cannot pivot beyond its mint scope. See [Admin CLI](/docs/operating/admin-cli/) for token revocation and [Remote Control CLI](/docs/operating/remote-control-cli/) for how delegation tokens are used.

## Worker identity and TOFU pinning

Each Worker has a persistent **composite static keypair** (X25519 + ML-KEM-1024 + SLH-DSA-SHAKE-256f), generated on first run and stored in the Worker's local state. The Hub stores only the public halves.

The Frontend pins this identity **TOFU** ("trust on first use"). On the first connection to a Worker, the browser records the Worker's composite public key. On every later connection it compares the key the Hub hands over against the pinned one:

- **First use** — no pin exists, so the handshake proceeds and the key is recorded.
- **Match** — the connection proceeds silently.
- **Mismatch** — the Frontend stops and asks you to decide.

This is what defeats a compromised Hub. Because the Hub is the party that tells your browser the Worker's key, a malicious Hub might try to substitute its own key and impersonate the Worker. TOFU pinning catches that: the substituted key won't match the pin, and you get an explicit prompt instead of a silent man-in-the-middle.

### The "Worker public key changed" dialog

When a mismatch occurs, the Frontend shows a dialog titled **"Worker public key changed"**:

> The public key for worker `<workerId>` has changed since the last connection. This could indicate a legitimate key rotation or a potential security issue.

It displays an **Expected:** fingerprint and an **Actual:** fingerprint, and warns: *"If you did not expect this change, reject the connection and verify the worker's identity before accepting."* Two buttons are offered — **Reject** and **Accept** (the Accept button is styled as a danger action). Dismissing the dialog counts as Reject. If the confirmation UI is not available for any reason, the transport defaults to reject (fail-closed).

> **Tip:** The fingerprints are **4 dash-joined English words** derived from a hash of the Worker's composite public key (for example, `able-bird-cage-dock`). The wordlist is identical across the browser and the Worker, so you can read the fingerprint over a trusted out-of-band channel (a phone call, an in-person check) and confirm it matches before accepting a changed key.

### When to accept and when to reject

- **Accept** only if you *expected* the change — for example, you deliberately re-generated the Worker's identity, or you wiped and re-registered the Worker. Verify the fingerprint out-of-band first.
- **Reject** if the change is unexpected. A surprise key change on a Worker you didn't touch is exactly the signal TOFU pinning exists to surface.

In the browser, the pin is kept for one year and refreshed on use. Pin management from the browser UI is limited; for the non-browser clients there are dedicated CLI pin stores covered in [Managing Workers](/docs/operating/managing-workers/):

- Worker-to-Worker (cross-worker) pins, cleared with `leapmux worker cross-worker-pins remove --target-worker-id=<id>`.
- `leapmux remote` CLI pins, cleared with `leapmux remote worker pins remove --worker-id=<id>`.

Both follow the same rule: first contact auto-pins, any later mismatch aborts the connection until you explicitly clear the pin.

## Solo mode: a reduced threat model

Solo mode collapses the trust boundary on purpose. It runs the Hub and the Worker **in the same process** on `127.0.0.1:4327` with **no authentication** — every request is auto-authenticated as the admin. Any local process that can reach the port can drive the Worker.

So in solo mode the threat model reduces to **local-host trust**. The E2EE channel, the composite keypair, and TOFU pinning all still operate end-to-end inside the single process, but that protocol-level separation offers **no protection against a local attacker** who can reach the loopback port.

> **Warning:** If you point solo mode at a non-loopback address, LeapMux warns you at startup:
>
> > solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, so anyone who can reach this port has full admin access without credentials. Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication.
>
> Heed it. If you need authentication, run `leapmux hub` (distributed mode) instead of exposing solo mode. See [Running LeapMux](/docs/operating/running-leapmux/) for the differences between run modes.

The bundled Worker that solo and dev modes auto-register is created in-process and flagged as auto-registered; it deliberately bypasses the registration-key flow, since presenting a bearer token to a local in-process RPC would be security theatre. The desktop app's no-TCP mode goes further and disables the TCP listener entirely, communicating only over a local Unix socket or named pipe — so there is no loopback port to attack at all.

## What sharing a workspace exposes

Sharing a workspace with another user or org member grants them **routing permission via the Hub only**. It does **not** hand them your content.

To actually read agent transcripts, terminal output, or files in a shared workspace, the invited user must open **their own** encrypted Noise channel to the Worker and pass **their own** verified user identity. The Hub never gains plaintext as a side effect of sharing — it only learns that another user is now permitted to route to that Worker. The Worker independently gates each inner request against the set of workspaces the Hub announced for that channel.

The practical upshot:

- Sharing changes *who is allowed to connect*, not *what the Hub can read*. The two columns in "What the Hub can and cannot see" do not move when you share.
- A user with routing permission still has to complete a full handshake and identity check against the Worker before they see anything.
- See [Workspaces](/docs/using/workspaces/) for the sharing UI and [Organizations & Members](/docs/using/organizations/) for the Owner/Admin/Member roles that govern who you can share with.

## At-rest encryption (separate from E2EE)

Distinct from the channel E2EE above, the Hub encrypts a small set of stored secrets **at rest** using a versioned XChaCha20-Poly1305 key ring kept in an `encryption.key` file (mode `0600`, default `<DataDir>/encryption.key`, auto-generated on first run). This protects OAuth provider client secrets and per-user OAuth access/refresh tokens if the Hub's database is exfiltrated without the key file.

This is **not** the same as the Frontend↔Worker channel keys, and it does not touch agent or terminal content (which never reaches the Hub). It is managed with `leapmux admin encryption-key rotate | remove | reencrypt`. The full keystore, key-rotation runbook, database backends, and backup/restore guidance live in [Encryption & Data](/docs/operating/encryption-and-data/).

> **Warning:** The Hub's database and its `encryption.key` file are a matched pair. Back them up together. Without the key file, the encrypted columns are permanently undecryptable.

## Recommendations for operators

If you run a Hub for a team, the security of the deployment rests largely on the host and a few files. Concrete steps:

1. **Protect the Hub host.** It can read all control-plane data — accounts, org/workspace records, layout, Worker registration metadata — and it sees transport metadata for every channel (traffic analysis is in scope). Treat it as a sensitive service: minimal access, patched OS, monitored.
2. **Terminate TLS in front of the Hub.** The Frontend↔Hub and Worker↔Hub legs are not E2EE; they rely on transport TLS. Put the Hub behind a reverse proxy with valid certificates. See [Running LeapMux](/docs/operating/running-leapmux/).
3. **Guard the `encryption.key` file like a top-grade secret.** It is plaintext base64 at mode `0600` — there is no master password or HSM wrapping. Back it up with the database, store both encrypted, and restrict access.
4. **Rotate encryption keys deliberately.** Use `rotate` → restart → `reencrypt`, and never `remove` an old version before re-encryption has migrated every row. The exact runbook is in [Encryption & Data](/docs/operating/encryption-and-data/).
5. **Never expose solo mode beyond loopback** for real use. If you bound it to a non-loopback address, you exposed unauthenticated admin access. Run `leapmux hub` for authenticated multi-user deployments, and firewall or tunnel any non-loopback access. See [Configuration](/docs/operating/configuration/) for listen addresses.
6. **Mint registration keys carefully.** A valid registration key immediately produces an active Worker — there is no separate approval queue, so possession of a live key *is* the gate. Keys are short-lived (5 minutes) and the UI dialog destroys them when closed; treat them as one-time secrets and prefer delivering them over a trusted channel. See [Managing Workers](/docs/operating/managing-workers/).
7. **Teach users to take the key-change dialog seriously.** The "Worker public key changed" prompt is the user-facing line of defense against a Hub swapping a Worker. Users should reject unexpected changes and verify the 4-word fingerprint out-of-band before ever accepting.
8. **Revoke credentials when needed, and know it tears down channels.** Password changes, account deletion, force-logout, and token revocation all force-close the affected user's open channels. Use the [Admin CLI](/docs/operating/admin-cli/) for these operations.

## Recommendations for security-conscious users

- **Verify a Worker's fingerprint on first connect** if you can, via a trusted out-of-band channel, before you start trusting that pin.
- **Reject, don't reflexively accept,** when the "Worker public key changed" dialog appears unexpectedly.
- **Remember the Hub sees workspace titles and activity metadata.** Don't put secrets in workspace names, and recall that *when* and *how much* you work is observable even though the content is not.
- **Sharing is routing, not disclosure to the Hub** — but it does let another authorized user open their own channel and read content. Share only with people you intend to give that access.

## Quick reference

The facts an operator looks up most often. The full crypto primitives are in the [Primitives](#primitives) table above; identity pinning and encryption modes are covered in their own sections.

| Property | Value |
|----------|-------|
| Noise protocol label | `Noise_NK_25519_ChaChaPoly_BLAKE2b` |
| Worker encryption mode flag | `--encryption-mode classic` \| `post-quantum` (default `post-quantum`) |
| Solo mode default bind | `127.0.0.1:4327`, no authentication (local trust only) |
| At-rest secret key file | `encryption.key` (mode `0600`, default `<DataDir>/encryption.key`) |

See also: [Managing Workers](/docs/operating/managing-workers/) · [Encryption & Data](/docs/operating/encryption-and-data/) · [Authentication Providers](/docs/operating/authentication-providers/) · [Accounts & Authentication](/docs/using/accounts/) · [Running LeapMux](/docs/operating/running-leapmux/).
