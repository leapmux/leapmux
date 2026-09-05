-- name: EnsureAgentInputQueueState :exec
INSERT INTO agent_input_queue_state (agent_id) VALUES (?)
ON CONFLICT(agent_id) DO NOTHING;

-- name: GetAgentInputQueueState :one
SELECT * FROM agent_input_queue_state WHERE agent_id = ?;

-- name: ListAgentInputQueueItems :many
SELECT * FROM agent_input_queue_items
WHERE agent_id = ?
ORDER BY order_index ASC;

-- name: ListAgentInputQueueAttachments :many
SELECT * FROM agent_input_queue_attachments
WHERE item_id = ?
ORDER BY position ASC;

-- name: ReserveAgentMessageSeq :one
UPDATE agents
SET message_seq_hwm = message_seq_hwm + 1
WHERE id = ?
RETURNING message_seq_hwm;

-- name: CreateMessageAtReservedSeq :exec
INSERT INTO messages (
  id, agent_id, seq, source, content, content_compression, input_fingerprint, depth, span_id,
  parent_span_id, span_type, span_lines, span_color, agent_provider,
  mark_type, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
