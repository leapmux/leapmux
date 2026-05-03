-- name: CreateMessage :one
INSERT INTO messages (id, agent_id, seq, source, content, content_compression, depth, span_id, parent_span_id, span_type, span_lines, span_color, agent_provider, created_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(agent_id),
  (SELECT COALESCE(MAX(m.seq), 0) + 1 FROM messages m WHERE m.agent_id = sqlc.arg(agent_id)),
  sqlc.arg(source),
  sqlc.arg(content),
  sqlc.arg(content_compression),
  sqlc.arg(depth),
  sqlc.arg(span_id),
  sqlc.arg(parent_span_id),
  sqlc.arg(span_type),
  sqlc.arg(span_lines),
  sqlc.arg(span_color),
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
SELECT EXISTS(SELECT 1 FROM messages m JOIN agents a ON m.agent_id = a.id WHERE m.agent_id = ? AND m.source = 1 AND m.seq > a.session_start_seq) AS has_messages;

-- name: DeleteMessageByAgentAndID :exec
DELETE FROM messages WHERE id = ? AND agent_id = ?;
