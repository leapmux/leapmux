---
title: "Admin CLI"
type: docs
weight: 20
---

`leapmux admin` is the operator-facing command-line interface for managing a LeapMux Hub's persistent data. It operates **directly against the Hub's database and on-disk encryption key file** — no running Hub, network call, or login is required. You run it on the machine that holds the Hub's data directory, typically as the same OS user that runs the Hub.

This is the tool you reach for when you need to do something the web UI cannot do, or when the Hub is offline: create the first user, reset a locked-out account's password, mint and revoke tokens, add an OAuth provider, rotate encryption keys, or inspect the database. For the network-facing, session-authenticated remote control of a *running* Hub, see [Remote Control CLI](/docs/16-remote-control-cli/) instead.

> **Warning:** Because `leapmux admin` writes straight to the database, anyone who can run it has full control over the Hub's data. Protect the data directory and the hosts that can reach it. There is no per-command authentication.

## How it works

The command tree is three levels deep at most:

```text
leapmux admin <group> <command> [flags]
leapmux admin worker reg-key <command> [flags]   # one nested subgroup
```

Running `leapmux admin` with no group prints the root help:

```text
Manage LeapMux resources.

Usage: leapmux admin <group> <command> [flags]

Groups:
  org               Manage organizations
  user              Manage users
  session           Manage sessions
  worker            Manage workers
  oauth-provider    Manage OAuth/OIDC providers
  encryption-key    Manage encryption keys
  db                Database utilities
  api-token         Manage durable API tokens (CLI / integrations)
  delegation-token  Manage worker-minted delegation tokens
```

Help works at every level: `-h`, `-help`, `--help`, and `help` all print the relevant usage and exit. `leapmux admin user --help` lists the `user` group's commands; `leapmux admin user list --help` shows that one command's flags. Running a group with no command exits with `error: <path> command is required`; an unknown group or command prints `unknown admin group: <name>` / `unknown <path> command: <name>`.

### Locating the data: `--data-dir` and `--config`

