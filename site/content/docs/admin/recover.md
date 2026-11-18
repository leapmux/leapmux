---
title: "Recovery"
description: "Use leapmux recover to bootstrap the first admin, reset a password with the hub stopped, rotate encryption keys, and inspect the database directly."
type: docs
weight: 11
---

`leapmux recover` is the **offline break-glass** command tree. It operates **directly against the Hub's database and on-disk encryption key file** — no running Hub, no network call, no login. You run it on the machine that holds the Hub's data directory, typically as the same OS user that runs the Hub.

Every other administration task — users, sessions, workers, OAuth providers, captcha, rate limits, instance settings, API tokens, delegation tokens — runs **online, authenticated, over RPC** with [`leapmux control admin ...`](/docs/admin/admin-cli/), or from the Preferences dialog's administration panels.

{{< callout type="warning" >}}
Because `leapmux recover` writes straight to the database, anyone who can run it has full control over the Hub's data. That is exactly its purpose — break-glass — and its entire surface is four groups. Protect the data directory and the hosts that can reach it. There is no per-command authentication.
{{< /callout >}}

## The tree

```text
leapmux recover bootstrap create-admin <username>   # refuses when any admin exists
leapmux recover password reset <user>               # prompts for the new password
leapmux recover db path | version | migrate
leapmux recover encryption-key rotate | remove <version> | reencrypt | rotate-pepper
```

`<user>` is a username, or `id:<id>` for a user id, as in [`leapmux control admin`](/docs/admin/admin-cli/). Running `leapmux recover` or one of its groups alone prints its help. `--help` works at every level, and so does `leapmux help recover <group>`. Any command name can be shortened as far as it stays unambiguous.

### Locating the data: `--data-dir` and `--config`

Every command resolves the database and the encryption key through two common flags:

| Flag | Type | Default | Purpose |
| --- | --- | --- | --- |
| `--data-dir` | string | (empty) | Data directory of the instance. Empty falls back to the `data_dir` of `--config`, or to `~/.config/leapmux/hub` without it. |
| `--config` | string | (empty) | Path to a Hub config file; loads its storage settings so the command targets the same backend the Hub uses. |

- With `--config`, storage settings come from the config file and the `LEAPMUX_HUB_*` variables; an explicit `--data-dir` overrides its `data_dir`. A `--config` path that does not exist fails, so a mistyped path never opens another database.
- Without `--config`, the command reads no config file and no variable, and opens the SQLite database in `--data-dir`. One rule serves a Hub, a solo and a dev instance: the default config file of one mode would select the wrong database for another.
- The SQLite database defaults to `{DataDir}/hub.db`; the encryption key file to `{DataDir}/encryption.key`.

{{< callout >}}
If your Hub runs on Postgres or MySQL, always pass `--config /path/to/hub.yaml`. Without it the CLI builds a SQLite-only config and would operate on `{DataDir}/hub.db` instead of your real backend.
{{< /callout >}}

`db path`, `encryption-key rotate`, and `encryption-key rotate-pepper` open no database: they read the key file or print the path. The other commands open the database, and create the data directory when it is absent. A command checks its arguments first, so a usage error creates nothing.

## `bootstrap create-admin`

Creates the **first administrator** on a hub that has no administrator yet — the one identity operation that cannot require an authenticated admin to already exist.

```bash
leapmux recover bootstrap create-admin alice
# (prompts for the password)
```

| Argument or flag | Description |
| --- | --- |
| `<username>` | Required. |
| `--password` | Prompted with no echo when omitted; fails when stdin is not a terminal. |
| `--display-name` | Optional display name; the username when omitted. |

The created user is always an administrator. The command **refuses once any admin exists**, and it points you at [`control admin user create --admin`](/docs/admin/admin-cli/) instead.

Every later user — admin included — is created online through [`control admin user create`](/docs/admin/admin-cli/).

## `password reset`

Resets any user's password with the hub stopped — the break-glass path when no administrator can sign in. Use it when the hub cannot serve; while the hub runs, [`leapmux control admin user reset-password`](/docs/admin/admin-cli/#user-passwords) does the same work over RPC and needs an administrator login.

```bash
leapmux recover password reset alice
# New password: (no echo)
```

| Argument or flag | Description |
| --- | --- |
| `<user>` | Required. A username, or `id:<id>` for a user id. |
| `--password` | New password; prompted (no echo) when omitted. Fails when you omit the flag and stdin is not a terminal, because there is nothing to prompt. |

