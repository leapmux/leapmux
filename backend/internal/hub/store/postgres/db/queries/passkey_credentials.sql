-- name: CreatePasskeyCredential :exec
INSERT INTO passkey_credentials (
    id, user_id, credential_id, public_key, sign_count, aaguid,
    backup_eligible, backup_state, transports, friendly_name, key_version, created_at, last_used_at
) VALUES (
    sqlc.arg(id),
    sqlc.arg(user_id),
    sqlc.arg(credential_id),
    sqlc.arg(public_key),
    sqlc.arg(sign_count),
    sqlc.narg(aaguid),
    sqlc.arg(backup_eligible),
    sqlc.arg(backup_state),
    sqlc.arg(transports),
    sqlc.arg(friendly_name),
    sqlc.arg(key_version),
    sqlc.arg(created_at),
    sqlc.narg(last_used_at)
);

-- name: GetPasskeyCredentialByID :one
SELECT * FROM passkey_credentials WHERE id = $1;

-- name: GetPasskeyCredentialByCredentialID :one
SELECT * FROM passkey_credentials WHERE credential_id = $1;

-- name: ListPasskeyCredentialsByUser :many
SELECT * FROM passkey_credentials WHERE user_id = $1 ORDER BY created_at;

-- name: CountPasskeyCredentialsByUser :one
SELECT COUNT(*) FROM passkey_credentials WHERE user_id = $1;

-- name: UpdatePasskeySignCount :execresult
UPDATE passkey_credentials
SET sign_count = sqlc.arg(sign_count), last_used_at = sqlc.arg(last_used_at)
WHERE credential_id = sqlc.arg(credential_id) AND user_id = sqlc.arg(user_id)
  AND (sign_count < sqlc.arg(sign_count) OR sqlc.arg(sign_count) = 0);

-- name: UpdatePasskeyFriendlyName :exec
UPDATE passkey_credentials
SET friendly_name = sqlc.arg(friendly_name)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: UpdatePasskeyPublicKey :exec
UPDATE passkey_credentials
SET public_key = sqlc.arg(public_key), key_version = sqlc.arg(key_version)
WHERE id = sqlc.arg(id) AND user_id = sqlc.arg(user_id);

-- name: DeletePasskeyCredential :exec
DELETE FROM passkey_credentials WHERE id = $1 AND user_id = $2;

-- name: DeleteAllPasskeyCredentialsByUser :exec
DELETE FROM passkey_credentials WHERE user_id = $1;

-- name: ListPasskeyCredentialsByKeyVersion :many
SELECT * FROM passkey_credentials WHERE key_version = $1;

-- name: CountPasskeyCredentialsByKeyVersion :one
SELECT COUNT(*) FROM passkey_credentials WHERE key_version = $1;
