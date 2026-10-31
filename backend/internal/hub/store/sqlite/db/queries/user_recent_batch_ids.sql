-- name: GetRecentBatchID :one
SELECT * FROM user_recent_batch_ids WHERE user_id = ? AND batch_id = ?;

-- name: InsertRecentBatchID :exec
INSERT INTO user_recent_batch_ids (
    user_id, batch_id, body_hash, principal_id,
    canonical_physical_ms, canonical_logical,
    op_count, epoch, expires_at
) VALUES (
    sqlc.arg(user_id),
    sqlc.arg(batch_id),
    sqlc.arg(body_hash),
    sqlc.arg(principal_id),
    sqlc.arg(canonical_physical_ms),
    sqlc.arg(canonical_logical),
    sqlc.arg(op_count),
    sqlc.arg(epoch),
    sqlc.arg(expires_at)
);

-- name: DeleteExpiredRecentBatchIDs :execresult
-- A batch identity survives while its committed epoch is still accepted.
-- Scope cleanup to one owner: another manager may have a different epoch.
DELETE FROM user_recent_batch_ids
WHERE user_id = sqlc.arg(user_id)
  AND expires_at < sqlc.arg(before)
  AND epoch < sqlc.arg(oldest_accepted_epoch);
