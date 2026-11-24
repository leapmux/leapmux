---
title: "Sign-in Providers"
description: "Configure OAuth and OpenID Connect single sign-on for a LeapMux Hub: GitHub, Google, Sign in with Apple (including secret rotation), and generic OIDC."
type: docs
weight: 8
---

This chapter explains how administrators configure OAuth and OpenID Connect (OIDC) sign-in providers on a LeapMux **Hub** (`hub` and `dev` modes; `solo` mode uses a single local account and does not use sign-in providers). For apps requesting access *to* your Hub, see [App Authorization](/docs/admin/app-authorization/). For the end-user sign-in and account-linking experience, see [Accounts & Connected Apps](/docs/using/accounts-and-apps/).

## Supported provider types

Select a preset with `--type` when adding a provider. Internally, LeapMux stores two provider kinds — `github` (plain OAuth 2.0) and `oidc` (OpenID Connect discovery and ID-token verification). You can add multiple providers of the same type; each gets its own ID, sign-in button, and callback URL.

| `--type` | Stored as | Default display name | Default issuer URL | Default scopes | Default `--trust-email` |
| --- | --- | --- | --- | --- | --- |
| `github` | `github` | `GitHub` | *(none)* | `read:user user:email` | `true` |
| `google` | `oidc` | `Google` | `https://accounts.google.com` | `openid profile email` | `true` |
| `apple` | `oidc` | `Apple` | `https://appleid.apple.com` | `openid name email` | `true` |
| `oidc` | `oidc` | *(required)* | *(required)* | `openid profile email` | *(required)* |

Providers are managed exclusively through `leapmux admin idp` against a **running** Hub (no `--data-dir` or `--config`). Authorize your CLI with an administrator account holding `admin:read` and `admin:settings` scopes (`leapmux auth login --hub https://hub.example.com --scope "admin:read admin:settings"`). See [Admin CLI](/docs/admin/admin-cli/).

## Prerequisites

1. **Decide on new-user sign-up (`signup_enabled`).** Unknown OAuth identities go through **Complete Sign Up** only when `signup_enabled` is `true` (`leapmux admin settings set signup_enabled true`). When `false` (default in `hub` mode), only already-linked accounts — or accounts auto-linked by verified email when `--trust-email=true` — can sign in.
2. **Configure `public_url`.** Every callback URL is derived from the Hub's base URL. Behind a reverse proxy, set `leapmux admin settings set public_url "https://hub.example.com"` first.

## Adding a provider

```bash
leapmux admin idp add \
  --type <github|google|apple|oidc> \
  --client-id <id> \
  --client-secret <secret> \
  [--name <display name>] \
  [--issuer-url <url>] \
  [--scopes "<space separated>"] \
  [--trust-email=<true|false>]
```

For OIDC providers (`google`, `apple`, `oidc`), the Hub fetches the issuer's OpenID Connect discovery document before storing anything. The client secret is encrypted at rest with the Hub's active encryption key, the provider is created **enabled**, and the command prints the generated `id` you need for the callback URL.

Every provider must return a verified email address — keep an email scope (`user:email` for GitHub, `openid` and `email` for OIDC) in `--scopes`.

### Examples

```bash
# GitHub (uses preset defaults)
leapmux admin idp add \
  --type github \
  --client-id Iv1.abc123def456 \
  --client-secret 0123456789abcdef0123456789abcdef01234567

# Google (custom display name)
leapmux admin idp add \
  --type google \
  --name "Sign in with Google" \
  --client-id 1234567890-abc.apps.googleusercontent.com \
  --client-secret GOCSPX-xxxxxxxxxxxxxxxxxxxxxxxx

# Sign in with Apple (client secret is your signed ES256 JWT)
leapmux admin idp add \
  --type apple \
  --client-id com.example.leapmux.web \
  --client-secret eyJhbGciOiJFUzI1NiIsImtpZCI6IjEyMzQ1Njc4OTAifQ...

# Generic OIDC (Okta, Auth0, Keycloak, Microsoft Entra ID, etc.)
leapmux admin idp add \
  --type oidc \
  --name "Acme SSO" \
  --issuer-url https://acme.okta.com \
  --client-id 0oa1b2c3d4e5f6g7h8i9 \
  --client-secret super-secret-value \
  --scopes "openid profile email" \
  --trust-email=true
```

## Sign in with Apple specifics

Sign in with Apple works end to end with `--type apple` and has two requirements to keep in mind:

- **HTTPS `public_url` is required.** Apple mandates `response_mode=form_post` whenever `name` or `email` scopes are requested. LeapMux automatically sends `response_mode=form_post` for `https://appleid.apple.com`, accepts the cross-site `POST` callback, parses Apple's string or boolean `email_verified` claim, and captures Apple's first-login `user` JSON display name. Because the flow-binding cookie for cross-site `POST` callbacks uses `SameSite=None; Secure`, browsers send it only over HTTPS.
- **Rotate the 6-month ES256 client secret JWT in place.** Apple requires an ES256-signed JWT as `--client-secret`, valid for at most 6 months. Before it expires, generate a new JWT and rotate it without changing the provider ID or losing linked accounts:

```bash
leapmux admin idp rotate-secret <providerID> --client-secret <new-es256-jwt>
```

## Callback and login URLs

LeapMux derives both URLs from the Hub's base URL and the provider's generated `<providerID>`:

