-- name: StoreControlRequest :one
-- Repeated announcements keep the current claim. A changed payload starts a new instance.
INSERT INTO control_requests (agent_id, agent_session_id, request_id, payload, claim_token, source_seq) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, request_id) DO UPDATE SET
    source_seq = CASE WHEN control_requests.payload = excluded.payload AND control_requests.agent_session_id = excluded.agent_session_id AND control_requests.source_seq > 0
        THEN control_requests.source_seq ELSE excluded.source_seq END,
    payload = excluded.payload,
    claim_token = CASE WHEN control_requests.payload = excluded.payload AND control_requests.agent_session_id = excluded.agent_session_id
        THEN control_requests.claim_token ELSE excluded.claim_token END,
    agent_session_id = excluded.agent_session_id
RETURNING claim_token, source_seq, agent_session_id;

-- name: CancelControlRequest :one
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ? RETURNING *;

-- name: DeleteControlRequestInstance :one
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ? AND claim_token = ? RETURNING *;

-- name: DeleteControlRequestsByAgentID :many
DELETE FROM control_requests WHERE agent_id = ? RETURNING *;

-- name: ListControlRequestsByAgentID :many
SELECT * FROM control_requests WHERE agent_id = ? ORDER BY created_at ASC;

-- name: GetControlRequest :one
SELECT * FROM control_requests WHERE agent_id = ? AND request_id = ?;
