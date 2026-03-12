-- name: CreateControlRequest :exec
INSERT INTO control_requests (agent_id, request_id, payload) VALUES (?, ?, ?)
ON CONFLICT (agent_id, request_id) DO UPDATE SET payload = excluded.payload;

-- name: DeleteControlRequest :exec
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ?;

-- name: DeleteControlRequestsByAgentID :exec
DELETE FROM control_requests WHERE agent_id = ?;

-- name: ListControlRequestsByAgentID :many
SELECT * FROM control_requests WHERE agent_id = ? ORDER BY created_at ASC;

-- name: GetControlRequest :one
SELECT * FROM control_requests WHERE agent_id = ? AND request_id = ?;
