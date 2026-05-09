-- name: InsertOrgOpBatch :exec
INSERT INTO org_op_batches (
    org_id, physical_ms, logical, last_logical, origin_client,
    principal_id, batch_id, body_hash, batch_payload, op_count, epoch
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ListOrgOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain.
SELECT * FROM org_op_batches
WHERE org_id = $1
  AND (physical_ms > sqlc.arg(after_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(after_physical_ms)::bigint
           AND (logical > sqlc.arg(after_logical)::bigint
                OR (logical = sqlc.arg(after_logical)::bigint
                    AND origin_client > sqlc.arg(after_origin_client)::text))))
ORDER BY physical_ms, logical, origin_client
LIMIT sqlc.arg(row_limit)::integer;

-- name: DeleteOrgOpBatchesThrough :exec
DELETE FROM org_op_batches
WHERE org_id = $1
  AND (physical_ms < sqlc.arg(through_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(through_physical_ms)::bigint
           AND (last_logical < sqlc.arg(through_logical)::bigint
                OR (last_logical = sqlc.arg(through_logical)::bigint
                    AND origin_client <= sqlc.arg(through_origin_client)::text))));

-- name: CountOrgOpBatches :one
SELECT COUNT(*) FROM org_op_batches WHERE org_id = $1;
