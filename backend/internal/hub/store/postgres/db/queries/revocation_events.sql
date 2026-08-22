-- name: InsertRevocationEvent :exec
INSERT INTO revocation_events (
    id, kind, subject_id, user_id, revoked_at, user_auth_generation
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: LockRevocationEventSequence :one
SELECT last_seq FROM revocation_event_sequence
WHERE id = 1
FOR UPDATE;

-- name: SetRevocationEventSequence :exec
UPDATE revocation_event_sequence
SET last_seq = $1
WHERE id = 1;

-- name: ListPublishedRevocationEventsAfter :many
SELECT
    seq, id, kind, subject_id, user_id, revoked_at,
    user_auth_generation, created_at, published_at
FROM revocation_events
WHERE seq > $1
ORDER BY seq ASC
LIMIT $2;

-- name: MaxPublishedRevocationEventSeq :one
SELECT last_seq FROM revocation_event_sequence WHERE id = 1;

-- name: HasPendingRevocationEvents :one
-- Cheap unpublished-events probe. The watcher reads this before opening a
-- publish write transaction so an idle Hub takes no writer lock. Served by
-- idx_revocation_events_pending (partial index on seq IS NULL).
SELECT EXISTS(SELECT 1 FROM revocation_events WHERE seq IS NULL);

-- name: InsertHubRuntimeLease :exec
INSERT INTO hub_runtime_lease (singleton_id, holder_id, cursor_seq, lease_expires_at)
VALUES (
    1,
    sqlc.arg(holder_id),
    sqlc.arg(cursor_seq),
    statement_timestamp() + CAST(sqlc.arg(lease_millis) AS BIGINT) * INTERVAL '1 millisecond'
);

-- name: RenewHubRuntimeLease :execrows
UPDATE hub_runtime_lease
SET cursor_seq = sqlc.arg(cursor_seq),
    lease_expires_at = statement_timestamp() + CAST(sqlc.arg(lease_millis) AS BIGINT) * INTERVAL '1 millisecond'
WHERE singleton_id = 1
  AND holder_id = sqlc.arg(holder_id)
  AND lease_expires_at > statement_timestamp()
  AND cursor_seq <= sqlc.arg(cursor_seq)
  AND sqlc.arg(cursor_seq) <= (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
  AND CAST(sqlc.arg(lease_millis) AS BIGINT) > 0;

-- name: DeleteHubRuntimeLease :execrows
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND holder_id = sqlc.arg(holder_id);

-- name: DeleteExpiredHubRuntimeLease :execrows
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND lease_expires_at <= statement_timestamp();

-- name: DeleteCompactablePublishedRevocationEvents :execrows
DELETE FROM revocation_events
WHERE revocation_events.id IN (
    SELECT old_event.id
    FROM revocation_events AS old_event
    WHERE old_event.published_at < sqlc.arg(cutoff)
      AND old_event.seq <= COALESCE(
          (SELECT cursor_seq FROM hub_runtime_lease WHERE singleton_id = 1),
          (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
      )
    ORDER BY old_event.seq ASC
    LIMIT 1000
);
