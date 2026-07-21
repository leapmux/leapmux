-- name: InsertRevocationEvent :exec
INSERT INTO revocation_events (
    id, kind, subject_id, user_id, revoked_at, user_auth_generation
) VALUES (
    sqlc.arg(id),
    sqlc.arg(kind),
    sqlc.arg(subject_id),
    sqlc.arg(user_id),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(revoked_at)),
    sqlc.arg(user_auth_generation)
);

-- name: LockRevocationEventSequence :one
UPDATE revocation_event_sequence
SET last_seq = last_seq
WHERE id = 1
RETURNING last_seq;

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
-- idx_revocation_events_pending (partial index on seq IS NULL).
SELECT EXISTS(SELECT 1 FROM revocation_events WHERE seq IS NULL);

-- name: InsertHubRuntimeLease :exec
INSERT INTO hub_runtime_lease (singleton_id, holder_id, cursor_seq, lease_expires_at)
VALUES (
    1,
    sqlc.arg(holder_id),
    sqlc.arg(cursor_seq),
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now', printf('+%f seconds', CAST(sqlc.arg(lease_millis) AS REAL) / 1000.0))
);

-- name: RenewHubRuntimeLease :execresult
UPDATE hub_runtime_lease
SET cursor_seq = sqlc.arg(cursor_seq),
    lease_expires_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', printf('+%f seconds', CAST(sqlc.arg(lease_millis) AS REAL) / 1000.0))
WHERE singleton_id = 1
  AND holder_id = sqlc.arg(holder_id)
  -- Raw compare: lease_expires_at is written canonical on both write paths
  -- (Insert/Renew both SET strftime('%Y-%m-%dT%H:%M:%fZ', ...)), so the
  -- liveness guard is millisecond-exact against the same canonical layout.
  AND lease_expires_at > strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  AND cursor_seq <= sqlc.arg(cursor_seq)
  AND sqlc.arg(cursor_seq) <= (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
  AND CAST(sqlc.arg(lease_millis) AS INTEGER) > 0;

-- name: DeleteHubRuntimeLease :execresult
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND holder_id = sqlc.arg(holder_id);

-- name: DeleteExpiredHubRuntimeLease :execresult
DELETE FROM hub_runtime_lease WHERE singleton_id = 1 AND lease_expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: DeleteCompactablePublishedRevocationEvents :execresult
-- Raw compare: published_at is written canonical on its only write path (the
-- publish UPDATE sets strftime('%Y-%m-%dT%H:%M:%fZ','now')), and the Go side
-- binds a formatSQLiteTime-formatted cutoff (CAST AS TEXT -> string param),
-- so the lexicographic < is byte-exact at full millisecond precision --
-- matching what postgres/mysql compact, with no strftime re-normalization per
-- row. Unlike a function wrap on the column, this is sargable: the partial
-- idx_revocation_events_published(published_at, seq) serves an upper-bounded
-- SEARCH (the `ev.seq <= ...` term implies seq IS NOT NULL, satisfying the
-- partial predicate).
DELETE FROM revocation_events
WHERE id IN (
    SELECT ev.id
    FROM revocation_events AS ev
    WHERE ev.published_at < CAST(sqlc.arg(cutoff) AS TEXT)
      AND ev.seq <= COALESCE(
          (SELECT cursor_seq FROM hub_runtime_lease WHERE singleton_id = 1),
          (SELECT last_seq FROM revocation_event_sequence WHERE id = 1)
      )
    ORDER BY ev.seq ASC
    LIMIT 1000
);
