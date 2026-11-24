---
title: "Accounts & Connected Apps"
description: "Sign in to LeapMux across run modes, manage your profile, passkeys, linked OAuth providers, and account recovery, and control authorized CLI tools and connected apps."
type: docs
weight: 11
aliases:
  - /docs/using/accounts/
  - /docs/using/connected-apps/
---

Every LeapMux account owns its workspaces, agents, and terminals privately — there is no cross-account sharing. Whether you see a sign-in screen depends on how LeapMux is running. This chapter explains how accounts behave in each run mode, how to sign in, verify your email, manage passkeys and recovery, and authorize or revoke connected apps.

## Account and authentication behavior by run mode

| Run mode | Accounts | Sign-in required? | Email verification & recovery | Connected Apps & OAuth scopes |
| --- | --- | --- | --- | --- |
| **`solo` (local IPC)** | Single built-in `solo` account | No (local socket only) | Disabled | Supported; scoped app tokens are enforced |
| **`solo` (TCP)** | Single built-in `solo` account | First TCP caller sets `solo` password; all TCP access requires sign-in afterward | Disabled | Supported; scoped app tokens are enforced |
| **`dev`** | Multi-user (first user becomes admin via `/setup`) | Yes (password, passkey, or configured OAuth) | Off unless SMTP is configured | Full OAuth 2.1 app authorization |
| **`hub` (without SMTP)** | Multi-user (first user becomes admin via `/setup`) | Yes | Off (emails stored unverified; no self-service recovery) | Full OAuth 2.1 app authorization |
| **`hub` (with SMTP)** | Multi-user (first user becomes admin via `/setup`) | Yes | Automatic verification required for non-admins; email recovery enabled | Full OAuth 2.1 app authorization |

In **solo mode**, profile name, email, and OAuth provider actions are disabled; **Preferences → Account → Password** is kept so you can set or update the `solo` password when enabling network access. For Hub administrator setup — initial `/setup`, enabling open sign-up, configuring SMTP or OAuth/OIDC providers, and admin password resets — see [Configuration](/docs/admin/configuration/), [Security & Authentication](/docs/admin/security-and-auth/), [Admin CLI](/docs/admin/admin-cli/), and [Recovery](/docs/admin/recover/).

## Signing up and signing in

{{< screenshot src="login-page" alt="LeapMux sign-in screen showing Username input, Password and Passkey method selector, Password field, and Sign in button" caption="Sign in with a password, a passkey, or an administrator-configured OAuth/OIDC provider." width="622" height="478" >}}

- **Sign in (`/login`):** Enter your username and choose **Password** or **Passkey**, or click a configured identity provider button (GitHub, Google, Apple, or OIDC). In **LeapMux Desktop** remote-Hub mode, clicking **Sign in with your browser** completes sign-in in your system browser.
- **Sign up (`/signup`):** Available when your Hub administrator enables open sign-up (off by default). Choose **Password** or **Passkey** sign-up, or sign up with an external OAuth/OIDC provider (which takes you to **Complete Sign Up** to pick a username). Unknown usernames and wrong passwords return the same generic error to prevent username probing.
- **Username rules:** 1–32 characters using lowercase `a–z`, `0–9`, and single internal hyphens (`solo` is always reserved; `admin` is reserved outside initial setup).
- **Password rules:** 8–128 printable ASCII characters (spaces allowed, including leading and trailing spaces, so passphrases work verbatim; no mandatory symbol or case rules). A live strength meter provides guidance without blocking valid passwords.

### Email verification

When the Hub has SMTP configured, new non-admin accounts receive a 6-character verification code (`XXX-XXX`, excluding ambiguous characters `0`, `1`, `I`, `O`, `L`) and a direct verification link. Codes expire after **30 minutes**, allow **5 wrong guesses** before invalidation, and enforce a **60-second** resend cooldown. Until verified, non-admin accounts can only manage their email, verify, or sign out.

### Sessions and signing out

Sessions stay active for **7 days of inactivity** by default (sliding forward with each action; Hub administrators can adjust the duration). Use **Log out** in the user menu to end your session (hidden in local-IPC solo mode). Changing your password automatically signs out all of your *other* browser sessions and revokes your API and delegation tokens so compromised sessions lose access immediately.

## Passkeys and account recovery

### Passkeys

Passkeys are WebAuthn credentials stored on your device or password manager. Register multiple passkeys (for example, a laptop and a phone) under **Preferences → Account → Passkeys**. Passkeys appear only on secure origins (HTTPS or `localhost`) matching the Hub's published URL; in Desktop remote-Hub mode, click **Open in browser** to enroll a passkey.

Removing your **last** passkey on a passwordless account (or clicking **Disable passkey sign-in**) prompts you to set a password in the same step so you cannot lock yourself out.

### Recovering your account