- **Login URL:** `<base-url>/auth/idp/<providerID>/login`
- **Callback URL (register with your provider):** `<base-url>/auth/idp/<providerID>/callback`

How the Hub resolves `<base-url>`:

1. **`public_url`** verbatim when set (must be scheme + host [+ port] with no path, query, or fragment).
2. Otherwise derived from `--listen` (`https` when `secure_cookies` is `true`, else `http`). Wildcard listen addresses (`:4327`, `0.0.0.0:4327`, and `[::]:4327`) all resolve the host to `localhost:<port>`.

| Scenario | `public_url` | `secure_cookies` | `--listen` | Resulting base URL |
| --- | --- | --- | --- | --- |
| Behind a reverse proxy | `https://hub.example.com` | *(any)* | *(any)* | `https://hub.example.com` |
| Local dev, no proxy | *(unset)* | `false` | `:4327` / `0.0.0.0:4327` / `[::]:4327` | `http://localhost:4327` |
| Direct TLS, no proxy | *(unset)* | `true` | `:4327` | `https://localhost:4327` |

## Trusting the provider's email

`--trust-email` controls automatic linking to an existing LeapMux account by matching verified email:

- **`--trust-email=true`:** links the OAuth identity to an existing account with the same verified email automatically.
- **`--trust-email=false`:** disables auto-linking; new OAuth identities go through **Complete Sign Up** (or are rejected if sign-up is disabled).

{{< callout type="warning" >}}
Enable `--trust-email=true` only for identity providers you control or fully trust to verify email ownership. For public or multi-tenant issuers, set `--trust-email=false`. Regardless of `--trust-email`, LeapMux always requires the provider to mark the email as verified.
{{< /callout >}}

## Managing existing providers

### List, enable, and disable

```bash
leapmux admin idp list
leapmux admin idp disable <providerID>
leapmux admin idp enable <providerID>
```

Disabling a provider hides its sign-in button and rejects new sign-ins without deleting the provider or its user links.

### Rotate a client secret

Replace a provider's stored client secret in place — keeping the same `<providerID>`, callback URL, and all linked user accounts:

```bash
leapmux admin idp rotate-secret <providerID> --client-secret <new-secret>
```

The Hub encrypts the new secret with its active encryption key and evicts the cached provider configuration automatically, so the new secret takes effect immediately with no Hub restart. Use this for routine credential rotation and Apple's 6-month ES256 client secret JWT renewal.

To change immutable settings (`client_id`, `issuer_url`, `scopes`, or `trust_email`), `remove` the provider and `add` it again, then update the callback URL registered at your provider to the new `<providerID>`.

### Remove a provider

```bash
leapmux admin idp remove <providerID>
```

Removing a provider permanently deletes its configuration, secret, and every linked user identity, and evicts the provider from the Hub's cache automatically.

If removal would leave any passwordless user with no remaining login method, the Hub refuses with a `conflict` error counting those accounts. Give those users a password or another linked provider first, or pass `--force` to remove the provider anyway (affected accounts with verified emails can recover via **Can't sign in?**; otherwise reset their password with `leapmux admin user reset-password` or `leapmux recover reset-password`).

{{< callout type="info" >}}
If stored client secrets fail to decrypt after an encryption-key rotation, run `leapmux recover encryption-key reencrypt` before removing the old key version. See [Encryption & Data](/docs/admin/encryption-and-data/#encryption-key-rotation-runbook).
{{< /callout >}}

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Provider reports `redirect_uri` mismatch | Callback URL registered at the provider does not match `<base-url>/auth/idp/<id>/callback`. | Run `leapmux admin idp list` and register the exact scheme, host, port, and path. |
| Users redirect to `http://localhost:4327/...` | `public_url` is unset behind a reverse proxy. | Set `public_url` (`leapmux admin settings set public_url "https://hub.example.com"`) and update the callback URL. |
| Sign in with Apple fails on callback cookie | Hub is served over plain HTTP (`SameSite=None; Secure` requires HTTPS). | Serve the Hub over HTTPS behind a reverse proxy and set an `https://` `public_url`. |
| Sign in with Apple stops working after 6 months | ES256 client secret JWT expired. | Generate a new JWT and run `leapmux admin idp rotate-secret <id> --client-secret <jwt>`. |
| `add` fails issuer validation | OIDC issuer URL is wrong, unreachable, or has a path appended to a preset. | Confirm Hub outbound network access to `<issuer>/.well-known/openid-configuration`; omit `--issuer-url` for Google/Apple presets. |
| Sign-in fails because provider returned no email | Email scope missing or unverified email. | Keep `user:email` (GitHub) or `openid email` (OIDC) in `--scopes`. |
| Sign-in rejected because sign-up is disabled | `signup_enabled` is `false` and the identity is unlinked. | Enable `signup_enabled`, or have the user link the provider under **Preferences → Account → Linked accounts**. |

## Related chapters

- [Accounts & Connected Apps](/docs/using/accounts-and-apps/) — end-user sign-in, sign-up, and account linking.
- [Admin CLI](/docs/admin/admin-cli/) — full `leapmux admin` command guide.
- [Configuration Reference](/docs/admin/configuration-reference/) — `public_url`, `secure_cookies`, and `signup_enabled`.
- [Security & Authentication](/docs/admin/security-and-auth/#passkeys-oauth-and-app-credentials) — OAuth flow binding and auto-linking security.
- [Encryption & Data](/docs/admin/encryption-and-data/) — at-rest encryption of client secrets and key rotation.
