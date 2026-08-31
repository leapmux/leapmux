---
title: "Admin CLI"
description: "leapmux control admin administers a running Hub over RPC: settings, users, sessions, Workers, sign-in providers, captcha, rate limits, and tokens."
type: docs
weight: 9
---

This chapter is the `leapmux control admin ...` command tree: the online, authenticated face of hub administration. It uses the same transport, authentication, JSON output envelope, and entity-ID flags as the rest of `leapmux control` — see [Control CLI](/docs/using/control-cli/) for those mechanics. For the offline break-glass tree (`bootstrap`, `password`, `encryption-key`, `db`), see [Recovery](/docs/admin/recover/).

Every group calls the hub's Admin RPCs with your normal control credential. The hub itself checks that the caller is an administrator, and answers `permission_denied` to every other login.

```text
leapmux control admin settings     list | get KEY | set KEY VALUE | set-secret KEY JSON | reset KEY
leapmux control admin user         list | get | create | update | delete | grant-admin | revoke-admin | reset-password | list-sessions
leapmux control admin session      list | revoke | revoke-user | purge-expired
leapmux control admin worker       list | get | deregister
leapmux control admin worker reg-key  list | revoke | purge-expired
leapmux control admin app         list | register | update | verify | unverify | allow-elevation | deny-elevation | revoke | delete
leapmux control admin idp  add | list | remove | enable | disable
leapmux control admin captcha      show | set | enable | disable | reset
leapmux control admin rate-limit   list | set | enable | disable | reset
leapmux control admin api-token    list | issue | revoke
leapmux control admin delegation-token  list | revoke
```

These verbs are RPC calls, so there is no `--data-dir` and no `--config`; neither flag exists on any admin leaf. `--hub` behaves like every other control group. Output uses the control JSON envelope (`{"data": ...}`) — pipe it through `jq`.

> **Offline break-glass is `leapmux recover`.** First-admin bootstrap, password reset with the hub stopped, `db`, and `encryption-key` surgery stay offline — see [Recovery](/docs/admin/recover/). Everything else is here.

## The admin gate and the worker-IPC transport

Admin commands **never** use the worker-IPC transport: they refuse when `LEAPMUX_CONTROL_SOCK` is set, because the worker's IPC bridge is a typing device, not a security boundary. From inside a spawned agent there is no way to reach the admin surface; run these from your own machine with an admin login.

## Hub settings

```bash
leapmux control admin settings list
leapmux control admin settings get smtp
leapmux control admin settings set smtp '{"port":465}'
leapmux control admin settings set-secret smtp '{"password":"..."}'
leapmux control admin settings reset smtp
```

`set` merges a partial JSON document (or a bare scalar) onto the key's current value. The hub validates the merged value and commits it in one transaction. The CLI refuses a value that opens a JSON document but does not parse; that check runs locally, before the CLI contacts the hub.

**Every write here needs an admin-scoped credential that verified recently.** Several of these keys are the hub's own security controls, so `set`, `set-secret` and `reset` each require a proven factor — from a command-line credential exactly as from a browser session.