Every command that touches data resolves the database and encryption key the same way, through two common flags:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--data-dir` | string | (empty) | Hub data directory. When empty, falls back to the default Hub data dir `~/.config/leapmux/hub`. |
| `--config` | string | (empty) | Path to a Hub config file; loads its storage settings (so the command targets the same backend the Hub uses). |

Resolution rules:

- If you pass `--config`, the Hub config file is loaded for storage settings. If you *also* pass `--data-dir`, it overrides the config's `DataDir`.
- If you omit `--config`, a minimal config is built from `--data-dir`. With both empty, the **default Hub data directory** `~/.config/leapmux/hub` is used.
- The SQLite database path defaults to `{DataDir}/hub.db` (override via config key `storage.sqlite.path`).
- The encryption key file defaults to `{DataDir}/encryption.key` (override via config key `encryption_key_path`).

> **Tip:** If your Hub runs on Postgres or MySQL, always pass `--config /path/to/hub.yaml` so the admin command connects to the same database. Without `--config`, the CLI builds a SQLite-only config and would operate on (or create) `{DataDir}/hub.db` instead of your real backend. See [Configuration](/docs/18-configuration/) for the config file format and [Encryption & Data](/docs/22-encryption-and-data/) for storage backends.

Three commands need only the config, not a live database connection, and therefore accept **only `--data-dir`** (no `--config`): `db path`, `encryption-key rotate`, and `encryption-key rotate-pepper`.

### Pagination

List commands that page accept `--limit` (int64, default `50`) and `--cursor` (string). When a page returns exactly `--limit` rows, the CLI prints a hint:

```text
Next page: --cursor 2026-06-01T12:34:56.789012345Z
```

The cursor is the last row's relevant timestamp in RFC3339Nano UTC. Most commands cursor on `created_at`; `session list` cursors on `last_active_at`. Pass the printed value back as `--cursor` to fetch the next page.

### Password prompting

Commands that take `--password` prompt interactively (with no echo) when you omit the flag — `Password:` on create, `New password:` on reset. If stdin is not a terminal and you omit `--password`, the command fails with `--password is required (stdin is not a terminal)`.

Shared validation across user commands:

- **Password**: 8–128 characters.
- **Username** (slug): max 32 characters; reserved system names are rejected.
- **Display name**: max 128 characters (falls back to the username when empty).
- **Email**: max 254 characters.

### Revocation and the running Hub

The revocation commands only mutate database rows; they never reach into the running Hub's process. How fast a *running* Hub reacts depends on which rows you touched.

**Watcher-driven (default ~2s).** `session revoke-user`, `user reset-password`, `api-token revoke`, and `delegation-token revoke` bump a column the Hub's **revocation watcher** polls — `users.tokens_revoked_at` for the first two, and each token's own `revoked_at` for the last two. On its next sweep (default ~2s) the watcher tears down the cached bearers and closes the open channels cross-process. You do not need to restart or signal the Hub.

**Session-cache TTL (~30s).** `session revoke` (a single session by ID) is the exception. It deletes one session row, but the watcher does **not** poll the `sessions` table — sessions are validated through an in-memory cache the admin CLI cannot evict from another process. A running Hub therefore keeps honoring that session until its cache entry expires, which is up to the session-cache TTL (~30s). For an immediate cross-process kill of a single user's access, use `session revoke-user` instead (it is watcher-driven).

---

## `org` — organizations

### `org list`

List organizations.

| Flag | Default | Description |
| --- | --- | --- |
| `--query` | (empty) | Search query (prefix match on org name). |
| `--limit` | `50` | Page size. |
| `--cursor` | (empty) | Pagination cursor (`created_at` in RFC3339Nano). |

Columns: `ID  NAME  PERSONAL  CREATED` (`PERSONAL` is `yes`/`no`). Empty result prints `No organizations found.`

```bash
leapmux admin org list --query acme
```

> **Note:** There is no `org create` in the admin CLI. Every user gets a personal org automatically when created (see `user create` below). Shared/team organizations are created and managed through the web UI — see [Organizations & Members](/docs/06-organizations-and-members/).

---

## `user` — users

All of `get`, `update`, `delete`, `reset-password`, `grant-admin`, `revoke-admin`, and `list-sessions` identify the target with **exactly one** of `--id` or `--username`. Passing neither errors with `--id or --username is required`; passing both errors with `--id and --username are mutually exclusive`. A miss prints `user not found: <value>`.

### `user list`

| Flag | Default | Description |
| --- | --- | --- |
| `--query` | (empty) | Search query (matches username, display name, email). |
| `--limit` | `50` | Page size. |
| `--cursor` | (empty) | Pagination cursor (`created_at`). |

Columns: `ID  USERNAME  DISPLAY_NAME  EMAIL  ADMIN  CREATED`. Empty: `No users found.`

### `user get`

Prints labeled fields: `ID`, `Org ID`, `Username`, `Display name`, `Email`, `Email verified` (yes/no), `Password set` (yes/no), `Admin` (yes/no), `Created at`, `Updated at`.

```bash
leapmux admin user get --username alice
```

### `user create`

Create a user together with their personal org.

| Flag | Default | Description |
| --- | --- | --- |
| `--username` | (required) | New username (slug). |
| `--password` | (prompted) | Password; prompted with no echo if omitted. |
| `--display-name` | (empty) | Display name; defaults to the username. |
| `--email` | (empty) | Email address. |
| `--email-verified` | `false` | Mark email as verified. |
| `--admin` | `false` | Grant admin privileges. |

Uniqueness conflicts return friendly errors (`username %q is already taken`, `email %q is already in use`). On success: `Created user "alice" (id: ...)`.

```bash
# Bootstrap the first admin user (you'll be prompted for the password)
leapmux admin user create --username admin --email admin@example.com \
  --email-verified --admin
