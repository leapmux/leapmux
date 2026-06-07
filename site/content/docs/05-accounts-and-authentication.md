---
title: "Accounts & Authentication"
type: docs
weight: 5
---

This chapter covers everything you need to get into LeapMux as a user: when you need an account at all, how to create the very first one, how to sign up and log in, how email verification and OAuth sign-in work, and how to manage your profile and password once you are in.

Whether you ever see a login screen depends on how LeapMux is being run. The first section makes that distinction; the rest assumes a multi-user deployment where accounts apply.

## When you need an account

LeapMux runs in several modes (see [Running LeapMux](/docs/17-running-leapmux/)). Two of them treat accounts very differently:

| Mode | Account needed? | What you see |
| --- | --- | --- |
| **Solo** (`leapmux solo`) | No | No login or signup screen. A single passwordless user named `solo` is created and auto-authenticated for every request. |
| **Dev** (`leapmux dev`) | Yes | Real password authentication. The first admin is created through the `/setup` flow. |
| **Hub** (`leapmux hub`) | Yes | Full authentication: signup, password login, sessions, OAuth, API tokens. |

In **solo mode** there is nothing to sign up for and nothing to log out of. If you navigate to `/login` or `/signup` you are redirected straight into the app. Account-related actions are intentionally disabled: changing your profile, email, or password, or unlinking an OAuth provider are all rejected as "not available in solo mode" (the exact wording is action-specific, for example **"profile changes are not available in solo mode"** or **"password changes are not available in solo mode"**).

> **Note:** Solo mode auto-authenticates *every* request as the admin. If you bind it to a non-loopback address, anyone who can reach the port has full admin access without credentials. LeapMux warns you about this at startup. For a shared or networked deployment, run `leapmux hub` (or `leapmux dev`) so real authentication applies. See [Security & Threat Model](/docs/23-security-and-threat-model/).

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

On success you are signed in and taken to your personal organization at `/o/{username}`.

A few things are special about this first account:

- It is **always created as an administrator**.
- Its email is **marked verified immediately**, even when email verification is otherwise required for everyone else.
- The username `admin` is **allowed** here (it is reserved only in public signup). The username `solo` is reserved everywhere and cannot be used.
- Each new user — including this one — gets a personal organization named after the username, with that user as the org **Owner**. See [Organizations & Members](/docs/06-organizations-and-members/).

> **Note:** The `/setup` screen only appears while no users exist. Once the first admin is created, visiting `/setup` redirects you to the login page. Setup is also race-safe: if two people submit at once, only one wins and the other is told sign-up is disabled.

## Signing up

After the first admin exists, new self-service accounts are only possible if the operator has enabled them with the `--signup-enabled` flag (it is **off by default**). See [Configuration](/docs/18-configuration/).

- **If signup is disabled**, visiting `/signup` shows a "not found" page titled **"Sign-up disabled"** with the message **"New account registration is not currently available."** and a **"Go to login"** link.
- **If signup is enabled**, you get the **"Sign Up"** page.

The form fields are the same as setup:

