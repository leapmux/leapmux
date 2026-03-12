-- name: CreateEmailVerification :exec
INSERT INTO email_verifications (id, user_id, token, expires_at) VALUES (?, ?, ?, ?);

-- name: GetEmailVerificationByToken :one
SELECT * FROM email_verifications WHERE token = ?;

-- name: DeleteEmailVerificationsByUserID :exec
DELETE FROM email_verifications WHERE user_id = ?;
