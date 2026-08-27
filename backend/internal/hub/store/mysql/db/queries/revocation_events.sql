-- name: InsertRevocationEvent :exec
INSERT INTO revocation_events (
    id, kind, subject_id, user_id, revoked_at, user_auth_generation
) VALUES (?, ?, ?, ?, ?, ?);

-- name: RevocationNow :one
SELECT NOW(3);

-- name: SessionRevokedEventExists :one
-- Was this session taken away by an administrator, rather than signed out?
-- The two paths delete the same row and leave the account's auth generation
-- alone, so this event row is the only durable fact that separates them.
-- Pending and published rows both count: the insert IS the fact.
--
-- Served by idx_revocation_events_session_revoked. MySQL has no partial
-- index, so it leads with kind instead: the one kind this asks about occupies
-- a contiguous range, and every event pays one entry. Without it this is a
-- full scan of the retention window's events, and it runs while the caller
-- holds the user-auth row lock. The same dialect split already applies to
-- idx_revocation_events_pending.
SELECT EXISTS(
    SELECT 1 FROM revocation_events
    WHERE subject_id = ? AND kind = 'session_revoked'
);

-- name: LockRevocationEventSequence :one
SELECT last_seq FROM revocation_event_sequence
WHERE id = 1
FOR UPDATE;

-- name: SetRevocationEventSequence :exec
UPDATE revocation_event_sequence
SET last_seq = ?
WHERE id = 1;

-- name: ListPublishedRevocationEventsAfter :many
SELECT
    seq, id, kind, subject_id, user_id, revoked_at,
    user_auth_generation, created_at, published_at
FROM revocation_events
WHERE seq > ?
ORDER BY seq ASC
LIMIT ?;

-- name: MaxPublishedRevocationEventSeq :one
SELECT last_seq FROM revocation_event_sequence WHERE id = 1;

-- name: HasPendingRevocationEvents :one
-- Cheap unpublished-events probe. The watcher reads this before opening a
-- publish write transaction so an idle Hub takes no writer lock. Served by
-- idx_revocation_events_pending; MySQL has no partial indexes, so the index
-- leads with seq and the seq IS NULL rows are seeked at its front (it also
-- spans published rows, unlike the sqlite/postgres partial equivalents).
SELECT EXISTS(SELECT 1 FROM revocation_events WHERE seq IS NULL);

-- name: InsertHubRuntimeLease :exec
INSERT INTO hub_runtime_lease (singleton_id, holder_id, cursor_seq, lease_expires_at)
VALUES (
    1,
    sqlc.arg(holder_id),
    sqlc.arg(cursor_seq),
    TIMESTAMPADD(MICROSECOND, sqlc.arg(lease_millis) * 1000, CURRENT_TIMESTAMP(6))
);

-- name: RenewHubRuntimeLease :execresult
UPDATE hub_runtime_lease
SET cursor_seq = sqlc.arg(cursor_seq),
    lease_expires_at = TIMESTAMPADD(MICROSECOND, sqlc.arg(lease_millis) * 1000, CURRENT_TIMESTAMP(6))
WHERE singleton_id = 1
  AND holder_id = sqlc.arg(holder_id)
  AND lease_expires_at > CURRENT_TIMESTAMP(6)
  AND cursor_seq <= sqlc.arg(cursor_seq)
  AND sqlc.arg(cursor_seq) <= (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
  AND sqlc.arg(lease_millis) > 0;

-- name: DeleteHubRuntimeLease :execresult
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND holder_id = sqlc.arg(holder_id);

-- name: DeleteExpiredHubRuntimeLease :execresult
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND lease_expires_at <= CURRENT_TIMESTAMP(6);

-- name: DeleteCompactablePublishedRevocationEvents :execresult
DELETE FROM revocation_events
WHERE id IN (
    SELECT id FROM (
        SELECT old_event.id
        FROM revocation_events AS old_event
        WHERE old_event.published_at < sqlc.arg(cutoff)
          AND old_event.seq <= COALESCE(
              (SELECT cursor_seq FROM hub_runtime_lease WHERE singleton_id = 1),
              (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
          )
        ORDER BY old_event.seq ASC
        LIMIT 1000
    ) old_events
);
