-- name: GetRecentBatchID :one
SELECT * FROM user_recent_batch_ids WHERE user_id = $1 AND batch_id = $2;

-- name: InsertRecentBatchID :exec
INSERT INTO user_recent_batch_ids (
    user_id, batch_id, body_hash, principal_id,
    canonical_physical_ms, canonical_logical,
    op_count, epoch, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: DeleteExpiredRecentBatchIDs :execresult
-- A batch identity survives while its committed epoch is still accepted.
-- Scope cleanup to one owner: another manager may have a different epoch.
DELETE FROM user_recent_batch_ids
WHERE user_id = sqlc.arg(user_id)
  AND expires_at < sqlc.arg(before)
  AND epoch < sqlc.arg(oldest_accepted_epoch);
