-- name: CreateMessage :one
-- seq is allocated from the agent's monotonic high-water (message_seq_hwm + 1),
-- NOT MAX(live seq) + 1, so a deleted tail seq is never reused. The agent row is
-- guaranteed to exist (messages.agent_id REFERENCES agents); the COALESCE is a
-- defensive fallback. A trigger advances message_seq_hwm after the insert.
INSERT INTO messages (id, agent_id, seq, source, content, content_compression, depth, span_id, parent_span_id, span_type, span_lines, span_color, agent_provider, created_at)
VALUES (
  sqlc.arg(id),
  sqlc.arg(agent_id),
  (COALESCE((SELECT a.message_seq_hwm FROM agents a WHERE a.id = sqlc.arg(agent_id)), 0) + 1),
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

-- GetAgentMessageBySpanIDAndSource finds the first message that opened the
-- given span (the tool_use / item-started side). Used by the to-do extractor
-- when a tool_result arrives and needs the paired request's input fields
-- (subject/description/activeForm for Claude TaskCreate).
-- name: GetAgentMessageBySpanIDAndSource :one
SELECT * FROM messages
WHERE agent_id = ? AND span_id = ? AND source = ?
ORDER BY seq ASC
LIMIT 1;

-- name: SetMessageDeliveryError :exec
UPDATE messages SET delivery_error = ? WHERE id = ? AND agent_id = ?;

-- name: UpdateNotificationThread :one
-- Reseq moves a consolidated notification row to the tail. Like CreateMessage it
-- allocates from the monotonic high-water (message_seq_hwm + 1), so the row's new
-- seq is strictly above every prior seq and the freed old seq is never reused. A
-- trigger advances message_seq_hwm after the update.
UPDATE messages
SET content = sqlc.arg(content),
    content_compression = sqlc.arg(content_compression),
    span_lines = sqlc.arg(span_lines),
    seq = (COALESCE((SELECT a.message_seq_hwm FROM agents a WHERE a.id = sqlc.arg(agent_id)), 0) + 1)
WHERE messages.id = sqlc.arg(id) AND messages.agent_id = sqlc.arg(agent_id)
RETURNING seq;

-- name: GetLatestMessageByAgentID :one
SELECT * FROM messages WHERE agent_id = ? ORDER BY seq DESC LIMIT 1;

-- name: HasUserMessages :one
SELECT EXISTS(SELECT 1 FROM messages m JOIN agents a ON m.agent_id = a.id WHERE m.agent_id = ? AND m.source = 1 AND m.seq > a.session_start_seq) AS has_messages;

-- name: DeleteMessageByAgentAndID :one
DELETE FROM messages WHERE id = ? AND agent_id = ?
RETURNING seq;

-- name: GetMaxSeqByAgentID :one
-- The agent's highest message seq, or 0 when it has none. Used after a delete to
-- report the authoritative new live-tail seq to windowed watchers. The CAST pins
-- the result to int64 so sqlc doesn't infer interface{} for COALESCE(MAX(...)).
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) AS max_seq FROM messages WHERE agent_id = ?;
