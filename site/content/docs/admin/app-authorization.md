---
title: "App Authorization"
description: "Register OAuth 2.1 apps on a LeapMux Hub, control permission ceilings, verify trusted apps, and manage elevation opt-in."
type: docs
weight: 9
---

A LeapMux Hub is an OAuth 2.1 authorization server. External programs and integrations request permission from an account holder on a Hub-rendered consent screen — the `leapmux` CLI itself uses the same flow.

This chapter covers the administrator side of app registrations. For viewing and disconnecting apps on your own account, see [Accounts & Connected Apps](/docs/using/accounts-and-apps/). For the OAuth wire specification, see [OAuth API](/docs/reference/oauth-api/). For signing users *in* with external identity providers (GitHub, Google, Apple, OIDC), see [Sign-in Providers](/docs/admin/sign-in-providers/).

## Who may register an app

Both ordinary users and administrators can register apps, with one visibility rule: only administrators can create `hub-wide` registrations.

| Visibility | Visible to | Editable by |
| --- | --- | --- |
| `private` (any user) | Only the registering user | That user |
| `hub-wide` (administrators only) | Everyone on the Hub | Administrators |

A `private` app is invisible to other users — the Hub omits it from listings and answers consent links from another account as though the app does not exist, preventing app enumeration.

Ordinary users register private apps in **Preferences → Apps**. Administrators manage hub-wide apps in **Preferences → Administration → Hub-wide Apps**, or via the CLI:

```bash
leapmux admin app register "Deployment bot" \
  --hub https://hub.example.com \
  --redirect-uri https://bot.example.com/callback \
  --scope "workspace:read agent:read agent:write" \
  --type confidential \
  --visibility hub-wide
```

{{< screenshot src="preferences-apps" alt="LeapMux Preferences Apps panel showing Connected apps and App registrations with verified badges, permission scope chips, and management actions" caption="Manage authorized apps and app registrations with live permission ceilings in Preferences → Apps and Preferences → Administration → Hub-wide Apps." width="1280" height="856" >}}

The response returns `client_id` and, for a confidential client, `client_secret` **once**. The Hub stores only a peppered digest of `client_secret` and cannot display it again.

