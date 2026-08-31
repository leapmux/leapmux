---
title: "Connected Apps"
description: "See which apps hold access to your LeapMux account, exactly what each one can do, and disconnect any of them without a password prompt."
type: docs
weight: 12
---

An app you authorize holds a credential on your account until you disconnect it. **Preferences → Apps → Connected apps** lists them — grouped by app, one row per machine — and each row says what that one can actually do. `leapmux control auth credentials` prints the same list from a terminal.

## Authorizing an app

When a program needs access, it sends you to a consent screen the Hub renders. The heading states the verdict, not the app's claim: **"Authorize {name}?"** for an app an administrator vouched for. Nobody vouched for the other kind, so its heading is **"Authorize an unverified app?"** and the name never enters it. An unverified app also shows a monogram of its name instead of an icon. The warning is plain:

> **Nobody verified this app on this hub.** It says its name is "{name}". Continue only if you started this yourself.

An app you did not go looking for is one to deny.

The screen says whose account it asks to use, and where it will return you — the redirect shown as a label, never a bare address.

The permission list shows every family the app reaches into, and each family in full: the permissions this app asks for are ticked, the rest are dimmed, and each carries its wire token beside a sentence in plain words — *Type into your terminals, which runs any command on your machine*, not `terminal:write`. An app that asks for nothing at all is one that could do nothing with your account, and the screen says that too.

Two rules guard the widest permissions. An app must **name** a hub-administration permission to ask for one — a request that omits its permission list never includes any — and only an administrator can grant it; anybody else's consent is refused outright. On the headless [device-code flow](/docs/using/control-cli/#leapmux-control-auth-login) a request that includes a hub-administration permission stops twice: the first **Authorize** returns the page with the caution stated beside it, and only the second one binds.

Authorizing needs a verified session, so the screen may ask you to prove a factor first — a password, a passkey, or a linked provider. That proof lasts {{< duration elevation-window >}}, so a second authorization in the same sitting does not ask again.

**Allow** returns you to the app. **Deny** returns you to it too, with a refusal, so it stops waiting rather than hanging until the request expires.

## Reading the list

The list is **grouped by app**. Each block identifies one app, and under it is one row per machine that app runs on.

- **The app name** heads the block, with an **unverified** badge beside it when no administrator vouched for it — the same verdict the consent screen states.
- **The installation** names each row — *trustin's MacBook* — because one app holds one credential per machine, and the app name alone cannot tell two rows apart.
- **hub administration** marks a credential that can administer the whole Hub.
- **The permissions** are the ones you granted, one chip each (`terminal:write`, `git:read`, …). Some are wider than they look: `terminal:write` runs commands, and `tunnel:open` reaches anything your machine can reach.

Each row also says when it was **last used** — or **added**, if it never was. A renewing credential says when its machine must **sign in again**. A fixed-lifetime one (minted by an administrator with a TTL) says when it **expires**. A row with neither never expires.

An empty list says how to start one: run `leapmux control auth login` to connect the command-line tool, or authorize an app from its own sign-in screen.

## Ending an app's access

Two endings, because they answer different questions.

**Disconnect**, on the app's own line, ends your whole authorization of it. Every machine it runs on loses access at once, any channel it opened closes, and the app must be authorized again to come back. The confirmation says how many machines it covers, so the number is never something you have to count yourself.

**Revoke**, on one installation's row, ends that machine only. The app keeps working everywhere else. This is how you sign one laptop out; the confirmation says so and points at **Disconnect** for the all-of-them ending.

Choose **Disconnect** whenever the decision is about the app rather than about a machine. Ending one installation of an app you no longer trust leaves it working on every other one.

Neither asks for a password. Both only ever reduce access. Somebody who just found an app is malicious should not have to find a password first; the delay helps the attacker.

## Registering your own app

**Preferences → Apps → App registrations** registers an app of your own. Yours is private: nobody else sees it, and nobody else can authorize it. An app registered under **Administration** is hub-wide: everybody on the Hub sees it and can authorize it.

You choose a name, the addresses the authorization may return to, and the permissions the app may **ask** for. That last set is a ceiling, not a grant — the consent screen still asks, and you can grant less.

The ceiling is live. **Take a permission off a registration and every credential the app already holds loses it**, at that app's next request — you do not have to disconnect it and start again. Putting one back never grants it: the account still has to consent, so a credential reaches the overlap of what its owner agreed to and what the registration allows.

A **confidential** client is a server you run, and it gets a client secret shown once. A **public** client is a binary a user holds, and it has no secret at all — handing one to a program that cannot keep it would only look like protection.

Retiring an app revokes every credential it holds, for every account. An app that never held one can be deleted outright.

## Solo mode

A solo LeapMux authorizes apps like any other. Reaching it is your authentication, so ordinary use asks for nothing — but a program that presents a credential is bound by that credential's permissions.

That is worth using on one machine: give a local agent a credential with `file:read` and it reads files and does nothing else, however it is prompted.

## See also

- [Accounts & Authentication](/docs/using/accounts/) — passwords, passkeys, and proving a factor
- [Settings & Preferences](/docs/using/settings/) — the Preferences dialog's **Apps** category
- [Control CLI](/docs/using/control-cli/) — the app that ships with LeapMux
- [App Authorization](/docs/admin/app-authorization/) — registering and verifying apps, for administrators