```

### `user update`

| Flag | Description |
| --- | --- |
| `--id` / `--username` | Lookup (exactly one). |
| `--display-name` | New display name. |
| `--email` | New email address. |
| `--email-verified` | `true` or `false` (any other value: `must be 'true' or 'false'`). |
| `--clear-pending-email` | Clear any in-flight email verification (token + attempt counter). |

At least one mutating field must be set, else: `no fields to update (use --display-name, --email, --email-verified, or --clear-pending-email)`. Updates run in a transaction. On success: `Updated user "alice" (id: ...)`.

```bash
leapmux admin user update --username alice --email alice@newcorp.com --email-verified=true
```

### `user delete`

| Flag | Description |
| --- | --- |
| `--id` / `--username` | Lookup. |
| `--force` | Required to delete an admin user. |

Deleting an admin without `--force` errors: `user %q is an admin; pass --force to confirm deletion`. In a single transaction the command marks the user's Workers deleted, removes Worker access grants, soft-deletes their workspaces, deletes their sessions, **revokes all the user's credentials** (API tokens + delegation tokens), removes org membership, deletes the user, and soft-deletes the personal org. On success: `Deleted user "alice" (id: ...) and personal org ...`.

```bash
leapmux admin user delete --username bob
```

### `user reset-password`

| Flag | Description |
| --- | --- |
| `--id` / `--username` | Lookup. |
| `--password` | New password; prompted (`New password:`) if omitted. |

In a transaction it updates the password hash, **deletes all the user's sessions**, and revokes all the user's credentials. A running Hub closes the affected channels on its next revocation sweep. On success: `Password reset for user "alice" (id: ...). All sessions revoked.`

```bash
# Reset a locked-out user (you'll be prompted for the new password)
leapmux admin user reset-password --username alice
```

### `user grant-admin` / `user revoke-admin`

Toggle admin privileges. Both take `--id` / `--username`.

```bash
leapmux admin user grant-admin --username alice
leapmux admin user revoke-admin --username alice
```

Output: `Granted admin privileges for user "alice" (id: ...)` / `Revoked admin privileges for user "alice" (id: ...)`.

### `user list-sessions`

List one user's active sessions. Takes `--id` / `--username`. Columns: `ID  CREATED  LAST_ACTIVE  EXPIRES  IP  USER_AGENT` (user agent truncated to 60 chars). Empty: `No active sessions for user %q.`

---

## `session` — sessions

### `session list`

List all active sessions across users.

| Flag | Default | Description |
| --- | --- | --- |
| `--limit` | `50` | Page size. |
| `--cursor` | (empty) | Pagination cursor (`last_active_at` from the previous page). |

Columns: `ID  USER_ID  USERNAME  LAST_ACTIVE  EXPIRES  IP  USER_AGENT` (user agent truncated to 60). Empty: `No active sessions.`

### `session revoke`

Revoke one session by ID.

| Flag | Description |
| --- | --- |
| `--id` | Session ID (required). Empty: `--id is required`. |

Not found: `session not found: <id>`. Success: `Revoked session <id>`.

> **Note:** Unlike `session revoke-user`, this command is **not** watcher-driven — a running Hub keeps honoring the session until its in-memory session-cache entry expires (~30s). See [Revocation and the running Hub](#revocation-and-the-running-hub).

### `session revoke-user`

Revoke **all** sessions for a user. Note this command uses `--user-id` (not `--id`):

| Flag | Description |
| --- | --- |
| `--user-id` / `--username` | Lookup (one). |

In a transaction it deletes all the user's sessions and also revokes their API and delegation tokens. Success reports the counts: `Revoked all sessions for user "alice" (id: ...); 2 api token(s) and 5 delegation token(s) also revoked`.

```bash
leapmux admin session revoke-user --username alice
```

### `session purge-expired`

Hard-delete every expired session row. No command-specific flags. Success: `Purged %d expired sessions.`

```bash
leapmux admin session purge-expired
```

---

## `worker` — workers

For the full picture of how Workers register and connect — registration keys, the no-pending-approval model, online/offline status, and TOFU key pinning — see [Managing Workers](/docs/19-managing-workers/).

### `worker list`

| Flag | Default | Description |
| --- | --- | --- |
| `--user-id` | (empty) | Filter by user ID. |
| `--username` | (empty) | Filter by username (resolved to a user ID; miss: `user not found: <username>`). |
| `--status` | `active` | Filter by status: `active`, `deregistering`, `deleted`, or `all`. |
| `--limit` | `50` | Page size. |
| `--cursor` | (empty) | Pagination cursor (`created_at`). |

An unknown status prints `unknown worker status: <s> (use: active, deregistering, deleted, all)`. Columns: `ID  OWNER  STATUS  AUTO  CREATED  LAST_SEEN` (`AUTO` marks auto-registered Workers; `LAST_SEEN` shows `-` when never seen). Empty: `No workers found.`

```bash
leapmux admin worker list --status all --username alice
```

### `worker get`

| Flag | Description |
| --- | --- |
| `--id` | Worker ID (required). Empty: `--id is required`. |

This **includes soft-deleted Workers** so you can audit deregistrations. Not found: `worker not found: <id>`. Prints `ID`, `Registered by`, `Status`, `Auto-registered` (yes/no), `Created at`, `Last seen at`, then an access-grants block (`USER_ID  GRANTED_BY  CREATED`) or `No access grants.`

### `worker deregister`

| Flag | Description |
| --- | --- |
| `--id` | Worker ID (required). |

Force-deregisters an active Worker. No active match: `worker %s not found or not active`. Success: `Deregistered worker <id>`.

```bash
leapmux admin worker deregister --id wkr_abc123
```

### `worker reg-key` — registration keys

A Worker joins a Hub by presenting a short-lived registration key. The admin CLI lets you **inspect and revoke** those keys, but it does **not** mint them — keys are created from the web UI's "Register worker" dialog (or the Worker-management RPC). See [Managing Workers](/docs/19-managing-workers/) for the full registration flow.

#### `worker reg-key list`

| Flag | Default | Description |
| --- | --- | --- |
| `--include-expired` | `false` | Include revoked or expired keys (forensics; default shows only live keys). |
| `--limit` | `50` | Page size. |
| `--cursor` | (empty) | Pagination cursor (`created_at`). |

Columns: `ID  CREATED_BY  CREATED  EXPIRES`. Empty: `No registration keys.`

#### `worker reg-key revoke`

| Flag | Description |
| --- | --- |
| `--id` | Registration key ID (required). |

Soft-deletes the key. Not found: `registration key not found: <id>`. Success: `Revoked registration key <id>`.

#### `worker reg-key purge-expired`

Hard-delete every key already past its expiry (revoked keys included), in batches of 1000 until drained. No command-specific flags. Success: `Purged %d expired registration keys.`

---

## `oauth-provider` — OAuth/OIDC login providers

These commands configure operator-level OAuth/OIDC sign-in. For a step-by-step walkthrough of each provider, see [Authentication Providers](/docs/21-authentication-providers/).

### `oauth-provider add`

| Flag | Description |
| --- | --- |
| `--type` | Provider type: `github`, `google`, `apple`, or `oidc` (required). |
| `--name` | Display name. |
| `--client-id` | OAuth client ID (required). |
| `--client-secret` | OAuth client secret (required). |
| `--issuer-url` | OIDC issuer URL. Silently ignored for `github` (plain OAuth2 has no issuer). |
| `--scopes` | Space-separated scopes. |
| `--trust-email` | `true` or `false` — trust email from this provider as verified. |

Required: `--type`, `--client-id`, `--client-secret`. Each type carries presets that fill in the defaults you omit:

| `--type` | Stored type | Default name | Default issuer | Default scopes | Default trust-email |
| --- | --- | --- | --- | --- | --- |
| `github` | `github` | `GitHub` | (none) | `read:user user:email` | `true` |
| `google` | `oidc` | `Google` | `https://accounts.google.com` | `openid profile email` | `true` |
| `apple` | `oidc` | `Apple` | `https://appleid.apple.com` | `openid name email` | `true` |
| `oidc` | `oidc` | (none — `--name` required) | (none — `--issuer-url` required) | `openid profile email` | (none — `--trust-email` required) |

