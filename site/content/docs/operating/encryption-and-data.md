---
title: "Encryption & Data"
description: "What LeapMux stores and encrypts at rest, how to rotate encryption keys, how database migrations work, and how to back up and restore a Hub safely."
type: docs
weight: 6
---

This chapter is for operators who run a LeapMux **Hub** (or a solo-mode instance) and need to understand what data is stored, what is encrypted at rest, how to rotate encryption keys, how database migrations work, and how to back up and restore everything safely.

It covers two distinct encryption systems that are easy to confuse:

- **Encryption at rest** — the Hub encrypts a small set of stored secrets (OAuth client secrets and OAuth tokens) using a local **keystore** (the `encryption.key` file). This chapter is mostly about this.
- **End-to-end encryption (E2EE)** — all Frontend-to-Worker traffic is encrypted so the Hub can route it but never read it. That protocol is covered in [Security & Threat Model](/docs/operating/security/); this chapter only touches the Worker key material you must back up.

For where these settings live and how to set them, see [Configuration](/docs/operating/configuration/). For the full `leapmux admin` command surface, see [Admin CLI](/docs/operating/admin-cli/).

## What is stored, and what is encrypted

LeapMux keeps three kinds of persistent state:

| Data | Where it lives | Encrypted at rest? |
| --- | --- | --- |
| Accounts, orgs, workspaces, members, Workers, sessions, API tokens | Hub database (`hub.db` or your SQL backend) | No (but secrets within it are hashed or encrypted — see below) |
| OAuth provider client secrets and per-user OAuth access/refresh tokens | Hub database | **Yes** — encrypted with the keystore key |
| API-token / delegation-token secrets | Hub database | No — stored as HMAC-SHA256 **hashes** (peppered), never as plaintext or reversible ciphertext |
| Worker public keys (for the E2EE handshake) | Hub database | No — public material, stored in the clear |
| Agent transcripts, terminal I/O, worktree/session state | Worker's local SQLite (`worker.db`) | No |
| Worker E2EE private keys + Hub auth token | Worker's `state.json` | No — plain JSON, file mode `0600` |

> **Note:** Agent chat transcripts, tool calls, terminal output, file contents, and diffs **never** reach the Hub in readable form and are **not** stored in the Hub database at all. They live only in the Worker's local database and are end-to-end encrypted in transit. See [Security & Threat Model](/docs/operating/security/).

### Encryption at rest details

The keystore encrypts secrets with **XChaCha20-Poly1305** (a 256-bit key, 24-byte random nonce per ciphertext). Each ciphertext records which key version produced it, so the Hub can pick the right key to decrypt as long as that key version is still in the ring.

Exactly three secret types are encrypted at rest in the Hub database:

- `oauth_providers.client_secret` — the OAuth provider's client secret.
- `oauth_tokens.access_token` — a user's OAuth access token.
- `oauth_tokens.refresh_token` — a user's OAuth refresh token.

(Short-lived pending-signup OAuth tokens are also encrypted while a signup is in flight.)

The keystore loads its versioned key ring from `encryption.key` (highest version = active) and uses it to encrypt and decrypt exactly those three columns in the Hub database; everything else — accounts, orgs, workspaces, Workers, hashed API-token secrets, and Worker public keys — is stored without the key.

The key lives in a separate file from the database, so a copy of the database alone leaves the encrypted OAuth secrets readable only as ciphertext — which is why the two must be backed up together.

API-token and delegation-token secrets are **not** encrypted — they are HMAC-SHA256 hashed with a dedicated, stable pepper that is independent of the encryption key ring, so the database never contains a recoverable token. See [Accounts & Authentication](/docs/using/accounts/) and [Admin CLI](/docs/operating/admin-cli/) for token management.

## The encryption key file (`encryption.key`)

The keystore is a **versioned key ring** stored in a single plain-text file.

