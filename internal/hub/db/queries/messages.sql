-- name: CreateMessage :exec
INSERT INTO messages (id, agent_id, seq, role, content, content_compression, thread_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

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

-- name: GetMaxSeqByAgentID :one
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) AS max_seq FROM messages WHERE agent_id = ?;

-- name: GetMessageByAgentAndID :one
SELECT * FROM messages WHERE id = ? AND agent_id = ?;

-- name: SetMessageDeliveryError :exec
UPDATE messages SET delivery_error = ? WHERE id = ? AND agent_id = ?;

-- name: GetMessageByAgentAndThreadID :one
SELECT * FROM messages WHERE agent_id = ? AND thread_id = ? AND role = 2 LIMIT 1;

-- name: UpdateMessageThread :exec
UPDATE messages SET content = ?, content_compression = ?, seq = ?, updated_at = ? WHERE id = ? AND agent_id = ?;

-- name: GetLatestMessageByAgentID :one
SELECT * FROM messages WHERE agent_id = ? ORDER BY seq DESC LIMIT 1;

-- name: DeleteMessageByAgentAndID :exec
DELETE FROM messages WHERE id = ? AND agent_id = ?;
