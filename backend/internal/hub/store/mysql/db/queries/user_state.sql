-- name: GetUserState :one
SELECT * FROM user_state WHERE user_id = ?;

-- name: UpsertUserState :exec
-- compaction_physical_ms is written from the SAME crdt.UserCrdtState this
-- statement marshals into state_payload, so the projected column can never
-- name a watermark the blob does not carry.
INSERT INTO user_state (user_id, state_payload, compaction_physical_ms)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE
    state_payload    = VALUES(state_payload),
    compaction_physical_ms = VALUES(compaction_physical_ms);