Stored provider types are only `github` or `oidc`; `google` and `apple` are stored as `oidc`. GitHub is plain OAuth2 (not OpenID Connect), so it has no issuer URL — the issuer applies only to the `oidc` types. For generic `oidc`, you must supply `--name`, `--issuer-url`, and `--trust-email` (their presets are empty). An unknown type prints `unknown provider type: %s (supported: github, google, apple, oidc)`.

For OIDC providers the command validates the issuer over the network first (`Validating OIDC issuer <url> ...`); a failure aborts with `issuer validation failed: ...`. The client secret is **encrypted with the active encryption key** before storage, and the provider is created **enabled**. Success: `Created OAuth provider "GitHub" (id: ..., type: github)`.

```bash
# GitHub: presets supply name, scopes, and trust-email
leapmux admin oauth-provider add --type github \
  --client-id Iv1.abc123 --client-secret "$GH_SECRET"

# Generic OIDC: name, issuer, and trust-email are mandatory
leapmux admin oauth-provider add --type oidc --name "Corp SSO" \
  --client-id corp-client --client-secret "$CORP_SECRET" \
  --issuer-url https://sso.example.com --trust-email=true
```

> **Warning:** `oauth-provider add` encrypts the client secret with the active encryption key, so the Hub must have an `encryption.key` file. If you have never run the Hub, run it once to auto-generate the key (see [Encryption & Data](/docs/22-encryption-and-data/)).

