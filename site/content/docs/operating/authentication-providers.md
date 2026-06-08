---
title: "Authentication Providers"
description: "Configure OAuth/OIDC single sign-on for a LeapMux Hub so users can log in with GitHub, Google, Apple, or any standards-compliant OpenID Connect provider."
type: docs
weight: 5
---

This chapter is for operators who run a LeapMux **Hub** and want to offer single sign-on. It covers configuring OAuth/OIDC providers so your users can sign in with GitHub, Google, Apple, or any standards-compliant OpenID Connect identity provider.

If you only want to know what the sign-in experience looks like as an end user (the "Sign in with…" buttons, completing a first OAuth sign-up, linking and unlinking accounts), see [Accounts & Authentication](/docs/using/accounts/). This chapter stays on the operator side: how providers are defined, how callback URLs are derived, and how to troubleshoot setup.

> **Note:** OAuth providers only apply to **Hub** deployments (`leapmux hub`, and `leapmux dev`). In **solo mode** every request is auto-authenticated as the local admin, so there is no login screen and providers do nothing. See [Running LeapMux](/docs/operating/running-leapmux/) for the difference between run modes.

## What you can configure

LeapMux supports two stored provider kinds, selected by the `--type` you pass when adding a provider:

| You pass `--type` | Stored as | Discovery | Notes |
|---|---|---|---|
| `github` | `github` | n/a (GitHub-specific endpoints) | First-class GitHub OAuth app support. |
| `google` | `oidc` | OpenID Connect | Preset issuer `https://accounts.google.com`. |
| `apple` | `oidc` | OpenID Connect | Preset issuer `https://appleid.apple.com`. |
| `oidc` | `oidc` | OpenID Connect | Generic provider for any compliant issuer (Okta, Auth0, Keycloak, Microsoft Entra ID, your own IdP, …). |

Under the hood there are only **two** stored provider types — `github` and `oidc`. `google` and `apple` are convenience presets that fill in the issuer URL and scopes for you, then store as `oidc`. You can add the same provider type more than once (for example two separate generic OIDC issuers); each gets its own row, its own button, and its own callback URL.

GitHub uses plain OAuth2, not OpenID Connect, so it has no issuer URL or discovery document — its endpoints are fixed. The issuer URL applies only to the `oidc`-stored providers, where the Hub uses it for discovery and ID-token verification.

All provider configuration is done with the `leapmux admin oauth-provider` command group, which operates directly on the Hub's database and encryption key file. There is **no UI** for adding providers — it is an operator task. For the full admin CLI surface, see [Admin CLI](/docs/operating/admin-cli/).

> **Note:** The admin CLI reads and writes the Hub's data store on disk. Run it on the machine that hosts the Hub, pointing it at the same data directory (or config file) the Hub uses. The admin CLI defaults to `~/.config/leapmux/hub`, which matches `leapmux hub`. For a `leapmux dev` instance the Hub store lives under the mode's own directory, so pass `--data-dir ~/.config/leapmux/dev/hub`. Otherwise pass `--data-dir` or `--config` to target a different location. See [Admin CLI](/docs/operating/admin-cli/) for how the data directory is resolved.

## Prerequisites

Before you add a provider, get two things in order.

### 1. Sign-up must be enabled for new OAuth users

When a user signs in with OAuth and **no** existing account is linked to that identity, LeapMux treats it as a new sign-up and sends them to a "Complete Sign Up" page to choose a username. That path is gated by the **`--signup-enabled`** flag (config key `signup_enabled`, default **false**).