Registering an app, editing its registration, changing its elevation setting, and verifying it require an elevated session because editing redirect URIs could divert authorization codes in flight. **Retiring** (`revoke`) an app and **deleting** an unused registration require no elevation because they only reduce access. See [Security & Authentication — Session elevation](/docs/admin/security-and-auth/#session-elevation).

## Permissions

Every grant is a set of scopes rendered as plain-language sentences on the consent screen:

| Scope | What the consent screen says |
| --- | --- |
| `account:read` | Read your profile: your username, your email address, and whether you are an administrator. |
| `account:write` | Change your profile and your account settings, including your password. |
| `workspace:read` | Read your workspaces, your tabs, and your layout. |
| `workspace:write` | Create, rename, move, and close your workspaces and tabs. Closing a tab stops its agent or terminal. |
| `worker:read` | List your Workers and connect to one. |
| `worker:admin` | Rename and deregister your Workers, and manage the keys that let a machine join. |
| `agent:read` | Read your coding-agent sessions, including every message in them. |
| `agent:write` | Send prompts to your coding agents and answer their permission requests. |
| `terminal:read` | Read the output of your terminals. |
| `terminal:write` | Type into your terminals, which runs any command on your machine. |
| `file:read` | Browse and read files on your machines. |
| `git:read` | Read the git state of your repositories: status, branches, diffs, and history. |
| `git:write` | Commit, push, and create or delete branches in your repositories. |
| `tunnel:open` | Open network connections from inside your private network to any address it can reach. |
| `admin:read` | Read this Hub's administration: every account, setting, Worker, and credential. |
| `admin:users` | Administer every account on this Hub, including resetting passwords. |
| `admin:settings` | Change this Hub's settings, including its security policy and its sign-in providers. |
| `admin:workers` | Administer every Worker on this Hub. |
| `admin:apps` | Register, edit, vouch for, retire, and delete the Hub's app registrations. |

Four rules govern all grants:

- **Permissions only subtract.** A grant narrows what an account can already do; granting `admin:users` on a non-admin account grants no admin access.
- **Implied scopes close automatically.** For example, `file:read` implies `worker:read`. The Hub stores the closed set so consent screens, credentials, and token responses agree.
- **The registration is a live ceiling.** Narrowing `--scope` on an app registration takes effect on the very next request from existing credentials without revoking them. Restoring the scope restores access within each user's original consent.
- **Admin scopes require an administrator on both sides.** Only administrators can register apps requesting `admin:*` scopes or grant them on consent.

Account authenticators remain outside every app grant: adding a passkey, changing a recovery email, unlinking a sign-in provider, and managing another app's credentials are always refused to app tokens so disconnecting an app always terminates its reach.

## Verifying an app

{{< screenshot src="oauth-consent" alt="LeapMux OAuth consent screen for an unverified app showing the warning banner, app name, callback address, checked granted scopes, dimmed ungranted scopes, and Deny and Allow buttons" caption="Unverified registrations display an Authorize an unverified app warning and show granted scopes beside dimmed ungranted scopes." width="760" height="708" >}}

New registrations start **unverified**, displaying *Authorize an unverified app?* on consent screens with the app's self-chosen label attributed in body text rather than the heading. Once you have reviewed an app, vouch for it:

```bash
leapmux admin app verify <client_id>
```

The warning disappears, and the app's row under **Preferences → Apps** identifies the administrator who verified it. Use `leapmux admin app unverify <client_id>` to withdraw verification.

## Elevation opt-in

Sensitive writes require a recently proven factor (a loopback browser step-up ceremony at `GET /oauth/authorize?step_up=...`, or a device-code ceremony initiated at `POST /oauth/step-up` and approved at `/oauth/device`). New custom app registrations have elevation **disabled** by default; the three built-in registrations ship with elevation enabled. Enable elevation for a trusted app with:

```bash
leapmux admin app allow-elevation <client_id>
```

Use `deny-elevation` to disable it. Disabling elevation blocks new step-up requests and stops the Hub from honoring any open elevation window on that app's bearer credentials at their next validation. See [OAuth API — Step-up](/docs/reference/oauth-api/#step-up-elevation) and [Security & Authentication](/docs/admin/security-and-auth/#verifying-a-command-line-credential).

## Open dynamic registration (RFC 7591)

Anonymous RFC 7591 dynamic client registration is **off by default** because allowing unauthenticated callers to create consent-screen entries creates a phishing surface. Enable it only when needed:

```bash
leapmux admin settings set open_app_registration true
```

While off, `registration_endpoint` is omitted from OAuth discovery metadata. Self-registered apps are always unverified and `hub-wide`.

## Retiring and deleting apps

- **Retire (`revoke`):** revokes the registration and every credential issued under it across all users in one transaction:

  ```bash
  leapmux admin app revoke <client_id>
  ```

- **Delete:** removes a registration that has never issued any credential (including revoked credentials retained for audit history in `leapmux admin api-token list --include-revoked`).

{{< callout type="info" >}}
The Hub ships with three built-in registrations — the `leapmux` CLI, LeapMux Desktop, and the service-account registration used by `leapmux admin api-token issue`. Only their elevation setting can be edited; the Hub protects them from retirement and deletion.
{{< /callout >}}

## Solo mode

A solo Hub enforces OAuth 2.1 app scopes identically to a team Hub. Credential-free calls over local IPC run as the unrestricted `solo` account, while any request carrying a bearer token is restricted to that token's scopes — enforced both at the Hub and inside the encrypted channel at the Worker.

## See also

- [Accounts & Connected Apps](/docs/using/accounts-and-apps/) — viewing and disconnecting apps on your own account.
- [OAuth API](/docs/reference/oauth-api/) — wire contract and step-up endpoints.
- [Admin CLI](/docs/admin/admin-cli/) — `leapmux admin app` commands.
- [Security & Authentication](/docs/admin/security-and-auth/) — session elevation and trust boundaries.
