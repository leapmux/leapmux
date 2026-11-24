---
title: "Connected Apps"
description: "Authorize CLI tools and integrations, inspect their permissions and devices, revoke access immediately, or register your own OAuth app in LeapMux."
type: docs
weight: 14
---

External tools — including the `leapmux` control CLI, LeapMux Desktop, CI scripts, and custom integrations — connect to your account through scoped OAuth credentials. You can inspect every authorized tool, see which machines it runs on, and revoke access at any time from **Preferences → Apps** (or from a terminal with `leapmux auth credentials`).

## Authorizing an app

When you run `leapmux auth login` or sign in from an external tool, LeapMux sends you to a consent screen hosted by your Hub before issuing any credential.

Check four things before clicking **Allow**:

1. **App verification.** If an administrator vouched for the app, the heading reads **"Authorize {name}?"**. Otherwise, the heading reads **"Authorize an unverified app?"** and shows a warning banner stating that nobody verified the app on this Hub. Deny any request you did not start yourself.
2. **Account and destination.** Confirm that the screen lists your account and the destination you expect to return to.
3. **Requested permissions.** Every permission family the app touches appears in full: requested scopes are checked, unrequested ones are dimmed, and each scope includes a plain-English description beside its token (for example, `terminal:write` explains that the app can type into your terminals and run any command on your machine).
4. **Hub administration scopes.** Administrative permissions are never included by default and can only be granted by an administrator. On the headless [device-code flow](/docs/using/control-cli/#leapmux-auth-login), approving a hub-administration scope requires a second confirmation click.

Authorizing an app requires a recently verified session (password, passkey, or linked sign-in provider), which stays verified for {{< duration elevation-window >}} so consecutive approvals do not re-prompt. Click **Allow** to grant access, or **Deny** to return an immediate refusal to the app.

## Viewing your connected apps

Open **Preferences → Apps** to review every app that holds access to your account, grouped by app with one row per machine.

{{< screenshot src="preferences-apps" alt="LeapMux Preferences Apps panel showing Connected apps and App registrations with verified badges, permission scope chips, and management actions" caption="Preferences → Apps lists your Connected apps alongside your App registrations and their permission ceilings." width="1280" height="856" >}}

Each connected app block shows:

| Field | What it tells you |
|---|---|
| **App name & badge** | The app's label, plus an **unverified** badge if no administrator vouched for it. |
| **Installation** | The machine label (for example, *alex's MacBook*), distinguishing separate logins of the same app on different machines. |
| **hub administration** | Appears only on credentials allowed to change Hub-wide administration settings. |
| **Permissions** | One chip per granted scope (`workspace:read`, `agent:write`, `terminal:write`, `tunnel:open`, …). Note that `terminal:write` can run shell commands and `tunnel:open` can reach services on your Worker's network. |
| **Activity & expiry** | When the credential was **last used** (or **added**), plus when it must **sign in again** or **expires**. |

You can view the exact same list from the command line:

```bash
leapmux auth credentials
```

If no apps are connected yet, run `leapmux auth login` on your machine to authorize the control CLI.

## Ending an app's access

Choose **Revoke** or **Disconnect** depending on whether you are removing one machine or cutting off the whole app:

| Action | Where to click | What it does | When to use it |
|---|---|---|---|
| **Revoke** | On a single installation row | Ends access for that one machine while leaving other machines connected. | You lost, wiped, or retired one laptop or devbox. |
| **Disconnect** | On the app header | Ends every installation of the app at once and closes its active channels. | You no longer use or trust the app. |

Neither **Revoke** nor **Disconnect** asks for a password or identity verification, so you can cut off a compromised or unwanted app immediately.

## Registering your own app

To connect a custom script, internal dashboard, or third-party integration, open **Preferences → Apps → App registrations** and click **Register an app**.

- **Private vs. hub-wide:** Registrations you create under **Preferences → Apps** are private to your account. Administrators can create hub-wide registrations visible to all users under **Administration → Hub-wide Apps**.
- **Client type:** Choose **Public** for local CLIs and desktop binaries that cannot keep a secret, or **Confidential** for a backend server (which receives a client secret shown once).
- **Redirect URLs:** List the exact callback addresses allowed after consent.
- **Permission ceiling:** Choose the maximum scopes the app may ask for. Consent can always grant fewer permissions than the ceiling, never more.

Changes to a registration's permission ceiling take effect immediately:

- **Removing a scope** strips that permission from every credential the app already holds on its next request.
- **Adding a scope back** does not grant it automatically — users must approve it on a new consent screen.
- **Retiring an app** revokes every credential it holds across all accounts (an app that has never issued credentials can be deleted directly).

Because changing redirect URLs or permission ceilings affects OAuth security, creating or editing a registration requires a verified session.

## Scoped credentials in solo mode

Even in single-user **solo mode**, LeapMux enforces OAuth scopes on any caller that presents an app credential. For example, you can give a local script or helper agent a credential limited to `file:read` so it can inspect files without being able to run terminal commands or modify workspaces.

## See also

- [Accounts & Authentication](/docs/using/accounts/) — sign-in, passwords, passkeys, and session verification.
- [Settings & Preferences](/docs/using/settings/#apps) — the **Apps** and **Hub-wide Apps** Preferences panels.
- [Control CLI](/docs/using/control-cli/) — signing in with `leapmux auth login` and inspecting credentials.
- [App Authorization](/docs/admin/app-authorization/) — administrator guide for vouching, hub-wide apps, and dynamic registration.
