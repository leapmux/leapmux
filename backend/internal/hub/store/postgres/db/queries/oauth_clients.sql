-- Registered apps. Every statement here is owner-aware: an app with a
-- non-NULL owner_user_id is visible and authorizable ONLY to that user, and a
-- NULL one is hub-wide. The rule lives in the WHERE clause rather than in a
-- Go check the caller must remember.

-- name: CreateOAuthClient :exec
INSERT INTO oauth_clients (
    client_id, owner_user_id, created_by_user_id, secret_hash, client_name,
    icon_blob, icon_media_type, client_uri, redirect_uris, scopes, grant_types,
    elevation_allowed, registration_source, verified_at, verified_by_user_id
) VALUES (
    sqlc.arg(client_id),
    sqlc.narg(owner_user_id),
    sqlc.narg(created_by_user_id),
    sqlc.narg(secret_hash),
    sqlc.arg(client_name),
    sqlc.narg(icon_blob),
    sqlc.arg(icon_media_type),
    sqlc.arg(client_uri),
    sqlc.arg(redirect_uris),
    sqlc.arg(scopes),
    sqlc.arg(grant_types),
    sqlc.arg(elevation_allowed),
    sqlc.arg(registration_source),
    sqlc.narg(verified_at),
    sqlc.narg(verified_by_user_id)
);

-- name: GetOAuthClient :one
-- The row WHATEVER its state, revoked included. A revoked app must still be
-- readable: loadBearer joins it to refuse a live credential on a retired app,
-- and the disconnect surface has to state what it revoked.
--
-- The icon BYTES are absent on purpose: every reader of the full row needs at
-- most whether an icon exists, and the column holds up to 64 KiB that the
-- token endpoint, every consent page and every device poll would otherwise
-- drag per request. GetOAuthClientIcon serves the one reader that wants bytes.
SELECT client_id, owner_user_id, created_by_user_id, secret_hash, client_name,
       COALESCE(LENGTH(icon_blob), 0) AS icon_bytes, client_uri, redirect_uris,
       scopes, grant_types, elevation_allowed, registration_source,
       verified_at, verified_by_user_id, created_at, updated_at, revoked_at
FROM oauth_clients WHERE client_id = sqlc.arg(client_id);

-- name: GetOAuthClientIcon :one
-- The icon bytes, their media type, and the three facts that gate serving
-- them, for the /oauth/apps/<id>/icon asset
-- endpoint alone. Serving the bytes from the full-row Get would put a 64 KiB
-- blob on every token exchange and consent page that only needs the row's
-- other columns.
SELECT icon_blob, icon_media_type, verified_at, registration_source, revoked_at
FROM oauth_clients WHERE client_id = sqlc.arg(client_id);

-- name: ListOAuthClientsVisibleTo :many
-- Every app this user may authorize: their own private ones plus every
-- hub-wide one. Keyset on (created_at DESC, client_id DESC), riding
-- idx_oauth_clients_owner.
--
-- include_revoked widens the page to retired rows, which the "include retired"
-- listing asks for; the default keeps the live-only shape every authorize
-- surface reads. The explicit column list drops the icon bytes for the same
-- reason GetOAuthClient does.
SELECT client_id, owner_user_id, created_by_user_id, secret_hash, client_name,
       COALESCE(LENGTH(icon_blob), 0) AS icon_bytes, client_uri, redirect_uris,
       scopes, grant_types, elevation_allowed, registration_source,
       verified_at, verified_by_user_id, created_at, updated_at, revoked_at
FROM oauth_clients
WHERE (revoked_at IS NULL OR sqlc.arg(include_revoked))
  AND (owner_user_id IS NULL OR owner_user_id = sqlc.arg(user_id))
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND client_id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, client_id DESC
LIMIT sqlc.arg('limit');

