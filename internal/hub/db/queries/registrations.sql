-- name: CreateRegistration :exec
INSERT INTO worker_registrations (id, hostname, os, arch, version, home_dir, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetRegistrationByID :one
SELECT * FROM worker_registrations WHERE id = ?;

-- name: ApproveRegistration :exec
UPDATE worker_registrations
SET status = 2, worker_id = ?, approved_by = ?
WHERE id = ? AND status = 1;

-- name: ExpireRegistrations :exec
UPDATE worker_registrations
SET status = 4
WHERE status = 1 AND expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: CreateUserSession :exec
INSERT INTO user_sessions (id, user_id, expires_at) VALUES (?, ?, ?);

-- name: GetUserSessionByID :one
SELECT * FROM user_sessions WHERE id = ? AND expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteUserSession :exec
DELETE FROM user_sessions WHERE id = ?;

-- name: DeleteExpiredUserSessions :exec
DELETE FROM user_sessions WHERE expires_at < strftime('%Y-%m-%dT%H:%M:%fZ', 'now');
