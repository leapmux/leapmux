-- name: GetOrgState :one
SELECT * FROM org_state WHERE org_id = ?;

-- name: UpsertOrgState :exec
INSERT INTO org_state (org_id, state_payload, current_epoch, epoch_started_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (org_id) DO UPDATE SET
    state_payload    = excluded.state_payload,
    current_epoch    = excluded.current_epoch,
    epoch_started_at = excluded.epoch_started_at,
    updated_at       = excluded.updated_at;

-- name: AdvanceOrgEpoch :exec
-- Bumps current_epoch + epoch_started_at without rewriting the (multi-MB)
-- state_payload. Called by the manager's epoch-rotation timer every 14d.
UPDATE org_state
SET current_epoch    = sqlc.arg(epoch),
    epoch_started_at = sqlc.arg(epoch_started_at),
    updated_at       = sqlc.arg(updated_at)
WHERE org_id = ?;
