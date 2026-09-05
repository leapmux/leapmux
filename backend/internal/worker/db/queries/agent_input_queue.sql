-- Only the statements that inputqueue/store.go actually calls live here, so
-- sqlc's schema check covers running code and nothing else. The queue's reads
-- are deliberately absent: snapshotTx truncates the text preview in SQL and
-- joins every item's attachment metadata in one statement, and a generated
-- SELECT * cannot express either. A query added here that no caller uses is a
-- second spelling of a statement store.go already holds, and the two drift.

-- name: EnsureAgentInputQueueState :exec
INSERT INTO agent_input_queue_state (agent_id) VALUES (?)
ON CONFLICT(agent_id) DO NOTHING;

-- name: ReserveAgentMessageSeq :one
UPDATE agents
SET message_seq_hwm = message_seq_hwm + 1
WHERE id = ?
RETURNING message_seq_hwm;