-- name: ListOAuthClientsOwnedBy :many
-- The apps this user REGISTERED, which is a different question from the one
-- above: it excludes the hub-wide catalogue, so the "App registrations" row
-- shows what the user can edit rather than everything they can authorize.
SELECT client_id, owner_user_id, created_by_user_id, secret_hash, client_name,
       COALESCE(LENGTH(icon_blob), 0) AS icon_bytes, client_uri, redirect_uris,
       scopes, grant_types, elevation_allowed, registration_source,
       verified_at, verified_by_user_id, created_at, updated_at, revoked_at
FROM oauth_clients
WHERE (revoked_at IS NULL OR sqlc.arg(include_revoked))
  AND owner_user_id = sqlc.arg(user_id)
  AND (sqlc.narg(cursor_time)::timestamptz IS NULL
       OR created_at < sqlc.narg(cursor_time)::timestamptz
       OR (created_at = sqlc.narg(cursor_time)::timestamptz AND client_id < sqlc.narg(cursor_id)))
ORDER BY created_at DESC, client_id DESC
LIMIT sqlc.arg('limit');

-- name: UpdateOAuthClient :execresult
-- The owner guard is IN the statement, and it is the whole authorization: a
-- NULL owner_user_id row is editable by an administrator (caller_is_admin,
-- bound by the calling service), and an owned one only by its owner. A
-- read-then-write pair would leave a window in which the row changes hands
-- between the two.
--
-- Editing a redirect URI is the single most dangerous write on this surface --
-- it diverts an in-flight authorization code to an address the editor chose --
-- which is why the calling service demands an elevated credential for it.
--
-- A BUILTIN registration is excluded: its fields are constants of the build,
-- so an edit would put the row and internal/hub/oauthapp into disagreement.
UPDATE oauth_clients
SET client_name = sqlc.arg(client_name),
    client_uri = sqlc.arg(client_uri),
    redirect_uris = sqlc.arg(redirect_uris),
    scopes = sqlc.arg(scopes),
    grant_types = sqlc.arg(grant_types),
    elevation_allowed = sqlc.arg(elevation_allowed),
    updated_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND revoked_at IS NULL
  AND registration_source <> 'builtin'
  AND (
      (owner_user_id IS NULL AND sqlc.arg(caller_is_admin))
      OR owner_user_id = sqlc.arg(caller_user_id)
  );

-- name: SetOAuthClientElevationAllowed :execresult
-- The elevation flag alone, because it is the one field the app list toggles
-- inline -- and because it is the ONE field a built-in registration may still
-- change. An operator who does not want `leapmux control admin ...` to elevate
-- must be able to say so.
UPDATE oauth_clients
SET elevation_allowed = sqlc.arg(elevation_allowed),
    updated_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND revoked_at IS NULL
  AND (
      (owner_user_id IS NULL AND sqlc.arg(caller_is_admin))
      OR owner_user_id = sqlc.arg(caller_user_id)
  );

-- name: SetOAuthClientIcon :execresult
UPDATE oauth_clients
SET icon_blob = sqlc.narg(icon_blob),
    icon_media_type = sqlc.arg(icon_media_type),
    updated_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND revoked_at IS NULL
  AND registration_source <> 'builtin'
  AND (
      (owner_user_id IS NULL AND sqlc.arg(caller_is_admin))
      OR owner_user_id = sqlc.arg(caller_user_id)
  );

-- name: SetOAuthClientVerified :execresult
-- Only an administrator vouches, and the two columns move together so the
-- half-vouch CHECK can never be violated.
UPDATE oauth_clients
SET verified_at = sqlc.narg(verified_at),
    verified_by_user_id = sqlc.narg(verified_by_user_id),
    updated_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND revoked_at IS NULL;

-- name: RevokeOAuthClient :execresult
-- Revoke, not delete. It is the verb the surface offers, because a hard delete
-- of an app with live credentials is refused by the RESTRICT foreign key --
-- and because revocation is what the caller can cascade with each credential's
-- lifecycle effects.
UPDATE oauth_clients
SET revoked_at = NOW(),
    updated_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND revoked_at IS NULL
  AND registration_source <> 'builtin'
  AND (
      (owner_user_id IS NULL AND sqlc.arg(caller_is_admin))
      OR owner_user_id = sqlc.arg(caller_user_id)
  );