### `oauth-provider list`

No command-specific flags. Columns: `ID  TYPE  NAME  TRUST_EMAIL  ENABLED` (`TRUST_EMAIL`/`ENABLED` are `yes`/`no`). Empty: `No OAuth providers configured.`

### `oauth-provider remove`

| Flag | Description |
| --- | --- |
| `--id` | Provider ID (required). |

Success: `Removed OAuth provider "GitHub" (id: ...)`.

### `oauth-provider enable` / `oauth-provider disable`

Both take `--id`. Success: `Enabled OAuth provider <id>` / `Disabled OAuth provider <id>`. Disabling keeps the provider configured but hides it from the login screen.

---

## `encryption-key` — encryption keys

The encryption key ring is a file (default `{DataDir}/encryption.key`, mode `0600`) holding versioned XChaCha20-Poly1305 keys plus a dedicated token pepper. The **highest version is the active key** used for all new encryption; older versions remain only to decrypt old data. Two admin commands consume the key file: `oauth-provider add` encrypts the client secret with the active key, and `api-token issue` hashes the token secret with the dedicated pepper (a standalone secret, independent of the key ring). The Hub itself also reads the ring for the OAuth token tables and the pepper for validating API and delegation tokens. For the full keystore model, rotation runbook, and backup guidance, see [Encryption & Data](/docs/22-encryption-and-data/).

> **Note:** The API-token / delegation-token pepper is a dedicated, stable secret stored in the key file but **independent** of the encryption key ring, so `rotate`, `reencrypt`, and `remove` never invalidate tokens. To deliberately invalidate every API and delegation token, use `rotate-pepper` (below).

> **Note:** `rotate` and `rotate-pepper` use `--data-dir` only (no `--config`); `reencrypt` and `remove` open the store and accept both `--data-dir` and `--config`.

### `encryption-key rotate`

Generate a new key version and make it active (version = previous + 1). It does **not** re-encrypt existing data; old ciphertext stays readable via the retained old key. The key file must already exist, else: `encryption key file not found at <path>` (with a hint to run the Hub once to auto-generate it). Output:

```text
Added encryption key version 2.
Restart the hub, then run: leapmux admin encryption-key reencrypt
```

### `encryption-key reencrypt`

Re-encrypt every secret that is not already under the active key version — OAuth provider client secrets and OAuth access/refresh tokens — rewriting them under the active version. Run this **after** `rotate` and a Hub restart. Success: `Re-encrypted %d secrets to key version %d.`

### `encryption-key remove`

| Flag | Default | Description |
| --- | --- | --- |
| `--version` | `0` | Key version to remove (must be `>= 1`). |

`remove` opens the database (so it accepts `--config`) to verify the version is unused before deleting it. Errors if `< 1` (`--version is required (must be >= 1)`), if it is the active version (`cannot remove active key version <N>`), if it is absent (`keystore: key version <N> not in ring`), or if any OAuth provider secret or OAuth token is still encrypted under it (`encryption key version <N> still encrypts ...; run 'leapmux admin encryption-key reencrypt' first`). Output:

```text
Removed encryption key version 1.
Restart the hub to apply.
```

> **Warning:** Removing a key version that still has data encrypted under it would make that data permanently undecryptable, so `remove` guards against it: it refuses to delete a version still encrypting OAuth provider secrets or OAuth tokens and tells you to run `reencrypt` first. (Transient `pending_oauth_signups` are not covered by the guard — they auto-expire.) Always run `reencrypt` after restarting the Hub, then `remove`.

### `encryption-key rotate-pepper`

