---
title: "Auth, OAuth Apps & Data"
description: "Fixes for Hub login, sign-up, passkeys, email verification, OAuth sign-in, third-party OAuth apps, step-up elevation, data persistence, and encryption key recovery."
type: docs
weight: 4
---

Fixes for account sign-in, passkeys, external identity providers, OAuth connected apps, step-up verification, persistence, and database/key backups.

## Login and sign-up

For account and session workflows, see [Accounts & Connected Apps](/docs/using/accounts-and-apps/) and [Security & Authentication](/docs/admin/security-and-auth/).

### Redirected to `/setup`

**Symptom**
Opening the Hub redirects to `/setup` (**"Welcome to LeapMux"**), or no account exists to log into.

**Cause**
A fresh multi-user Hub (`hub` or `dev`) has no users yet.

**Fix**
Complete the `/setup` form. The first registered account is always created as an administrator, after which `/setup` redirects to `/login`.

### Sign-up disabled

**Symptom**
Visiting `/signup` shows **"Sign-up disabled"**.

**Cause**
Public self-registration (`signup_enabled`) defaults to `false`.

**Fix**
Enable self-registration with `leapmux admin settings set signup_enabled true`, or have an administrator create accounts directly with `leapmux admin user create` (see [Admin CLI](/docs/admin/admin-cli/)).

### Invalid username or password

**Symptom**
Login fails with a generic invalid-credentials error.

**Cause**
The Hub returns the same message for unknown usernames and wrong passwords. Usernames are lowercase slugs; passwords are 8–128 printable ASCII characters.

**Fix**
Verify the exact lowercase username. To reset a lost password, an administrator runs `leapmux admin user reset-password <user>` against the live Hub (see [Admin CLI](/docs/admin/admin-cli/)), or runs `leapmux recover reset-password <user>` on the Hub host with the Hub stopped (see [Recovery](/docs/admin/recover/)).

Password resets sign out every session, revoke API and delegation tokens, and clear all passkeys. Registered Workers retain their credentials; after a suspected compromise, review and deregister unknown Workers. In `solo` mode, manage the `solo` password under **Preferences → Account → Password**.

### Passkey sign-in fails

**Symptom**
The **Passkey** option is missing or disabled, or the browser WebAuthn ceremony fails.

| Symptom | Cause | Fix |
|---|---|---|
| **Passkey** is disabled citing an insecure page | Plain HTTP away from `localhost` disables WebAuthn in browsers | Serve the Hub over HTTPS or access it on `localhost`. |
| **Passkey** option is absent and **Add passkey** cites the address | Current browser origin does not match `public_url` (`localhost` and `127.0.0.1`, or different ports, are distinct origins) | Set `public_url` to the exact origin users visit (`leapmux admin settings set public_url https://hub.example.com`). |
| **Passkey** is disabled citing browser support | Browser lacks WebAuthn support | Use a browser with passkey support. |
| `"origin header is required"` | Reverse proxy stripped the `Origin` header | Preserve `Origin` for `/leapmux.v1.*` RPC requests. |
| Passkey-only account cannot elevate | Page cannot run a passkey ceremony (HTTP or mismatched `public_url`) | Fix HTTPS or `public_url`. Elevation does not fall back to a linked OAuth sign-in provider. |
| Passkey fails after account recovery | Password reset and `/recover-account` clear all passkeys by design | Sign in with the new password and re-add passkeys under **Preferences → Account → Passkeys**. |
| Lost passkey and no password set | Account has no remaining usable sign-in method | Use **Can't sign in?** (`/recover-account`) when SMTP is configured, or ask an admin to reset the password. |

### Actions refused until email is verified

**Symptom**
After sign-up, most actions fail and the Hub redirects to **"Verify your email"**.

**Cause**
When SMTP is configured on the Hub, non-admin accounts must verify their email before using the Hub. Verification codes (`XXX-XXX`) expire after 30 minutes with a 5-attempt budget.

**Fix**
Enter the code or click the link from the verification email, or click **Resend code** (60-second cooldown). If SMTP is not configured, email verification is disabled.

### OAuth sign-in fails or provider is hidden

**Symptom**
No OAuth sign-in buttons appear on `/login`, or OAuth login ends in an error.

| Symptom | Cause | Fix |
|---|---|---|
| No OAuth buttons shown | No enabled identity provider configured | Add a provider with `leapmux admin idp add` (see [Sign-in Providers](/docs/admin/sign-in-providers/)). |
| Provider returned no email address | Missing email scope | Configure the provider with `email` / `user:email` scope. |
| Rejected after **Complete Sign Up** | New user while `signup_enabled` is `false` | Enable `signup_enabled` or sign in to an existing account with the same verified email to link it. |
| Signup link invalid or expired | Pending signup exceeded its 5-minute window or `?token=` was reused/omitted | Restart OAuth sign-in from `/login` and choose a username promptly. |
| Cannot unlink OAuth provider | It is the account's sole sign-in method | Set a password first under **Preferences → Account → Password**. |