-- name: RevokeAPITokensForOAuthClient :execresult
-- The cascade of a disconnect or a revocation: every credential the app holds
-- for this user dies with it. An EMPTY user_id revokes across every user,
-- which is what retiring the app itself means.
UPDATE api_tokens
SET revoked_at = NOW()
WHERE client_id = sqlc.arg(client_id)
  AND (sqlc.arg(user_id) = '' OR user_id = sqlc.arg(user_id))
  AND revoked_at IS NULL;

-- name: ListAPITokenIDsForOAuthClient :many
-- The ids the cascade above will revoke, read BEFORE it runs so the caller can
-- apply each row's lifecycle effects (cache eviction, lease drain, channel
-- close) after the transaction commits. Lifecycle effects ACCUMULATE, so they
-- must not run inside a transaction the store may retry.
--
-- granted_scopes rides along, because a caller that is NARROWING the app's
-- registered ceiling rather than retiring it needs to know which of these
-- credentials actually loses something: a scope removed from the registration
-- costs a channel teardown only for the credentials that held it, and closing
-- every one of a hub-wide app's channels would be an outage for accounts whose
-- grant never reached the removed permission.
SELECT id, user_id, granted_scopes FROM api_tokens
WHERE client_id = sqlc.arg(client_id)
  AND (sqlc.arg(user_id) = '' OR user_id = sqlc.arg(user_id))
  AND revoked_at IS NULL;

-- name: CountLiveAPITokensForOAuthClient :one
SELECT COUNT(*) FROM api_tokens
WHERE client_id = sqlc.arg(client_id) AND revoked_at IS NULL;

-- name: CountLiveAPITokensForOAuthClients :many
-- The batched form of CountLiveAPITokensForOAuthClient, for a listing page:
-- one GROUP BY round trip answers every row's live count, where the
-- per-client form cost one query per row on a page that grows with the
-- number of registered apps.
SELECT client_id, COUNT(*) AS live_credentials FROM api_tokens
WHERE revoked_at IS NULL AND client_id = ANY(sqlc.slice(client_ids))
GROUP BY client_id;

-- name: CountAPITokensForOAuthClient :one
-- EVERY credential row, revoked ones included, because that is what the
-- RESTRICT foreign key counts. CountLiveAPITokensForOAuthClient answers a
-- different question -- what the app can still do -- and using it as the
-- delete precondition told an operator to revoke and then refused the delete
-- anyway, with a raw constraint error.
SELECT COUNT(*) FROM api_tokens
WHERE client_id = sqlc.arg(client_id);

-- name: DeleteEphemeralGrantsForOAuthClient :execresult
-- The one-shot artifacts of a flow, cleared so the delete below can proceed.
--
-- A device grant lives ten minutes and an authorization code one, and each is
-- consumed or abandoned; neither is history anybody reads. Without this an app
-- that ran a single abandoned device flow could never be deleted, and the
-- operator met a foreign-key error naming a table they have no surface for.
-- api_tokens is deliberately NOT here: a revoked credential IS history, and the
-- delete refuses while one exists.
DELETE FROM device_authorizations WHERE client_id = sqlc.arg(client_id);

-- name: DeleteAuthorizationCodesForOAuthClient :execresult
-- The sibling of DeleteEphemeralGrantsForOAuthClient; see its comment.
DELETE FROM oauth_authorization_codes WHERE client_id = sqlc.arg(client_id);

-- name: DeleteOAuthClient :execresult
-- The HARD delete, for an app that never held a credential. The RESTRICT
-- foreign key refuses it otherwise, which is the point: a delete that silently
-- orphaned credentials would be worse than a refusal.
DELETE FROM oauth_clients
WHERE client_id = sqlc.arg(client_id)
  AND registration_source <> 'builtin'
  AND (
      (owner_user_id IS NULL AND sqlc.arg(caller_is_admin))
      OR owner_user_id = sqlc.arg(caller_user_id)
  );
