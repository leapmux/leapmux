-- name: InsertLifecycleOutbox :exec
INSERT INTO lifecycle_outbox (org_id, op_type, payload)
VALUES ($1, $2, $3);

-- name: ListPendingLifecycleOutbox :many
-- Paged scan so a wedged outbox cannot OOM the dispatcher; callers
-- iterate to drain.
SELECT * FROM lifecycle_outbox
WHERE org_id = $1 AND consumed_at IS NULL
ORDER BY id
LIMIT $2;

-- name: MarkLifecycleOutboxConsumed :exec
UPDATE lifecycle_outbox SET consumed_at = $1 WHERE id = $2;

-- name: DeleteConsumedLifecycleOutboxBefore :execresult
DELETE FROM lifecycle_outbox WHERE consumed_at IS NOT NULL AND consumed_at < $1;
