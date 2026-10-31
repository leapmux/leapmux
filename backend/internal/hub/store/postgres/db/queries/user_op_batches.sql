-- name: InsertUserOpBatch :exec
INSERT INTO user_op_batches (
    user_id, physical_ms, logical, batch_id, batch_payload, transitions_payload, op_count
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListUserOpBatchesAfter :many
-- Paged scan so a far-behind subscriber cannot OOM the broadcaster;
-- callers iterate to drain. The cursor is compared by (physical_ms, logical)
-- alone; see the sqlite query (canonical home) for why that is exact.
SELECT * FROM user_op_batches
WHERE user_id = $1
  AND (physical_ms > sqlc.arg(after_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(after_physical_ms)::bigint
           AND logical > sqlc.arg(after_logical)::bigint))
ORDER BY physical_ms, logical
LIMIT sqlc.arg(row_limit)::integer;

-- name: DeleteUserOpBatchesThrough :exec
-- Boundary alignment with decideResume's `>` test documented in the
-- sqlite query (canonical home). Keys on the batch's LAST canonical HLC,
-- (physical_ms, logical + op_count - 1).
DELETE FROM user_op_batches
WHERE user_id = $1
  AND (physical_ms < sqlc.arg(through_physical_ms)::bigint
       OR (physical_ms = sqlc.arg(through_physical_ms)::bigint
           AND (logical + op_count - 1) <= sqlc.arg(through_logical)::bigint));

-- name: CountUserOpBatches :one
SELECT COUNT(*) FROM user_op_batches WHERE user_id = $1;

-- name: DeleteUserOpBatchesBeforePhysical :execresult
-- Retention backstop for op batches, across ALL users. Both floors
-- (cutoff_physical_ms and user_state.compaction_physical_ms), why the cutoff is
-- an HLC physical rather than a wall-clock time, and why the compaction test is a
-- strict < are documented in the sqlite query (canonical home). The predicate
-- below must stay identical to it: a sweep that deletes a different set per
-- backend is exactly the failure that comment exists to prevent.
--
-- Batched by PRIMARY KEY, not ctid. This dialect is shared with YugabyteDB,
-- which rejects the system column outright ("system column ctid is not
-- supported yet", SQLSTATE 0A000) -- so a ctid-batched sweep compiles, ships,
-- and then fails at runtime on that backend, silently leaving op batches to
-- accumulate forever. The full primary key identifies a row just as precisely
-- and is portable. Only the Docker-backed integration suite
-- exercises YugabyteDB, which is why the difference is easy to miss.
DELETE FROM user_op_batches
WHERE (user_id, physical_ms, logical) IN (
    SELECT b.user_id, b.physical_ms, b.logical FROM user_op_batches b
    WHERE b.physical_ms < sqlc.arg(cutoff_physical_ms)
      AND b.physical_ms < (SELECT us.compaction_physical_ms FROM user_state us WHERE us.user_id = b.user_id)
    LIMIT 1000
);
