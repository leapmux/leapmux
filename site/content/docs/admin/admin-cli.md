---
title: "Admin CLI"
description: "Use leapmux admin to administer a running Hub over RPC: settings, users, sessions, Workers, apps, sign-in providers, captcha, rate limits, tokens, and network status."
type: docs
weight: 13
---

`leapmux admin` is the online, authenticated command tree for administering a **running** Hub over RPC. It uses the same target selection (`--hub`, `LEAPMUX_HUB`), output formatting (`--json` envelope `{"data": ...}`), and error conventions as the rest of `leapmux` (see [CLI Guide](/docs/using/cli-guide/)). For every flag and example, see [Admin Command Reference](/docs/reference/commands/admin/). For offline break-glass administration with the Hub stopped, see [Recovery](/docs/admin/recover/).

```text
leapmux admin settings          list | get <key> | set <key> <value> | set-secret <key> <json> | reset <key>
leapmux admin user              list | get <user> | create <username> | update <user> | delete <user>
                                        grant-admin <user> | revoke-admin <user> | reset-password <user>
leapmux admin session           list | revoke <session> | revoke-user <user> | purge-expired
leapmux admin worker            list | get <worker> | deregister <worker>
leapmux admin worker reg-key    list | revoke <reg-key> | purge-expired
leapmux admin app               list | register <name> | update <app> | verify <app> | unverify <app>
                                        allow-elevation <app> | deny-elevation <app> | revoke <app> | delete <app>
leapmux admin idp               add | list | enable <idp> | disable <idp> | rotate-secret <idp> | remove <idp>
leapmux admin captcha           get | set | enable | disable | reset
leapmux admin rate-limit        list | set <operation> | enable <operation> | disable <operation> | reset <operation>
leapmux admin api-token         list | issue <user> | revoke <token>
leapmux admin delegation-token  list | revoke <token>
leapmux admin network           status
```

### Target selectors and authentication

- **`<user>`** accepts a username, `username:<name>`, `id:<id>`, or a unique user ID prefix of at least 4 characters.
- **Every other entity argument** accepts a full ID, `id:<id>`, a unique ID prefix of at least 4 characters, `name:<name>`/`title:<title>`, or a bare case-insensitive name/title match.
- Admin commands never accept `self`, and take neither `--data-dir` nor `--config`.
- **Admin commands refuse to run inside a LeapMux tab** (whenever `LEAPMUX_CONTROL_SOCK` is set, even with `--hub`), so coding agents inside tabs cannot act as administrators. Run `leapmux admin` from your own shell outside LeapMux.

### Elevation and admin scopes

Authorize your CLI with the `admin:*` scopes you need (`admin:read`, `admin:users`, `admin:settings`, `admin:workers`, `admin:apps`):

```bash
leapmux auth login --hub https://hub.example.com --scope "admin:read admin:settings admin:users admin:workers admin:apps"
```

Every Hub administration command — including reads — requires an administrator account and an active **step-up elevation window** ({{< duration elevation-window >}}, extended by writes up to {{< duration elevation-cap >}}). Listing, retiring (`revoke`), and deleting unused app registrations under `leapmux admin app` require no elevation.