You do not run a separate command. A refused command prints an address and a short code, waits while you approve it in a browser, and then runs. The credential stays verified for {{< duration elevation-window >}}, and every write slides that window forward. Reads (`list`, `get`) need no verification at all. See [Verifying a command-line credential](/docs/admin/security/#verifying-a-command-line-credential).

**When a write takes effect** depends on the key's propagation class. A `hot` key reaches the hub instance that serves the write at once, because that instance replaces its cached settings snapshot right after the commit. Another hub instance on the same database picks the same change up within ~30 seconds, the lifetime of its own settings cache. A `restart` key applies only after a hub restart.

Every verb that reports one key states the class: `list`, `get`, and `set` each carry a `propagation` field of `hot` or `restart`, and the Preferences dialog's administration panels show a "Requires Restart" badge.

[Configuration](/docs/admin/configuration/) documents what each key does. The [`captcha`](#captcha) and [`rate-limit`](#rate-limits) groups are sugar over the same settings keys; each composes the partial documents for you.

## Captcha

The `captcha` group is client-side sugar over the `captcha.*` settings keys: each verb composes the partial JSON documents for you and sends them in one atomic write.

```bash
leapmux control admin captcha show
leapmux control admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux control admin captcha set --cost 20000            # tune the active provider in place
leapmux control admin captcha enable
leapmux control admin captcha disable
leapmux control admin captcha reset [--provider altcha|recaptcha_v3|turnstile]
```

`show` reports every captcha settings key (`captcha.enabled`, `captcha.selected`, `captcha.altcha`, `captcha.recaptcha_v3`, `captcha.turnstile`). `enable` and `disable` take no flags, and `disable` leaves the honeypot check active.

`set` flags, and the provider that owns each one:

| Flag | Owning provider | Meaning |
| --- | --- | --- |
| `--provider` | any | Target and select `altcha`, `recaptcha_v3`, or `turnstile`. Omit it to tune the active provider in place. |
| `--algorithm` | `altcha` | ALTCHA algorithm. |
| `--cost` | `altcha` | Algorithm cost parameter. |
| `--memory-cost` | `altcha` | Algorithm memory cost. |
| `--parallelism` | `altcha` | Algorithm parallelism. |
| `--expires` | `altcha` | Challenge expiry in seconds. |
| `--site-key` | `recaptcha_v3`, `turnstile` | Provider site key. |
| `--secret` | `recaptcha_v3`, `turnstile` | Provider secret. The hub stores it encrypted. |
| `--min-score` | `recaptcha_v3` | Minimum score, greater than 0 and not greater than 1. |

The CLI refuses four mistakes:

- A tuning flag whose owning provider is not the target is **refused**, never dropped and never applied to a different key. The error identifies the flag and the target provider.
- An empty `--site-key` or `--secret` is refused, because an empty half fails every verification.
- An invocation that passes no flag at all is refused; pass `--provider` or a tuning flag.
- The hub refuses a selected external provider whose key pair is incomplete, so pass `--site-key` and `--secret` in the same invocation that selects `recaptcha_v3` or `turnstile`.

`--provider` also **enables** captcha when it switches provider, so a hub you disabled for debugging does not stay undefended through a provider change. Tuning in place leaves the switch alone.

`reset` with no flag returns every captcha key to its default. `reset --provider X` resets that provider's row only; when X is the selected provider, the command returns the selection to its ALTCHA default first, so every intermediate state stays legal.

## Rate limits

The `rate-limit` group is sugar over the `rate_limit.<operation>` settings keys. Two operations are catalogued, and a typo answers with the known names before the CLI dials the hub:

| Operation | Limits | Keyed by |
|---|---|---|
| `elevation` | Failed attempts to verify your identity for a sensitive change. | The user. Hidden in solo mode, which has one. |
| `oauth_anonymous` | The authorization server's anonymous endpoints — `/oauth/device-authorization`, `/oauth/token`, `/oauth/revoke`, `/oauth/register`, `/oauth/step-up`, and the app icons. | The client address. Enforced in solo mode too, because those endpoints are served there. |

```bash
leapmux control admin rate-limit list
leapmux control admin rate-limit set --operation elevation --max-attempts 5 --window 900
leapmux control admin rate-limit enable  --operation elevation
leapmux control admin rate-limit disable --operation elevation
leapmux control admin rate-limit reset   --operation elevation
```

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--operation` | `set`, `enable`, `disable`, `reset` | The operation to limit. Required; known values: `elevation`, `oauth_anonymous`. |
| `--max-attempts` | `set` | Failed attempts allowed per window (1–1000). |
| `--window` | `set` | Window length in seconds (60–86400). |

`list` takes no flags and reports every `rate_limit.*` key. `set` needs `--max-attempts`, `--window`, or both; it merges the field you pass and keeps the other. `set` never writes the switch — `enable` and `disable` own it — so adjusting a window cannot re-arm a limiter that you deliberately turned off.

## User passwords

```bash
leapmux control admin user reset-password --username alice
# New password: (no echo)
```

Address the user with `--id` or `--username`. The CLI prompts for the password when `--password` is omitted, so the secret stays out of the shell history and out of the process table.

This verb needs a **browser session that verified recently**, and an API token cannot run it — see [Headless service accounts](#headless-service-accounts). With the Hub stopped, `leapmux recover` resets a password offline.

A reset destroys every credential the old password authenticated: all of the user's sessions are deleted, and all of their API and delegation tokens are revoked. The envelope reports the two token counts. Resetting your **own** password ends your own sessions and tokens too, including the credential that made the call — log in again with the new password.

The offline twin is [`leapmux recover password reset`](/docs/admin/recover/#password-reset), for a hub that is stopped.

## API tokens

```bash
leapmux control admin api-token issue --user-id usr_... --installation-name "ci-bot" --ttl 3600
```

Address the owner with `--user-id` or `--username`; the `user` verbs spell the first flag `--id`. The envelope carries the secrets exactly once; they cannot be retrieved later. Use the access token as the bearer for a headless `LEAPMUX_HUB=...` control CLI.

`--ttl` picks **which kind of credential** this is, and the two kinds are exclusive:

- **Omit it** (or pass `0`) for the ordinary renewing credential: an access token that lives {{< duration access-token >}} plus a refresh token, exactly what `auth login` mints. The envelope carries `access_token`, `refresh_token` and `token_id`.
- **Pass a number of seconds** for a fixed-lifetime service credential. It lives exactly that long, up to {{< duration absolute-cap >}}, and it carries **no refresh token** — the envelope's `refresh_token` is empty. Nothing renews it. When the issuer is itself a command-line credential, its remaining life caps the TTL (see [Headless service accounts](#headless-service-accounts)).

The two do not combine. A credential with both a long TTL and a refresh token loses the TTL the first time it renews, because the row records an expiry and never the lifetime it was minted from.

The hub emails the owner whenever this verb issues a credential for them, on the same terms as a browser consent: only to a verified address, and only when SMTP is configured.

`--scope` specifies the permissions the credential holds; omitting it grants everything the owner can do **except** administer the hub. The hub refuses an admin permission for an owner who is not an administrator, rather than minting a credential whose grant and authority disagree. It also refuses to issue a credential **wider than the one issuing it**, so a chain of self-issued credentials terminates at the browser consent that started it.

`api-token list` reports `granted_scopes` on every row, so "which credentials can administer this hub" is answerable. The whole vocabulary is in [App Authorization](/docs/admin/app-authorization/#permissions).

## Headless service accounts

The interactive `leapmux control auth login` flows ([Control CLI](/docs/using/control-cli/#authentication)) are for humans. For unattended scripts and integrations, mint a durable bearer token with `leapmux control admin` instead:

```bash
leapmux control admin api-token issue --user-id usr_... --installation-name "ci-bot"
```

This prints an `access_token` of the form `lmx_a<id>_<secret>` exactly once. Write it into a credential file under `LEAPMUX_CONTROL_CONFIG_DIR` — see [Credential file location](/docs/using/control-cli/#credential-file-location) — or send it yourself as the `Authorization: Bearer` header. Issuing, listing, and revoking these tokens is covered under [API tokens](#api-tokens) below.

A credential issued this way belongs to the built-in **service account** registration rather than to the control CLI. It appears in the owner's connected-apps list under that name.

A service account that must run `leapmux control admin ...` needs the admin permissions too. Name them, and only for an owner who is already an administrator:

```bash
leapmux control admin api-token issue --user-id usr_... --installation-name "ci-bot" \
  --scope "admin:read admin:users"
```

A credential can never issue one wider than itself. An administrator running this from a credential that holds `admin:users` alone is refused a request for `tunnel:open`, so the chain terminates at the browser consent that started it.

**What an admin-scoped credential cannot do.** One rule decides it, rather than a list of verb names: **an admin verb that creates a new way into an account needs a browser session that verified recently.** A command-line credential is refused there however recently it verified, because the verb hands out authority the credential itself did not have, and the session that would verify it is the granting one.

Today the rule covers four things:

- `user create` — a new account, optionally an administrator, with a password the caller picks.
- `user reset-password` — a password on any account, without the old one.
- `user grant-admin` **and** `user revoke-admin` — one hub procedure carries both directions, so the refusal covers the demotion as well as the promotion. Plan an emergency demotion from a browser; a CI credential cannot run it.
- `user update --email` and `user update --email-verified` — the address receives the recovery link, so writing one hands over a way in.

Every other admin write that needs verification accepts a **command-line credential that verified recently**: `api-token issue`, `user delete`, `user update --display-name` and `--clear-pending-email`, `settings set` / `set-secret` / `reset` with the `captcha` and `rate-limit` sugar over them, and the `idp` add, remove, enable and disable verbs. Reads need no verification at all.

`api-token issue` is on that second list because a headless service account has to be able to renew. What limits it instead is the credential it mints: **a credential issued by another credential does not renew, and it expires no later than the one that issued it.** So a chain of self-issued credentials gets shorter each time and ends at the browser consent that started it. To issue one that renews, run the verb from a browser-backed session.

## See also

- [Control CLI](/docs/using/control-cli/) — authentication, the JSON envelope, entity-ID resolution, and the user-facing command groups.
- [Configuration](/docs/admin/configuration/) — what each hub instance setting does.
- [Recovery](/docs/admin/recover/) — the offline break-glass tree, for when the hub cannot serve.
- [Sign-in Providers](/docs/admin/sign-in-providers/) — the `idp` verbs in their administrator context.
- [App Authorization](/docs/admin/app-authorization/) — the `app` verbs and the permission vocabulary.
- [Security & Threat Model](/docs/admin/security/) — why every write here needs a recently proven factor.
