---
title: "Authentication & Session Security"
description: "Detailed security rules for LeapMux session elevation, passkeys and WebAuthn, OAuth flow binding, app credentials, delegation tokens, bot protection, and credential revocation."
type: docs
weight: 11
---

This page covers authentication, authorization, and session-lifecycle security on a LeapMux Hub. For the high-level threat model and trust boundaries, see [Security & Threat Model](/docs/admin/security/). For channel encryption and Worker key architecture, see [Cryptography & Key Architecture](/docs/admin/cryptography/).

## Session elevation

A sensitive account change requires a **recently proven factor**, not only a live session cookie. Proving a factor **elevates** the session for {{< duration elevation-window >}}. Every sensitive write succeeds without another prompt and slides that window forward. Elevation is capped at **{{< duration elevation-cap >}}** from the moment the factor was proven, so an all-day session returns to un-elevated state and a stolen cookie cannot extend elevation indefinitely.

A sign-in with a password or passkey proves a factor immediately and starts a {{< duration fresh-authentication-window >}} window. Every rule below applies to that window: sensitive actions proceed without another prompt, sensitive writes slide it forward, and the {{< duration elevation-cap >}} cap counts from the original sign-in.

In the browser, a refused action opens **Verify your identity** above the current dialog, preserves form input, and resumes automatically once you verify. See [Accounts & Authentication](/docs/using/accounts/#managing-your-profile) for the user workflow.

In the desktop app's remote-Hub mode, select **Verify in your browser** in the step-up dialog to prove the factor on the Hub's page in your system browser. Both desktop sign-in and step-up require an elevated browser session; if the browser session lacks one, `/elevate` grants the full {{< duration elevation-window >}} after proof. The desktop session itself starts un-elevated, so its first sensitive action triggers its own loopback step-up, and the Hub opens the window when the app redeems the authorization code.

### Actions that require elevation

- Changing or setting your password
- Any passkey change: registering, renaming, removing, or disabling passkey sign-in
- Changing your account email
- Removing a linked OAuth provider
- Authorizing an app
- Registering an app, editing its registration, changing its elevation setting, or vouching for it
- Changing any **Hub setting**, from the Preferences dialog or `leapmux admin settings`

Why these rules exist:

- **Recovery identities:** Account email receives recovery links, and linked providers are login methods. Controlling either lets an attacker return after the current session ends.
- **App registrations:** Editing a registration can rewrite redirect URIs and divert authorization codes in flight. **Disconnecting** an app, **retiring** a registration, and **deleting** an empty registration require no elevation because they only reduce access.
- **`Admin*` services:** Every `Admin*` procedure requires a current window, including reads and access-reducing verbs (`session revoke`, `api-token revoke`). Reads require the window and do not slide it. `AppService` applies its own rules: listing apps, retiring an app, and deleting an empty registration work without a window (see [App Authorization](/docs/admin/app-authorization/)).
- **Hub settings:** Settings control `signup_enabled`, captcha, rate limits, SMTP, and `public_url` (which determines the WebAuthn relying party). Protecting them prevents a stolen administrator cookie from weakening Hub security controls.

### Browser-only admin verbs vs. elevated CLI credentials

One rule decides whether a command-line credential may run an elevated `Admin*` verb: **does the verb create a new way into an account?**

- **Browser session required (CLI credentials refused):** creating a user (`user create`), resetting a password (`user reset-password`), granting or revoking admin (`user grant-admin` / `user revoke-admin`, because one procedure handles both), and editing a user's email or verified flag (`user update --email` / `--email-verified`).
- **Elevated CLI credential accepted:** `api-token issue` (the one credential-minting exception so headless service accounts can renew with human step-up; the minted token does not renew and expires no later than its issuer), `user delete`, `user update --display-name` / `--clear-pending-email`, `settings` (including `captcha` and `rate-limit`), `idp` management, and admin reads.

### How you elevate

| Your account today | How you elevate |
| --- | --- |
| Password set | Enter your password. |
| Passkey registered | Authenticate with an existing passkey. |
| Linked provider only (no password, no passkey) | Sign in again at that provider. |
| No password, no passkey, no linked provider | Cannot elevate. Set a password first; see [The account with nothing to prove](#the-account-with-nothing-to-prove). |

Provider elevation applies **only** to an account with neither a password nor a passkey. An account holding a password or passkey must prove that stronger factor; the verification screen omits providers it would refuse.

{{< callout type="info" >}}
A provider elevation confirms that the browser still holds a live session at the identity provider, not that the user re-entered a credential just now. LeapMux requests fresh authentication, but providers do not enforce it reliably (and GitHub does not support it). For an account whose only login method is that provider, this introduces no extra risk; add a password or passkey if you want a stronger factor.
{{< /callout >}}

In the browser, an elevated session displays its deadline and an **End now** button at the top of **Account** and every **Administration** section. The tab that performs a sliding write updates its displayed deadline immediately from the response; other open tabs refresh their view once their cached deadline passes.

Inside a LeapMux agent or terminal tab, delegation tokens carry no elevation and cannot invoke any of these procedures.

### Verifying a command-line credential

Command-line credentials carry their own elevation window on the same terms, but cannot prove a factor in the terminal — otherwise a stolen credential file could elevate itself. `leapmux` starts the appropriate ceremony automatically when a command is refused:

- **Loopback ceremony** (credentials from default `leapmux auth login`): opens a browser on the CLI's machine. Verify your identity if prompted and approve the request; the browser sends an authorization code to the CLI's loopback listener, and the command retries after redemption.
- **Device-code ceremony** (credentials from `leapmux auth login --device-code` or `leapmux admin api-token issue`): prints a verification URL and short code for approval in any browser:

```text
$ leapmux admin settings set public_url https://hub.example.com
This command needs you to verify your identity.
  1. Visit https://hub.example.com/oauth/device
  2. Enter the code: 7XC-8DZ
Or open: https://hub.example.com/oauth/device?user_code=7XC-8DZ
```

Three security properties govern both ceremonies:

- **A credential cannot approve its own request.** Only an elevated browser session can approve step-up. A stolen credential file can initiate a ceremony but cannot complete it.
- **Step-up opens a window on the existing credential.** In loopback step-up, the window opens on code redemption (the CLI may refresh its bearer token first); in device-code step-up, it opens on approval. Step-up issues no new credential and sends no app-authorization email. The window lasts {{< duration elevation-window >}} with the same {{< duration elevation-cap >}} cap. Writes slide it only when their handler opts in; reads never slide it.
- **Choose `--device-code` for headless hosts and containers.** Loopback step-up requires a browser that can reach the CLI's loopback listener.

An unattended job can use a narrowly scoped, short-lived credential while its window is active; a human must approve a new window after expiry.

### The account with nothing to prove

An account with neither a password, a passkey, nor a linked provider cannot elevate. To avoid permanent lockout, two rules admit adding the **first** password or passkey:

1. **Elevated session (checked first):** any session that already proved a factor (including provider re-authentication) is admitted immediately.
2. **First-credential fallback:** if not elevated, the Hub admits the request only when the session authenticated within the last **{{< duration fresh-authentication-window >}}** and the account has a durable identity (a verified email or a linked OAuth provider).

An older captured cookie cannot attach a persistent credential; signing in again gives a fresh session.

### What still needs more than elevation

- **Registering a passkey requires a browser** because WebAuthn ceremonies run between a browser and an authenticator. Password changes and passkey management (rename, delete, disable passkey sign-in) work from an elevated CLI credential.
- **Changing a password revokes every other session, API token, and delegation token.** Only the credential that performed the change survives. Registered Workers keep their credentials and can still mint delegation tokens.
- **Removing the last passkey or disabling passkey sign-in** on a passwordless account requires setting a new password in the same request so the account keeps a sign-in method.
- **Self-service account recovery and admin `reset-password` delete every passkey** on the account so compromised devices cannot sign back in. Completing recovery with a passkey clears any prior password too. Linked providers and registered Workers remain; after a suspected compromise, review and deregister unknown Workers.

## Passkeys and WebAuthn

Passkey ceremonies run only when two conditions hold: the browser is in a secure context (HTTPS or loopback) with WebAuthn support, and the page origin matches the Hub's published origin (`public_url` or loopback listen address). See [Configuration Guide — Passkeys](/docs/admin/configuration/#passkeys) for origin rules and UI behavior.

Storage properties:

- `passkey_credentials` encrypts `public_key`, `aaguid`, `transports`, and `friendly_name` at rest with the Hub keystore (`credential_id` and `sign_count` stay indexed in SQL).
- WebAuthn ceremony challenges and ephemeral session state live only in Hub memory and are never written to the database.

## OAuth and OIDC security

### OAuth flow binding

Starting an OAuth login binds the flow to the browser that started it. The Hub sets a short-lived `HttpOnly` cookie with a random nonce, and the callback completes only when that browser presents the matching cookie. Each flow gets its own cookie so concurrent logins in one browser do not overwrite each other. When a callback leads to first-time sign-up, a second `HttpOnly` cookie binds the username completion page to the same browser.

Without flow binding, an attacker could start a login under their own identity and trick a victim into visiting the live callback URL, signing the victim into the attacker's account (login CSRF) or linking a new account to the attacker's identity.

A callback the Hub rejects leaves the flow open so an attacker who learns a live `state` value cannot abort someone else's sign-in.

### Provider email verification and auto-linking

LeapMux requires every OAuth/OIDC provider to assert a verified email address. Auto-linking to an existing account by email runs only when the provider is configured with `--trust-email=true`. Set `--trust-email=false` for public or multi-tenant identity providers that you do not control. Pending OAuth sign-ups and flow states live only in Hub memory. See [Sign-in Providers](/docs/admin/sign-in-providers/#trusting-the-providers-email).

## App credentials

`leapmux auth login` mints an app credential: an access token living {{< duration access-token >}} that renews automatically, backed by a refresh token living {{< duration refresh-token >}}, written to one mode-`0600` file per Hub (see [Credential file location](/docs/using/control-cli/#credential-file-location)).

Eight rules restrict every app credential:

- **Authorization requires an elevated session.** Both browser and device-code consent require a verified browser session and slide its elevation window.
- **Permissions only subtract.** Every grant is a closed set of named scopes enforced at the Hub and again inside the encrypted channel at the Worker. A grant never adds privileges the account lacks.
- **Hub administration is opt-in.** Ordinary logins omit admin scopes even for administrator accounts. Request admin scopes explicitly (`leapmux auth login --scope "admin:read admin:users"`).
- **Account authenticators stay outside every grant.** Adding a passkey, changing the recovery email, unlinking a sign-in provider, and managing another app's credentials are always refused to app credentials because they create authority outliving app disconnection.
- **Absolute lifetime is capped.** Refreshing slides the refresh window forward, but never past **{{< duration absolute-cap >}}** from initial authorization.
- **Re-login retires the prior credential.** Logging in again revokes the previous token.
- **Delegated minting shrinks scope and lifetime.** A credential issued by another credential does not renew, expires no later than its issuer, and can never hold wider scopes than its issuer.
- **Renewing and fixed-TTL credentials are mutually exclusive.** `leapmux auth login` and default `admin api-token issue` mint renewing credentials; `admin api-token issue --ttl <seconds>` mints a fixed-lifetime service token with no refresh token.

When SMTP is configured and the user's email is verified, LeapMux emails the account whenever a credential is issued, listing the app, installation name, and every granted permission (and noting when an administrator issued it).

Users manage credentials under **Preferences → Apps → Connected apps**: **Disconnect** revokes every installation of the app at once, while **Revoke** ends one machine. Neither requires elevation. See [Connected Apps](/docs/using/connected-apps/) and [App Authorization](/docs/admin/app-authorization/).

## Delegation tokens and Worker credentials

### What a delegation token can reach

A Worker mints a short-lived delegation token (living {{< duration access-token >}} and revoked when its tab closes) for the agent or terminal inside a tab. It carries the Worker **owner's** identity and can reach only the Worker that minted it plus that owner's other Workers. The Hub re-checks this machine limit on every `OpenChannel`.

On reachable Workers, a delegation token has the owner's full host-level Worker permissions (filesystem, Git, tunnels, system info). Path traversal is normalized, but nothing jails an agent to one project directory. Every data-touching Worker RPC is owner-only (except the no-op liveness ping).

**A delegation token is limited by owner and machine, not by workspace or tab.** An agent in one workspace can read another agent's messages, write to another terminal, close tabs, edit layout across all of its owner's workspaces, and list the owner's full Hub workspace and tab inventory. Treat all work under one user account as a single trust domain for agents; use separate user accounts when you need a stronger boundary.

{{< callout type="info" >}}
The Worker re-checks the owner identity on every RPC to catch Hub bugs. Because the Worker learns its owner from the Hub, this check does not protect against a compromised or malicious Hub.
{{< /callout >}}

### Worker credentials and tab publication

A registered Worker uses its bearer token (`state.json`) for registration state, its outbound Hub stream, and publishing public metadata for Agent, Terminal, File, and Image tabs hosted on that Worker into its owner's writable workspaces.

The Worker persists each resource and durable creation operation locally, then asks the Hub to commit the tab ID, destination, ordering, and initial viewer preferences transactionally with the layout change. Repeating a creation returns the recorded result, and cancellations record durably so delayed retries cannot recreate a canceled tab. File paths, image references, agent settings, and startup recovery inputs never leave the Worker. A Worker credential grants no authority to publish tabs for another Worker.

## Bot protection: rate limits and ALTCHA

The Hub protects unauthenticated and sensitive surfaces with two layers (see [Configuration Guide — Bot protection](/docs/admin/configuration/#bot-protection-captcha--rate-limits) and [Admin CLI](/docs/admin/admin-cli/#rate-limits) for tuning):

- **Proof-of-work captcha (built-in ALTCHA, default):** runs client-side `PBKDF2/SHA-256` challenges with zero outbound egress and one HMAC per challenge on the Hub. The Hub activates ALTCHA automatically only when the browser has a secure WebCrypto context and the Hub is externally reachable (`public_url` over HTTPS or `secure_cookies=true`). A honeypot check runs even when captcha is disabled. External providers (Google reCAPTCHA v3, Cloudflare Turnstile) verify online on every non-empty token.
- **Rate limits:**
  - `elevation` (per user, default 5/15m) — slows brute-force attempts against the identity-verification dialog.
  - `email_change` (per user, default 6/15m) and `mail_limits` (per recipient with `+` tag folding, default 10/hr and 10s failure cooldown) — protects the SMTP relay from mail-bombing loops.
  - `login_anonymous` (verified client IP, default 10/15m) — guards sign-in, including solo mode where no captcha runs.
  - `oauth_anonymous` (verified client IP, default 600/10m) — guards anonymous OAuth 2.1 endpoints (`/oauth/device-authorization`, `/oauth/token`, `/oauth/revoke`, `/oauth/register`, `/oauth/step-up`, and app icons).

## Credential and device revocation

### Channels don't outlive their credential

An open Noise channel cannot outlive the credential that authorized it. The Hub force-closes affected channels on:

- Logout and admin force-logout (`session revoke`, `session revoke-user`)
- Password change or password reset
- Account deletion
- API token or delegation token revocation
- Refresh-token reuse detection: presenting a previous refresh token after its 60-second retry window revokes the credential automatically (tokens two or more rotations old fail as invalid without revoking)

Two behaviors are worth noting:

- **Password change spares the current session.** The Hub restamps the active session's channels to the new authentication generation before revoking all other sessions and tokens.
- **Token rotation preserving or widening scope keeps channels open.** The Hub re-arms existing channels at the new expiry. A rotation that narrows the grant closes channels so the narrower scope takes effect immediately. Profile changes (such as role updates) only invalidate cached user data.

Teardown is immediate on the live Hub serving the request. Revocations written offline by `leapmux recover` (or left mid-flight by a stopped Hub) propagate through a durable revocation ledger on the Hub's next sweep.

Revoke credentials online with `leapmux admin`: `session revoke`, `session revoke-user`, `api-token revoke`, and `delegation-token revoke` (see [Admin CLI](/docs/admin/admin-cli/)).

## See also

- [Security & Threat Model](/docs/admin/security/) — trust boundaries, solo mode, and administrator recommendations.
- [Cryptography & Key Architecture](/docs/admin/cryptography/) — hybrid post-quantum `Noise_NK`, key storage, and pinning verification.
- [App Authorization](/docs/admin/app-authorization/) — OAuth 2.1 app registrations, permission vocabulary, and elevation opt-in.
- [Sign-in Providers](/docs/admin/sign-in-providers/) — configuring GitHub, Google, Apple, and generic OIDC providers.
- [Accounts & Authentication](/docs/using/accounts/) — end-user sign-in, passkeys, and account recovery.
