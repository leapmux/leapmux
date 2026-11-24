---
title: "Admin CLI"
description: "leapmux admin administers a running Hub over RPC: settings, users, sessions, Workers, sign-in providers, captcha, rate limits, and tokens."
type: docs
weight: 10
---

This chapter is the `leapmux admin ...` command tree: the online, authenticated face of hub administration. It selects the Hub, signs in, prints its output and reports its errors as the rest of `leapmux` does — see [Control CLI](/docs/using/control-cli/) for those mechanics. The [Control Command Reference](/docs/reference/control-commands/#leapmux-admin) lists every admin command with its arguments, flags and examples. For the offline break-glass tree (`create-admin`, `reset-password`, `encryption-key`, `db`), see [Recovery](/docs/admin/recover/).

The CLI uses your normal control credential. The Hub requires administrator privileges and a current step-up window for every procedure on an `Admin*` service, including reads. The `admin app` commands call `AppService`. This service checks ownership and requires the `admin:apps` permission from bearer credentials. Registering, updating, vouching for an app, and changing its elevation setting require a current window. Listing apps, retiring an app, and deleting an empty registration work without one. See [Session elevation](/docs/admin/security/#session-elevation).

```text
leapmux admin settings          list | get <key> | set <key> <value> | set-secret <key> <json> | reset <key>
leapmux admin user              list | get <user> | create <username> | update <user> | delete <user>
                                        grant-admin <user> | revoke-admin <user> | reset-password <user>
leapmux admin session           list | revoke <session> | revoke-user <user> | purge-expired
leapmux admin worker            list | get <worker> | deregister <worker>
leapmux admin worker reg-key    list | revoke <reg-key> | purge-expired
leapmux admin app               list | register <name> | update <app> | verify <app> | unverify <app>
                                        allow-elevation <app> | deny-elevation <app> | revoke <app> | delete <app>
leapmux admin idp               add | list | remove <idp> | enable <idp> | disable <idp>
leapmux admin captcha           get | set | enable | disable | reset
leapmux admin rate-limit        list | set <operation> | enable <operation> | disable <operation> | reset <operation>
leapmux admin api-token         list | issue <user> | revoke <token>
leapmux admin delegation-token  list | revoke <token>
leapmux admin network           status
```

`<user>` is a username, `username:<name>`, `id:<id>`, or a unique user ID prefix of at least 4 characters. Every other entity argument accepts a full ID, `id:<id>`, a unique ID prefix of at least 4 characters, `name:<name>`/`title:<title>`, or a bare case-insensitive name/title match. Admin commands never accept `self`, because they act across all users.

These commands are RPC calls, so there is no `--data-dir` and no `--config`; neither flag exists on any admin command. `--hub` selects the Hub as it does for every other control command. The output is text; `--json` prints the JSON envelope (`{"data": ...}`) for `jq`.

> **Offline break-glass is `leapmux recover`.** First-admin bootstrap, password reset with the hub stopped, `db`, and `encryption-key` surgery stay offline — see [Recovery](/docs/admin/recover/). Everything else is here.

## Admin commands stay out of a LeapMux tab

Admin commands **never** run inside a LeapMux tab. They refuse when `LEAPMUX_CONTROL_SOCK` is set, even with an explicit `--hub`, because that flag would carry your stored credential and an agent could act as an administrator through it. The Worker's socket is a convenience for the tab, not a security boundary. Run these commands from your own shell, outside LeapMux, with an admin login.

## Hub settings

```bash
leapmux admin settings list
leapmux admin settings get smtp
leapmux admin settings set smtp '{"port":465}'
leapmux admin settings set-secret smtp '{"password":"..."}'
leapmux admin settings reset smtp
```

`set` merges a partial JSON document (or a bare scalar) onto the key's current value. The hub validates the merged value and commits it in one transaction. The CLI refuses a value that opens a JSON document but does not parse; that check runs locally, before the CLI contacts the hub.

**Every command here needs an admin-scoped credential that holds a current window.** Several of these keys are the hub's own security controls, and the rule covers the reads too: `list` and `get` ask for a proof exactly as `set` does, from a command-line credential as from a browser session. A read needs the window and does not slide it.

You do not run a separate command. The default ceremony opens a browser on the CLI's own machine, and the refused command runs once you prove the factor there. A credential from `leapmux auth login --device-code`, or one an administrator issued, instead prints an address and a short code to approve in a browser. The window lasts {{< duration elevation-window >}}, and every write slides it forward. See [Verifying a command-line credential](/docs/admin/security/#verifying-a-command-line-credential).

**When a write takes effect** depends on the key's propagation class. A `hot` key reaches the serving Hub at once, because it replaces its cached settings snapshot after the commit. The runtime lease permits one serving Hub per database. A new Hub loads stored settings when it starts. A `restart` key applies only after a Hub restart.

`list`, `get`, `set`, `set-secret`, and `reset` each state the class in a `propagation` field of `hot` or `restart`, and the Preferences dialog's administration panels show a "Requires Restart" badge.

[Configuration](/docs/admin/configuration/) documents what each key does. The [`captcha`](#captcha) and [`rate-limit`](#rate-limits) groups are sugar over the same settings keys; each composes the partial documents for you.

## Captcha

The `captcha` group is client-side sugar over the `captcha.*` settings keys: each verb composes the partial JSON documents for you and sends them in one atomic write.

```bash
leapmux admin captcha get
leapmux admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux admin captcha set --cost 20000            # tune the active provider in place
leapmux admin captcha enable
leapmux admin captcha disable
leapmux admin captcha reset [--provider altcha|recaptcha_v3|turnstile]
```

`get` reports every captcha settings key (`captcha.enabled`, `captcha.selected`, `captcha.altcha`, `captcha.recaptcha_v3`, `captcha.turnstile`). `enable` and `disable` take no flags, and `disable` leaves the honeypot check active.

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

The `rate-limit` group is sugar over the `rate_limit.<operation>` settings keys. Four operations are catalogued, and a typo answers with the known names before the CLI dials the hub:

| Operation | Limits | Keyed by |
|---|---|---|
| `elevation` | Failed attempts to verify your identity for a sensitive change. | The user. Hidden in solo mode, which has one. |
| `email_change` | Requests to change an account email that reach the mail machinery. | The user. Hidden in solo mode, which refuses email changes. |
| `login_anonymous` | Failed passwords at the sign-in form. | The client address. Enforced in solo mode too, which puts no captcha in front of sign-in. |
| `oauth_anonymous` | The authorization server's anonymous endpoints — `/oauth/device-authorization`, `/oauth/token`, `/oauth/revoke`, `/oauth/register`, `/oauth/step-up`, and the app icons. | The client address. Enforced in solo mode too, because those endpoints are served there. |

The two address-keyed budgets use the verified client IP. By default, this is the direct TCP peer. The `trusted_proxy_ranges` setting lets a verified proxy supply `Forwarded` or X-Forwarded data. The Hub ignores those headers from every other peer. See [Trusted reverse proxies](/docs/admin/configuration/#trusted-reverse-proxies).

```bash
leapmux admin rate-limit list
leapmux admin rate-limit set elevation --max-attempts 5 --window 900
leapmux admin rate-limit enable elevation
leapmux admin rate-limit disable elevation
leapmux admin rate-limit reset elevation
```

| Argument or flag | Applies to | Meaning |
| --- | --- | --- |
| `<operation>` | `set`, `enable`, `disable`, `reset` | The operation to limit: `elevation`, `email_change`, `login_anonymous` or `oauth_anonymous`. |
| `--max-attempts` | `set` | Failed attempts allowed per window (1–1000). |
| `--window` | `set` | Window length in seconds (60–86400). |

`list` takes no flags and reports every `rate_limit.*` key. `set` needs `--max-attempts`, `--window`, or both; it merges the field you pass and keeps the other. `set` never writes the switch — `enable` and `disable` own it — so adjusting a window cannot re-arm a limiter that you deliberately turned off.

## Administrator membership

The Hub retains at least one live administrator after setup. It refuses to delete or demote the last administrator, including when requests run concurrently. Deleted accounts do not count as remaining administrators.

The Hub serializes administrator removal with a shared in-memory mutex. Deletion and signup rollback for ordinary users do not acquire that mutex. The Hub checks the current role while holding the user lock.

`user delete --force` confirms the deletion of an administrator, and `user revoke-admin --force` the removal of your own administrator access. Neither overrides the last-administrator rule. Create or promote another administrator before deleting or demoting the current one.

## User passwords

```bash
leapmux admin user reset-password alice
# New password: (no echo)
```

`<user>` is a username, or `id:<id>`. The CLI prompts for the password when `--password` is omitted, so the secret stays out of the shell history and out of the process table.

This verb needs a **browser session that verified recently**, and an API token cannot run it — see [Headless service accounts](#headless-service-accounts). With the Hub stopped, `leapmux recover` resets a password offline.

A reset destroys every credential the old password authenticated: all of the user's sessions are deleted, and all of their API and delegation tokens are revoked. The envelope reports the two token counts. Resetting your **own** password ends your own sessions and tokens too, including the credential that made the call — log in again with the new password.

The offline twin is [`leapmux recover reset-password`](/docs/admin/recover/#reset-password), for a hub that is stopped.

## API tokens

```bash
leapmux admin api-token issue ci-bot --installation-name "ci-runner-3" --ttl 3600
```

The owner, `<user>`, takes the same forms as in every `user` command: a username, or `id:<id>`. The result carries the secrets exactly once; they cannot be retrieved later. Use the access token as the bearer for a headless `LEAPMUX_HUB=...` control CLI.

`--ttl` picks **which kind of credential** this is, and the two kinds are exclusive:

- **Omit it** (or pass `0`) for the ordinary renewing credential: an access token that lives {{< duration access-token >}} plus a refresh token, exactly what `auth login` mints. The envelope carries `access_token`, `refresh_token` and `token_id`.
- **Pass a number of seconds** for a fixed-lifetime service credential. It lives exactly that long, up to {{< duration absolute-cap >}}, and it carries **no refresh token** — the envelope's `refresh_token` is empty. Nothing renews it. When the issuer is itself a command-line credential, its remaining life caps the TTL (see [Headless service accounts](#headless-service-accounts)).

The two do not combine. A credential with both a long TTL and a refresh token loses the TTL the first time it renews, because the row records an expiry and never the lifetime it was minted from.

The hub emails the owner whenever this verb issues a credential for them, on the same terms as a browser consent: only to a verified address, and only when SMTP is configured.

`--scope` specifies the permissions the credential holds; omitting it grants everything the owner can do **except** administer the hub. The hub refuses an admin permission for an owner who is not an administrator, rather than minting a credential whose grant and authority disagree. It also refuses to issue a credential **wider than the one issuing it**, so a chain of self-issued credentials terminates at the browser consent that started it.

`api-token list` reports `granted_scopes` on every row, so "which credentials can administer this hub" is answerable. The whole vocabulary is in [App Authorization](/docs/admin/app-authorization/#permissions).

## Headless service accounts

The interactive `leapmux auth login` flows ([Control CLI](/docs/using/control-cli/#authentication)) are for humans. For unattended scripts and integrations, mint a durable bearer token with `leapmux admin` instead:

```bash
leapmux admin api-token issue ci-bot --installation-name "ci-runner-3"
```

This prints an `access_token` of the form `lmx_a<id>_<secret>` exactly once. Write it into a credential file under `LEAPMUX_CONTROL_CONFIG_DIR` — see [Credential file location](/docs/using/control-cli/#credential-file-location) — or send it yourself as the `Authorization: Bearer` header. Issuing, listing, and revoking these tokens is covered under [API tokens](#api-tokens) below.

A credential issued this way belongs to the built-in **service account** registration rather than to the control CLI. It appears in the owner's connected-apps list under that name.

A service account that must run `leapmux admin ...` needs the admin permissions too. Name them, and only for an owner who is already an administrator:

```bash
leapmux admin api-token issue ci-bot --installation-name "ci-runner-3" \
  --scope "admin:read admin:users"
```

A credential can never issue one wider than itself. An administrator running this from a credential that holds `admin:users` alone is refused a request for `tunnel:open`, so the chain terminates at the browser consent that started it.

**What an admin-scoped credential cannot do.** One rule decides it, rather than a list of verb names: **an admin verb that creates a new way into an account needs a browser session that verified recently.** A command-line credential is refused there however recently it verified, because the verb hands out authority the credential itself did not have, and the session that would verify it is the granting one.

Today the rule covers four things:

- `user create` — a new account, optionally an administrator, with a password the caller picks.
- `user reset-password` — a password on any account, without the old one.
- `user grant-admin` **and** `user revoke-admin` — one hub procedure carries both directions, so the refusal covers the demotion as well as the promotion. Plan an emergency demotion from a browser; a CI credential cannot run it.
- `user update --email` and `user update --email-verified` — the address receives the recovery link, so writing one hands over a way in.

The remaining `Admin*` procedures accept a **command-line credential that holds a current window**. These include `api-token issue`, `user delete`, and the `--display-name` and `--clear-pending-email` options of `user update`. The window also covers `settings set`, `set-secret`, and `reset`, including the `captcha` and `rate-limit` commands that use them. It covers `idp add`, `remove`, `enable`, and `disable` as well. Reads such as `settings list` also need the window. A job can run these commands unattended while its credential holds a current window. A person must approve a new window after it expires. Scope the credential to what the job needs. See [Session elevation](/docs/admin/security/#session-elevation).

`api-token issue` is on that second list because a headless service account has to be able to renew. What limits it instead is the credential it mints: **a credential issued by another credential does not renew, and it expires no later than the one that issued it.** So a chain of self-issued credentials gets shorter each time and ends at the browser consent that started it. To issue one that renews, run the verb from a browser-backed session.

## See also

- [Control CLI](/docs/using/control-cli/) — authentication, the output, the errors, targets, and the user-facing command groups.
- [Control Command Reference](/docs/reference/control-commands/) — every command with its arguments, flags and examples.
- [Configuration](/docs/admin/configuration/) — what each hub instance setting does.
- [Recovery](/docs/admin/recover/) — the offline break-glass tree, for when the hub cannot serve.
- [Sign-in Providers](/docs/admin/sign-in-providers/) — the `idp` verbs in their administrator context.
- [App Authorization](/docs/admin/app-authorization/) — the `app` verbs and the permission vocabulary.
- [Security & Threat Model](/docs/admin/security/) — why every write here needs a recently proven factor.
