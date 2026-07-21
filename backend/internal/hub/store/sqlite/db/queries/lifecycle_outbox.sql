-- name: InsertLifecycleOutbox :exec
INSERT INTO lifecycle_outbox (org_id, op_type, payload)
VALUES (?, ?, ?);

-- name: ListPendingLifecycleOutbox :many
-- Paged scan so a wedged outbox cannot OOM the dispatcher; callers
-- iterate to drain. `limit` is required (use a large value to
-- effectively disable paging).
SELECT * FROM lifecycle_outbox
WHERE org_id = ? AND consumed_at IS NULL
ORDER BY id
LIMIT ?;

-- name: MarkLifecycleOutboxConsumed :exec
UPDATE lifecycle_outbox
SET consumed_at = sqlc.arg(consumed_at)
WHERE id = sqlc.arg(id);

-- name: DeleteConsumedLifecycleOutboxBefore :execresult
-- Raw compare: consumed_at is stored canonical (MarkLifecycleOutboxConsumed
-- binds a SQLiteTime), and the Go side binds a SQLiteTime cutoff (same
-- canonical layout), so the lexicographic < is byte-exact.
DELETE FROM lifecycle_outbox WHERE consumed_at IS NOT NULL AND consumed_at < sqlc.arg(before);
