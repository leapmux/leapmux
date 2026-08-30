---
title: "OAuth API"
description: "The wire contract for LeapMux's OAuth 2.1 authorization server: endpoints, grant types, PKCE, scopes, refresh rotation, and revocation."
type: docs
weight: 6
---

This is the implementable specification for a program that wants access to an account on a LeapMux Hub. For the administrator's side — registering apps, verifying them, controlling what they may ask for — see [App Authorization](/docs/admin/app-authorization/).

A Hub is an OAuth 2.1 authorization server. It follows RFC 6749 and RFC 6750 as OAuth 2.1 profiles them, with RFC 7636 (PKCE), RFC 8628 (device authorization), RFC 7009 (revocation), RFC 7591 (dynamic registration), RFC 8414 and RFC 9728 (metadata).

## Discovery

Fetch the metadata before anything else. It is anonymous, and everything below is derived from it rather than hard-coded.

```
GET /.well-known/oauth-authorization-server
GET /.well-known/oauth-protected-resource
```

The authorization-server document lists the endpoints, the grant types the Hub serves, and `code_challenge_methods_supported`, which is always exactly `["S256"]`. `registration_endpoint` is **absent** while open registration is off, which is the default — so a client library that reads it correctly does not attempt dynamic registration on a Hub that refuses it.

## Client registration

Every request specifies a `client_id`. There is no anonymous client and no client that registers itself by accident.

- An administrator or a user registers the app; see [App Authorization](/docs/admin/app-authorization/).
- Or, where the Hub allows it, the app registers itself through `POST /oauth/register` (RFC 7591).

A **public** client holds no secret. That is the honest shape for a binary a user holds — a CLI, a desktop app, a browser app — and PKCE is what protects it. A **confidential** client holds a secret, which means a server the app administrator runs. The secret crosses once, at registration.

An app's name is what a consent screen states, and its icon is stored at registration and served same-origin from `/oauth/apps/<client_id>/icon` — a consent page fetches nothing from the app's own servers.

## Redirect addresses

A redirect address must match one the app registered, exactly, with one exception: for a **loopback** address (`127.0.0.1` or `localhost`) the **port is ignored**, per RFC 8252 section 7.3. A native app binds whatever port is free at launch, so the registered address carries no port and any port matches. An IPv6 loopback literal such as `[::1]` is refused at registration: the consent page's content security policy cannot state an IPv6 host, so the login would hang. Use `127.0.0.1` or `localhost`.

Everything else is compared literally. No prefix match, no wildcard host, no scheme substitution.

## Authorization code with PKCE

The flow for anything that can open a browser.

```
GET /oauth/authorize
  ?client_id=<id>
  &response_type=code
  &redirect_uri=<registered address>
  &state=<opaque, required>
  &code_challenge=<BASE64URL(SHA256(verifier))>
  &code_challenge_method=S256
  &scope=<space-delimited, optional>
  &installation_name=<label for this copy of the app, optional>
```

`state` is **required**, not recommended: a client that omits it cannot tell its own callback from one an attacker triggered. `code_challenge_method` must be `S256`; `plain` is not a challenge at all.

An **omitted** `scope` is the app's registered ceiling minus every `admin:` permission. An admin permission must be specified.

The account holder sees a consent screen — served by `/oauth/authorize` itself, and answering through `POST /oauth/consent`, its only submit target. Both are browser-facing pages a client library never calls directly; they are listed here because a proxy or a content policy in front of the Hub must not block them.

Either way the browser returns to `redirect_uri`:

```
<redirect_uri>?code=<one-shot code>&state=<echoed>
<redirect_uri>?error=access_denied&error_description=...&state=<echoed>
```

An error travels back to the app once the `client_id` and the address are both valid. Before that — an unknown client, an unregistered address — the Hub renders its own page and redirects nowhere, because redirecting to an address it did not verify is an open redirect.

Exchange the code within {{< duration authorization-code-ttl >}}:

```
POST /oauth/token
  grant_type=authorization_code
  client_id=<id>
  code=<code>
  code_verifier=<the verifier>
  redirect_uri=<the same address>
```

`redirect_uri` must be present and identical to the one the authorization used. Everything else comes from the stored grant — the app, the permissions, the label — and nothing from this request, so holding a code cannot widen what the account approved.

**A code is one-shot.** Presenting it twice means it leaked, so the second presentation is refused *and* the credential the first one minted is revoked (RFC 6749 section 4.1.2).

## Device authorization

The flow for a machine with no browser: a headless server, an SSH session, a container.

```
POST /oauth/device-authorization
  client_id=<id>
  scope=<space-delimited, optional>
  installation_name=<label, optional>
```

The response carries `device_code`, `user_code`, `verification_uri`, `verification_uri_complete`, `expires_in` and `interval`. Show the user code and the URL; the person opens it on any device they can sign in on. `verification_uri` is the Hub's own page at `/oauth/device`, where the code is typed or confirmed — never the token endpoint, which a machine polls.

`verification_uri_complete` carries the user code and nothing else. The permissions the app asked for live in the grant the Hub stored, so a rewritten link cannot ask somebody to approve one thing while the grant records another. A grant that reaches the hub-administration family answers the first **Authorize** with a confirmation page and binds nothing until the second: the typed-code flow authorizes a machine the person cannot see, so the highest-stakes ask takes a deliberate second stop.

