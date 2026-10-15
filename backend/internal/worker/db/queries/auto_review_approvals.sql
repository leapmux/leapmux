-- name: StoreAutoReviewApproval :execrows
INSERT INTO auto_review_approval_payloads (delivery_id, message_id)
SELECT sqlc.arg(delivery_id), sqlc.arg(message_id)
WHERE EXISTS (SELECT 1 FROM messages WHERE id = sqlc.arg(message_id) AND agent_id = sqlc.arg(agent_id));

-- name: GetAutoReviewApproval :one
SELECT * FROM auto_review_approvals
WHERE agent_id = ? AND agent_session_id = ? AND review_id = ?;
