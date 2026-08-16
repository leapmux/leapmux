-- ConsumeAltchaSalt records a solved challenge's salt as used: 1 row =
-- first use accepted; the duplicate-key error the wrapper maps to 0
-- rows = replay denied. An ON DUPLICATE KEY UPDATE no-op cannot carry
-- that decision here: this connection runs with clientFoundRows (rows
-- matched, not changed), under which the duplicate reports as 1.
-- name: ConsumeAltchaSalt :execrows
INSERT INTO altcha_used_salts (salt, expires_at) VALUES (?, ?);

-- HasAltchaSalt reports, read-only, whether a salt row exists. The
-- verifier consults it BEFORE the memory-hard solution check so a
-- replayed payload costs one indexed read instead of a full key
-- derivation; ConsumeAltchaSalt stays the single-use authority.
-- name: HasAltchaSalt :one
SELECT EXISTS (SELECT 1 FROM altcha_used_salts WHERE salt = ?);

-- name: DeleteExpiredAltchaSalts :execrows
DELETE FROM altcha_used_salts WHERE expires_at < ?;
