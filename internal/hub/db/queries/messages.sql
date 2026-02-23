-- name: CreateMessage :one
INSERT INTO messages (id, agent_id, seq, role, content, content_compression, thread_id, created_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(agent_id),
  (SELECT COALESCE(MAX(m.seq), 0) + 1 FROM messages m WHERE m.agent_id = sqlc.arg(agent_id)),
  sqlc.arg(role),
  sqlc.arg(content),
  sqlc.arg(content_compression),
  sqlc.arg(thread_id),
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

-- name: GetMessageByAgentAndThreadID :one
SELECT * FROM messages WHERE agent_id = ? AND thread_id = ? AND role = 2 LIMIT 1;

-- name: UpdateMessageThread :one
UPDATE messages
SET content = sqlc.arg(content),
    content_compression = sqlc.arg(content_compression),
    seq = (SELECT COALESCE(MAX(m.seq), 0) + 1 FROM messages m WHERE m.agent_id = sqlc.arg(agent_id)),
    updated_at = sqlc.arg(updated_at)
WHERE messages.id = sqlc.arg(id) AND messages.agent_id = sqlc.arg(agent_id)
RETURNING seq;

-- name: GetLatestMessageByAgentID :one
SELECT * FROM messages WHERE agent_id = ? ORDER BY seq DESC LIMIT 1;

-- name: DeleteMessageByAgentAndID :exec
DELETE FROM messages WHERE id = ? AND agent_id = ?;
