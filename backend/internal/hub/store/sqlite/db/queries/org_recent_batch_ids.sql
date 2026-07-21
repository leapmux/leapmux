-- name: GetRecentBatchID :one
SELECT * FROM org_recent_batch_ids WHERE org_id = ? AND batch_id = ?;

-- name: InsertRecentBatchID :exec
INSERT INTO org_recent_batch_ids (
    org_id, batch_id, body_hash, principal_id,
    canonical_physical_ms, canonical_logical, canonical_client,
    op_count, epoch, expires_at
) VALUES (
    sqlc.arg(org_id),
    sqlc.arg(batch_id),
    sqlc.arg(body_hash),
    sqlc.arg(principal_id),
    sqlc.arg(canonical_physical_ms),
    sqlc.arg(canonical_logical),
    sqlc.arg(canonical_client),
    sqlc.arg(op_count),
    sqlc.arg(epoch),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(expires_at))
);

-- name: DeleteExpiredRecentBatchIDs :execresult
-- Raw compare: expires_at is stored canonical (InsertRecentBatchID wraps the
-- bound instant in strftime), and the Go side binds a formatSQLiteTime-
-- formatted cutoff (CAST AS TEXT -> string param), so the lexicographic < is
-- byte-exact and sargable for idx_org_recent_batch_ids_expires.
DELETE FROM org_recent_batch_ids WHERE expires_at < CAST(sqlc.arg(before) AS TEXT);
