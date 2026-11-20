-- name: GetInputReplyState :one
SELECT input.seq AS accepted_seq, CAST(COALESCE((
    SELECT reply.input_turn_outcome FROM messages AS reply
    WHERE reply.agent_id = input.agent_id
      AND reply.agent_session_id = input.agent_session_id
      AND reply.native_input_id = input.native_input_id
      AND reply.native_turn_id = binding.native_turn_id
      AND reply.native_input_id <> ''
      AND reply.seq > input.seq
      AND reply.input_turn_outcome IS NOT NULL
    ORDER BY reply.seq
    LIMIT 1
), binding.retired_outcome, 0) AS INTEGER) AS outcome,
EXISTS (
    SELECT 1 FROM control_requests AS prompt
    WHERE prompt.agent_id = input.agent_id
      AND prompt.agent_session_id = input.agent_session_id
      AND prompt.agent_session_id = (SELECT agent_session_id FROM agents WHERE id = input.agent_id)
      AND binding.native_turn_id <> ''
      AND prompt.native_turn_id = binding.native_turn_id
      AND prompt.non_blocking = 0
) AS waiting_for_user
FROM messages AS input
LEFT JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.idempotency_key GLOB sqlc.arg(input_key_pattern)
  AND input.native_input_id <> '';

-- name: ListInputReplyMessages :many
SELECT reply.*
FROM messages AS input
JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
    AND binding.native_turn_id <> ''
JOIN messages AS reply ON reply.agent_id = input.agent_id
    AND reply.agent_session_id = input.agent_session_id
    AND reply.native_input_id = input.native_input_id
    AND reply.native_turn_id = binding.native_turn_id
    AND reply.native_input_id <> ''
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.idempotency_key GLOB sqlc.arg(input_key_pattern)
  AND input.native_input_id <> ''
  AND reply.seq > input.seq
  AND reply.seq > sqlc.arg(cursor_seq)
  AND reply.seq <= COALESCE((
      SELECT MIN(ended.seq) FROM messages AS ended
      WHERE ended.agent_id = input.agent_id
        AND ended.agent_session_id = input.agent_session_id
        AND ended.native_input_id = input.native_input_id
        AND ended.native_turn_id = binding.native_turn_id
        AND ended.native_input_id <> ''
        AND ended.seq > input.seq
        AND ended.input_turn_outcome IS NOT NULL
  ), 9223372036854775807)
ORDER BY reply.seq
LIMIT sqlc.arg(page_size);

-- name: BindNativeInputTurn :exec
INSERT INTO native_agent_inputs (agent_id, agent_session_id, native_input_id, native_turn_id)
VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET native_turn_id = excluded.native_turn_id
WHERE native_agent_inputs.native_turn_id = '';

-- name: RetireNativeInputTurn :exec
INSERT INTO native_agent_inputs (agent_id, agent_session_id, native_input_id, retired_outcome)
VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET retired_outcome = excluded.retired_outcome
WHERE native_agent_inputs.retired_outcome IS NULL;

-- name: RetireNativeInputsAfterRestart :exec
INSERT INTO native_agent_inputs (agent_id, agent_session_id, native_input_id, retired_outcome)
SELECT input.agent_id, input.agent_session_id, input.native_input_id, sqlc.arg(unproved_outcome)
FROM messages AS input
WHERE input.native_input_id <> ''
  AND input.source = sqlc.arg(user_source)
  AND input.idempotency_key GLOB sqlc.arg(input_key_pattern)
  AND NOT EXISTS (
      SELECT 1 FROM messages AS ended
      JOIN native_agent_inputs AS expected ON expected.agent_id = ended.agent_id
          AND expected.agent_session_id = ended.agent_session_id
          AND expected.native_input_id = ended.native_input_id
          AND expected.native_turn_id = ended.native_turn_id
      WHERE ended.agent_id = input.agent_id
        AND ended.agent_session_id = input.agent_session_id
        AND ended.native_input_id = input.native_input_id
        AND ended.native_input_id <> ''
        AND ended.seq > input.seq
        AND ended.input_turn_outcome IS NOT NULL
  )
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET retired_outcome = excluded.retired_outcome
WHERE native_agent_inputs.retired_outcome IS NULL;
