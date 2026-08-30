---
title: "App Authorization"
description: "Register apps on a LeapMux Hub, control what each may ask for, and vouch for the ones you trust. The OAuth 2.1 authorization server, from the operator's side."
type: docs
weight: 5
---

> **Not this chapter:** signing users *in* with GitHub, Google, or your own OIDC issuer is [Sign-in Providers](/docs/operating/sign-in-providers/).
> This chapter is the other direction — apps that ask **your Hub** for access to an account on it.

A LeapMux Hub is an OAuth 2.1 authorization server. Any program can ask an account holder for permission, and the account holder decides on a consent screen the Hub renders. The control CLI uses the same flow as everything else; it is registered like any other app.

This chapter is for operators. If you want to see and disconnect the apps on **your own** account, see [Connected Apps](/docs/using/connected-apps/). If you write an app, the wire contract is [OAuth API](/docs/reference/oauth-api/).

## Who may register an app

Both an administrator and an ordinary user can register one, and the difference is who sees it:

| Registered by | Visible to | Editable by |
|---|---|---|
| An ordinary user | That user alone | That user |
| An administrator | Everybody on the Hub | Administrators |

A private app is invisible in every sense. Another account does not see it in a listing, and a consent screen for it does not render even for somebody handed the link — the Hub answers as though the app does not exist. That is deliberate: telling a stranger "that app exists but is not yours" answers the question the rule exists to refuse.

A user registers an app from **Preferences → Apps**. An operator uses the CLI:

```bash
leapmux control admin app register \
  --hub https://leapmux.example.com \
  --name "Deployment bot" \
  --redirect-uri https://bot.example.com/callback \
  --scope "workspace:read agent:read agent:write" \
  --type confidential \
  --visibility hub-wide
```

The response carries the `client_id` and, for a confidential client, the `client_secret` **once**. The Hub stores only its hash and cannot show it again.