On success the password is updated, **every passkey on the account is deleted**, every session for the user is revoked, and all their API and delegation tokens are revoked. The revocations are written in the same transaction, so a running hub applies them automatically. The command reports the user it reset, by username and by ID, and states that it revoked the sessions and passkeys.

Passwords are 8–128 printable ASCII characters, spaces included (see [Password requirements](/docs/using/accounts/#password-requirements)).

## `encryption-key` — encryption keys

The encryption key ring is a file (default `{DataDir}/encryption.key`, mode `0600`) holding versioned XChaCha20-Poly1305 keys plus a dedicated token pepper. The **highest version is the active key** used for all new encryption; older versions remain only to decrypt old data. For the full keystore model and backup guidance, see [Encryption & Data](/docs/admin/encryption-and-data/).

{{< callout type="info" >}}
The token pepper is a dedicated, stable secret stored in the key file but **independent** of the key ring. `rotate`, `reencrypt`, and `remove` leave it unchanged. To invalidate the credential classes listed under `rotate-pepper` below, use that command.
{{< /callout >}}

{{< callout type="info" >}}
`rotate` and `rotate-pepper` read the key file and open no database; `reencrypt` and `remove` open the database too.
{{< /callout >}}

### `encryption-key rotate`

Generate a new key version and make it active. It does **not** re-encrypt existing data. The key file must already exist (run the Hub once to auto-generate it). The command reports the version it added, and it tells you to restart the hub and then to run `leapmux recover encryption-key reencrypt`.

### `encryption-key reencrypt`

Re-encrypts every secret not already under the active key version — OAuth provider client secrets, OAuth access/refresh tokens, hub-settings secret halves, and passkey public keys. Run after `rotate` and a hub restart.

### `encryption-key remove <version>`

`<version>` is the key version to remove, from `1` to `4294967295`.

Refuses the active version, an absent version, or a version that still encrypts anything (`...; run 'leapmux recover encryption-key reencrypt' first`). Always `reencrypt` after a restart, then `remove`.

### `encryption-key rotate-pepper`

| Flag | Default | Description |
| --- | --- | --- |
| `--yes` | `false` | Required confirmation for the credential invalidations listed below. |

Regenerates the token pepper. The encryption key ring stays unchanged. Restart the Hub to apply. Pepper rotation, or a lost `encryption.key`, has these effects:

- Signs out every session
- Invalidates every Worker auth token
- Invalidates API and delegation tokens and confidential app client secrets
- Invalidates live registration keys and pending device codes

Account-recovery links do not use the pepper. Pepper rotation does not invalidate them. Their existing expiry, attempt limits, and consumption checks still apply.

Solo mode and the desktop app reissue their own Worker's token. For each standalone Worker:

1. Remove it in the Hub's Worker list.
2. Stop it.
3. Delete its `state.json`.
4. Register it with a new registration key. It receives a new Worker ID.

Re-issue API tokens online with `leapmux control admin api-token issue <user> --hub <url>`.

The rotation runbook:

```bash
leapmux recover encryption-key rotate              # adds version 2, makes it active
# ... restart the hub so it writes new secrets under version 2 ...
leapmux recover encryption-key reencrypt           # rewrites all old secrets to version 2
leapmux recover encryption-key remove 1            # once nothing references version 1
```

## `db` — database utilities

Opening the store auto-applies pending migrations, so a normal hub start already migrates the schema. These commands confirm state, find the file, or roll to a specific version where supported.

### `db path`

Prints the resolved SQLite database path. (Always the SQLite path; on Postgres/MySQL use your database server's tooling.)

### `db version`

```text
Current schema version: 1
Latest available version: 1
```

### `db migrate`

| Flag | Default | Description |
| --- | --- | --- |
| `--to` | the latest version | Target migration version, from `0`. |

Mainly useful for down-migrating (where the backend supports it) or as a confirmation — opening the store already migrates up.

```bash
leapmux recover db migrate --config /etc/leapmux/hub.yaml
```

## Related chapters

- [Admin CLI](/docs/admin/admin-cli/) — the online `control admin` surface (users, sessions, tokens, workers, OAuth, settings).
- [Configuration](/docs/admin/configuration/) — what each instance setting does.
- [Encryption & Data](/docs/admin/encryption-and-data/) — the keystore model.