Poll the token endpoint no faster than `interval` seconds:

```
POST /oauth/token
  grant_type=urn:ietf:params:oauth:grant-type:device_code
  client_id=<id>
  device_code=<device_code>
```

| Body | Meaning |
|---|---|
| `authorization_pending` | Nobody answered yet. Keep polling. |
| `slow_down` | You polled too fast. Add five seconds and continue. |
| `access_denied` | Somebody refused. Final. |
| `expired_token` | The grant timed out after {{< duration device-code-ttl >}}. Start again. |

An **absent** decision on the consent form is a refusal, and it is final: a grant somebody denied can never become approved.

## The token response

```json
{
  "access_token": "lmx_a...",
  "refresh_token": "lmx_a...",
  "token_type": "Bearer",
  "expires_in": 3600,
  "refresh_expires_in": 7776000,
  "scope": "workspace:read worker:read file:read",
  "token_id": "...",
  "user_id": "...",
  "username": "..."
}
```

`scope` is what the credential **reaches**, with its implications closed — so it may be **wider** than what was asked for, and `file:read` comes back with `worker:read` beside it. It is never wider in a way the consent screen did not show.

Reaching is the account's consent intersected with the app's **registered ceiling**, and the Hub applies that intersection at every request rather than only at the consent. So an owner who removes a permission from a registration takes it from the credentials the app already holds, and this field says so at the next refresh. The consent itself is not rewritten: put the permission back on the registration and the credential reaches it again, with no fresh authorization.

Present the access token as `Authorization: Bearer <access_token>`.

## Refreshing

```
POST /oauth/token
  grant_type=refresh_token
  client_id=<id>
  refresh_token=<refresh_token>
  scope=<optional, to NARROW>
```

The refresh token **rotates**: each exchange returns a new pair and retires the old one. Presenting a retired refresh token after a short grace window means it leaked, so the Hub revokes the whole credential (RFC 6749 section 6).

An optional `scope` may only narrow. A refresh cannot widen a grant, because the account holder is not there to approve it — and it is measured against what the credential **reaches**, so asking for a permission the app's registration no longer lists answers `invalid_scope` rather than succeeding and failing at the next call.

Two deadlines apply. A credential refreshes for {{< duration refresh-token >}} from its last rotation, and it stops entirely {{< duration absolute-cap >}} after the consent that created it. The second is why an `invalid_grant` reading *this credential reached its maximum lifetime* means the app must be authorized again rather than retried.

## Revocation

```
POST /oauth/revoke
  token=<access or refresh token>
  client_id=<id>                    # always for a public client
  client_secret=<secret>            # or HTTP Basic, for a confidential one
```

The client is authenticated before anything else, per RFC 7009 section 2.1. A **confidential** client sends its secret — HTTP Basic or the body, the two forms RFC 6749 section 2.3.1 defines — and a **public** client identifies itself with its `client_id`. In both cases only the app a credential was issued to may end it, so one app cannot tear down another's installations. The token itself must also be presented in full: an identifier alone revokes nothing.

Per RFC 7009 section 2.2, the answer is `200` for a token that was revoked **and** for one that was already invalid — an invalid token is not an error a client can act on. That uniformity is also the non-disclosure: the response separates nothing, so the endpoint cannot be used to discover which token identifiers are live.

A missing `token` parameter is the one shape refusal: `400 invalid_request`. A client that cannot be authenticated, or one that is not the credential's own, is refused before the token is read.

## Errors

Every token-endpoint error is `400` with an RFC 6749 section 5.2 body, except `invalid_client`, which is `401`. The distinction is one a client library acts on: `401` says the client's own credentials are wrong, and `400` says the grant is finished.

```json
{ "error": "invalid_grant", "error_description": "code expired or already consumed" }
```

The Hub puts no client's command in a description. One endpoint serves every registered app, so a remedy that made sense for one would be noise to the rest; the standard code plus the fact is what travels, and each client renders its own remedy.

## Step-up

Some actions need a recently proven factor. A credential that lacks one is refused with `FAILED_PRECONDITION` and a marker header; the app then runs:

```
POST /oauth/step-up
  Authorization: Bearer <access_token>
```

This opens a device-style ceremony against the **existing** credential. It issues nothing. The account holder proves a factor in a browser, and the credential gains a window; the app polls `/oauth/token` with the device-code grant to learn when.

An app is refused this ceremony unless its owner allowed it. See [App Authorization](/docs/admin/app-authorization/#elevation).

## Rate limits

The three anonymous endpoints — device authorization, the token endpoint, and dynamic registration — share one per-address budget. Nothing there presents a secret somebody had to guess, so no error counts against a failure window; the limit is a ceiling on a loop, not a lockout. An administrator adjusts it as `rate_limit.oauth_anonymous`.

## See also

- [App Authorization](/docs/admin/app-authorization/) — registering and verifying apps
- [Connected Apps](/docs/using/connected-apps/) — what an account holder sees
- [Control CLI](/docs/using/control-cli/) — a worked client, and the one that ships
