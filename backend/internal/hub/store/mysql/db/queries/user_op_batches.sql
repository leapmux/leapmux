-- name: InsertUserOpBatch :exec
INSERT INTO user_op_batches (
    user_id, physical_ms, logical, last_logical, origin_client,
    principal_id, batch_id, body_hash, batch_payload, op_count, epoch
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListUserOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain.
SELECT * FROM user_op_batches
WHERE user_id = ?
  AND (physical_ms > sqlc.arg(after_physical_ms)
       OR (physical_ms = sqlc.arg(after_physical_ms)
           AND (logical > sqlc.arg(after_logical)
                OR (logical = sqlc.arg(after_logical)
                    AND origin_client > sqlc.arg(after_origin_client)))))
ORDER BY physical_ms, logical, origin_client
LIMIT ?;

-- name: DeleteUserOpBatchesThrough :exec
DELETE FROM user_op_batches
WHERE user_id = ?
  AND (physical_ms < sqlc.arg(through_physical_ms)
       OR (physical_ms = sqlc.arg(through_physical_ms)
           AND (last_logical < sqlc.arg(through_logical)
                OR (last_logical = sqlc.arg(through_logical)
                    AND origin_client <= sqlc.arg(through_origin_client)))));

-- name: CountUserOpBatches :one
SELECT COUNT(*) FROM user_op_batches WHERE user_id = ?;
