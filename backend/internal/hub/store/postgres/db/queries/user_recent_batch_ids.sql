-- name: GetRecentBatchID :one
SELECT * FROM user_recent_batch_ids WHERE user_id = $1 AND batch_id = $2;

-- name: InsertRecentBatchID :exec
INSERT INTO user_recent_batch_ids (
    user_id, batch_id, body_hash, principal_id,
    canonical_physical_ms, canonical_logical, canonical_client,
    op_count, epoch, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: DeleteExpiredRecentBatchIDs :execresult
DELETE FROM user_recent_batch_ids WHERE expires_at < $1;
