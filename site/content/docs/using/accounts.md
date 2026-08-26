---
title: "Accounts & Authentication"
description: "Getting into LeapMux as a user: when you need an account, creating the first one, signing up and logging in, email verification, OAuth, and your profile."
type: docs
weight: 1
---

This chapter covers everything you need to get into LeapMux as a user: when you need an account at all, how to create the very first one, how to sign up and log in, how email verification and OAuth sign-in work, and how to manage your profile and password once you are in.

Whether you ever see a login screen depends on how LeapMux is being run. The first section makes that distinction; the rest assumes a multi-user deployment where accounts apply.

## When you need an account

LeapMux runs in several modes (see [Running LeapMux](/docs/operating/running-leapmux/)). Two of them treat accounts very differently:

| Mode | Account needed? | What you see |
| --- | --- | --- |
| **Solo** (`leapmux solo`) | No | No login or signup screen. A single passwordless user named `solo` is created and auto-authenticated for every request. |
| **Dev** (`leapmux dev`) | Yes | Real password authentication. The first admin is created through the `/setup` flow. |
| **Hub** (`leapmux hub`) | Yes | Full authentication: signup, password login, sessions, OAuth, API tokens. |

In **solo mode** there is nothing to sign up for and nothing to log out of. If you navigate to `/login` or `/signup` you are redirected straight into the app. Account-related actions are intentionally disabled: solo mode refuses a change to your profile, your email, or your password, and it refuses to unlink an OAuth provider. Each refusal identifies the action that solo mode does not support.

> **Note:** Solo mode auto-authenticates *every* request as the admin. If you bind it to a non-loopback address, anyone who can reach the port has full admin access without credentials. LeapMux warns you about this at startup. For a shared or networked deployment, run `leapmux hub` (or `leapmux dev`) so real authentication applies. See [Security & Threat Model](/docs/operating/security/).

The rest of this chapter applies to **hub** and **dev** mode, where accounts are real.

## First-run setup: creating the first admin

When a hub or dev instance has no users yet, it is in *setup mode*. The first person to register becomes the administrator.

1. Open the instance in your browser. With no users present, the root path sends you to **`/setup`**.
2. You see the heading **"Welcome to LeapMux"** and the intro **"Create the first administrator account to get started."**
3. Fill in the form, top to bottom:
   - **Username**
   - **Display Name**
   - **Email**
   - **New Password** (with a live strength meter)
   - **Confirm Password**
4. Click **Create account** (the button reads **Creating account...** while it works).

On success you are signed in and taken to the app home at `/`.

A few things are special about this first account:

