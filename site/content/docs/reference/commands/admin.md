---
title: "Admin"
description: "Reference for leapmux admin commands: administer Hub users, sessions, settings, and tokens."
type: docs
weight: 8
---

<!-- The command tree generates this page. Do not edit it by hand: change the command, then run
     go test ./cmd/leapmux -run TestControlReference_IsCurrent -update-golden
     in backend/. -->

Every command on this page also takes the [flags of every command](/docs/reference/commands/#flags-of-every-command). See [Control CLI](/docs/using/control-cli/) for target selection, output formatting, and authentication.

## `leapmux admin`

Administer the hub; needs an administrator's login (offline repair: "leapmux recover").

### `leapmux admin settings`

Read and change the hub's settings: sign-in policy, SMTP, timeouts, limits.

#### `leapmux admin settings list`

List every setting with its effective value.

Aliases: `ls`.

```text
leapmux admin settings list [flags]
```

```sh
leapmux admin settings list
```

#### `leapmux admin settings get`

Show one setting, its value and its shape.

```text
leapmux admin settings get <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |

```sh
leapmux admin settings get session_duration_seconds
```

#### `leapmux admin settings set`

Set the public value of a setting from JSON or a bare value.

This command changes state.

```text
leapmux admin settings set <key> <value> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |
| `<value>` | A JSON value. A word that is not JSON is stored as a string. |

```sh
leapmux admin settings set signup_enabled false

# Set two fields of a setting that holds a document.
leapmux admin settings set smtp '{"host":"smtp.example.com","port":587}'
```

#### `leapmux admin settings set-secret`

Set the secret fields of a setting; the hub stores them encrypted.

This command changes state.

```text
leapmux admin settings set-secret <key> <json> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |
| `<json>` | A JSON object of the secret fields, such as {"password":"..."}. |

```sh
leapmux admin settings set-secret smtp '{"password":"..."}'
```

#### `leapmux admin settings reset`

Restore the default value of a setting.

This command changes state.

```text
leapmux admin settings reset <key> [flags]
```

| Argument | Description |
|---|---|
| `<key>` | A setting key, as "leapmux admin settings list" prints it. |

```sh
leapmux admin settings reset session_duration_seconds
```

### `leapmux admin user`

Manage users.

#### `leapmux admin user list`

List users.

Aliases: `ls`.

```text
leapmux admin user list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--query string` | search query (empty = list all) |

```sh
leapmux admin user list

# Search by name or email.
leapmux admin user list --query alice
```

#### `leapmux admin user get`

Show a user.

```text
leapmux admin user get <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin user get alice

# Select a user by id.
leapmux admin user get id:Ab3dE9
```

#### `leapmux admin user create`

Create a user.

This command changes state.

```text
leapmux admin user create <username> [flags]
```

| Flag | Description |
|---|---|
| `--password string` | password (prompted for when omitted, so it stays out of the shell history) |
| `--display-name string` | display name |
| `--email string` | email address |
| `--email-verified` | mark email as verified |
| `--admin` | grant admin privileges |

```sh
leapmux admin user create alice --email alice@example.com
```

#### `leapmux admin user update`

Change a user's name, email or verification.

This command changes state.

```text
leapmux admin user update <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--display-name string` | new display name |
| `--email string` | new email address |
| `--email-verified string` | mark email as verified (one of: true, false) |
| `--clear-pending-email` | clear any in-flight email verification (token + attempt counter) |

```sh
leapmux admin user update alice --display-name "Alice Kim"
```

#### `leapmux admin user delete`

Delete a user.

Aliases: `rm`.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin user delete <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--force` | required to delete an admin user |

```sh
leapmux admin user delete alice
```

#### `leapmux admin user grant-admin`

Make a user an administrator.

This command changes state.

```text
leapmux admin user grant-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin user grant-admin alice
```

#### `leapmux admin user revoke-admin`

Take a user's administrator access away.

This command changes state.

```text
leapmux admin user revoke-admin <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--force` | required to remove your own administrator access |

```sh
leapmux admin user revoke-admin alice
```

#### `leapmux admin user reset-password`

Set a user's password and revoke every credential they hold.

This command changes state.

```text
leapmux admin user reset-password <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--password string` | new password (prompted for when omitted, so it stays out of the shell history) |

```sh
# Prompt for the new password.
leapmux admin user reset-password alice
```

### `leapmux admin session`

List and revoke sign-in sessions.

#### `leapmux admin session list`

List active sessions, optionally filtered to one user.

Aliases: `ls`.

```text
leapmux admin session list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |

```sh
leapmux admin session list

# List one user's active sessions.
leapmux admin session list --user alice
```

#### `leapmux admin session revoke`

Revoke a session.

This command changes state.

```text
leapmux admin session revoke <session> [flags]
```

| Argument | Description |
|---|---|
| `<session>` | A session id or unique prefix, as "leapmux admin session list" prints it. |

```sh
leapmux admin session revoke 4f1aC2
```

#### `leapmux admin session revoke-user`

Revoke every session of a user.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin session revoke-user <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

```sh
leapmux admin session revoke-user alice
```

#### `leapmux admin session purge-expired`

Delete every expired session.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin session purge-expired [flags]
```

```sh
leapmux admin session purge-expired
```

### `leapmux admin worker`

Manage the workers of every user.

#### `leapmux admin worker list`

List the workers of every user.

Aliases: `ls`.

```text
leapmux admin worker list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--status string` | worker status filter (one of: active, deregistering, deleted, all; default: active) |

```sh
leapmux admin worker list

# Include the workers that are gone.
leapmux admin worker list --status all
```

#### `leapmux admin worker get`

Show a worker of any user.

```text
leapmux admin worker get <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id or unique prefix of 4 or more characters, as "leapmux admin worker list" prints it. |

```sh
leapmux admin worker get 5f2c9a1d
```

#### `leapmux admin worker deregister`

Deregister a worker of any user.

This command changes state.

```text
leapmux admin worker deregister <worker> [flags]
```

| Argument | Description |
|---|---|
| `<worker>` | A worker id or unique prefix of 4 or more characters, as "leapmux admin worker list" prints it. |

```sh
leapmux admin worker deregister 5f2c9a1d
```

#### `leapmux admin worker reg-key`

Manage worker registration keys.

##### `leapmux admin worker reg-key list`

List worker registration keys.

Aliases: `ls`.

```text
leapmux admin worker reg-key list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--include-expired` | include revoked or expired keys (forensics; default shows only live keys) |

```sh
leapmux admin worker reg-key list
```

##### `leapmux admin worker reg-key revoke`

Revoke a worker registration key.

This command changes state.

```text
leapmux admin worker reg-key revoke <reg-key> [flags]
```

| Argument | Description |
|---|---|
| `<reg-key>` | A registration key id or unique prefix, as "leapmux admin worker reg-key list" prints it. |

```sh
leapmux admin worker reg-key revoke 9Zt4Kp
```

##### `leapmux admin worker reg-key purge-expired`

Delete every expired or revoked registration key.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin worker reg-key purge-expired [flags]
```

```sh
leapmux admin worker reg-key purge-expired
```

### `leapmux admin app`

Manage app registrations: what a consent screen authorizes.

#### `leapmux admin app list`

List app registrations.

Aliases: `ls`.

```text
leapmux admin app list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--include-revoked` | include retired apps |

```sh
leapmux admin app list
```

#### `leapmux admin app register`

Register an app; the hub shows its secret once.

This command changes state.

```text
leapmux admin app register <name> [flags]
```

| Flag | Description |
|---|---|
| `--uri string` | the app's home page |
| `--redirect-uri string` | comma-separated addresses the authorization may return to (required for the authorization_code grant) |
| `--scope string` | space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write (required) |
| `--grant-type string` | comma-separated OAuth grant types; empty takes the RFC 7591 default of authorization_code and refresh_token |
| `--type string` | public (a binary a user holds; PKCE protects it) or confidential (a server that can keep a secret) (one of: public, confidential; default: public) |
| `--visibility string` | hub-wide (every account may authorize it; administrators only) or private (yours alone) (one of: hub-wide, private; default: hub-wide) |
| `--allow-elevation` | let this app run the step-up stage, which is what a sensitive change needs |

```sh
leapmux admin app register "Release bot" --scope "workspace:read" --redirect-uri http://127.0.0.1/callback
```

#### `leapmux admin app update`

Change the editable fields of an app.

This command changes state.

```text
leapmux admin app update <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

| Flag | Description |
|---|---|
| `--name string` | new app name |
| `--uri string` | new home page |
| `--redirect-uri string` | replace the redirect list (comma-separated) |
| `--scope string` | replace the permission ceiling; space-separated permissions this app may ask for, from: account:read, account:write, admin:apps, admin:read, admin:settings, admin:users, admin:workers, agent:read, agent:write, file:read, git:read, git:write, terminal:read, terminal:write, tunnel:open, worker:admin, worker:read, workspace:read, workspace:write |
| `--grant-type string` | replace the grant-type list (comma-separated) |

```sh
leapmux admin app update lmx_app_x1y2 --name "Release bot 2"
```

#### `leapmux admin app verify`

Vouch for an app, so that its consent screen stops warning.

This command changes state.

```text
leapmux admin app verify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app verify lmx_app_x1y2
```

#### `leapmux admin app unverify`

Withdraw a vouch for an app.

This command changes state.

```text
leapmux admin app unverify <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app unverify lmx_app_x1y2
```

#### `leapmux admin app allow-elevation`

Let an app ask a user to prove a factor again.

This command changes state.

```text
leapmux admin app allow-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app allow-elevation lmx_app_x1y2
```

#### `leapmux admin app deny-elevation`

Take that permission away; every open elevation ends at the next call.

This command changes state.

```text
leapmux admin app deny-elevation <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app deny-elevation lmx_app_x1y2
```

#### `leapmux admin app revoke`

Retire an app and revoke every credential it holds.

This command changes state.

```text
leapmux admin app revoke <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app revoke lmx_app_x1y2
```

#### `leapmux admin app delete`

Delete an app that never held a credential.

Aliases: `rm`.

This command changes state.

```text
leapmux admin app delete <app> [flags]
```

| Argument | Description |
|---|---|
| `<app>` | An app's client id or unique prefix, as "leapmux admin app list" prints it. |

```sh
leapmux admin app delete lmx_app_x1y2
```

### `leapmux admin idp`

Manage the OAuth and OIDC providers that users sign in with.

#### `leapmux admin idp add`

Add an OAuth or OIDC sign-in provider.

This command changes state.

```text
leapmux admin idp add [flags]
```

| Flag | Description |
|---|---|
| `--type string` | provider type (one of: github, google, apple, oidc; required) |
| `--name string` | display name (defaults to the preset name) |
| `--client-id string` | OAuth client ID |
| `--client-secret string` | OAuth client secret |
| `--issuer-url string` | OIDC issuer URL |
| `--scopes string` | space-separated scopes |
| `--trust-email string` | trust verified provider emails (required for generic OIDC) (one of: true, false) |

```sh
leapmux admin idp add --type github --client-id Iv1.abc --client-secret s3cr3t
```

#### `leapmux admin idp list`

List the sign-in providers.

Aliases: `ls`.

```text
leapmux admin idp list [flags]
```

```sh
leapmux admin idp list
```

#### `leapmux admin idp remove`

Remove a sign-in provider.

Aliases: `rm`.

This command changes state.

```text
leapmux admin idp remove <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

| Flag | Description |
|---|---|
| `--force` | required to remove a provider that is the last login method of an account |

```sh
leapmux admin idp remove 3hKd9s
```

#### `leapmux admin idp enable`

Enable a sign-in provider.

This command changes state.

```text
leapmux admin idp enable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

```sh
leapmux admin idp enable 3hKd9s
```

#### `leapmux admin idp disable`

Disable a sign-in provider.

This command changes state.

```text
leapmux admin idp disable <idp> [flags]
```

| Argument | Description |
|---|---|
| `<idp>` | A sign-in provider id or unique prefix, as "leapmux admin idp list" prints it. |

```sh
leapmux admin idp disable 3hKd9s
```

### `leapmux admin captcha`

Manage the bot protection: ALTCHA, reCAPTCHA v3 or Turnstile (a solo hub never asks).

#### `leapmux admin captcha get`

Show the captcha configuration in effect.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha get [flags]
```

```sh
leapmux admin captcha get
```

#### `leapmux admin captcha set`

Change the captcha settings, or switch the provider.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha set [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | provider alias; omit to tune the active provider (one of: altcha, recaptcha_v3, turnstile) |
| `--algorithm string` | ALTCHA algorithm (one of: ARGON2ID, PBKDF2/SHA-256, PBKDF2/SHA-384, PBKDF2/SHA-512, SCRYPT, SHA-256, SHA-384, SHA-512) |
| `--cost int` | algorithm cost parameter; 0 restores the default |
| `--memory-cost int` | algorithm memory cost; 0 restores the default |
| `--parallelism int` | algorithm parallelism; 0 restores the default |
| `--expires int` | challenge expiry seconds; 0 restores the default |
| `--site-key string` | provider site key |
| `--secret string` | provider secret (stored encrypted) |
| `--min-score number` | recaptcha_v3 minimum score (0 &lt; v &lt;= 1); 0 restores the default |

```sh
leapmux admin captcha set --provider turnstile --site-key 0x4AAA --secret s3cr3t
```

#### `leapmux admin captcha enable`

Turn captcha verification on.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha enable [flags]
```

```sh
leapmux admin captcha enable
```

#### `leapmux admin captcha disable`

Turn captcha verification off; the honeypot check stays.

This command changes state.

Does not support `-q` / `--quiet`.

```text
leapmux admin captcha disable [flags]
```

```sh
leapmux admin captcha disable
```

#### `leapmux admin captcha reset`

Restore the captcha defaults of every provider, or of one.

This command changes state.

```text
leapmux admin captcha reset [flags]
```

| Flag | Description |
|---|---|
| `--provider string` | reset one provider's row (omit = all captcha settings) (one of: altcha, recaptcha_v3, turnstile) |

```sh
leapmux admin captcha reset

# Reset one provider.
leapmux admin captcha reset --provider altcha
```

### `leapmux admin rate-limit`

Manage the rate limit of each user operation.

#### `leapmux admin rate-limit list`

List the rate limits of every operation.

Aliases: `ls`.

```text
leapmux admin rate-limit list [flags]
```

```sh
leapmux admin rate-limit list
```

#### `leapmux admin rate-limit set`

Change the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit set <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

| Flag | Description |
|---|---|
| `--max-attempts int` | failed attempts allowed per window (1-1000); 0 restores the default |
| `--window int` | window seconds (60-86400); 0 restores the default |

```sh
leapmux admin rate-limit set elevation --max-attempts 5 --window 900
```

#### `leapmux admin rate-limit enable`

Turn on the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit enable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit enable elevation
```

#### `leapmux admin rate-limit disable`

Turn off the rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit disable <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit disable elevation
```

#### `leapmux admin rate-limit reset`

Restore the default rate limit of an operation.

This command changes state.

```text
leapmux admin rate-limit reset <operation> [flags]
```

| Argument | Description |
|---|---|
| `<operation>` | One of: elevation, email_change, login_anonymous, oauth_anonymous, solo_password_setup. |

```sh
leapmux admin rate-limit reset elevation
```

### `leapmux admin api-token`

Manage durable API tokens for scripts and integrations.

#### `leapmux admin api-token list`

List API tokens.

Aliases: `ls`.

```text
leapmux admin api-token list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--client-id string` | filter by app (empty = all apps) |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |

```sh
leapmux admin api-token list

# List one user's tokens.
leapmux admin api-token list --user alice
```

#### `leapmux admin api-token issue`

Issue an API token that &lt;user&gt; owns.

This command changes state.

```text
leapmux admin api-token issue <user> [flags]
```

| Argument | Description |
|---|---|
| `<user>` | A username, "id:&lt;id&gt;" for a user id, or "username:&lt;name&gt;". |

| Flag | Description |
|---|---|
| `--client-id string` | the app this credential belongs to (empty = the built-in service-account registration) |
| `--installation-name string` | human-visible name for this installation, e.g. ci-runner-3 (required) |
| `--ttl int` | fixed lifetime in seconds, with no refresh token (0 = the renewing credential: 1h access + refresh) |
| `--scope string` | space- or comma-separated permissions, e.g. "file:read git:read" (empty = everything except admin:*) |

```sh
leapmux admin api-token issue ci-bot --installation-name ci-runner-3 --scope "workspace:read"
```

#### `leapmux admin api-token revoke`

Revoke an API token.

This command changes state.

```text
leapmux admin api-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id or unique prefix, as the list command of this group prints it. |

```sh
leapmux admin api-token revoke 7Qx2Lm
```

### `leapmux admin delegation-token`

Manage the delegation tokens that workers mint.

#### `leapmux admin delegation-token list`

List the delegation tokens that workers minted.

Aliases: `ls`.

```text
leapmux admin delegation-token list [flags]
```

| Flag | Description |
|---|---|
| `--limit int` | page size (default: 50) |
| `--cursor string` | the cursor that the previous page printed |
| `--user string` | filter by user: a username, "id:&lt;id&gt;", or "username:&lt;name&gt;" (empty = all users) |
| `--include-revoked` | include revoked tokens (forensics; default lists live tokens only) |

```sh
leapmux admin delegation-token list
```

#### `leapmux admin delegation-token revoke`

Revoke a delegation token.

This command changes state.

```text
leapmux admin delegation-token revoke <token> [flags]
```

| Argument | Description |
|---|---|
| `<token>` | A token id or unique prefix, as the list command of this group prints it. |

```sh
leapmux admin delegation-token revoke 7Qx2Lm
```

### `leapmux admin network`

Inspect the Hub's live TCP listeners, IPC sockets, and host network interfaces.

#### `leapmux admin network status`

Show the Hub's live TCP listeners, local IPC endpoints, and machine interfaces.

Does not support `-q` / `--quiet`.

```text
leapmux admin network status [flags]
```

```sh
leapmux admin network status
leapmux admin network status --json
```
