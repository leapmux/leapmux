-- name: CreateControlRequest :exec
-- A re-store of an existing (agent_id, request_id) row refreshes BOTH the payload and the claim_token
-- (a re-issued id is a NEW instance and must mint a fresh token), so a stale duplicate of the prior
-- instance can no longer match the current instance's answer claim.
INSERT INTO control_requests (agent_id, request_id, payload, claim_token) VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, request_id) DO UPDATE SET payload = excluded.payload, claim_token = excluded.claim_token;

-- name: DeleteControlRequest :exec
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ?;

-- name: DeleteControlRequestsByAgentID :many
DELETE FROM control_requests WHERE agent_id = ? RETURNING request_id;

-- name: ListControlRequestsByAgentID :many
SELECT * FROM control_requests WHERE agent_id = ? ORDER BY created_at ASC;

-- name: GetControlRequest :one
SELECT * FROM control_requests WHERE agent_id = ? AND request_id = ?;
