-- name: HasAgentTurnEnd :one
SELECT EXISTS (
    SELECT 1 FROM agent_turn_ends
    WHERE agent_id = ? AND agent_session_id = ? AND idempotency_key = ?
);

-- name: ClaimAgentTurnEnd :execrows
INSERT INTO agent_turn_ends (agent_id, agent_session_id, idempotency_key)
VALUES (?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, idempotency_key) DO NOTHING;

-- name: CloneAgentTurnEndsForResume :exec
INSERT INTO agent_turn_ends (agent_id, agent_session_id, idempotency_key)
SELECT sqlc.arg(target_agent_id), source.agent_session_id, source.idempotency_key
FROM agent_turn_ends AS source WHERE source.agent_id = sqlc.arg(source_agent_id);