Also verify the OpenID Connect (OIDC) issuer is reachable from the Hub and `public_url` is set accurately.

## OAuth apps and step-up elevation

For administrator setup see [App Authorization](/docs/admin/app-authorization/); for protocol details see [OAuth API](/docs/reference/oauth-api/).

### Consent page never appears

**Symptom**
`leapmux auth login` or a third-party app opens a browser and hangs or lands on an error page.

| Symptom | Cause | Fix |
|---|---|---|
| Browser hits "not found" | `public_url` is wrong, so metadata points clients to an unreachable URL | Fix `public_url` with `leapmux admin settings set public_url <origin>`. |
| Repeatedly asks to verify identity | Session could not elevate (no password or passkey on account) | Set a password first, then retry. |
| `invalid_client` | Unknown `client_id`, or a private app owned by another user | Register the app hub-wide, or register your own private app. |
| `invalid_request` about `redirect_uri` | Presented URI does not match a registered URI | Match scheme, host, escaped path, and raw query exactly (only loopback ports vary). |
| App keeps waiting after **Deny** | Hub returned `error=access_denied`, but the app ignored the callback | Check the client app's callback handler. |

### App refused a permission

**Symptom**
An authorized app receives `permission_denied` specifying a missing scope (for example `terminal:write`).

**Cause and fix**
Check the app's granted scopes under **Preferences → Apps → Connected apps**. Re-authorize the app to request additional scopes — refreshing can only narrow a grant. Account sign-in settings are never grantable to apps, and `admin:*` scopes on a non-admin account grant nothing.

### Step-up elevation refused

**Symptom**
An app reports that verification is required, and starting step-up fails with `unauthorized_client`.

**Cause and fix**
Step-up elevation is disabled by default on registered apps. Enable it for a trusted app with:

```bash
leapmux admin app allow-elevation <client_id>
```

Disabling elevation closes existing windows on the next request immediately.

### `invalid_grant` requires re-authorization

**Symptom**
A previously working app credential fails with `invalid_grant`.

| Error description | Cause | Fix |
|---|---|---|
| *token revoked* | App disconnected, retired, or account password reset | Authorize the app again. |
| *refresh reuse detected; token revoked* | Retired refresh token replayed after the 60-second grace window (leak or shared credential file) | Authorize again; give each machine its own credential file. |
| *this credential reached its maximum lifetime* | {{< duration absolute-cap >}} absolute cap reached | Authorize the app again. |
| *code expired or already consumed* | Authorization code expired ({{< duration authorization-code-ttl >}}) or was exchanged twice | Restart the login flow. Replaying a consumed code also revokes the credential minted by the first exchange. |

### Dynamic registration returns `403`

**Symptom**
`POST /oauth/register` returns `403`, or a client library reports that registration is unsupported.

**Cause and fix**
Open registration (`open_app_registration`) is off by default so anonymous callers cannot create consent-screen entries. Prefer registering apps explicitly, or enable open registration with:

```bash
leapmux admin settings set open_app_registration true
```

## Database and encryption recovery

### Data disappears between restarts

**Symptom**
Workspaces, accounts, agents, or terminals reset after restarting a container or daemon.

**Cause**
The Hub stores its database (`hub.db`) and key file (`encryption.key`) in its data directory, and Workers store `state.json` and `worker.db` in theirs. Running without persistent storage starts fresh on every restart.

**Fix**
- **Docker:** mount a persistent volume at `/data` (`-v leapmux-data:/data`).
- **CLI:** use a stable `--data-dir` (or the defaults under `~/.config/leapmux/<mode>/`).
- **Upgrades:** keep the same data directory or volume; migrations run automatically at startup.

### Hub fails to start or decrypt after restore

**Symptom**
After restoring a database backup, the Hub fails to start due to decryption errors, or accounts, workspaces, passkeys, and tokens fail to decrypt. Active sessions sign out and Worker tokens are rejected.

**Cause**
The restored Hub database does not match the `encryption.key` file on disk. The Hub encrypts sensitive database columns with `encryption.key` and uses its token pepper to validate sessions, API tokens, and Worker tokens; the database and key file must come from the same backup set.

**Fix**
- Restore the matching `encryption.key` from the same backup as the database, then restart the Hub.
- If `encryption.key` was lost permanently and you initialize a fresh key file, existing encrypted data cannot be recovered, all sessions sign out, and standalone Worker tokens become invalid. `solo` mode and Desktop auto-reissue their bundled Worker token; for each standalone Worker, remove it in the Hub UI, stop the Worker, delete `<data-dir>/state.json`, and register it again with a fresh key.
- For planned key rotation, run `leapmux recover encryption-key rotate`, restart the Hub as instructed, and run `leapmux recover encryption-key reencrypt`. See [Encryption & Data](/docs/admin/encryption-and-data/) and [Recovery](/docs/admin/recover/).

{{< callout type="warning" >}}
Always back up `encryption.key` alongside the Hub database. Losing the key file makes encrypted Hub data unrecoverable and invalidates all active sessions, API tokens, and standalone Worker tokens.
{{< /callout >}}
