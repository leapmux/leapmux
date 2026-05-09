-- name: InsertOrgOpBatch :exec
INSERT INTO org_op_batches (
    org_id, physical_ms, logical, last_logical, origin_client,
    principal_id, batch_id, body_hash, batch_payload, op_count, epoch
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListOrgOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain. `row_limit` is required (use a large value
-- to effectively disable paging).
--
-- Every parameter must be named with sqlc.arg(). Mixing positional `?`
-- with sqlc.arg() in SQLite makes sqlc emit numbered placeholders
-- (`?3`, `?4`, ...) while keeping the trailing `LIMIT ?` un-numbered;
-- SQLite then assigns that `?` an index one greater than the largest
-- already in the statement, leaving a gap that the generated Go code
-- never binds (see https://www.sqlite.org/lang_expr.html#parameters).
SELECT * FROM org_op_batches
WHERE org_id = sqlc.arg(org_id)
  AND (physical_ms > sqlc.arg(after_physical_ms)
       OR (physical_ms = sqlc.arg(after_physical_ms)
           AND (logical > sqlc.arg(after_logical)
                OR (logical = sqlc.arg(after_logical)
                    AND origin_client > sqlc.arg(after_origin_client)))))
ORDER BY physical_ms, logical, origin_client
LIMIT sqlc.arg(row_limit);

-- name: DeleteOrgOpBatchesThrough :exec
DELETE FROM org_op_batches
WHERE org_id = ?
  AND (physical_ms < sqlc.arg(through_physical_ms)
       OR (physical_ms = sqlc.arg(through_physical_ms)
           AND (last_logical < sqlc.arg(through_logical)
                OR (last_logical = sqlc.arg(through_logical)
                    AND origin_client <= sqlc.arg(through_origin_client)))));

-- name: CountOrgOpBatches :one
SELECT COUNT(*) FROM org_op_batches WHERE org_id = ?;
