-- name: StoreControlRequest :one
-- Repeated announcements keep the current claim inside one runtime owner.
-- A changed owner, payload, or session starts a new instance.
-- Only non-blocking control requests persist in SQLite.
INSERT INTO control_requests (agent_id, agent_session_id, request_id, payload, claim_token, source_seq)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, request_id) DO UPDATE SET
    source_seq = CASE WHEN control_requests.claim_token = excluded.claim_token AND control_requests.payload = excluded.payload AND control_requests.agent_session_id = excluded.agent_session_id AND control_requests.source_seq > 0
        THEN control_requests.source_seq ELSE excluded.source_seq END,
    payload = excluded.payload,
    claim_token = excluded.claim_token,
    agent_session_id = excluded.agent_session_id
RETURNING claim_token, source_seq, agent_session_id;

-- name: CancelControlRequest :one
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ? RETURNING *;

-- name: DeleteControlRequestInstance :one
DELETE FROM control_requests WHERE agent_id = ? AND request_id = ? AND claim_token = ? RETURNING *;

-- name: DeleteObservedControlRequest :one
-- Recording an answer must leave a replacement request intact.
DELETE FROM control_requests
WHERE agent_id = sqlc.arg(agent_id) AND request_id = sqlc.arg(request_id) AND claim_token = sqlc.arg(claim_token)
AND payload = COALESCE(CAST(sqlc.arg(request_payload) AS BLOB), X'')
AND agent_session_id = sqlc.arg(agent_session_id)
RETURNING *;

-- name: DeleteControlRequestsByAgentID :many
DELETE FROM control_requests WHERE agent_id = ? RETURNING *;

-- name: DeleteNonBlockingControlRequestsOutsideSession :many
-- A delayed retirement must not delete the questions of a newer session.
DELETE FROM control_requests
WHERE control_requests.agent_id = sqlc.arg(agent_id)
    AND control_requests.agent_session_id <> sqlc.arg(agent_session_id)
    AND (SELECT agent_session_id FROM agents WHERE id = sqlc.arg(agent_id)) = sqlc.arg(agent_session_id)
RETURNING *;

-- name: ListControlRequestsByAgentID :many
SELECT * FROM control_requests WHERE agent_id = ? ORDER BY created_at ASC;

-- name: GetControlRequest :one
SELECT * FROM control_requests WHERE agent_id = ? AND request_id = ?;

-- name: GetControlRequestClaim :one
SELECT claim_token FROM control_requests WHERE agent_id = ? AND request_id = ?;
