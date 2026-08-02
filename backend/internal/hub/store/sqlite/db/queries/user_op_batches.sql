-- name: InsertUserOpBatch :exec
INSERT INTO user_op_batches (
    user_id, physical_ms, logical, last_logical, origin_client,
    principal_id, batch_id, body_hash, batch_payload, transitions_payload, op_count, epoch
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListUserOpBatchesAfter :many
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
SELECT * FROM user_op_batches
WHERE user_id = sqlc.arg(user_id)
  AND (physical_ms > sqlc.arg(after_physical_ms)
       OR (physical_ms = sqlc.arg(after_physical_ms)
           AND (logical > sqlc.arg(after_logical)
                OR (logical = sqlc.arg(after_logical)
                    AND origin_client > sqlc.arg(after_origin_client)))))
ORDER BY physical_ms, logical, origin_client
LIMIT sqlc.arg(row_limit);

-- name: DeleteUserOpBatchesThrough :exec
-- Boundary alignment: this delete keys on the batch's LAST canonical HLC
-- (physical_ms, last_logical, origin_client) with a `<=` test, while
-- crdt.Manager.decideResume admits a resume cursor with a strict `>` test
-- against op_retention_watermark. The floor from laggedRetentionWatermark
-- always carries logical=0 (the lag case), so a batch whose last op is at
-- (floor.physical, last_logical>=1) survives deletion here AND a cursor at
-- that batch passes resume -- the two boundaries stay consistent. A floor
-- with a non-zero logical would delete a batch the resume predicate still
-- admits, opening a silent gap in the client's delta. TestLaggedRetentionWatermark_ZerosLogicalForLagCase
-- pins the zero-logical invariant.
DELETE FROM user_op_batches
WHERE user_id = ?
  AND (physical_ms < sqlc.arg(through_physical_ms)
       OR (physical_ms = sqlc.arg(through_physical_ms)
           AND (last_logical < sqlc.arg(through_logical)
                OR (last_logical = sqlc.arg(through_logical)
                    AND origin_client <= sqlc.arg(through_origin_client)))));

-- name: CountUserOpBatches :one
SELECT COUNT(*) FROM user_op_batches WHERE user_id = ?;

-- name: DeleteUserOpBatchesBeforePhysical :execresult
-- Retention backstop for op batches, across ALL users.
--
-- Manager.maybeCompact deletes by HLC, but it only runs while a user is
-- committing: once compaction_watermark catches up to max_hlc its tick
-- short-circuits, so a dormant account keeps its final OpRetentionTTL of
-- batches (and their transitions_payload record snapshots) forever. This
-- sweep drains that tail on the shared cleanup schedule.
--
-- The cutoff is an HLC physical, NOT the committed_at wall clock, so this
-- deletes exactly the set decideResume refuses. The two are NOT
-- interchangeable even though HLC physical is Unix epoch-ms: Clock.Tick
-- clamps physical monotonically and Clock.Observe re-seeds it from the
-- persisted state, so after a backward clock correction physical runs
-- permanently ahead of wall time -- and committed_at is assigned by the DB
-- server, a different machine under Postgres/MySQL. Sweeping on committed_at
-- would then delete rows whose HLC a live cursor still passes, and
-- ListUserOpBatchesAfter would silently return the surviving later rows,
-- shipping a delta with a hole in it. Batched via LIMIT; the caller drains
-- until a pass deletes nothing. The LIMIT is a hard literal rather than a bind,
-- and the batching FORM is the one thing the three dialects genuinely diverge on
-- (rowid here, the full primary key in postgres, a bare DELETE ... LIMIT in
-- mysql); each names its own reason, and storetest covers all three with a
-- >LIMIT seed so a form that stopped honouring it cannot pass.
--
-- TWO floors bound this delete and BOTH are load-bearing:
--   1. cutoff_physical_ms -- the retention window (now - OpRetentionTTL).
--   2. user_state.compaction_physical_ms -- how far that user's state_payload
--      has actually absorbed.
--
-- (2) is not redundant. Bootstrap rebuilds a user as state_payload + every
-- batch ABOVE compaction_watermark, and state_payload is rewritten only by
-- Manager.maybeCompact's 60s tick: Manager.Stop does no final pass, and
-- maybeCompact returns without advancing on a CompactBatch error. So a hub
-- restart, an idle eviction, or one transient write error within 60s of a
-- user's last commit leaves compaction_watermark BELOW max_hlc, with that tail
-- living only in this table. Sweeping on the retention window alone would drop
-- those rows once the account went dormant past the TTL, and the next Bootstrap
-- would silently replay a short tail -- the user's last edits gone for good.
-- The per-user DeleteUserOpBatchesThrough never had this hazard because its
-- floor is clamped to compaction_watermark and written in the same transaction
-- as the state blob; this predicate is how the cross-user sweep earns the same
-- bound.
--
-- Strict < against compaction_physical_ms is deliberate, and its cost is real.
-- The watermark carries a logical component this column does not, so a batch AT
-- the boundary physical may be only PARTLY absorbed: ops at or below the
-- watermark's logical are in state_payload, ops committed later in that same
-- millisecond are not, and nothing in the row tells the two apart. Deleting it
-- could drop the only copy of those later ops, so it is kept.
--
-- Kept FOREVER, though, not "swept on a later pass" -- for a dormant user there
-- is no later pass that would reach it. maybeCompact only advances
-- compaction_watermark while the user is committing, and its tick short-circuits
-- once the watermark reaches max_hlc, so an idle account's floor stops moving and
-- nothing at or above it is ever eligible again. That ballast is intentional and
-- bounded per user: the batches sharing that one physical millisecond, plus any
-- tail a restart or a failed CompactBatch left unabsorbed. It drains the moment
-- the user commits again and compaction advances past it. Trading it away by
-- relaxing to <= would delete unabsorbed ops, which is silent data loss --
-- Bootstrap would replay a short tail and raise no error.
--
-- A user with no user_state row has never compacted, so the correlated subquery
-- yields NULL, the comparison is NULL, and none of their batches are deleted --
-- which is correct, since every one of them is unabsorbed.
DELETE FROM user_op_batches WHERE rowid IN (
    SELECT b.rowid FROM user_op_batches b
    WHERE b.physical_ms < sqlc.arg(cutoff_physical_ms)
      AND b.physical_ms < (SELECT us.compaction_physical_ms FROM user_state us WHERE us.user_id = b.user_id)
    LIMIT 1000
);
