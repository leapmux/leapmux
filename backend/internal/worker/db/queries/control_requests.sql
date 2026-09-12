-- name: StoreControlRequest :one
-- Repeated announcements keep the current claim. A changed payload starts a new instance.
INSERT INTO control_requests (agent_id, request_id, payload, claim_token, source_seq) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (agent_id, request_id) DO UPDATE SET
    source_seq = CASE WHEN control_requests.payload = excluded.payload AND control_requests.source_seq > 0
        THEN control_requests.source_seq ELSE excluded.source_seq END,
    payload = excluded.payload,
    claim_token = CASE WHEN control_requests.payload = excluded.payload
        THEN control_requests.claim_token ELSE excluded.claim_token END
RETURNING claim_token, source_seq;

-- name: DeleteControlRequest :exec
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ?;

-- name: DeleteControlRequestsByAgentID :many
DELETE FROM control_requests WHERE agent_id = ? RETURNING request_id;

-- name: ListControlRequestsByAgentID :many
SELECT * FROM control_requests WHERE agent_id = ? ORDER BY created_at ASC;

-- name: GetControlRequest :one
SELECT * FROM control_requests WHERE agent_id = ? AND request_id = ?;
