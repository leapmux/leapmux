-- Clock rule: expires_at columns are written by the hub process, so every
-- comparison of them binds the hub's clock (sqlc.arg(now)), never the
-- database clock. Timestamps that record when something happened
-- (created_at, updated_at) keep the database clock.

-- name: CreateWebAuthnSession :exec
INSERT INTO webauthn_sessions (id, kind, user_id, payload_json, session_data, expires_at, created_at)
VALUES (
    sqlc.arg(id),
    sqlc.arg(kind),
    sqlc.narg(user_id),
    sqlc.arg(payload_json),
    sqlc.arg(session_data),
    sqlc.arg(expires_at),
    sqlc.arg(created_at)
);

-- name: GetWebAuthnSession :one
SELECT * FROM webauthn_sessions WHERE id = $1;

-- name: DeleteWebAuthnSession :exec
DELETE FROM webauthn_sessions WHERE id = $1;

-- name: ConsumeWebAuthnCeremonySession :execresult
DELETE FROM webauthn_sessions
WHERE id = sqlc.arg(id) AND kind = sqlc.arg(kind) AND expires_at > sqlc.arg(now);

-- name: DeleteWebAuthnSessionsByUser :exec
DELETE FROM webauthn_sessions WHERE user_id = $1;

-- name: DeleteWebAuthnSessionsByUserAndKind :exec
DELETE FROM webauthn_sessions WHERE user_id = $1 AND kind = $2;

-- name: DeleteExpiredWebAuthnSessions :execresult
DELETE FROM webauthn_sessions WHERE expires_at < sqlc.arg(now);
