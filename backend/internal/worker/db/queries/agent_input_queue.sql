-- The queue snapshot reads live in inputqueue/store.go. They truncate text
-- previews in SQL and read attachment metadata without loading its content.

-- name: EnsureAgentInputQueueState :exec
INSERT INTO agent_input_queue_state (agent_id) VALUES (?)
ON CONFLICT(agent_id) DO NOTHING;

-- name: ReserveAgentMessageSeq :one
UPDATE agents
SET message_seq_hwm = message_seq_hwm + 1
WHERE id = ?
RETURNING message_seq_hwm;

-- name: HasQueuedAgentInput :one
SELECT EXISTS(
    SELECT 1 FROM agent_input_queue_items
    WHERE agent_id = ? AND state = ? AND removed_at IS NULL
);