**Every write to a registration needs a recently proven factor** — registering, editing, allowing the step-up ceremony, and vouching. Editing is why: rewriting a redirect list diverts an authorization code already in flight to an address the editor chose, which is the most dangerous write on this surface. In the browser the first such write in a sitting opens a **Verify your identity** dialog; on the CLI the refusal prints an address and a short code you approve in a browser (see [Verifying a command-line credential](/docs/operating/security/#verifying-a-command-line-credential)). **Retiring** an app and **deleting** an empty registration need none: both only reduce what the app can reach, and demanding a fresh factor from somebody who just realized an app is malicious is the wrong failure mode.

## Permissions

A grant is a set of named permissions. Each one is a sentence on the consent screen, because a screen that listed `terminal:write` would ask somebody to approve running any command on their machine without saying so.

| Permission | What the consent screen says |
|---|---|
| `account:read` | Read your profile: your username, your email address and whether you are an administrator. |
| `account:write` | Change your profile and your account settings, including your password. |
| `workspace:read` | Read your workspaces, your tabs and your layout. |
| `workspace:write` | Create, rename, move and close your workspaces and tabs. |
| `worker:read` | List your workers and connect to one. |
| `worker:admin` | Rename and deregister your workers, and manage the keys that let a machine join. |
| `agent:read` | Read your coding-agent sessions, including every message in them. |
| `agent:write` | Send prompts to your coding agents and answer their permission requests. |
| `terminal:read` | Read the output of your terminals. |
| `terminal:write` | Type into your terminals, which runs any command on your machine. |
| `file:read` | Browse and read files on your machines. |
| `git:read` | Read the git state of your repositories: status, branches, diffs and history. |
| `git:write` | Commit, push, and create or delete branches in your repositories. |
| `tunnel:open` | Open network connections from inside your private network to any address it can reach. |
| `admin:read` | Read this hub's administration: every account, setting, worker and credential. |
| `admin:users` | Administer every account on this hub, including resetting passwords. |
| `admin:settings` | Change this hub's settings, including its security policy and its sign-in providers. |
| `admin:workers` | Administer every worker on this hub. |
| `admin:apps` | Register, edit, vouch, retire and delete the hub's app registrations. |

Four rules bind the whole vocabulary.

**A permission only ever subtracts.** A grant narrows what an account can already do; it never adds. An app granted `admin:users` on an ordinary account administers nothing, because the account does not.

**Some permissions imply others.** `file:read` implies `worker:read`, because reading a file means reaching the machine that holds it. The Hub closes the set when it stores the grant, so the credential, the consent screen, and the token response all show the same list.

**The registration is a LIVE ceiling.** `--scope` sets what the app may *ask* for, and the Hub applies it at every request rather than only at the consent — so narrowing it takes the permission from the credentials the app already holds, at their next call. Use that when an app should keep working with less; **Retire** is for when it should not keep working at all. The ceiling only narrows: the account's consent is untouched, so restoring the permission on the registration restores what the account already agreed to, with no fresh authorization. Editing a registration needs a recently proven factor.

**An admin permission needs an administrator on both sides.** A non-administrator cannot register an app that *asks* for one, and cannot grant one on a consent screen. The refusal lands at registration, so an operator learns it before the app ships rather than when a user meets it.

Nothing an app holds reaches the account's own authenticators. Adding a passkey, changing the recovery address, unlinking a sign-in provider, and managing another app's credential are outside every grant, whatever the consent screen offered — those create authority that outlives the app's connection, so disconnecting the app would no longer withdraw what it was given.

## Verifying an app

A new registration is **unverified**, and its consent screen says so: the heading reads *Authorize an unverified app?* and the app's chosen name appears in a paragraph that attributes it, never in the heading. That matters because the name is a string the registrant picked, and a name in the heading reads as the Hub's own words.

An administrator vouches for an app once they know what it is:

```bash
leapmux control admin app verify --client-id <client_id>
```

The warning disappears and the screen identifies the administrator who vouched. `unverify` withdraws it.

## Elevation

A **sensitive** change — a password, a passkey, the recovery address — needs a recently proven factor. That proof is a *step-up*, and it happens in a browser.

An app is refused the step-up ceremony by default. Its owner turns it on per app:

```bash
leapmux control admin app allow-elevation --client-id <client_id>
```

Turning it off closes every open window on the next request, because the Hub re-reads the flag each time it validates a credential rather than trusting what was true when the window opened.

The setting is not a trust tier and not a permission. The elevation window multiplies whatever the grant already allows, so no permission could express it and mean anything.

## Open registration

RFC 7591 dynamic registration lets a program register itself with no account at all. It is **off by default**, and the default is the decision: an anonymous caller who can create a registration can create a row that appears on a consent screen, which is a phishing surface as much as a convenience.

```bash
leapmux control admin settings set open_app_registration true
```

While it is off, `registration_endpoint` is absent from the Hub's metadata document, so a conformant client library does not try and does not report the refusal as a Hub failure. A self-registered app is always unverified and always hub-wide.

## Retiring an app

**Retire** revokes the registration and every credential it holds, for every account, in one transaction:

```bash
leapmux control admin app revoke --client-id <client_id>
```

**Delete** removes a registration that never held a credential. The Hub refuses it otherwise and says how many credentials exist, so you can retire instead. A revoked credential still counts: it is history that the account's own list shows, and deleting the app it belonged to would leave that history pointing at nothing.

Two registrations ship with the Hub — the control CLI and the service account that holds administrator-issued credentials. Neither can be edited, retired, or deleted, because their fields are constants of the build. Their elevation setting is the one thing that moves, so an operator who does not want `leapmux control admin ...` to elevate can say so.

## Solo mode

A solo Hub authorizes apps like any other. Reaching the port is the authentication there, so a request with no credential is the solo account with no limits — but a request that presents a credential asks to be its **app**, and the Hub binds that credential's permissions instead.

That is what makes the model useful on one machine: hand a local agent a credential with `file:read` and it reads files, nothing else, and the Worker enforces it inside the encrypted channel where the Hub cannot see.

## See also

- [Connected Apps](/docs/using/connected-apps/) — the account holder's side
- [OAuth API](/docs/reference/oauth-api/) — the wire contract
- [Control CLI](/docs/operating/control-cli/) — `leapmux control admin app ...`
- [Security & Threat Model](/docs/operating/security/) — session elevation and the trust boundaries
