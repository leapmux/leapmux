-- ConsumeAltchaSalt records a solved challenge's salt as used. The
-- conflict no-op makes the call the single-use decision: 1 row = first
-- use accepted, 0 rows = replay denied.
-- name: ConsumeAltchaSalt :execrows
INSERT INTO altcha_used_salts (salt, expires_at) VALUES ($1, $2) ON CONFLICT (salt) DO NOTHING;

-- HasAltchaSalt reports, read-only, whether a salt row exists. The
-- verifier consults it BEFORE the memory-hard solution check so a
-- replayed payload costs one indexed read instead of a full key
-- derivation; ConsumeAltchaSalt stays the single-use authority.
-- name: HasAltchaSalt :one
SELECT EXISTS (SELECT 1 FROM altcha_used_salts WHERE salt = $1);

-- name: DeleteExpiredAltchaSalts :execrows
DELETE FROM altcha_used_salts WHERE expires_at < $1;