| Property | Value |
| --- | --- |
| Default path | `{data_dir}/encryption.key` (default data dir: `~/.config/leapmux/hub`) |
| Config override | `encryption_key_path` (YAML or env `LEAPMUX_HUB_ENCRYPTION_KEY_PATH`) — **no CLI flag** |
| File mode | `0600` (owner read/write only); parent directory `0750` |
| Format | one key per line as `version:base64stdkey`; blank lines and lines starting with `#` are ignored |
| Auto-generated | yes — the first time the Hub starts, it creates the file with a single version-1 key |

A key file looks like this:

```text
# leapmux encryption key ring
1:aGVsbG8gd29ybGQgdGhpcyBpcyBub3QgYSByZWFsIGtleQ==
2:YW5vdGhlciBmYWtlIGtleSBmb3IgZG9jdW1lbnRhdGlvbg==
```

The **highest version number is the active key** — it encrypts all new secrets. Older versions stay in the file only to decrypt data written before a rotation.

> **Warning:** Treat `encryption.key` as a top-grade secret. There is no master-password or HSM wrapping around it — the file contains the raw keys. Anyone who has both this file and a copy of the database can decrypt every stored OAuth secret. Conversely, **losing this file makes all encrypted columns permanently unreadable** (see [Backup & restore](#backup--restore)).

There is nothing to configure to turn encryption on. Running the Hub once is enough:

```bash
leapmux hub
# On first start, the Hub generates ~/.config/leapmux/hub/encryption.key
# and logs: "encryption keystore loaded" active_version=1 versions=1
```

## Key rotation

Rotation generates a new active key version. Existing ciphertext stays readable through the retained older versions until you re-encrypt it. The encryption-key commands are:

| Command | Summary |
| --- | --- |
| `leapmux admin encryption-key rotate` | Generate and add a new encryption key version |
| `leapmux admin encryption-key reencrypt` | Re-encrypt all secrets with the active key |
| `leapmux admin encryption-key remove --version <N>` | Remove an old encryption key version |
| `leapmux admin encryption-key rotate-pepper --yes` | Regenerate the token pepper (invalidates all API/delegation tokens) |

These commands do **not** all take the same flags, because they touch different things:

| Command | Flags it accepts | What it operates on |
| --- | --- | --- |
| `rotate` | `--data-dir` only | The local `encryption.key` file |
| `rotate-pepper` | `--data-dir`, `--yes` (required) | The local `encryption.key` file |
| `remove` | `--data-dir`, `--config`, `--version` (required) | The local key file **and** the Hub database |
| `reencrypt` | `--data-dir`, `--config` | The local key file **and** the Hub database |

`reencrypt` and `remove` open the database, so both accept `--config` (point it at the same config file the Hub uses, so they target the same backend). `remove` reads the database to check whether any ciphertext still depends on the version being removed (see the runbook warning below). `rotate` and `rotate-pepper` work purely on the local key file and accept `--data-dir` only — passing `--config` to either makes the command fail flag parsing. See [Admin CLI](/docs/operating/admin-cli/) for the full admin flag reference.

> **Note:** `rotate` and `rotate-pepper` only read and rewrite the local key file — they do not touch the database, which is why they take no `--config`. `reencrypt` and `remove` open the database (and, like any store-opening command, run pending migrations first).

### Rotation runbook

Follow these steps in order. The `remove` step is guarded — it refuses to delete a version that still encrypts data — so skipping `reencrypt` fails loudly instead of destroying data.

1. **Add a new key version.**

   ```bash
   leapmux admin encryption-key rotate
   # Added encryption key version 2.
   # Restart the hub, then run: leapmux admin encryption-key reencrypt
   ```

   This generates a random 32-byte key as version 2, makes it active, and rewrites `encryption.key`. Existing data is untouched and still decrypts using version 1.

2. **Restart the Hub** so it loads the new active key and uses it for all new writes.

   ```bash
   # systemd example
   sudo systemctl restart leapmux-hub
   ```

3. **Re-encrypt existing secrets to the new version.**

   ```bash
   leapmux admin encryption-key reencrypt
   # Re-encrypted 7 secrets to key version 2.
   ```

   This walks every OAuth provider secret and OAuth token still encrypted under an older version, decrypts it, and rewrites it under the active version. Rows already at the active version are skipped.

4. **(Optional) Remove the retired version** once nothing references it, then restart the Hub.

   ```bash
   leapmux admin encryption-key remove --version 1
   # Removed encryption key version 1.
   # Restart the hub to apply.
   sudo systemctl restart leapmux-hub
   ```

> **Warning:** `remove` permanently destroys a key version, so any ciphertext still encrypted under it would become undecryptable. As a guardrail, `remove` opens the database and **refuses** to delete a version that still encrypts OAuth provider secrets or OAuth tokens — it reports what still references the version and tells you to run `reencrypt` first. It also refuses to delete the **active** version (`cannot remove active key version N`) and errors if the version is not in the ring (`keystore: key version N not in ring`). `--version` is required and must be `>= 1`. Transient `pending_oauth_signups` are intentionally outside the guard — they auto-expire, so a half-finished signup simply fails and the user retries.

### Rotation and API tokens

Pepper rotation is **not** part of the key-rotation runbook above, and you should not add it there — encryption-key rotation deliberately leaves API and delegation tokens working.

The pepper used to hash API-token and delegation-token secrets is a **dedicated, stable secret**. It lives alongside the encryption keys in `encryption.key` but is **independent** of the encryption key ring: rotating, re-encrypting, or removing encryption keys never changes it, so existing API and delegation tokens keep validating across key rotation. (An `encryption.key` file created before this behavior is seeded with a pepper automatically the next time it is loaded.)

To deliberately invalidate **every** API token and delegation token — for example after a suspected keystore compromise — regenerate the pepper:

```bash
leapmux admin encryption-key rotate-pepper --yes
# Regenerated the API-token pepper.
# All existing API tokens and delegation tokens are now invalid.
# Restart the hub to apply, then re-issue API tokens with: leapmux admin api-token issue
```

Token hashes are one-way, so regenerating the pepper cannot migrate existing tokens — it invalidates them all at once, and they must be reissued (`leapmux admin api-token issue`) or re-authenticated. The command requires `--yes` and takes effect on the next Hub restart. See [Admin CLI](/docs/operating/admin-cli/) for `api-token` and `delegation-token` management.

## Databases

### Hub database backends

The Hub stores all relational data in one of six interchangeable SQL backends, selected by `storage.type`. For backup and restore, what matters is which backend you run and whether it needs a DSN:

| `storage.type` | DSN required? |
| --- | --- |
| `sqlite` (default; also used when empty) | No — file-backed |
| `postgres` | Yes — `storage.postgres.dsn` |
| `mysql` | Yes — `storage.mysql.dsn` |
| `cockroachdb` | Yes — `storage.cockroachdb.dsn` |
| `yugabytedb` | Yes — `storage.yugabytedb.dsn` |
| `tidb` | Yes — `storage.tidb.dsn` |

The exact Go drivers and which backends reuse them (CockroachDB and YugabyteDB on the PostgreSQL driver, TiDB on the MySQL driver) are in the full driver-reuse table in [Configuration](/docs/operating/configuration/).

The default SQLite database lives at `{data_dir}/hub.db`. SQLite runs in WAL mode, so while the Hub is running you will also see two sidecar files: `hub.db-wal` and `hub.db-shm`. On a clean shutdown the Hub checkpoints and truncates the WAL.

For the full set of connection-pool, cache, and DSN options for each backend, see [Configuration](/docs/operating/configuration/). The full storage-key reference (max-conns, lifetimes, etc.) lives there and is not repeated here.

> **Note:** Use a config file (or CLI flags) to set storage options. Because of how environment variables are mapped, the nested `storage.*` keys are most reliably set via YAML or flags rather than env vars. See [Configuration](/docs/operating/configuration/).

### Worker database

Each Worker keeps its **own** separate SQLite database at `{data_dir}/worker.db` (default Worker data dir: `~/.config/leapmux/worker`). It holds transient agent and session state — agents, messages, terminals, worktrees, todos, control requests, and so on. It uses the same WAL/foreign-key settings as the Hub's SQLite and is chmod'd to `0600`. Worker SQLite tuning flags (`--db-max-conns`, `--db-cache-size`, `--db-mmap-size`) are documented in [Configuration](/docs/operating/configuration/) and [Running LeapMux](/docs/operating/running-leapmux/).

## Migrations

LeapMux uses **goose** for schema migrations, and migrations are applied **automatically every time the database is opened** — at Hub startup, and whenever a store-opening admin command runs. In normal operation you never have to run migrations by hand; starting the Hub brings the schema up to date.

Each backend embeds its own migrations. The current schema is a single initial migration, so the latest version is `1`.

The `leapmux admin db` commands let you inspect and (where supported) control migrations:

| Command | Summary | Notes |
| --- | --- | --- |
| `leapmux admin db path` | Print the database path | Always prints `{data_dir}/hub.db`. Because `db path` takes no `--config`, it cannot load the config file, so it never reflects a custom `storage.sqlite.path` and does **not** consult `storage.type` — it prints the default SQLite path even when a SQL backend is configured. |
| `leapmux admin db version` | Show current schema version | Opens the store (which applies pending migrations), then prints current and latest versions. |
| `leapmux admin db migrate` | Run schema migrations | `--version <int64>` selects a target (default `-1` = latest). |

Example:

```bash
leapmux admin db version
# Current schema version: 1
# Latest available version: 1

leapmux admin db migrate
# Current schema version: 1
# Latest available version: 1
# Already at latest version.
```

Because opening the store already migrates up to the latest version, an explicit `migrate` to the latest is mostly a confirmation. Supplying an explicit `--version` lower than the current schema attempts a **down**-migration to that version (where the backend's migrations support it). Targeting a specific version prints `Migrating to version N...` and then the new current version.

```bash
# Target a specific version (down-migration support depends on the backend)
leapmux admin db migrate --version 1
```

Like `encryption-key reencrypt`, both `admin db version` and `admin db migrate` open the store, so both accept `--data-dir` and `--config`; pass `--config` so the command targets the same backend the Hub uses. `admin db path` does not open the store, so it accepts `--data-dir` only. See [Admin CLI](/docs/operating/admin-cli/).

## Backup & restore

> **Warning:** The Hub database and the `encryption.key` file are a **matched pair**. Back them up together and restore them together. With the database but no key file, every encrypted OAuth secret is permanently unreadable. With the key file but no database, you have nothing to decrypt.

**On-disk data to back up (Hub host vs. each Worker host):**

```text
┌──────────────────────────────┐    ┌────────────────────────────┐
│  ~/.config/leapmux/hub/      │    │  ~/.config/leapmux/worker/ │
│                              │    │                            │
│  ┌────────────────────────┐  │    │  ┌──────────────────────┐  │
│  │ hub.db   (ciphertext)  │  │    │  │ state.json  (0600)   │  │
│  └────────────────────────┘  │    │  │ identity + E2EE keys │  │
│    matched pair: back up     │    │  └──────────────────────┘  │
│    & restore together        │    │  ┌──────────────────────┐  │
│  ┌────────────────────────┐  │    │  │ worker.db            │  │
│  │ encryption.key (0600)  │  │    │  │ (transient state)    │  │
│  └────────────────────────┘  │    │  └──────────────────────┘  │
│                              │    │                            │
└──────────────────────────────┘    └────────────────────────────┘
```

### What to back up

For a Hub:

1. **The Hub database.**
   - SQLite: back up `hub.db`. For a consistent copy, stop the Hub first (it truncates the WAL on clean shutdown) or use a SQLite-aware online-backup tool. If you hot-copy the file, copy the `hub.db-wal` and `hub.db-shm` sidecars alongside it.
   - Postgres / MySQL (and the CockroachDB / YugabyteDB / TiDB variants): take a normal logical dump (for example `pg_dump` or `mysqldump`). LeapMux has no built-in backup, dump, or restore command — use your database's standard tooling.
2. **The `encryption.key` file** (default `~/.config/leapmux/hub/encryption.key`, or wherever `encryption_key_path` points). Store it with the same care as any private key.

For each Worker:

3. **`state.json`** (default `~/.config/leapmux/worker/state.json`). This holds the Worker's identity: its `worker_id`, Hub auth token, and its private E2EE keys (X25519, ML-KEM-1024, SLH-DSA). Losing it forces re-registration with a new registration key and a new key identity — and because the Worker's public keys are registered in the Hub, a new identity will trip the Frontend's "Worker public key changed" pin-mismatch dialog. See [Managing Workers](/docs/operating/managing-workers/) and [Security & Threat Model](/docs/operating/security/).
4. `worker.db` is transient agent/session state. It is not a secret, but back it up if you want to preserve agent transcripts and terminal history across a rebuild.

> **Tip:** A correct, restorable backup of a single-machine Hub is: a consistent copy of `hub.db` (Hub stopped) **plus** `encryption.key`, both taken at the same time. For a distributed deployment, add each Worker's `state.json`.

### Restore

1. Stop the Hub (and the affected Workers).
2. Restore the database into place (copy the SQLite file back, or load your SQL dump into the target server).
3. Restore `encryption.key` to its original path (or set `encryption_key_path` to wherever you placed it).
4. Restore each Worker's `state.json` to its data dir.
5. Start the Hub. It will apply any pending migrations automatically on open, then load the keystore.

### Disaster-recovery notes

- **Lost `encryption.key`, database intact:** encrypted OAuth secrets are unrecoverable. Generate a new key by starting the Hub (it auto-creates a version-1 key), then have users re-link their OAuth providers and re-enter provider client secrets. Non-encrypted data (accounts, orgs, workspaces) is unaffected.
- **Lost `hub.db`, key intact:** the key alone cannot reconstruct accounts or workspaces. Restore the database from backup.
- **Lost a Worker's `state.json`:** re-register the Worker (a fresh registration key from the Hub UI). The first Frontend to reconnect after the identity changes sees the **"Worker public key changed"** dialog and must explicitly Accept the new key. See [Managing Workers](/docs/operating/managing-workers/).
- **Lost `worker.db`:** the Worker recovers as a fresh Worker; in-progress agent transcripts and terminal scrollback held only in that database are lost. The Worker's identity (`state.json`) is unaffected.

## Encryption modes (E2EE)

The keystore in this chapter protects data **at rest**. Wholly separate from it, each Worker also negotiates a **transport** encryption mode for Frontend-to-Worker channels — this is the at-rest-vs-transport distinction worth keeping straight, since the two are easy to confuse. The transport mode is set with the Worker's `--encryption-mode` flag (or `encryption_mode` in YAML / `LEAPMUX_WORKER_ENCRYPTION_MODE`):

| Mode | Meaning |
| --- | --- |
| `post-quantum` (**default**) | Hybrid classical + post-quantum handshake — secure even if either the classical or the post-quantum algorithm is broken. |
| `classic` | Classical-only handshake (no post-quantum protection). Smaller handshake messages. |

Unless you have a specific reason to choose `classic`, leave it at the default. The accepted aliases and the fail-safe resolution of unrecognized values are documented in [Configuration](/docs/operating/configuration/).

The handshake primitives, Worker identity pinning (TOFU), and the key-change dialog are covered in [Security & Threat Model](/docs/operating/security/). Configuration precedence and the full flag list are in [Configuration](/docs/operating/configuration/).

## See also

- [Configuration](/docs/operating/configuration/) — config precedence, the full storage-key reference, data directories, and listen addresses.
- [Security & Threat Model](/docs/operating/security/) — the E2EE protocol, the Hub-as-relay trust boundary, Worker TOFU pinning, and the solo-mode caveat.
- [Admin CLI](/docs/operating/admin-cli/) — the complete `leapmux admin` reference, including `encryption-key`, `db`, and token commands.
- [Managing Workers](/docs/operating/managing-workers/) — registering and approving Workers, registration keys, and key-pin handling.
