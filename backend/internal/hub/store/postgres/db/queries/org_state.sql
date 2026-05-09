-- name: GetOrgState :one
SELECT * FROM org_state WHERE org_id = $1;

-- name: UpsertOrgState :exec
INSERT INTO org_state (org_id, state_payload, current_epoch, epoch_started_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (org_id) DO UPDATE SET
    state_payload    = EXCLUDED.state_payload,
    current_epoch    = EXCLUDED.current_epoch,
    epoch_started_at = EXCLUDED.epoch_started_at,
    updated_at       = EXCLUDED.updated_at;

-- name: AdvanceOrgEpoch :exec
UPDATE org_state
SET current_epoch    = sqlc.arg(epoch)::bigint,
    epoch_started_at = sqlc.arg(epoch_started_at)::timestamptz,
    updated_at       = sqlc.arg(updated_at)::timestamptz
WHERE org_id = $1;
