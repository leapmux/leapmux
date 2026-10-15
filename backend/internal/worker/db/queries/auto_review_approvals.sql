-- name: ClaimAutoReviewApproval :execrows
INSERT INTO auto_review_approvals (agent_id, agent_session_id, review_id, message_id, state)
SELECT sqlc.arg(agent_id), sqlc.arg(agent_session_id), sqlc.arg(review_id), sqlc.arg(message_id), sqlc.arg(state)
WHERE EXISTS (SELECT 1 FROM messages WHERE id = sqlc.arg(message_id) AND agent_id = sqlc.arg(agent_id))
ON CONFLICT (agent_id, agent_session_id, review_id) DO NOTHING;

-- name: GetAutoReviewApproval :one
SELECT * FROM auto_review_approvals
WHERE agent_id = ? AND agent_session_id = ? AND review_id = ?;

-- name: SetAutoReviewApprovalState :execrows
UPDATE auto_review_approvals SET state = sqlc.arg(state)
WHERE agent_id = sqlc.arg(agent_id) AND agent_session_id = sqlc.arg(agent_session_id)
AND review_id = sqlc.arg(review_id) AND state = sqlc.arg(required_state);

-- name: ReleaseAutoReviewApproval :exec
DELETE FROM auto_review_approvals
WHERE agent_id = ? AND agent_session_id = ? AND review_id = ? AND state = ?;