- It is **always created as an administrator**.
- Its email is **unverified**, like every other new address. That never blocks you, because administrators are exempt from the verification gate. It does mean **Forgot password** will not send a reset link to that address, so verify it from **Preferences → Account** once SMTP is configured. See [Email verification](#email-verification).
- The username `admin` is **allowed** here (it is reserved in public signup and OAuth completion). The username `solo` is reserved everywhere and cannot be used.

> **Note:** The `/setup` screen only appears while no users exist. Once the first admin is created, visiting `/setup` redirects you to the login page. Setup is also race-safe: if two people submit at once, only one wins and the other is told sign-up is disabled.

## Signing up

After the first admin exists, new self-service accounts are only possible if the operator has enabled the `signup_enabled` setting (`leapmux control admin settings set signup_enabled true`; it is **off by default**). See [Configuration](/docs/operating/configuration/).

- **If signup is disabled**, visiting `/signup` shows a "not found" page titled **"Sign-up disabled"**, which states that new account registration is not available and offers a **"Go to login"** link.
- **If signup is enabled**, you get the **"Sign Up"** page.

The form fields are the same as setup:

| Field | Notes |
| --- | --- |
| **Username** | Required. Lowercase slug, 1–32 characters. See [Username rules](#username-rules). |
| **Display Name** | Optional; falls back to your username if left blank. |
| **Email** | Required. |
| **New Password** | 8–128 printable ASCII characters, spaces included. See [Password requirements](#password-requirements). |
| **Confirm Password** | Must match. |
| **Sign-up method** | **Password** (default) or **Passkey**. Passkey sign-up registers a WebAuthn credential instead of setting a password. |

The submit button reads **Sign up** or **Sign up with passkey** (and **Signing up...** while submitting). It stays disabled until you enter a username, a valid email, and (for password sign-up) a valid, matching password. A footer link, **"Already have an account? Sign in"**, takes you to the login page.

If your operator configures OAuth/OIDC providers, a list of provider buttons appears above the form under the verb **"Sign up with"** (for example, **"Sign up with GitHub"**), followed by the divider **"or create an account with email"**. See [Signing in with OAuth / OIDC](#signing-in-with-oauth--oidc).

What happens after you submit depends on whether the hub has SMTP configured (see [Email verification](#email-verification)):

- **SMTP not configured:** you are signed in immediately and taken to `/`. Your email is stored as unverified in the database; the runtime requirement stays off until SMTP is configured.
- **SMTP configured:** LeapMux sends a verification email and routes you to the email-verification screen. Signup is **fail-closed**: if the verification email cannot be sent, the account is not created and you see an error (retry when mail works).

> **Note:** The username `solo` is rejected in all signup paths, and `admin` is additionally reserved for public signup and OAuth completion (it is allowed only in `/setup`). Self-service signups are never administrators.

## Logging in

Visit `/login`. The page is headed **"LeapMux"** and starts with a **Username** field.

A **Sign-in method** chooser always offers **Password** and **Passkey** (LeapMux does not reveal which methods an account supports before you submit):

| Method | What you do |
| --- | --- |
| **Password** | Fill **Password**, then click **Sign in**. |
| **Passkey** | Click **Sign in with passkey** and complete the WebAuthn prompt in your browser or device. |

Click **Sign in** or **Sign in with passkey** (**Signing in...** while it works). The button stays disabled until the username is filled and any captcha challenge is solved. An account that cannot use the method you picked returns a generic failure (same shape as a wrong password).

- A **"Sign up"** link appears in the footer only when self-service signup is enabled.
- **Forgot password?** appears under the password form when the hub has SMTP configured (see [Forgot password](#forgot-password)).
- If OAuth providers are configured, their buttons appear above the form under the verb **"Sign in with"** with an **"or"** divider.
- If you were redirected to login from a protected page, you are sent back there after signing in (LeapMux only honors a same-site relative path, as an open-redirect safeguard).

> **Note:** Both an unknown username and a wrong password produce the same error, and it says only that the credentials are invalid. This is deliberate — it prevents anyone from probing which usernames exist.

## Email verification

Email verification is **automatic whenever the operator configures SMTP** on the hub (`host` and `from_address` both set). There is no separate `email_verification_required` toggle — configuring mail is what turns verification on. When SMTP is absent, sign-ups skip verification entirely.

When verification applies, you must verify your email before you can use most of LeapMux.

### Verifying your email

You reach the **"Verify your email"** screen automatically right after signing up (when verification is required), or by clicking the link in the verification email. The screen reads:

> Enter the 6-character code we sent to your inbox, or click the link in that email.

- The input expects a code in the form **`XXX-XXX`**. You can type it with or without the hyphen and in any case — LeapMux normalizes it for you.
- Click **Verify** (**Verifying…** while it works). If you submit an empty field, the form asks you for the 6-character code from your email.
- A separate **Resend code** button requests a new code.

The verification email arrives with the subject **"[LeapMux] Verify your email address"** and contains both the code and a direct link. Clicking the link opens the verification screen with the code pre-filled and submits it automatically.

On success you are signed in fully and taken to `/`.

### Code limits

| Limit | Value |
| --- | --- |
| Code length / format | 6 characters, shown as `XXX-XXX` |
| Code lifetime | **30 minutes** |
| Wrong-guess budget | **5 attempts** — the 6th wrong guess invalidates the code, and you must request a new one |
| Resend cooldown | **60 seconds** between requests |

The code alphabet deliberately omits look-alike characters (no `0`, `1`, `I`, `O`, or `L`), so what you read in the email is what you type. An expired code and a wrong code report the same generic error so neither leaks information.

> **Tip:** If you wait too long and your code expires, press **Resend code** to get a fresh one. The screen confirms that a fresh code went to your inbox.

> **Warning:** While verification applies and you are an unverified non-admin user, you can only view your own account, log out, change/verify your email, and resend the code. LeapMux refuses every other action until you verify. Administrators are exempt.

## Passkeys

Passkeys are WebAuthn credentials stored by your browser or device. They let you sign in without typing a password, and you can register more than one (for example, a laptop and a phone).

### Signing up with a passkey

On the **Sign Up** page, choose **Passkey** under **Sign-up method**, fill in username, display name, and email, then click **Sign up with passkey**. Your browser prompts you to create the credential. The account has **no password** until you set one later from your profile.

If SMTP is configured, passkey sign-up follows the same verification path as password sign-up: you land on **Verify your email** until the code is accepted.

### Signing in with a passkey

Enter your username on the login page, choose **Passkey** in the **Sign-in method** chooser, then click **Sign in with passkey** and approve the WebAuthn prompt.

You can still choose **Password** when your account has a password, even if passkeys are also registered. An account that cannot use the method you picked returns a generic failure.

### Managing passkeys in your profile

Open **Preferences → Account** (or **Profile** from the app menu) — it is the first section, and the one the dialog opens on. The **Passkeys** row lists every credential, when it was last used, and actions to rename or remove one.

| Action | What it requires |
| --- | --- |
| **Add passkey** | A verified session (see below), a secure page, and a Hub that runs ceremonies at the address you opened it by (see the note below). |
| **Rename passkey** | A verified session. |
| **Remove passkey** | A verified session. Removing your **last** passkey requires setting a password at the same time. |
| **Disable passkey sign-in** | Removes **all** passkeys. Passkey-only accounts must set a password as part of this flow. |

The first of these in a sitting opens a **Verify your identity** dialog. The passkey rows ask **at the click**, before their own dialog opens, so you answer one credential prompt at a time and never lose a half-filled form to a refusal. Enter your password or use a passkey, and the session stays verified for two hours — every further change lands without another prompt, and each one extends the two hours. While it lasts, the top of the Account section says so and offers **End now**. See [Session elevation](/docs/operating/security/#session-elevation) for the limits.

The same dialog guards the rest of **Preferences → Account**: changing your password, changing your account email, and removing a linked provider. One answer covers them all for the next two hours, so a sitting that touches several settings asks once. Your **Profile** name and your **Command-line credentials** are the two rows it does not cover.

> **Note:** Two parties decide whether a passkey ceremony can run on the page you are on, and each one can stop it. **Add passkey** is disabled with the reason on it whenever either does, and the login page offers no **Passkey** option.
>
> - **Your browser** runs a passkey only on a secure page: HTTPS, or a `localhost` address. On a plain-HTTP address it exposes no WebAuthn API at all, and no setting on the Hub changes that.
> - **The Hub** accepts only the addresses it publishes. Reach the same Hub by another one — a LAN IP behind the reverse proxy, a tunnel host, a port `public_url` does not name — and every ceremony is refused. Open the Hub at its configured URL, or ask an administrator to publish the address you reach it by. An administrator who sets **Public base URL** in **Preferences → Administration → General** sees **Add passkey** follow the change at once, with no page reload.
>
> See [Passkey sign-in fails or the authenticator never appears](/docs/reference/troubleshooting/#passkey-sign-in-fails-or-the-authenticator-never-appears).

> **Note:** An account with **neither a password nor a passkey** has nothing to verify with, so it takes a different rule: adding its **first** password or passkey needs a sign-in from the last five minutes. Sign out and back in through your provider if you have been signed in longer than that.

> **Note:** An OAuth-only account has no password to reset, so **Forgot password** cannot recover it. Set a password soon after OAuth signup if you want that break-glass path; see [Forgot password](#forgot-password).

## Forgot password

When SMTP is configured, the login page shows **Forgot password?** under the password form. It is hidden for passkey-only sign-in (there is no password to reset on that path).

1. Open **Forgot password?** or visit `/forgot-password`.
2. Enter your **email or username**.
3. Click **Send reset link**.

If an account with that address exists and its email is verified (when verification applies), LeapMux emails a one-hour reset link. The response is always the same whether or not an account matched — this prevents username probing.

4. Open the link (or paste the token from `/reset-password?token=…`) and choose a new password.

If that browser is already signed in, the page says so and offers **Sign out and continue** rather than taking you to the app. The link is single-use, so it stays unspent until you actually choose a new password: sign out and the same address shows the form.

Completing a self-service reset **clears every passkey** on the account, revokes other sessions, and revokes API/delegation tokens — the same break-glass posture as an admin password reset. Set a new passkey afterward if you still want passwordless sign-in.

If you signed up with a passkey and never set a password, use **Disable passkey sign-in** in your profile to add a password instead of the forgot-password flow.

## Signing in with OAuth / OIDC

If your operator configures one or more external identity providers — GitHub, Google, Apple, or a generic OIDC provider — you can sign in or sign up with them instead of (or in addition to) a password. Configuring providers is an operator task; see [Authentication Providers](/docs/operating/authentication-providers/).

### The flow

1. On the login or signup page, click a provider button (for example, **"Sign in with GitHub"**).
2. Your browser is handed off to the provider, where you authorize LeapMux.
3. The provider sends you back to LeapMux, which finishes the sign-in.

In short, the redirect chain is: Browser -> Hub (starts sign-in) -> Provider (you authorize) -> Hub callback (finishes sign-in, establishes session).

What happens at step 3 depends on whether the identity is already known:

- **Already linked** to a LeapMux account → you are logged straight in.
- **Not linked, but the verified email matches an existing account** → LeapMux may link the identity automatically and log you in. This only happens when the operator has marked that provider as trusting emails.
- **A brand-new identity** → if self-service signup is enabled, you are taken to a short completion page; otherwise sign-in is refused because there is no account to attach the identity to.

> **Note:** OAuth sign-in requires the provider to return a **verified** email address. If a provider does not return an email — typically because the "email" scope was not granted — LeapMux cannot complete the sign-in.

### Completing an OAuth signup

For a new identity, you land on the **"Complete Sign Up"** page. It greets you with **"Signed in via {provider}. Choose a username to finish creating your account."** and has these fields:

| Field | Notes |
| --- | --- |
| **Username** | Required. Same slug rules as everywhere else; `solo` and `admin` are both reserved here. |
| **Display Name** | Pre-filled from the provider; editable. |
| **Email** | Read-only, shown only if the provider supplied one. |

Click **Create account** (**Creating account...** while it works). On success you are signed in. If your email still needs verification, you are routed to the verification screen first; otherwise you go straight to `/`.

Accounts created this way have no password set. You can add one later from your profile (see [Managing your profile](#managing-your-profile)), which is useful as a fallback login method.

## Password requirements

LeapMux enforces a character set and a length on passwords:

| Rule | Value |
| --- | --- |
| Character set | **Printable ASCII only** (spaces included) |
| Minimum length | **8 characters** |
| Maximum length | **128 characters** |
| Complexity (uppercase, digits, symbols) | Not required |

A password holds printable ASCII characters only — every character from the space (0x20) through the tilde (0x7E): unaccented letters, digits, spaces, and the punctuation on a US keyboard. **Spaces count**, at the start and at the end of the password as well, so a passphrase such as `correct horse battery staple` is taken exactly as you type it. An accented letter, a CJK character, and an emoji are each refused, and so is a control character — the tab or newline a paste sometimes carries. The refusal identifies the character set that a password must stay inside.

The two ends of the range answer two different problems. The upper end keeps one character equal to one byte, so the browser and the Hub measure the length identically and a password the form accepts is one the Hub accepts. The lower end keeps out a character you cannot type again, which would leave you locked out of the account.

There is **no** mandatory mix of character types. The signup, setup, and password-change forms show a live **strength meter** with the labels **Weak**, **Fair**, **Good**, and **Strong**, but this is advisory only — it never blocks you. The form also warns you when your confirmation does not match the password.

> **Tip:** The meter rewards length and variety, but it penalizes any password that is all letters or all digits — regardless of how long it is. So an all-letters passphrase (even with mixed case) won't score above **Fair**; add at least one digit or symbol to reach **Good** or **Strong**. The meter is advisory only — it never blocks you, so treat it as guidance, not a gate.

Passwords are stored hashed with Argon2id using OWASP-recommended parameters; LeapMux never stores or transmits your plaintext password after hashing. See [Encryption & Data](/docs/operating/encryption-and-data/).

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

When you log in, LeapMux issues a session and stores it in a secure, `HttpOnly` cookie that your browser sends on every request. Key facts:

| Property | Value |
| --- | --- |
| Session lifetime | **7 days after your last activity** (operators can change this — see [`session_duration_seconds`](/docs/operating/configuration/)) |
| Cookie name | `leapmux-session` (or `__Host-leapmux-session` when the operator enables secure cookies behind TLS) |
| Cookie flags | `HttpOnly`, `Path=/`, `SameSite=Lax` |

**The clock runs from your last activity, not from your login.** Each action you take in the app slides the expiry forward and refreshes the cookie, so a session you keep using does not run out. The lifetime is an idle timeout: stay away for the whole period without touching LeapMux and you are signed out.

**Staying signed in.** As long as your session has not expired, reloading the page keeps you logged in — LeapMux restores your session on load. If your session has expired or been revoked, a failed request quietly signs you out (no error is shown); just log in again.

**Signing out.** Use the log-out action in the app. It ends your session on the server and clears the cookie. (In solo mode, "log out" does nothing — there is no session to end.)

**Changing your password signs out your other sessions.** When you change your password, every *other* active session is invalidated (the one you are using stays signed in), and your API and delegation tokens are revoked. This is a security feature: if someone else had a session, changing your password locks them out. See [Remote Control CLI](/docs/operating/control-cli/) for operator-side session management.

## Managing your profile

Open the **"Profile"** dialog from the app to manage your account. Preferences opens on **Account**, its first section, and each heading below is one row of it; the details of each field and persistence behavior live in [Settings & Preferences](/docs/using/settings/), so this is a summary.

While the session is verified, a panel at the top of the section says so and offers **End now** — see [Session elevation](/docs/operating/security/#session-elevation).

### Profile

- **Username** — editable. Taken usernames are rejected.
- **Display Name** — your shown name.
- Save with **Save Profile** (disabled until you change something valid). The dialog confirms the update on success.

### Email

- **Current Email** shows your address (or **"Not set"**) with a **(verified)** or **(unverified)** badge. An unverified address has a **Resend code** button beside it; the code goes in at `/verify-email`. A pending change shows the new address and asks you to verify it from your inbox.
- Enter a new address in **New Email** and click **Change Email**. This is one of the changes that needs a verified session — see [Session elevation](/docs/operating/security/#session-elevation).
- If verification is required, LeapMux sends a verification email and tells you to check your inbox. You must verify the new address before it takes effect. Otherwise the dialog confirms the new address at once. Admins change email immediately.
- Either way the new address starts out **unverified**, because nobody has confirmed it yet. Until you do, **Forgot password** cannot send a reset link to it.

### Password

- The button reads **Change Password** if you already have a password, or **Set Password** if your account is OAuth-only or passkey-only.
- Changing your password needs a **verified session**: the first sensitive change in a sitting opens a **Verify your identity** dialog, and the next two hours are covered. You do not retype your current password into this form.
- An account with no password and no passkey sets its first password without a prompt, but only within five minutes of signing in.
- On success the dialog confirms that it changed the password, or that it set the first one.

### Passkeys

See [Passkeys](#passkeys) above for the full passkey management surface in this dialog.

### Command-line credentials

Every device signed in with `leapmux control auth login` appears here, with the name it reported at consent time, when it was last used, and when it must sign in again. A credential granted hub administration says so.

**Revoke** ends a credential immediately; that device must sign in again. Revoking is the one account change that needs no verification, so you can act the moment you suspect a device is lost. `leapmux control auth credentials` prints the same list from a terminal.

See [Command-line credentials](/docs/operating/security/#command-line-credentials) for what a credential can do, how long it lives, and the email notice you get when one is issued.

### Linked accounts

- Lists the identity providers you sign in through, each with an **Unlink** button. An account that signs in through none says so.
- Unlinking needs a **verified session**, like the other changes in this dialog — see [Session elevation](/docs/operating/security/#session-elevation).
- LeapMux refuses to unlink your **only** login method when you have no password — set a password first. This keeps you from locking yourself out.

> **Tip:** If you signed up via OAuth and want a fallback, set a password under **Password** before unlinking any provider.

## Where to go next

- [Settings & Preferences](/docs/using/settings/) — the full Profile dialog and other preferences.
- [Authentication Providers](/docs/operating/authentication-providers/) — configuring OAuth/OIDC as an operator.
- [Running LeapMux](/docs/operating/running-leapmux/) and [Configuration](/docs/operating/configuration/) — choosing a run mode, the `signup_enabled` setting, and SMTP (which controls email verification and password reset).
- [Security & Threat Model](/docs/operating/security/) — what authentication does and does not protect.
