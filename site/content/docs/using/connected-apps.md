---
title: "Connected Apps"
description: "See which apps hold access to your LeapMux account, exactly what each one can do, and disconnect any of them without a password prompt."
type: docs
weight: 9
---

An app you authorize holds a credential on your account until you disconnect it. **Preferences → Account → Connected apps** lists them, and each row says what that one can actually do.

## Authorizing an app

When a program needs access, it sends you to a consent screen the Hub renders. The screen states the app's name and lists every permission in a sentence — *Type into your terminals, which runs any command on your machine*, not `terminal:write`.

Read the warning before the list. **Nobody verified this app on this hub** means no administrator vouched for it, and the name below is the one the app claims for itself. An app you did not go looking for is one to deny.

Authorizing needs a recently proven factor, so the screen may ask for your password or passkey first. That proof lasts {{< duration elevation-window >}}, so a second authorization in the same sitting does not ask again.

**Allow** returns you to the app. **Deny** returns you to it too, with a refusal, so it stops waiting rather than hanging until the request expires.

## Reading the list

The list is **grouped by app**. Each block identifies one app, and under it is one row per machine that app runs on.

- **The app name** heads the block, with **unverified** beside it when no administrator vouched for it — the same label the consent screen shows.
- **The installation** identifies each row inside — *trustin's MacBook* — because one app holds one credential per machine, and the app name alone cannot tell two rows apart.
- **hub administration** marks a credential that can administer the whole Hub. It is read from the permissions themselves, so it cannot disagree with the list below it.
- **The permissions** are every one you granted, spelled out. Some are wider than they look: `terminal:write` runs commands, and `tunnel:open` reaches anything your machine can reach.

A row also says when the credential was last used and when it stops working.

## Ending an app's access

Two endings, because they answer different questions.

**Disconnect**, on the app's own line, ends your whole authorization of it. Every machine it runs on loses access at once, any channel it opened closes, and the app must be authorized again to come back. The confirmation says how many machines it covers, so the number is never something you have to count yourself.

**Revoke**, on one installation's row, ends that machine only. The app keeps working everywhere else. This is how you sign one laptop out.

Reach for **Disconnect** whenever the decision is about the app rather than about a machine. Ending one installation of an app you no longer trust leaves it working on every other one.

Neither asks for a password. Both only ever reduce access, and asking somebody who just realized an app is malicious to first find their password is the wrong way round — the delay is the attacker's gain.

## Registering your own app

**Preferences → Apps** registers an app of your own. Yours is private: nobody else sees it, and nobody else can authorize it. An administrator's app is visible to everybody on the Hub.

You choose a name, the addresses the authorization may return to, and the permissions the app may **ask** for. That last set is a ceiling, not a grant — the consent screen still asks, and you can grant less.

The ceiling is live. **Take a permission off a registration and every credential the app already holds loses it**, at that app's next request — you do not have to disconnect it and start again. Putting one back never grants it: the account still has to consent, so a credential reaches the overlap of what its owner agreed to and what the registration allows.

A **confidential** client is a server you run, and it gets a client secret shown once. A **public** client is a binary a user holds, and it has no secret at all — handing one to a program that cannot keep it would only look like protection.

Retiring an app revokes every credential it holds, for every account. An app that never held one can be deleted outright.

## Solo mode

A solo LeapMux authorizes apps like any other. Reaching it is your authentication, so ordinary use asks for nothing — but a program that presents a credential is bound by that credential's permissions.

That is worth using on one machine: give a local agent a credential with `file:read` and it reads files and does nothing else, however it is prompted.

## See also

- [Accounts & Authentication](/docs/using/accounts/) — passwords, passkeys, and proving a factor
- [App Authorization](/docs/operating/app-authorization/) — registering and verifying apps, for operators
- [Control CLI](/docs/operating/control-cli/) — the app that ships with LeapMux