When your CLI credential lacks a current window, `leapmux` triggers browser step-up automatically (a loopback browser prompt via `GET /oauth/authorize?step_up=...` for `leapmux auth login`, or `POST /oauth/step-up` with a verification URL at `/oauth/device` and user code for `--device-code` and service-account tokens) and retries the command once approved. **Durable-authority user commands — `admin user create`, `reset-password`, `grant-admin`, `revoke-admin`, and `update --email`/`--email-verified` — require either an elevated browser session or an elevated `leapmux auth login` CLI credential holding `admin:users` (service-account tokens and third-party OAuth apps are refused).** See [Security & Authentication — Verifying a command-line credential](/docs/admin/security-and-auth/#verifying-a-command-line-credential).

## Hub settings

```bash
leapmux admin settings list
leapmux admin settings get smtp
leapmux admin settings set smtp '{"port":465}'
leapmux admin settings set-secret smtp '{"password":"..."}'
leapmux admin settings reset smtp
```

`set` merges a partial JSON document (or bare scalar) onto the key's current value, validates the merged document, and commits it atomically.

- **`hot` keys** take effect **immediately** on commit (the Hub updates its live snapshot in memory; only one serving Hub runs per database).
- **`restart` keys** take effect after `leapmux daemon hub restart`.

Each command reports `propagation` as `hot` or `restart`. See [Configuration Reference — Instance settings](/docs/admin/configuration-reference/#instance-settings-database) for all keys, JSON shapes, and defaults.

## Network status

Inspect the Hub's live bind status, local IPC endpoints, configured `extra_listen_addresses`, and host network interfaces:

```bash
leapmux admin network status
leapmux admin network status --json
```

`network status` reports each bound TCP address, its source (startup flag vs. `extra_listen_addresses`), and any OS bind error alongside the host's network interfaces — useful when troubleshooting `listen` binds or solo `Network access` listeners.

## Captcha

`leapmux admin captcha` wraps the `captcha.*` settings keys in atomic commands:

```bash
leapmux admin captcha get
leapmux admin captcha set --provider turnstile --site-key 0x4AAAA... --secret 0x4AAAA...
leapmux admin captcha set --cost 20000            # tune the active provider in place
leapmux admin captcha enable
leapmux admin captcha disable
leapmux admin captcha reset [--provider altcha|recaptcha_v3|turnstile]
```

| Flag | Owning provider | Meaning |
| --- | --- | --- |
| `--provider` | any | Select `altcha`, `recaptcha_v3`, or `turnstile` (also enables captcha). Omit to tune the active provider in place. |
| `--algorithm`, `--cost`, `--memory-cost`, `--parallelism`, `--expires` | `altcha` | ALTCHA proof-of-work algorithm, cost parameters, and challenge expiry in seconds. |
| `--site-key`, `--secret` | `recaptcha_v3`, `turnstile` | External provider site key and encrypted secret (pass both when selecting an external provider). |
| `--min-score` | `recaptcha_v3` | Minimum score (`> 0` and `<= 1`). |

Flags that belong to a different provider than the target are refused. `disable` turns off captcha verification while leaving the honeypot active.

## Rate limits

`leapmux admin rate-limit` manages all **five** catalogued operations (`rate_limit.<operation>`):

| Operation | Limits | Keyed by |
| --- | --- | --- |
| `elevation` | Failed identity verification attempts for sensitive changes | User (omitted in `solo`) |
| `email_change` | Email-change requests reaching SMTP | User (omitted in `solo`) |
| `login_anonymous` | Failed password sign-in attempts | Verified client IP (active in `solo`, `dev`, `hub`) |
| `oauth_anonymous` | Anonymous OAuth 2.1 endpoints (`/oauth/*` and app icons) | Verified client IP (active in `solo`, `dev`, `hub`) |
| `solo_password_setup` | Initial TCP password-setup requests in `solo` mode | Verified client IP (active in `solo`) |

```bash
leapmux admin rate-limit list
leapmux admin rate-limit set elevation --max-attempts 5 --window 900
leapmux admin rate-limit enable elevation
leapmux admin rate-limit disable elevation
leapmux admin rate-limit reset elevation
```

`--max-attempts` accepts `1`–`1000`; `--window` accepts `60`–`86400` seconds. `set` updates only the flags you pass and never re-enables a limiter you disabled with `disable`.

## Users and administrator membership

With an elevated credential holding `admin:users`, you can manage the full user lifecycle from the CLI:

```bash
leapmux admin user list
leapmux admin user create alice --display-name "Alice" --admin
leapmux admin user update alice --email alice@example.com --email-verified true
leapmux admin user grant-admin alice
leapmux admin user revoke-admin bob --force
leapmux admin user reset-password alice
leapmux admin user delete bob
```

- **Last-administrator protection:** the Hub always retains at least one active administrator and refuses to delete or demote the last one, even concurrently. Pass `--force` to confirm deleting another administrator (`user delete --force`) or demoting yourself (`user revoke-admin --force`); neither flag overrides the last-administrator rule.
- **Password prompts:** omitting `--password` on `create` or `reset-password` prompts with no echo so passwords stay out of shell history and process lists.
- **Reset blast radius:** `user reset-password` deletes every passkey and revokes every session, API token, and delegation token for that user (including your own session if you reset your own password). For offline resets with the Hub stopped, see [`leapmux recover reset-password`](/docs/admin/recover/#reset-password).

## Sign-in providers (`idp`)

Manage GitHub, Google, Sign in with Apple, and generic OIDC providers over RPC:

```bash
leapmux admin idp add --type apple --client-id com.example.web --client-secret <es256-jwt>
leapmux admin idp list
leapmux admin idp rotate-secret <idp> --client-secret <new-secret>
leapmux admin idp disable <idp>
leapmux admin idp enable <idp>
leapmux admin idp remove <idp> [--force]
```

`rotate-secret` updates the encrypted client secret in place and evicts the Hub's cached provider config immediately without changing `<idp>` or removing linked user accounts — ideal for rotating Apple's 6-month ES256 client secret JWT. See [Sign-in Providers](/docs/admin/sign-in-providers/).

## API tokens

```bash
leapmux admin api-token issue ci-bot --installation-name "ci-runner-3" --ttl 3600
```

`<user>` accepts a username, `username:<name>`, `id:<id>`, or a unique 4+ character user ID prefix. The command prints the credential secrets **once**.

- **Omit `--ttl` (or pass `0`):** mints a renewing credential (access token {{< duration access-token >}} + refresh token, up to {{< duration absolute-cap >}}), unless issued by another CLI credential.
- **Pass `--ttl <seconds>`:** mints a fixed-lifetime service credential with no refresh token (up to {{< duration absolute-cap >}}).
- **`--scope`:** omitting `--scope` grants all non-admin owner permissions. Admin scopes require both the target user and the issuer to hold them; a credential can never issue one wider than itself.

{{< callout type="info" >}}
When SMTP is configured and the target user's email is verified, the Hub emails the user whenever an administrator issues a token for their account, listing the installation name and every granted scope.
{{< /callout >}}

## Headless service accounts

For CI runners and automation, issue a service-account token as shown under [API tokens](#api-tokens) above:

```bash
leapmux admin api-token issue ci-bot --installation-name "ci-runner-3" \
  --scope "admin:read admin:workers" --ttl 86400
```

Save the printed `access_token` (`lmx_a<id>_<secret>`) in a credential file at `LEAPMUX_CONTROL_CONFIG_DIR/credentials/<hub-key>.json` (see [CLI Guide](/docs/using/cli-guide/)) or pass it in `Authorization: Bearer`.

- Service tokens belong to the built-in **service account** registration and appear under the user's connected apps.
- A credential issued by another CLI credential does not renew and expires no later than its issuer, capping delegation chains.
- Any admin command run with a service token requires an active step-up window approved in an elevated browser via the device-code ceremony (`/oauth/step-up`). An unattended job can run elevated commands while its window is open; a human must approve a new window after expiry.

## See also

- [CLI Guide](/docs/using/cli-guide/) — authentication, targeting, output formats, and exit codes.
- [Admin Command Reference](/docs/reference/commands/admin/) — complete flag-by-flag reference for `leapmux admin`.
- [Recovery](/docs/admin/recover/) — offline break-glass commands when the Hub is stopped.
- [Security & Authentication](/docs/admin/security-and-auth/) — elevation windows and step-up ceremonies.
- [Configuration Reference](/docs/admin/configuration-reference/) — every Hub instance setting.
