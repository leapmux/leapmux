-- name: InsertUserOpBatch :exec
INSERT INTO user_op_batches (
    user_id, physical_ms, logical, batch_id, batch_payload, transitions_payload, op_count
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListUserOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain. The cursor is compared by (physical_ms, logical)
-- alone; see the sqlite query (canonical home) for why that is exact.
SELECT * FROM user_op_batches
WHERE user_id = ?
  AND (physical_ms > sqlc.arg(after_physical_ms)
       OR (physical_ms = sqlc.arg(after_physical_ms)
           AND logical > sqlc.arg(after_logical)))
ORDER BY physical_ms, logical
LIMIT ?;

-- name: DeleteUserOpBatchesThrough :exec
-- Boundary alignment with decideResume's `>` test documented in the
-- sqlite query (canonical home). Keys on the batch's LAST canonical HLC,
-- (physical_ms, logical + op_count - 1).
DELETE FROM user_op_batches
WHERE user_id = ?
  AND (physical_ms < sqlc.arg(through_physical_ms)
       OR (physical_ms = sqlc.arg(through_physical_ms)
           AND (logical + op_count - 1) <= sqlc.arg(through_logical)));

-- name: CountUserOpBatches :one
SELECT COUNT(*) FROM user_op_batches WHERE user_id = ?;

-- name: DeleteUserOpBatchesBeforePhysical :execresult
-- Retention backstop for op batches, across ALL users. Both floors
-- (cutoff_physical_ms and user_state.compaction_physical_ms), why the cutoff is
-- an HLC physical rather than a wall-clock time, and why the compaction test is a
-- strict < are documented in the sqlite query (canonical home). The predicate
-- below must stay identical to it: a sweep that deletes a different set per
-- backend is exactly the failure that comment exists to prevent.
--
-- Written as a single-table DELETE with a correlated subquery rather than a
-- JOIN because MySQL rejects LIMIT in a multi-table DELETE. The DELETE form is
-- what differs here; the predicate is not.
DELETE FROM user_op_batches
WHERE physical_ms < sqlc.arg(cutoff_physical_ms)
  AND physical_ms < (SELECT us.compaction_physical_ms FROM user_state us WHERE us.user_id = user_op_batches.user_id)
LIMIT 1000;