When SMTP is configured, click **Can't sign in?** on `/login` (or visit `/recover-account`) and enter your username or email. Any account with a **verified email address** receives a single-use recovery link valid for **1 hour** (5-attempt budget) that lets you set a new password or enroll a new passkey — covering a lost password, lost passkey, or lost OAuth provider alike.

Completing recovery clears existing passkeys and revokes all other sessions and app tokens while keeping linked OAuth providers intact. If your account has no verified email address, ask a Hub administrator to reset your password (see [Admin CLI](/docs/admin/admin-cli/) or [Recovery](/docs/admin/recover/)).

## Managing your account

{{< screenshot src="preferences-account" alt="LeapMux Preferences Account panel showing verified session banner, Profile username and display name fields, and Email settings" caption="Update your profile, email, password, passkeys, and linked sign-in providers under Preferences → Account." width="1280" height="856" >}}

Open **Preferences → Account** (`⌘,` / `Ctrl+,`):

- **Profile** — update your username and display name.
- **Email** — view verification status, resend a verification code, or request an email change (requires verification when SMTP is enabled; new addresses stay unverified until confirmed).
- **Password** — change your password, or set your first password on an OAuth-only, passkey-only, or `solo` account.
- **Passkeys** — add, rename, remove, or disable passkeys.
- **Linked accounts** — view and unlink OAuth/OIDC providers. LeapMux refuses to unlink your only sign-in method until you set a password.

{{< callout type="info" >}}
Sensitive account changes (email, password, passkeys, unlinking providers, and editing app registrations) require a **verified session**. After you confirm your identity, verification stays active for {{< duration elevation-window >}} (or for {{< duration fresh-authentication-window >}} right after a password/passkey login), and you can click **End now** in the top banner at any time. See [Security & Authentication](/docs/admin/security-and-auth/).
{{< /callout >}}

## Connected Apps

External tools — including `leapmux auth login`, LeapMux Desktop, CI scripts, and custom integrations — access your account via scoped OAuth 2.1 credentials rather than your password. Even in **solo mode**, LeapMux enforces every granted scope on callers presenting an app credential.

### Authorizing an app

{{< screenshot src="oauth-consent" alt="LeapMux OAuth consent screen for an unverified app showing the warning banner, app name, callback address, checked granted scopes, dimmed ungranted scopes, and Deny and Allow buttons" caption="The Hub consent screen highlights unverified apps and lists each granted scope in plain language beside dimmed ungranted scopes." width="760" height="708" >}}

When an app requests access, your Hub shows a consent screen (requiring a verified session). Before clicking **Allow**, check:

1. **Verification status:** Administrator-vouched apps read **"Authorize {name}?"**; unvouched apps display an **"Authorize an unverified app?"** warning banner. Deny any request you did not initiate.
2. **Account and destination:** Confirm your account and expected callback destination.
3. **Requested permissions:** Each requested scope is checked and explained in plain language (for example, `terminal:write` can run shell commands on your Workers, and `tunnel:open` can reach services on a Worker's network).
4. **Hub administration scopes:** Never requested by default and available only to administrators; headless device-code approvals require a second confirmation click for admin scopes.

### Inspecting and revoking apps

{{< screenshot src="preferences-apps" alt="LeapMux Preferences Apps panel showing Connected apps and App registrations with verified badges, permission scope chips, and management actions" caption="Review active Connected apps by machine, revoke individual installations, or manage private App registrations in Preferences → Apps." width="1280" height="856" >}}

Open **Preferences → Apps → Connected apps** (or run `leapmux auth credentials`) to see every authorized app grouped by app name and machine label, with its granted scope chips, last-used time, and expiration date:

| Action | Where | Effect |
| --- | --- | --- |
| **Revoke** | Single installation row | Ends access for that one machine while leaving other machines connected. |
| **Disconnect** | App header | Revokes every installation of the app immediately and closes its active channels. |

Neither **Revoke** nor **Disconnect** asks for session re-verification, so you can cut off a lost device or unwanted app immediately.

### Registering your own app

Under **Preferences → Apps → App registrations**, click **Register an app** to create a private OAuth client for your own scripts or tools (administrators can also register Hub-wide apps under **Administration → Hub-wide Apps**):

- Choose **Public** (for CLIs/desktop tools that cannot store a secret) or **Confidential** (for servers; receives a one-time client secret).
- List exact allowed redirect URLs and choose a **permission ceiling** (the maximum scopes the app may request).
- Narrowing a ceiling strips removed scopes from existing credentials immediately; expanding a ceiling requires fresh user consent. **Retire** revokes all credentials issued to that app.

## Related chapters

- [CLI Guide](/docs/using/cli-guide/) — signing in with `leapmux auth login` and managing credentials.
- [Settings & Preferences](/docs/using/settings/) — all Preferences categories and scope tiers.
- [Security & Authentication](/docs/admin/security-and-auth/) — session elevation, OAuth providers, and threat model.
- [Configuration](/docs/admin/configuration/) — Hub administrator settings for sign-up, SMTP, and network access.
