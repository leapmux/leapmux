-- name: GetUserState :one
SELECT * FROM user_state WHERE user_id = ?;

-- name: UpsertUserState :exec
-- compaction_physical_ms is written from the SAME crdt.UserCrdtState this
-- statement marshals into state_payload, so the projected column can never
-- name a watermark the blob does not carry.
INSERT INTO user_state (user_id, state_payload, compaction_physical_ms)
VALUES (
    sqlc.arg(user_id),
    sqlc.arg(state_payload),
    sqlc.arg(compaction_physical_ms)
)
ON CONFLICT (user_id) DO UPDATE SET
    state_payload    = excluded.state_payload,
    compaction_physical_ms = excluded.compaction_physical_ms;
