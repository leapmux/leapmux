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
SET consumed_at = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(consumed_at))
WHERE id = sqlc.arg(id);

-- name: DeleteConsumedLifecycleOutboxBefore :execresult
-- Raw compare: consumed_at is stored canonical (MarkLifecycleOutboxConsumed
-- wraps the bound instant in strftime), and the Go side binds a
-- formatSQLiteTime-formatted cutoff (CAST AS TEXT -> string param), so the
-- lexicographic < is byte-exact.
DELETE FROM lifecycle_outbox WHERE consumed_at IS NOT NULL AND consumed_at < CAST(sqlc.arg(before) AS TEXT);
