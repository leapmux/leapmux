-- name: InsertUserOpBatch :exec
INSERT INTO user_op_batches (
    user_id, physical_ms, logical, last_logical, origin_client,
    principal_id, batch_id, body_hash, batch_payload, op_count, epoch
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ListUserOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain.
SELECT * FROM user_op_batches
WHERE user_id = $1
  AND (physical_ms > sqlc.arg(after_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(after_physical_ms)::bigint
           AND (logical > sqlc.arg(after_logical)::bigint
                OR (logical = sqlc.arg(after_logical)::bigint
                    AND origin_client > sqlc.arg(after_origin_client)::text))))
ORDER BY physical_ms, logical, origin_client
LIMIT sqlc.arg(row_limit)::integer;

-- name: DeleteUserOpBatchesThrough :exec
DELETE FROM user_op_batches
WHERE user_id = $1
  AND (physical_ms < sqlc.arg(through_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(through_physical_ms)::bigint
           AND (last_logical < sqlc.arg(through_logical)::bigint
                OR (last_logical = sqlc.arg(through_logical)::bigint
                    AND origin_client <= sqlc.arg(through_origin_client)::text))));

-- name: CountUserOpBatches :one
SELECT COUNT(*) FROM user_op_batches WHERE user_id = $1;
