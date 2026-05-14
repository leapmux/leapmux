-- name: GetOrgState :one
SELECT * FROM org_state WHERE org_id = ?;

-- name: UpsertOrgState :exec
INSERT INTO org_state (org_id, state_payload, current_epoch, epoch_started_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    state_payload    = VALUES(state_payload),
    current_epoch    = VALUES(current_epoch),
    epoch_started_at = VALUES(epoch_started_at),
    updated_at       = VALUES(updated_at);

-- name: AdvanceOrgEpoch :exec
UPDATE org_state
SET current_epoch    = sqlc.arg(epoch),
    epoch_started_at = sqlc.arg(epoch_started_at),
    updated_at       = sqlc.arg(updated_at)
WHERE org_id = ?;
