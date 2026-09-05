---
title: "Accounts & Authentication"
description: "Getting into LeapMux as a user: when you need an account, creating the first one, signing up and signing in, email verification, OAuth, account recovery, and your profile."
type: docs
weight: 11
---

This chapter covers everything you need to get into LeapMux as a user: when you need an account at all, how to create the very first one, how to sign up and sign in, how email verification and OAuth sign-in work, how to recover an account you are locked out of, and how to manage your profile and password once you are in.

Whether you ever see a login screen depends on the mode LeapMux runs in. The first section makes that distinction; the rest assumes a multi-user deployment where accounts apply.

## When you need an account

LeapMux runs in several modes (see [Running LeapMux](/docs/admin/running-leapmux/)). Two of them treat accounts very differently:

| Mode | Account needed? | What you see |
| --- | --- | --- |
| **Solo** (`leapmux solo`) | One fixed account | No signup screen. Local IPC uses `solo` without a credential. TCP asks the first caller to set its password. |
| **Dev** (`leapmux dev`) | Yes | Real password authentication. The first admin is created through the `/setup` flow. |
| **Hub** (`leapmux hub`) | Yes | Full authentication: signup, password login, sessions, OAuth, API tokens. |

In **solo mode** there is nothing to sign up for. Local IPC opens the app without a credential. A passwordless TCP connection shows only the first-password setup screen. It cannot call another protected RPC. After setup, TCP shows the normal sign-in page. Solo mode also disables most account actions in every state: it refuses a change to your profile or your email, and it refuses to detach an OAuth provider. Each refusal identifies the action that solo mode does not support.

**Password is the exception.** **Preferences → Account → Password** is the one account row solo mode keeps, and it both sets the account's first password and changes it afterwards. Setting one is also what lets the Hub answer on a network address, so **Preferences → Administration → Network access** asks for that first password as well, beside the addresses it guards. See [Network access](/docs/admin/configuration/#network-access).

{{< callout type="info" >}}
If the Hub answers on a non-loopback address before setup, the first TCP caller can claim the `solo` account. LeapMux warns at startup. Set the password from a trusted connection. Every TCP address then asks for it, including `127.0.0.1`. For a multi-user deployment, run `leapmux hub` or `leapmux dev`. See [Security & Threat Model](/docs/admin/security/).
{{< /callout >}}

The rest of this chapter applies to **hub** and **dev** mode, where accounts are real.

## First-run setup: creating the first admin

When a hub or dev instance has no users yet, it is in *setup mode*. The first person to register becomes the administrator.

1. Open the instance in your browser. With no users present, the root path sends you to **`/setup`**.
2. You see the heading **"Welcome to LeapMux"** and the intro **"Create the first administrator account."**
3. Fill in the form, top to bottom:
   - **Username**
   - **Display Name**
   - **Email**
   - **New Password** (with a live strength meter)
   - **Confirm Password**
4. Click **Create account**.

On success you are signed in and taken to the app home at `/`.

This first account differs in three ways:

- It is **always created as an administrator**.
- Its email is **unverified**, like every other new address. That never blocks you, because administrators are exempt from the verification gate. It does mean [account recovery](#recovering-your-account) will not send a link to that address, so verify it from **Preferences → Account** once you configure SMTP. See [Email verification](#email-verification).
- The username `admin` is **allowed** here (it is reserved in public signup and OAuth completion). The username `solo` is reserved everywhere and cannot be used.

{{< callout type="info" >}}
The `/setup` screen only appears while no users exist. Once the first admin is created, visiting `/setup` redirects you to the login page. Setup is also race-safe: if two people submit at once, only one wins and the other is told sign-up is disabled.
{{< /callout >}}

## Signing up

After the first admin exists, new self-service accounts are only possible if the administrator enables the `signup_enabled` setting (`leapmux control admin settings set signup_enabled true`; it is **off by default**). See [Configuration](/docs/admin/configuration/).

- **If signup is disabled**, visiting `/signup` shows a "not found" page titled **"Sign-up disabled"**, which states that new account registration is not available and offers a **"Go to login"** link.
- **If signup is enabled**, you get the **"Sign Up"** page.

The form fields are the same as setup:

| Field | Notes |
| --- | --- |
| **Username** | Required. Lowercase slug, 1–32 characters. See [Username rules](#username-rules). |
| **Display Name** | Optional; falls back to your username if left blank. |
| **Email** | Required when the hub has SMTP configured; otherwise optional. |
| **New Password** | 8–128 printable ASCII characters, spaces included. See [Password requirements](#password-requirements). |
| **Confirm Password** | Must match. |
| **Sign-up method** | **Password** (default) or **Passkey**. Passkey sign-up registers a WebAuthn credential instead of setting a password. |

The submit button reads **Sign up** or **Sign up with passkey**. It stays disabled until you enter a username, an email (required only when the hub has SMTP configured), and (for password sign-up) a valid, matching password. A footer link, **"Already have an account? Sign in"**, takes you to the login page.

If your administrator configures OAuth/OIDC providers, a list of provider buttons appears above the form under the verb **"Sign up with"** (for example, **"Sign up with GitHub"**), followed by the divider **"or create an account with email"**. See [Signing in with OAuth / OIDC](#signing-in-with-oauth--oidc).

What happens after you submit depends on whether the hub has SMTP configured (see [Email verification](#email-verification)):

- **SMTP not configured:** you are signed in immediately and taken to `/`. Your email is stored as unverified in the database; the runtime requirement stays off until SMTP is configured.
- **SMTP configured:** LeapMux sends a verification email and routes you to the email-verification screen. Signup is **fail-closed**: if the verification email cannot be sent, the account is not created and you see an error (retry when mail works).

{{< callout type="info" >}}
The username `solo` is rejected in all signup paths, and `admin` is additionally reserved for public signup and OAuth completion (it is allowed only in `/setup`). Self-service signups are never administrators.
{{< /callout >}}

## Signing in

Visit `/login`. The page is headed **"LeapMux"** and starts with a **Username** field.

A **Sign-in method** chooser offers **Password** and **Passkey** (the **Passkey** option is disabled or absent where ceremonies cannot run — see [Passkeys](#passkeys)):

| Method | What you do |
| --- | --- |
| **Password** | Fill **Password**, then click **Sign in**. |
| **Passkey** | Click **Sign in with passkey** and complete the WebAuthn prompt in your browser or device. |

Click **Sign in** or **Sign in with passkey**. The button stays disabled until the username is filled and any captcha challenge is solved. An account that cannot use the method you picked returns a generic failure (same shape as a wrong password).

- A **"Sign up"** link appears in the footer only when self-service signup is enabled.
- **Can't sign in?** appears under the form when the hub has SMTP configured (see [Recovering your account](#recovering-your-account)).
- If OAuth providers are configured, their buttons appear above the form under the verb **"Sign in with"** with an **"or"** divider.
- If you were redirected to login from a protected page, you are sent back there after signing in (LeapMux only honors a same-site relative path, as an open-redirect safeguard).

{{< callout type="info" >}}
Both an unknown username and a wrong password produce the same error, and it says only that the credentials are invalid. This is deliberate — it prevents anyone from probing which usernames exist.
{{< /callout >}}

## Email verification

Email verification is **automatic whenever the administrator configures SMTP** on the hub (`host` and `from_address` both set). There is no separate `email_verification_required` toggle — configuring mail is what turns verification on. When SMTP is absent, sign-ups skip verification entirely.

When verification applies, you must verify your email before you can use most of LeapMux.

### Verifying your email

You reach the **"Verify your email"** screen automatically right after signing up (when verification is required), or by clicking the link in the verification email. The screen reads:

> Enter the 6-character code we sent to your inbox, or click the link in that email.

- The input expects a code in the form **`XXX-XXX`**. You can type it with or without the hyphen and in any case — LeapMux normalizes it for you.
- Click **Verify**. If you submit an empty field, the form asks you for the 6-character code from your email.
- A separate **Resend code** button requests a new code.
- Each verification and each resend passes the hub's captcha challenge when one is configured, like the sign-in forms do.

The verification email arrives with the subject **"[LeapMux] Verify your email address"** and contains both the code and a direct link. Clicking the link opens the verification screen with the code pre-filled and submits it as soon as the captcha allows: by itself when the hub runs no captcha or the provider solves without a click, and right after you solve the challenge when it needs one.

On success you are signed in fully and taken to `/`.

### Code limits

| Limit | Value |
| --- | --- |
| Code length / format | 6 characters, shown as `XXX-XXX` |
| Code lifetime | **30 minutes** |
| Wrong-guess budget | **5 attempts** — the 6th wrong guess invalidates the code, and you must request a new one |
| Resend cooldown | **60 seconds** between requests — invalidating a code by guessing wrong does **not** shorten it, and a **failed** send blocks the retry for only the failed-send window (**10 seconds** by default) |
| Captcha | Required on every verify and resend when the hub has captcha enabled |

The code alphabet deliberately omits look-alike characters (no `0`, `1`, `I`, `O`, or `L`), so what you read in the email is what you type. An expired code and a wrong code report the same generic error so neither leaks information.

{{< callout >}}
If you wait too long and your code expires, press **Resend code** to get a fresh one. The screen confirms that a fresh code went to your inbox.
{{< /callout >}}

{{< callout type="warning" >}}
While verification applies and you are an unverified non-admin user, you can only view your own account, sign out, change/verify your email, and resend the code. LeapMux refuses every other action until you verify. Administrators are exempt.
{{< /callout >}}

## Passkeys

Passkeys are WebAuthn credentials stored by your browser or device. They let you sign in without typing a password, and you can register more than one (for example, a laptop and a phone).

### Signing up with a passkey

On the **Sign Up** page, choose **Passkey** under **Sign-up method**, fill in username, display name, and email, then click **Sign up with passkey**. Your browser prompts you to create the credential. The account has **no password** until you set one later from your profile.

If SMTP is configured, passkey sign-up follows the same verification path as password sign-up: you land on **Verify your email** until the code is accepted.

### Signing in with a passkey

Enter your username on the login page, choose **Passkey** in the **Sign-in method** chooser, then click **Sign in with passkey** and approve the WebAuthn prompt.

You can still choose **Password** when your account has a password, even if passkeys are also registered. An account that cannot use the method you picked returns a generic failure.

### Managing passkeys in your profile

Open **Preferences → Account** — it is the first section, and the one the dialog opens on. The **Passkeys** row lists every credential with its name and when it was last used (or added), and offers these actions:

| Action | What it does | What it requires |
| --- | --- | --- |
| **Add passkey** | Registers another credential. | A [verified session](/docs/admin/security/#session-elevation), and a page where ceremonies can run (see the note below). |
| **Rename passkey** | Changes a credential's name. | A verified session. |
| **Remove passkey** | Deletes one credential. | A verified session. Removing your **last** passkey from an account with **no password** also sets a password in the same step. |
| **Disable passkey sign-in** | Deletes **all** credentials at once (offered once at least one passkey exists). | A verified session. A passkey-only account sets a password as part of the same flow. |

Every action above is sensitive: the first one in a sitting opens the **Verify your identity** dialog, and one answer covers every further sensitive change for a while. What counts as sensitive, how long the answer lasts, and what you can prove it with are covered once, in [Session elevation](/docs/admin/security/#session-elevation).

{{< callout type="info" >}}
Passkeys appear only where they can run: the page must be secure (HTTPS or `localhost`), and it must reach the Hub by an address the Hub publishes. When either is missing, the **Passkey** option is disabled with the reason on it — or removed outright — and **Add passkey** carries the reason. See [Passkeys](/docs/admin/configuration/#passkeys) in the administration chapter for the rule and its remedies, or [Passkey sign-in fails or the authenticator never appears](/docs/reference/troubleshooting/#passkey-sign-in-fails-or-the-authenticator-never-appears) for diagnosis.
{{< /callout >}}

{{< callout type="info" >}}
An account with **neither a password nor a passkey** follows stricter rules for adding its **first** one — see [The account with nothing to prove](/docs/admin/security/#the-account-with-nothing-to-prove).
{{< /callout >}}

{{< callout >}}
A passkey lost with its device does not lock the account out — see [Recovering your account](#recovering-your-account).
{{< /callout >}}

## Signing in with OAuth / OIDC

If your administrator configures one or more external identity providers — GitHub, Google, Apple, or a generic OIDC provider — you can sign in or sign up with them instead of (or in addition to) a password. Configuring providers is an administrative task; see [Sign-in Providers](/docs/admin/sign-in-providers/).

### The flow

1. On the login or signup page, click a provider button (for example, **"Sign in with GitHub"**).
2. Your browser is handed off to the provider, where you authorize LeapMux.
3. The provider sends you back to LeapMux, which finishes the sign-in.

The redirect chain is: Browser -> Hub (starts sign-in) -> Provider (you authorize) -> Hub callback (finishes sign-in, establishes session).

What happens at step 3 depends on whether the identity is already known:

- **Already linked** to a LeapMux account → you are signed in at once.
- **Not linked, but the verified email matches an existing account** → LeapMux may link the identity automatically and log you in. This only happens when the administrator marked that provider as one that trusts emails.
- **A brand-new identity** → if self-service signup is enabled, you are taken to a short completion page; otherwise sign-in is refused because there is no account to attach the identity to.

{{< callout type="info" >}}
OAuth sign-in requires the provider to return a **verified** email address. If a provider does not return an email — typically because the "email" scope was not granted — LeapMux cannot complete the sign-in.
{{< /callout >}}

### Completing an OAuth signup

For a new identity, you land on the **"Complete Sign Up"** page. It greets you with **"Signed in via {provider}. Choose a username to finish creating your account."** and has these fields:

| Field | Notes |
| --- | --- |
| **Username** | Required. Same slug rules as everywhere else; `solo` and `admin` are both reserved here. |
| **Display Name** | Pre-filled from the provider; editable. |
| **Email** | Read-only when the provider supplied one; otherwise editable, and required when the hub has SMTP configured. |

Click **Create account**. On success you are signed in. If your email still needs verification, you are routed to the verification screen first; otherwise you go straight to `/`.

Accounts created this way have no password set. You can add one later from your profile (see [Managing your profile](#managing-your-profile)), which is useful as a fallback login method — and if you lose the provider itself, [recover the account](#recovering-your-account) by email.

## Recovering your account

When the hub has SMTP configured, the login page offers **Can't sign in?** under the form. Recovery asks one thing of the account — a **verified email address** — and nothing about how it signs in, so the same flow covers a lost password, a lost passkey, and a lost provider alike.

1. Open **Can't sign in?** or visit `/recover-account`.
2. Enter your **email or username** and click **Send recovery link**.
3. Open the emailed link (or paste the token from `/recover-account/complete?token=…`) and spend it on a replacement sign-in factor:
   - **Set new password** — the account's **first** one if it never had a password.
   - **Recover with passkey** — enroll a new passkey instead (offered when the hub runs passkey ceremonies). This also **removes the password**.

The link is **single-use** and **expires after one hour**, and it stays unspent until you submit the completion. Either path draws on one five-attempt budget. If the browser is already signed in when you open it, the page says so and offers **Sign out and continue** — sign out, and the same address shows the form. The hub answers the request identically whether or not the identifier matched, so the flow cannot be used to probe which accounts exist.

Completing recovery **clears every passkey** on the account, revokes other sessions, and revokes API/delegation tokens — the same break-glass posture as an admin password reset. Linked providers stay linked. Sign in with the factor you chose: the new password, or the passkey the recovery enrolled.

An **unverified** email address gets no link. Verify it from **Preferences → Account** while you can still sign in; when you cannot sign in at all (for example, the first admin never verified the address), an administrator can set a password with `leapmux control admin user reset-password` or the offline `leapmux recover password reset` (see [Admin CLI](/docs/admin/admin-cli/) and [Recovery](/docs/admin/recover/)).

## Password requirements

Passwords are enforced identically in the browser and on the server:

- **8–128 characters**
- **Printable ASCII only**: every character from the space (0x20) through the tilde (0x7E) — unaccented letters, digits, spaces, and the punctuation on a US keyboard. **Spaces count**, even at the start and end, so a passphrase like `correct horse battery staple` is taken exactly as you type it.
- Accented letters, CJK characters, emoji, and control characters (the tab or newline a paste sometimes carries) are each refused.
- **No complexity rule**: uppercase letters, digits, and symbols are never required.

The forms show a live strength meter (**Weak** to **Strong**); it is guidance, not a gate — it never blocks you, though an all-letters or all-digits password scores no higher than **Fair**.

## Username rules

Usernames are GitHub-style slugs, enforced identically in the browser and on the server:

- **1–32 characters**
- Lowercase letters `a–z`, digits `0–9`, and hyphens only
- No leading or trailing hyphen
- No consecutive hyphens (`--`)

`solo` is reserved in every account-creation path. `admin` is reserved for public signup and OAuth completion, but allowed during first-run `/setup`.

## Ownership

Every account owns its own workspaces, agents, and terminals. There is no sharing, inviting, or team tenancy — you see exactly the workspaces you create, and nobody else's. See [Concepts & Architecture](/docs/getting-started/concepts/) for how ownership fits into the object hierarchy.

## Sessions and signing out

When you sign in, LeapMux issues a session and stores it in a secure, `HttpOnly` cookie that your browser sends on every request. Key facts:

| Property | Value |
| --- | --- |
| Session lifetime | **7 days after your last activity** (administrators can change this — see [`session_duration_seconds`](/docs/admin/configuration/)) |
| Cookie name | `leapmux-session` (or `__Host-leapmux-session` when the administrator enables secure cookies behind TLS) |
| Cookie flags | `HttpOnly`, `Path=/`, `SameSite=Lax` |

**The clock runs from your last activity, not from your login.** Each action you take in the app slides the expiry forward and refreshes the cookie, so a session you keep using does not run out. The lifetime is an idle timeout: stay away for the whole period without touching LeapMux and you are signed out.

**Staying signed in.** As long as your session did not expire, a page reload keeps you signed in — LeapMux restores your session on load. If your session expired, or somebody revoked it, a failed request signs you out without a message; sign in again.

**Signing out.** Use **Log out** in the user menu. It ends your session on the server and clears the cookie. (Solo mode offers no **Log out** item — there is no session to end.)

**Changing your password signs out your other sessions.** When you change your password, LeapMux invalidates every *other* active session (the current one stays signed in) and revokes your API and delegation tokens. This is a security feature: if someone else had a session, changing your password locks them out. See [Control CLI](/docs/using/control-cli/) for administrator-side session management.

## Managing your profile

Manage your account from the **Preferences** dialog (the user menu's **Preferences...**, or `⌘,`), in its **Account** section — the section the dialog opens on. Each heading below is one row of it, except **Connected apps**, which sits one category over in **Apps**; the details of each field and persistence behavior live in [Settings & Preferences](/docs/using/settings/), so this is a summary.

While the session is verified, a panel at the top of the section says so and offers **End now** — see [Session elevation](/docs/admin/security/#session-elevation).

### Profile

- **Username** — editable. Taken usernames are rejected.
- **Display Name** — your shown name.
- Save with **Save Profile** (disabled until you change something valid). The dialog confirms the update on success.

### Email

- **Current Email** shows your address (or **"Not set"**) with a **(verified)** or **(unverified)** badge. An unverified address has a **Resend code** button beside it; the code goes in at `/verify-email`. A pending change shows the new address and asks you to verify it from your inbox.
- Enter a new address in **New Email** and click **Change Email**. This is one of the changes that needs a verified session — see [Session elevation](/docs/admin/security/#session-elevation).
- If verification is required, LeapMux sends a verification email and tells you to check your inbox. You must verify the new address before it takes effect. Otherwise the dialog confirms the new address at once. Admins change email immediately.
- Either way the new address starts out **unverified**, because nobody confirmed it yet. Until you do, [account recovery](#recovering-your-account) cannot send a link to it.

### Password

- The button reads **Change Password** if you already have a password, or **Set Password** if your account is OAuth-only or passkey-only.
- Changing your password needs a **verified session**: the first sensitive change in a sitting opens a **Verify your identity** dialog, and the next {{< duration elevation-window >}} are covered. You do not retype your current password into this form.
- An account with no password and no passkey sets its first password without a prompt, but only within five minutes of signing in.
- On success the dialog confirms that it changed the password, or that it set the first one.

### Passkeys

See [Passkeys](#passkeys) above for the full passkey management surface in this dialog.

### Connected apps

This row sits one category over, in the dialog's **Apps** section, beside **App registrations**. Every app that holds access to your account appears here, **grouped by app**: one block per app, and under it one row per machine that app runs on. Each row lists every permission you granted, when it was last used, and when it stops working. A credential that can administer the Hub says so.

**Disconnect**, on the app's line, ends your whole authorization of it — every machine at once. **Revoke**, on one row, ends that machine only and leaves the app working elsewhere. Either way the app must be authorized again to come back. Both are among the few account changes that need no verification, so you can act the moment you suspect an app is malicious.

`leapmux control auth credentials` prints the same list from a terminal.

See [Connected Apps](/docs/using/connected-apps/) for how to read a row and what registering your own app involves, and [App credentials](/docs/admin/security/#app-credentials) for what a credential can do, how long it lives, and the email notice you get when one is issued.

### Linked accounts

- Lists the identity providers you sign in through, each with an **Unlink** button. An account that signs in through none says so.
- Unlinking needs a **verified session**, like the other changes in this dialog — see [Session elevation](/docs/admin/security/#session-elevation).
- LeapMux refuses to unlink your **only** login method when you have no password — set a password first. This keeps you from locking yourself out.

{{< callout >}}
If you signed up via OAuth and want a fallback, set a password under **Password** before unlinking any provider.
{{< /callout >}}

## Where to go next

- [Settings & Preferences](/docs/using/settings/) — the full Preferences dialog and every other setting.
- [Sign-in Providers](/docs/admin/sign-in-providers/) — configuring OAuth/OIDC as an administrator.
- [Running LeapMux](/docs/admin/running-leapmux/) and [Configuration](/docs/admin/configuration/) — choosing a run mode, the `signup_enabled` setting, and SMTP (which controls email verification and account recovery).
- [Security & Threat Model](/docs/admin/security/) — what authentication does and does not protect.
