---
title: "Recovery"
description: "Offline break-glass administration with leapmux recover: first-admin bootstrap, password reset, encryption key rotation, and database utilities."
type: docs
weight: 14
---

`leapmux recover` is the **offline break-glass** command tree. It operates directly against the Hub's database and on-disk `encryption.key` file — no running Hub, no network call, and no login. Run it on the Hub host as the OS account that owns the Hub's data directory.

For online administration on a running Hub (users, sessions, Workers, sign-in providers, apps, settings, tokens), use [`leapmux admin`](/docs/admin/admin-cli/) or **Preferences → Administration**.

## Command overview

```text
leapmux recover create-admin <username>        # refuses once any admin exists
leapmux recover reset-password <user>          # prompts for new password offline
leapmux recover db path | version | migrate
leapmux recover encryption-key rotate | reencrypt | remove <version> | rotate-pepper
```

`<user>` accepts a username, `username:<name>`, or `id:<id>`.

## Locating the Hub data directory

Every `leapmux recover` command accepts `--data-dir` and `--config`:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--data-dir` | *(empty → `data_dir` of `--config`, or `~/.config/leapmux/hub`)* | Directory directly containing `hub.db` and `encryption.key`. |
| `--config` | *(empty)* | Path to `hub.yaml` so `recover` connects to the same PostgreSQL, MySQL, or SQLite backend as the Hub. |

{{< callout type="warning" >}}
**`solo` and `dev` store Hub files inside `<data_dir>/hub/`, NOT `<data_dir>/`!**

Passing the top-level `solo` or `dev` data directory (`~/.config/leapmux/solo` or `~/.config/leapmux/dev`) opens or creates an **orphan database** instead of your real Hub database. Always pass the `hub/` subdirectory:

| Mode | Default Hub data directory to pass to `--data-dir` |
| --- | --- |
| `hub` | `~/.config/leapmux/hub` (or your `hub.yaml` `--config` path) |
| `solo` | `~/.config/leapmux/solo/hub` |
| `dev` | `~/.config/leapmux/dev/hub` |
{{< /callout >}}

Path resolution rules:

- **With `--config`:** reads storage settings from that YAML file and `LEAPMUX_HUB_*` variables (an explicit `--data-dir` overrides `data_dir`). A missing `--config` file fails immediately. Always pass `--config /path/to/hub.yaml` when the Hub uses PostgreSQL, MySQL, CockroachDB, YugabyteDB, or TiDB.
- **Without `--config`:** ignores config files and environment variables and opens SQLite at `<data-dir>/hub.db` with `<data-dir>/encryption.key`.
- `db path`, `encryption-key rotate`, and `encryption-key rotate-pepper` do not open the database; all other commands open it without running auto-migrations (except `create-admin`, which bootstraps a fresh database).

## `create-admin`

Creates the **first administrator** on a Hub with no existing administrators:

```bash
leapmux recover create-admin alice --config /etc/leapmux/hub.yaml
# Password: (no echo)
```

| Argument or flag | Meaning |
| --- | --- |
| `<username>` | Required username for the first administrator. |
| `--password` | Password; prompted with no echo when omitted (fails if stdin is not a terminal). |
| `--display-name` | Optional display name (defaults to `<username>`). |

`create-admin` refuses once any administrator exists. Create all subsequent accounts online with [`leapmux admin user create`](/docs/admin/admin-cli/#users-and-administrator-membership).

## `reset-password`

Resets any user's password offline when no administrator can sign in:

```bash
leapmux recover reset-password alice --config /etc/leapmux/hub.yaml
# New password: (no echo)
```

| Argument or flag | Meaning |
| --- | --- |
| `<user>` | Required: a username, `username:<name>`, or `id:<id>`. |
| `--password` | New password (8–128 printable ASCII characters); prompted with no echo when omitted. |

On success, `reset-password` updates the password, **deletes every passkey on the account**, and revokes all sessions, API tokens, and delegation tokens in the same transaction. It requires an existing `encryption.key` and never auto-migrates. While the Hub is running, use [`leapmux admin user reset-password`](/docs/admin/admin-cli/#users-and-administrator-membership) instead.

## `encryption-key`

Manage the Hub's `XChaCha20-Poly1305` key ring and token pepper in `encryption.key` (all four commands require an existing key file):

| Command | Opens DB? | What it does |
| --- | --- | --- |
| `leapmux recover encryption-key rotate` | No | Appends a new active key version without modifying existing rows. |
| `leapmux recover encryption-key reencrypt` | Yes | Re-encrypts all persistent encrypted rows still under older versions to the active version. |
| `leapmux recover encryption-key remove <version>` | Yes | Deletes a retired version (`1`–`4294967295`); refuses if the version is active or still encrypts any persistent row. |
| `leapmux recover encryption-key rotate-pepper --yes` | No | Regenerates the token pepper (invalidating all sessions, Worker tokens, API/delegation tokens, confidential app secrets, and registration keys on next Hub restart). |

Routine key rotation sequence:

```bash
leapmux recover encryption-key rotate --config /etc/leapmux/hub.yaml
leapmux daemon hub restart --config /etc/leapmux/hub.yaml
leapmux recover encryption-key reencrypt --config /etc/leapmux/hub.yaml
leapmux recover encryption-key remove 1 --config /etc/leapmux/hub.yaml
leapmux daemon hub restart --config /etc/leapmux/hub.yaml
```

For the full key-rotation runbook, pepper-rotation blast radius, and standalone Worker recovery steps after rotating the pepper, see [Encryption & Data — Key rotation](/docs/admin/encryption-and-data/#key-rotation-vs-pepper-rotation).

## `db` — database utilities

Normal Hub startup runs schema migrations automatically. `db version` and `db migrate` require an existing `encryption.key` and open the database without auto-migrating first (pass `--config` for non-SQLite backends):

- **`leapmux recover db path`** — prints the resolved SQLite path (`{data_dir}/hub.db` or `storage.sqlite.path`; always reports the SQLite path even when another backend is configured).
- **`leapmux recover db version`** — prints the database's current and latest available schema versions.
- **`leapmux recover db migrate [--to <version>]`** — migrates to the latest version (or a specific `--to` target where supported).

```bash
leapmux recover db version --config /etc/leapmux/hub.yaml
leapmux recover db migrate --config /etc/leapmux/hub.yaml
```

{{< callout type="warning" >}}
Anyone with shell access to run `leapmux recover` has direct, unauthenticated control over the Hub's database and encryption keys. Restrict OS permissions on the Hub host and data directory accordingly.
{{< /callout >}}

## Related chapters

- [Encryption & Data](/docs/admin/encryption-and-data/) — keystore design, full key/pepper rotation runbook, and backups.
- [Admin CLI](/docs/admin/admin-cli/) — online `leapmux admin` commands for a running Hub.
- [Configuration Reference](/docs/admin/configuration-reference/#storage-backends) — Hub storage backends and `encryption_key_path`.
- [Accounts & Connected Apps](/docs/using/accounts-and-apps/) — password rules and self-service recovery.