| Flag | Default | Description |
| --- | --- | --- |
| `--yes` | `false` | Required confirmation — this invalidates **all** API and delegation tokens. |

Regenerate the dedicated token pepper. This **invalidates every existing API token and delegation token** (their one-way HMAC hashes can no longer be reproduced), so it is gated behind `--yes`. The encryption key ring is untouched. Without `--yes` it refuses and explains the consequence. With `--yes`:

```text
Regenerated the API-token pepper.
All existing API tokens and delegation tokens are now invalid.
Restart the hub to apply, then re-issue API tokens with: leapmux admin api-token issue
```

Use it after a suspected keystore compromise, or whenever you want to force every client to re-authenticate. Takes effect on the next Hub restart; re-issue tokens with [`api-token issue`](#api-token--durable-api-tokens).

The full rotation runbook:

```bash
leapmux admin encryption-key rotate              # adds version 2, makes it active
# ... restart the hub so it writes new secrets under version 2 ...
leapmux admin encryption-key reencrypt           # rewrites all old secrets to version 2
leapmux admin encryption-key remove --version 1  # once nothing references version 1
# ... restart the hub ...
# Note: rotate-pepper is NOT part of this flow. Key rotation leaves API and
# delegation tokens working; only run rotate-pepper to deliberately invalidate them.
```

---

## `db` — database utilities

Opening the store auto-applies any pending migrations, so a normal Hub start already migrates the schema to the latest version. These commands let you confirm the schema state, find the database file, or roll to a specific version where supported. See [Encryption & Data](/docs/22-encryption-and-data/) for the storage backends and migration model.

### `db path`

Print the resolved SQLite database path (`{DataDir}/hub.db` by default). Uses `--data-dir` only.

> **Note:** `db path` always prints the SQLite path regardless of the configured backend — it does not consult `storage.type`. On a Postgres or MySQL Hub, use your database server's own tooling to locate the data.

### `db version`

Opens the store (which migrates to latest) and prints:

```text
Current schema version: 1
Latest available version: 1
```

### `db migrate`

| Flag | Default | Description |
| --- | --- | --- |
| `--version` | `-1` | Target migration version (`-1` for latest). |

Prints the current and latest versions first. With `--version >= 0` it migrates to that version (`Migrating to version N...`); with the default it confirms `Already at latest version.` or migrates up. It ends with `Migration complete. Current version: N`. Because opening the store already migrates up, an explicit `migrate` is mainly useful for down-migrating to a specific version (where the backend supports it) or as a confirmation.

```bash
leapmux admin db version --config /etc/leapmux/hub.yaml
```

---

## `api-token` — durable API tokens

Durable bearer tokens for headless service accounts, the CLI, and integrations. Issued tokens authenticate the [Remote Control CLI](/docs/16-remote-control-cli/).

### `api-token issue`

| Flag | Default | Description |
| --- | --- | --- |
| `--user` | (required) | User ID the token acts as. |
| `--client-type` | `cli` | Client type (`cli`, `integration`, ...). |
| `--client-name` | (required) | Human-visible client name. |
| `--ttl` | `0` | Access-token TTL in seconds (`0` = default 1h). |

Missing required flags: `--user and --client-name are required`. The access TTL defaults to 1 hour when `--ttl <= 0`; the refresh TTL is fixed at 90 days. The bearer is in the form `lmx_a<token_id>_<secret>` and is **emitted once — it cannot be retrieved later**:

```text
Token minted. Capture it now — it cannot be retrieved later:

  access_token  = lmx_a<id>_<secret>
  refresh_token = lmx_a<id>_<secret>
  token_id      = <id>
```

```bash
# Mint a token for a CI service account
leapmux admin api-token issue --user usr_ci42 --client-name "GitHub Actions"
```

### `api-token list`

| Flag | Default | Description |
| --- | --- | --- |
| `--user` | (empty) | User ID; empty walks all users (capped at 1000). |
| `--client-type` | (empty) | Filter by client type (empty = all). |

Columns (tab-aligned): `ID  USER  TYPE  NAME  CREATED  LAST USED  EXPIRES` (`LAST USED`/`EXPIRES` show `-` when null).

> **Tip:** On large deployments, pass `--user` so the listing does not stop at the 1000-user scan cap.

### `api-token revoke`

| Flag | Description |
| --- | --- |
| `--id` | Token ID (required). |

Marks the token revoked. Not found or already revoked: `token %s not found or already revoked`. Success:

```text
Revoked api_token <id>
note: a running hub will evict the bearer cache and close any open channels
authenticated by this token within its revocation-watcher sweep interval (default 2s)
```

```bash
leapmux admin api-token revoke --id tok_abc123
```

---

## `delegation-token` — Worker-minted delegation tokens

Delegation tokens are minted by Workers for the agents and terminals they spawn. You normally only touch them to audit or force-revoke.

### `delegation-token list`

| Flag | Default | Description |
| --- | --- | --- |
| `--user` | (empty) | User ID; empty walks all users (capped at 1000). |

Columns (tab-aligned): `ID  USER  WORKER  WORKSPACE  AGENT  TERMINAL  CREATED  EXPIRES` (`AGENT`/`TERMINAL` show `-` when empty).

### `delegation-token revoke`

| Flag | Description |
| --- | --- |
| `--id` | Token ID (required). |

Marks the token revoked. Not found or already revoked: `token %s not found or already revoked`. Success:

```text
Revoked delegation_token <id>
note: hub revocation watcher will pick this up on its next sweep (default ~2s)
```

The minting Worker may also revoke it in-process via its own endpoint for zero-latency eviction; the admin command guarantees revocation regardless.

---

## Common recipes

**Create an org with an owner.** Creating a user automatically provisions their personal org with them as owner:

```bash
leapmux admin user create --username owner --email owner@example.com --email-verified --admin
```

**Reset a user's password (and kick existing sessions).**

```bash
leapmux admin user reset-password --username alice
```

**List and revoke sessions.**

```bash
leapmux admin session list
leapmux admin session revoke --id ses_abc123          # one session
leapmux admin session revoke-user --username alice     # all of a user's sessions + tokens
```

**Approve a Worker.** The admin CLI does not mint registration keys — generate one from the "Register worker" dialog in the web UI, run the Worker with it, then verify and (if needed) revoke leftover keys:

```bash
leapmux admin worker reg-key list                      # see live keys
leapmux admin worker list                              # confirm the worker registered
leapmux admin worker reg-key revoke --id rk_old        # revoke an unused key
```

**Add an OAuth provider.**

```bash
leapmux admin oauth-provider add --type github \
  --client-id Iv1.abc123 --client-secret "$GH_SECRET"
leapmux admin oauth-provider list
```

**Create, re-encrypt under, and rotate an encryption key.**

```bash
leapmux admin encryption-key rotate
# restart the hub
leapmux admin encryption-key reencrypt
leapmux admin encryption-key remove --version 1
# restart the hub
```

**Invalidate all API and delegation tokens.** Regenerate the token pepper — for example after a suspected keystore compromise — then restart the Hub and re-issue tokens. This is independent of encryption-key rotation (the recipe above) and does not touch the key ring:

```bash
leapmux admin encryption-key rotate-pepper --yes
# restart the hub
leapmux admin api-token issue --user usr_... --client-name "ci-runner"   # re-issue as needed
```

**Check and run DB migrations.**

```bash
leapmux admin db version --config /etc/leapmux/hub.yaml
leapmux admin db migrate --config /etc/leapmux/hub.yaml
```

**Issue, list, and revoke an API token.**

```bash
leapmux admin api-token issue --user usr_ci42 --client-name "CI bot"
leapmux admin api-token list --user usr_ci42
leapmux admin api-token revoke --id tok_abc123
```

## See also

- [Managing Workers](/docs/19-managing-workers/) — registration keys, approval, TOFU pinning, and Worker selection.
- [Authentication Providers](/docs/21-authentication-providers/) — configuring GitHub, Google, Apple, and generic OIDC sign-in end to end.
- [Encryption & Data](/docs/22-encryption-and-data/) — keystore internals, key rotation, storage backends, migrations, and backup/restore.
- [Remote Control CLI](/docs/16-remote-control-cli/) — controlling a *running* Hub over the network with an API token.
- [Configuration](/docs/18-configuration/) — config file keys, precedence, and storage settings.
