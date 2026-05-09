-- name: GetRecentBatchID :one
SELECT * FROM org_recent_batch_ids WHERE org_id = ? AND batch_id = ?;

-- name: InsertRecentBatchID :exec
INSERT INTO org_recent_batch_ids (
    org_id, batch_id, body_hash, principal_id,
    canonical_physical_ms, canonical_logical, canonical_client,
    op_count, epoch, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteExpiredRecentBatchIDs :execresult
DELETE FROM org_recent_batch_ids WHERE expires_at < ?;
