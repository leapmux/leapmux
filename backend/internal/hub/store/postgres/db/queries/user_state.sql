-- name: GetUserState :one
SELECT * FROM user_state WHERE user_id = $1;

-- name: UpsertUserState :exec
INSERT INTO user_state (user_id, state_payload, current_epoch, epoch_started_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id) DO UPDATE SET
    state_payload    = EXCLUDED.state_payload,
    current_epoch    = EXCLUDED.current_epoch,
    epoch_started_at = EXCLUDED.epoch_started_at,
    updated_at       = EXCLUDED.updated_at;

-- name: AdvanceUserEpoch :exec
UPDATE user_state
SET current_epoch    = sqlc.arg(epoch)::bigint,
    epoch_started_at = sqlc.arg(epoch_started_at)::timestamptz,
    updated_at       = sqlc.arg(updated_at)::timestamptz
WHERE user_id = $1;
