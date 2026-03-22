-- name: CreateMessage :one
INSERT INTO messages (id, agent_id, seq, role, content, content_compression, depth, scope_id, thread_lines, agent_provider, created_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(agent_id),
  (SELECT COALESCE(MAX(m.seq), 0) + 1 FROM messages m WHERE m.agent_id = sqlc.arg(agent_id)),
  sqlc.arg(role),
  sqlc.arg(content),
  sqlc.arg(content_compression),
  sqlc.arg(depth),
  sqlc.arg(scope_id),
  sqlc.arg(thread_lines),
  sqlc.arg(agent_provider),
  sqlc.arg(created_at)
)
RETURNING seq;

-- name: ListMessagesByAgentID :many
SELECT * FROM messages
WHERE agent_id = ? AND seq > ?
ORDER BY seq ASC
LIMIT ?;

-- name: ListAllMessagesByAgentID :many
SELECT * FROM messages
WHERE agent_id = ? AND seq > ?
ORDER BY seq ASC;

-- name: ListMessagesByAgentIDReverse :many
SELECT * FROM messages
WHERE agent_id = ? AND seq < ?
ORDER BY seq DESC
LIMIT ?;

-- name: ListLatestMessagesByAgentID :many
SELECT * FROM messages
WHERE agent_id = ?
ORDER BY seq DESC
LIMIT ?;

-- name: GetMessageByAgentAndID :one
SELECT * FROM messages WHERE id = ? AND agent_id = ?;

-- name: SetMessageDeliveryError :exec
UPDATE messages SET delivery_error = ? WHERE id = ? AND agent_id = ?;

-- name: UpdateNotificationThread :one
UPDATE messages
SET content = sqlc.arg(content),
    content_compression = sqlc.arg(content_compression),
    seq = (SELECT COALESCE(MAX(m.seq), 0) + 1 FROM messages m WHERE m.agent_id = sqlc.arg(agent_id))
WHERE messages.id = sqlc.arg(id) AND messages.agent_id = sqlc.arg(agent_id)
RETURNING seq;

-- name: GetLatestMessageByAgentID :one
SELECT * FROM messages WHERE agent_id = ? ORDER BY seq DESC LIMIT 1;

-- name: HasUserMessages :one
SELECT EXISTS(SELECT 1 FROM messages WHERE agent_id = ? AND role = 1) AS has_messages;

-- name: DeleteMessageByAgentAndID :exec
DELETE FROM messages WHERE id = ? AND agent_id = ?;