- With `--signup-enabled=false`, an OAuth sign-in for an unknown identity is rejected with `sign-up is disabled; no existing account linked to this identity`. Only users whose OAuth identity is already linked (or auto-linked by verified email — see [Trusting the provider's email](#trusting-the-providers-email)) can sign in.
- With `--signup-enabled=true`, new OAuth users can self-register through the completion page.

Decide which behavior you want and set the flag accordingly. See [Configuration](/docs/operating/configuration/) and [Running LeapMux](/docs/operating/running-leapmux/) for where flags and config keys live.

### 2. The Hub must know its public origin

LeapMux derives every OAuth **callback URL** from the Hub's public base URL. If that origin is wrong, the provider will redirect users to an address that doesn't reach your Hub, and login fails. This is the single most common setup mistake — read [Callback and login URLs](#callback-and-login-urls) before you register an app with your provider.

## Adding a provider

The command is:

```bash
leapmux admin oauth-provider add \
  --type <github|google|apple|oidc> \
  --client-id <id> \
  --client-secret <secret> \
  [--name <display name>] \
  [--issuer-url <url>] \
  [--scopes "<space separated>"] \
  [--trust-email=<true|false>]
```

### Flags

| Flag | Required | Description |
|---|---|---|
| `--type` | Yes | One of `github`, `google`, `apple`, `oidc`. Selects the preset. |
| `--client-id` | Yes | The OAuth client ID issued by your provider. |
| `--client-secret` | Yes | The OAuth client secret. Encrypted at rest with the Hub's active encryption key before it is stored. |
| `--name` | For `oidc` | Display name shown on the sign-in button. Presets supply a default name (e.g. `GitHub`); generic `oidc` has none, so `--name` is required. |
| `--issuer-url` | For OIDC | The OIDC issuer URL. Presets for `google`/`apple` set this; generic `oidc` requires it. Silently ignored for `github` (plain OAuth2 has no issuer). |
| `--scopes` | No | Space-separated scopes. Falls back to the preset default if omitted. |
| `--trust-email` | For `oidc` | `true` or `false`. Whether to treat the provider's email as verified for auto-linking. Presets for `github`/`google`/`apple` default to `true`; generic `oidc` requires an explicit value. |

### Preset defaults

If you omit `--name`, `--issuer-url`, `--scopes`, or `--trust-email`, these defaults apply per type:

| `--type` | Default name | Default issuer | Default scopes | Default `trust-email` |
|---|---|---|---|---|
| `github` | `GitHub` | (none) | `read:user user:email` | `true` |
| `google` | `Google` | `https://accounts.google.com` | `openid profile email` | `true` |
| `apple` | `Apple` | `https://appleid.apple.com` | `openid name email` | `true` |
| `oidc` | (none — must pass `--name`) | (none — must pass `--issuer-url`) | `openid profile email` | (none — must pass `--trust-email`) |

> **Note:** OAuth sign-in **requires** a verified email from the provider — LeapMux refuses an identity that returns no email. Keep an email scope in your scope list (`user:email` for GitHub, `email`/`openid` for OIDC). If you remove it, sign-in fails with `OAuth provider did not return an email address; ensure the 'email' scope is granted`.

### What happens when you add a provider

1. The flags are merged with the preset.
2. For OIDC-based types (`google`, `apple`, `oidc`), the Hub validates the issuer over the network first, printing `Validating OIDC issuer <url> ...`. It fetches the issuer's OpenID Connect discovery document; if that fails, the command aborts with `issuer validation failed: …` and nothing is stored. GitHub providers skip this step.
3. The client secret is encrypted with the Hub's **active encryption key** and stored. (For key rotation and re-encryption, see [Encryption & Data](/docs/operating/encryption-and-data/).)
4. The provider is created **enabled** and assigned a generated ID.
5. The command prints `Created OAuth provider "<name>" (id: <id>, type: <github|oidc>)`.

> **Important:** Note the printed `id`. You need it to build the exact callback URL to register with your provider, and to `enable`/`disable`/`remove` the provider later.

### Examples

GitHub (relies entirely on presets — only client credentials needed):

```bash
leapmux admin oauth-provider add \
  --type github \
  --client-id Iv1.abc123def456 \
  --client-secret 0123456789abcdef0123456789abcdef01234567
```

Google (presets supply the issuer and scopes; override the display name):

```bash
leapmux admin oauth-provider add \
  --type google \
  --name "Sign in with Google" \
  --client-id 1234567890-abc.apps.googleusercontent.com \
  --client-secret GOCSPX-xxxxxxxxxxxxxxxxxxxxxxxx
```

Generic OIDC (e.g. Okta, Auth0, Keycloak, Entra ID):

```bash
leapmux admin oauth-provider add \
  --type oidc \
  --name "Acme SSO" \
  --issuer-url https://acme.okta.com \
  --client-id 0oa1b2c3d4e5f6g7h8i9 \
  --client-secret super-secret-value \
  --scopes "openid profile email" \
  --trust-email=true
```

> **Tip:** If you are not sure which flags a preset requires or what the exact help text is, run `leapmux admin oauth-provider add --help`.

## Callback and login URLs

LeapMux derives both the user-facing login URL and the provider callback URL from the Hub's **base URL**, using the provider's generated ID:

- **Login URL** (the button target): `<base-url>/auth/oauth/<providerID>/login`
- **Callback URL** (what you register with the provider): `<base-url>/auth/oauth/<providerID>/callback`

You must register the **callback URL** as an allowed redirect URI in your provider's OAuth app settings. Because `<providerID>` is generated when you run `add`, you can only build the exact URL **after** creating the provider. The usual order is:

1. Create the provider with `leapmux admin oauth-provider add` (note the printed `id`).
2. Construct the callback URL: `<base-url>/auth/oauth/<id>/callback`.
3. Paste that into your provider's "Authorized redirect URI" / "Callback URL" field.

### How the base URL is determined

The Hub computes its base URL as follows:

1. If **`--public-url`** is set (config key `public_url`), that value is used verbatim. This is the case for almost any production deployment behind a reverse proxy or a TLS terminator.
2. Otherwise, the base URL is derived from the listen address and cookie settings:
   - Scheme is `https` when **`secure_cookies`** is enabled, otherwise `http`.
   - The host is the `--listen` value. A bare `:port` (e.g. `:4327`) becomes `localhost:<port>`.

| Scenario | `--public-url` | `secure_cookies` | `--listen` | Resulting base URL |
|---|---|---|---|---|
| Behind a reverse proxy | `https://hub.example.com` | (any) | (any) | `https://hub.example.com` |
| Local dev, no proxy | (unset) | `false` | `:4327` | `http://localhost:4327` |
| Direct TLS, no proxy | (unset) | `true` | `:4327` | `https://localhost:4327` |

So if your provider's callback URL is `https://hub.example.com`, run the Hub with `--public-url https://hub.example.com`. For a Google provider created with id `prov_abc123`, the value you register with Google would be:

```text
https://hub.example.com/auth/oauth/prov_abc123/callback
```

> **Warning:** `--public-url` must be a bare absolute origin — scheme + host (+ port), with **no path, query, or fragment**. Sub-path deployments such as `https://example.com/leapmux` are rejected. Front LeapMux at the root of a hostname (or subdomain), not under a path prefix.

> **Note:** `secure_cookies` is a **config-file-only** setting (config key `secure_cookies`); there is no CLI flag for it. When you terminate TLS with a reverse proxy and set `--public-url https://…`, the public URL already carries the `https` scheme, so the callback URL is correct regardless of `secure_cookies`. Still set `secure_cookies: true` behind TLS so session cookies get the secure flag — see [Accounts & Authentication](/docs/using/accounts/). Details of the reverse-proxy setup live in [Running LeapMux](/docs/operating/running-leapmux/).

## Trusting the provider's email

The `--trust-email` flag controls **account auto-linking** by verified email:

- **`--trust-email=true`** — when an OAuth sign-in returns a verified email that matches an existing LeapMux account's verified email, the OAuth identity is linked to that account automatically and the user is logged in. This lets a user who already signed up with a password later "Sign in with GitHub" and land in the same account without manual linking.
- **`--trust-email=false`** — no auto-linking. A new OAuth identity always goes through the "Complete Sign Up" flow (or is rejected if sign-up is disabled), even if the email matches an existing account.

> **Warning:** Set `--trust-email=true` only for providers you control or fully trust to assert email ownership. Auto-linking grants access to an existing account based solely on a matching verified email; an identity provider that lets users set an arbitrary verified email could be used to take over accounts. For a public, multi-tenant IdP, prefer `--trust-email=false`. Regardless of this flag, LeapMux still requires the provider to mark the email as verified (OIDC `email_verified=true`; GitHub's primary verified email).

## Listing, enabling, disabling, and removing providers

### List

```bash
leapmux admin oauth-provider list
```

Columns: `ID  TYPE  NAME  TRUST_EMAIL  ENABLED`. When none are configured it prints `No OAuth providers configured.` Only **enabled** providers appear as sign-in buttons.

### Enable / disable

Disabling a provider hides its button and rejects new sign-ins through it without deleting the configuration, so you can re-enable it later without re-registering the OAuth app.

```bash
leapmux admin oauth-provider disable --id <providerID>
leapmux admin oauth-provider enable  --id <providerID>
```

### Remove

```bash
leapmux admin oauth-provider remove --id <providerID>
```

This deletes the provider configuration (including the stored client secret) permanently.

> **Tip:** A running Hub caches built provider configurations. Treat a provider's settings as immutable once created — to change a client ID, secret, issuer, scopes, or `trust-email`, `remove` the provider and `add` it again. Re-adding generates a **new** provider ID, which changes the callback URL, so update your provider's registered redirect URI to match.

## The end-user experience

Once at least one provider is enabled, LeapMux shows the buttons automatically:

- The **login** page shows a "Sign in with …" section above the username/password form (e.g. "Sign in with GitHub"), separated by an "or" divider.
- The **sign-up** page (when `--signup-enabled=true`) shows a "Sign up with …" section above the form, separated by an "or create an account with email" divider.
- A first-time OAuth user is taken to a **"Complete Sign Up"** page to choose a username; the email (if the provider supplied one) is shown read-only.
- Users can link and unlink OAuth identities from the **Profile** dialog's "Linked Accounts" section. A user cannot unlink their only login method without first setting a password.

All of this UI is covered in detail in [Accounts & Authentication](/docs/using/accounts/); it requires no extra configuration beyond enabling the provider.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Provider's page shows "redirect URI mismatch" / "invalid redirect_uri" | The redirect URI registered with the provider doesn't exactly match `<base-url>/auth/oauth/<id>/callback`. | Run `leapmux admin oauth-provider list` to get the `id`, rebuild the callback URL, and register it verbatim (scheme, host, port, path all matter). |
| Users land on `http://localhost:4327/...` instead of your domain, or login loops | The Hub's base URL is wrong because `--public-url` is unset behind a proxy. | Start the Hub with `--public-url https://your-host` and re-derive/re-register the callback URL. See [Callback and login URLs](#callback-and-login-urls). |
| `add` fails with `issuer validation failed: …` | The OIDC issuer URL is wrong/unreachable, or its discovery document is invalid. | Verify `--issuer-url` is the canonical issuer (LeapMux fetches its OpenID Connect discovery document). Confirm the Hub host has network access to it. For Google/Apple use the preset; do not append a path. |
| `--type is required (github, google, apple, oidc)` or `unknown provider type` | Missing or misspelled `--type`. | Pass one of `github`, `google`, `apple`, `oidc`. |
| `--name is required for generic OIDC providers` | Generic `oidc` with no `--name`. | Pass `--name "<display name>"`. |
| `--issuer-url is required for OIDC providers` | Generic `oidc` with no `--issuer-url`. | Pass `--issuer-url https://your-issuer`. |
| `--trust-email is required for generic OIDC providers …` | Generic `oidc` with no `--trust-email`. | Pass `--trust-email=true` or `--trust-email=false` (read [Trusting the provider's email](#trusting-the-providers-email) first). |
| Sign-in fails with `OAuth provider did not return an email address; ensure the 'email' scope is granted` | The provider returned no email, usually because the email scope is missing. | Include an email scope (`user:email` for GitHub; `email`/`openid` for OIDC) and grant it in the provider's app settings. |
| OAuth login rejected with `sign-up is disabled; no existing account linked to this identity` | `--signup-enabled=false` and no account is linked to that identity. | Enable sign-up (`--signup-enabled=true`), or have the user link the identity from their Profile after a password login. |
| Button doesn't appear | Provider is disabled, or not created. | `leapmux admin oauth-provider list`; if `ENABLED` is `no`, run `enable --id <id>`. |
| Changed scopes/secret but behavior is unchanged | The Hub caches provider config as immutable. | `remove` and re-`add` the provider, then update the registered callback URL to the new id, and restart the Hub if needed. |

> **Tip:** If a stored client secret can no longer be decrypted after an encryption-key change, you have rotated keys without re-encrypting secrets. Run the re-encryption step described in [Encryption & Data](/docs/operating/encryption-and-data/).

## Related chapters

- [Accounts & Authentication](/docs/using/accounts/) — the end-user sign-in, sign-up, and account-linking experience.
- [Admin CLI](/docs/operating/admin-cli/) — full `leapmux admin` reference, including data-directory resolution.
- [Running LeapMux](/docs/operating/running-leapmux/) — run modes, listen addresses, `--public-url`, and reverse-proxy setup.
- [Configuration](/docs/operating/configuration/) — config precedence and the full key reference (`signup_enabled`, `public_url`, `secure_cookies`, …).
- [Encryption & Data](/docs/operating/encryption-and-data/) — how client secrets are encrypted at rest, key rotation, and re-encryption.