| Field | Notes |
| --- | --- |
| **Username** | Required. Lowercase slug, 1–32 characters. See [Username rules](#username-rules). |
| **Display Name** | Optional; falls back to your username if left blank. |
| **Email** | Optional, unless your operator requires verification. |
| **New Password** | 8–128 characters. See [Password requirements](#password-requirements). |
| **Confirm Password** | Must match. |

The submit button reads **Sign up** (and **Signing up...** while submitting). It stays disabled until you have entered a username and a valid, matching password. A footer link, **"Already have an account? Sign in"**, takes you to the login page.

If your operator has configured OAuth/OIDC providers, a list of provider buttons appears above the form under the verb **"Sign up with"** (for example, **"Sign up with GitHub"**), followed by the divider **"or create an account with email"**. See [Signing in with OAuth / OIDC](#signing-in-with-oauth--oidc).

What happens after you submit depends on whether email verification is required:

- **Verification not required:** you are signed in immediately and taken to `/o/{username}`.
- **Verification required:** you see **"Check your email to verify your account."**, and you are routed to the email-verification screen. A failed verification email does **not** undo your signup — your account exists and you can request a fresh code.

> **Note:** The username `solo` is rejected in all signup paths, and `admin` is additionally reserved for public signup and OAuth completion (it is allowed only in `/setup`). Self-service signups are never administrators.

## Logging in

Visit `/login`. The page is headed **"LeapMux"** and has two fields — **Username** and **Password**.

Click **Sign in** (**Signing in...** while it works). The button is disabled until both fields are filled.

- A **"Sign up"** link appears in the footer only when self-service signup is enabled.
- If OAuth providers are configured, their buttons appear above the form under the verb **"Sign in with"** with an **"or"** divider.
- If you were redirected to login from a protected page, you are sent back there after signing in (LeapMux only honors a same-site relative path, as an open-redirect safeguard).

> **Note:** Both an unknown username and a wrong password produce the same error, **"invalid credentials."** This is deliberate — it prevents anyone from probing which usernames exist.

## Email verification

Email verification is optional and controlled by the operator's `--email-verification-required` flag (off by default; it requires an SMTP server to be configured). When it is on, you must verify your email before you can use most of LeapMux.

### Verifying your email

You reach the **"Verify your email"** screen automatically right after signing up (when verification is required), or by clicking the link in the verification email. The screen reads:

> Enter the 6-character code we sent to your inbox, or click the link in that email.

- The input expects a code in the form **`XXX-XXX`**. You can type it with or without the hyphen and in any case — LeapMux normalizes it for you.
- Click **Verify** (**Verifying…** while it works). If you submit an empty field, you are reminded to **"Enter the 6-character code from your email."**
- A separate **Resend code** button requests a new code.

The verification email arrives with the subject **"[LeapMux] Verify your email address"** and contains both the code and a direct link. Clicking the link opens the verification screen with the code pre-filled and submits it automatically.

On success you are signed in fully and taken to `/o/{username}`.

### Code limits

| Limit | Value |
| --- | --- |
| Code length / format | 6 characters, shown as `XXX-XXX` |
| Code lifetime | **30 minutes** |
| Wrong-guess budget | **5 attempts** — the 6th wrong guess invalidates the code, and you must request a new one |
| Resend cooldown | **60 seconds** between requests |

The code alphabet deliberately omits look-alike characters (no `0`, `1`, `I`, `O`, or `L`), so what you read in the email is what you type. An expired code and a wrong code report the same generic error so neither leaks information.

> **Tip:** If you wait too long and your code expires, just press **Resend code** to get a fresh one. Successful resends report **"A fresh code has been sent to your inbox."**

> **Warning:** While verification is required and you are an unverified non-admin user, you can only view your own account, log out, change/verify your email, and resend the code. Every other action is blocked with "email verification required" until you verify. Administrators are exempt.

## Signing in with OAuth / OIDC

If your operator has configured one or more external identity providers — GitHub, Google, Apple, or a generic OIDC provider — you can sign in or sign up with them instead of (or in addition to) a password. Configuring providers is an operator task; see [Authentication Providers](/docs/21-authentication-providers/).

### The flow

1. On the login or signup page, click a provider button (for example, **"Sign in with GitHub"**).
2. Your browser is handed off to the provider, where you authorize LeapMux.
3. The provider sends you back to LeapMux, which finishes the sign-in.

**The OAuth / OIDC sign-in flow:**

```text
   ┌─────────────────────────────────────────────────────┐
   │                       Browser                       │
   │               (login or signup page)                │
   └─────────────────────────────────────────────────────┘
                              │  1. Click "Sign in with …"
                              ▼
   ┌─────────────────────────────────────────────────────┐
   │                         Hub                         │
   │                   starts sign-in                    │
   └─────────────────────────────────────────────────────┘
                              │  2. Redirect to provider
                              ▼
   ┌─────────────────────────────────────────────────────┐
   │      Provider: GitHub / Google / Apple / OIDC       │
   │               (you authorize LeapMux)               │
   └─────────────────────────────────────────────────────┘
                              │  3. Redirect back with auth code
                              ▼
   ┌─────────────────────────────────────────────────────┐
   │                    Hub callback                     │
   │        finishes sign-in, establishes session        │
   └─────────────────────────────────────────────────────┘
```

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

Click **Create account** (**Creating account...** while it works). On success you are signed in. If your email still needs verification, you are routed to the verification screen first; otherwise you go straight to `/o/{username}`.

Accounts created this way have no password set. You can add one later from your profile (see [Managing your profile](#managing-your-profile)), which is useful as a fallback login method.

## Password requirements

LeapMux enforces only length on passwords:

| Rule | Value |
| --- | --- |
| Minimum length | **8 characters** |
| Maximum length | **128 characters** |
| Complexity (uppercase, digits, symbols) | Not required |

There is **no** mandatory mix of character types. The signup, setup, and password-change forms show a live **strength meter** with the labels **Weak**, **Fair**, **Good**, and **Strong**, but this is advisory only — it never blocks you. The form also warns **"Passwords do not match."** when your confirmation differs.

> **Tip:** The meter rewards length and variety. A long passphrase scores well even without symbols; an all-letters or all-digits password is penalized. Use the meter as guidance, not a gate.

Passwords are stored hashed with Argon2id using OWASP-recommended parameters; LeapMux never stores or transmits your plaintext password after hashing. See [Encryption & Data](/docs/22-encryption-and-data/).

## Username rules

Usernames are GitHub-style slugs, enforced identically in the browser and on the server:

- **1–32 characters**
- Lowercase letters `a–z`, digits `0–9`, and hyphens only
- No leading or trailing hyphen
- No consecutive hyphens (`--`)

`solo` is reserved in every account-creation path. `admin` is reserved for public signup and OAuth completion, but allowed during first-run `/setup`.

> **Note:** Your username doubles as the name of your personal organization. Changing your username also renames that org — LeapMux warns you when you edit it.

## Sessions and signing out

When you log in, LeapMux issues a session and stores it in a secure, `HttpOnly` cookie that your browser sends on every request. Key facts:

| Property | Value |
| --- | --- |
| Session lifetime | **24 hours** |
| Cookie name | `leapmux-session` (or `__Host-leapmux-session` when the operator enables secure cookies behind TLS) |
| Cookie flags | `HttpOnly`, `Path=/`, `SameSite=Lax` |

**Staying signed in.** As long as your session has not expired, reloading the page keeps you logged in — LeapMux restores your session on load. If your session has expired or been revoked, a failed request quietly signs you out (no error is shown); just log in again.

**Signing out.** Use the log-out action in the app. It ends your session on the server and clears the cookie. (In solo mode, "log out" does nothing — there is no session to end.)

**Changing your password signs out your other sessions.** When you change your password, every *other* active session is invalidated (the one you are using stays signed in), and your API and delegation tokens are revoked. This is a security feature: if someone else had a session, changing your password locks them out. See [Admin CLI](/docs/20-admin-cli/) for operator-side session management.

## Managing your profile

Open the **"Profile"** dialog from the app to manage your account. It has up to four sections; the details of each field and persistence behavior live in [Settings & Preferences](/docs/14-settings-and-preferences/), so this is a summary.

### Profile

- **Username** — editable; changing it renames your personal organization (you are warned). Taken usernames are rejected.
- **Display Name** — your shown name.
- Save with **Save Profile** (disabled until you change something valid). Success shows **"Profile updated."**

### Email

- **Current Email** shows your address (or **"Not set"**) with a **(verified)** or **(unverified)** badge. A pending change shows **"Pending email change to … — check your inbox to verify."**
- Enter a new address in **New Email** and click **Change Email**.
- If verification is required, you get **"Verification email sent. Check your inbox."** and must verify the new address before it takes effect; otherwise you see **"Email updated."** Admins change email immediately.

### Password

- The button reads **Change Password** if you already have a password, or **Set Password** if your account is OAuth-only.
- If you have a password, a **Current Password** field appears and is required. OAuth-only users can set a password without one.
- Success shows **"Password changed."** or **"Password set."**

### Linked Accounts

- Shown only if you have linked OAuth providers. Each row lists the provider name and an **Unlink** button.
- LeapMux refuses to unlink your **only** login method when you have no password — set a password first. This keeps you from locking yourself out.

> **Tip:** If you signed up via OAuth and want a fallback, set a password under **Password** before unlinking any provider.

## Where to go next

- [Organizations & Members](/docs/06-organizations-and-members/) — your personal org, roles, and switching orgs.
- [Settings & Preferences](/docs/14-settings-and-preferences/) — the full Profile dialog and other preferences.
- [Authentication Providers](/docs/21-authentication-providers/) — configuring OAuth/OIDC as an operator.
- [Running LeapMux](/docs/17-running-leapmux/) and [Configuration](/docs/18-configuration/) — choosing a run mode and the `--signup-enabled` / `--email-verification-required` flags.
- [Security & Threat Model](/docs/23-security-and-threat-model/) — what authentication does and does not protect.
