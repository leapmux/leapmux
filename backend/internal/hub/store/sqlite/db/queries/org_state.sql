-- name: GetOrgState :one
SELECT * FROM org_state WHERE org_id = ?;

-- name: UpsertOrgState :exec
-- The DO UPDATE reuses the strftime-transformed excluded values, so both the
-- insert and update paths store the canonical layout.
INSERT INTO org_state (org_id, state_payload, current_epoch, epoch_started_at, updated_at)
VALUES (
    sqlc.arg(org_id),
    sqlc.arg(state_payload),
    sqlc.arg(current_epoch),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(epoch_started_at)),
    strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(updated_at))
)
ON CONFLICT (org_id) DO UPDATE SET
    state_payload    = excluded.state_payload,
    current_epoch    = excluded.current_epoch,
    epoch_started_at = excluded.epoch_started_at,
    updated_at       = excluded.updated_at;

-- name: AdvanceOrgEpoch :exec
-- Bumps current_epoch + epoch_started_at without rewriting the (multi-MB)
-- state_payload. Called by the manager's epoch-rotation timer every 14d.
-- Every parameter must be named with sqlc.arg(): the previous bare `?` in the
-- WHERE clause alongside sqlc.arg() SET params made sqlc emit numbered
-- placeholders (?2..?4) plus an un-numbered trailing `?`, which modernc counts
-- as index 5 -- every call failed with "missing argument with index 5".
UPDATE org_state
SET current_epoch    = sqlc.arg(epoch),
    epoch_started_at = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(epoch_started_at)),
    updated_at       = strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.arg(updated_at))
WHERE org_id = sqlc.arg(org_id);
