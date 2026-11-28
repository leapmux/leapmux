-- name: GetInputReplyState :one
SELECT input.seq AS accepted_seq, CAST(COALESCE((
    SELECT reply.input_turn_outcome FROM messages AS reply
    WHERE reply.agent_id = input.agent_id
      AND reply.agent_session_id = input.agent_session_id
      AND reply.native_turn_id = binding.native_turn_id
      AND reply.native_turn_id <> ''
      AND (reply.native_input_id = input.native_input_id OR reply.native_input_id = '')
      AND reply.seq > input.seq
      AND reply.input_turn_outcome IS NOT NULL
    ORDER BY reply.seq
    LIMIT 1
), binding.retired_outcome, 0) AS INTEGER) AS outcome,
input.agent_session_id,
input.native_input_id,
CAST(COALESCE(binding.native_turn_id, '') AS TEXT) AS native_turn_id,
agent.agent_session_id AS current_agent_session_id
FROM messages AS input
JOIN agents AS agent ON agent.id = input.agent_id
LEFT JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.from_input_queue = 1
  AND input.native_input_id <> '';

-- name: ListInputReplyMessages :many
SELECT reply.*
FROM messages AS input
JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
    AND binding.native_turn_id <> ''
JOIN messages AS reply INDEXED BY idx_messages_native_turn ON reply.agent_id = input.agent_id
    AND reply.agent_session_id = input.agent_session_id
    AND reply.native_turn_id = binding.native_turn_id
    AND reply.native_turn_id <> ''
    AND (reply.native_input_id = input.native_input_id OR reply.native_input_id = '')
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.from_input_queue = 1
  AND input.native_input_id <> ''
  AND reply.seq > input.seq
  AND reply.seq > sqlc.arg(cursor_seq)
  AND reply.seq <= COALESCE((
      SELECT MIN(ended.seq) FROM messages AS ended
      WHERE ended.agent_id = input.agent_id
        AND ended.agent_session_id = input.agent_session_id
        AND ended.native_turn_id = binding.native_turn_id
        AND ended.native_turn_id <> ''
        AND (ended.native_input_id = input.native_input_id OR ended.native_input_id = '')
        AND ended.seq > input.seq
        AND ended.input_turn_outcome IS NOT NULL
  ), 9223372036854775807)
ORDER BY reply.seq
LIMIT sqlc.arg(page_size);

-- name: GetCanonicalInputReplyOutcome :one
SELECT published.outcome AS outcome, EXISTS (
    SELECT missing.ordinal FROM native_agent_reply_items AS missing
    WHERE missing.agent_id = published.agent_id
      AND missing.agent_session_id = published.agent_session_id
      AND missing.native_turn_id = published.native_turn_id
      AND missing.ordinal > binding.reply_after_ordinal
      AND missing.message_id IS NULL
) AS missing_items
FROM messages AS input
JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
JOIN native_agent_reply_turns AS published ON published.agent_id = binding.agent_id
    AND published.agent_session_id = binding.agent_session_id
    AND published.native_turn_id = binding.native_turn_id
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.from_input_queue = 1
  AND binding.reply_after_ordinal IS NOT NULL;

-- name: HasCanonicalInputReply :one
SELECT EXISTS (
    SELECT 1 FROM messages AS input
    JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
        AND binding.agent_session_id = input.agent_session_id
        AND binding.native_input_id = input.native_input_id
    WHERE input.agent_id = sqlc.arg(agent_id)
      AND input.id = sqlc.arg(input_id)
      AND input.source = sqlc.arg(user_source)
      AND input.from_input_queue = 1
      AND binding.reply_after_ordinal IS NOT NULL
);

-- name: ListCanonicalInputReplyMessages :many
SELECT reply.*
FROM messages AS input
JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
JOIN native_agent_reply_items AS member ON member.agent_id = binding.agent_id
    AND member.agent_session_id = binding.agent_session_id
    AND member.native_turn_id = binding.native_turn_id
    AND member.ordinal > binding.reply_after_ordinal
JOIN messages AS reply ON reply.id = member.message_id AND reply.agent_id = member.agent_id
    AND reply.agent_session_id = member.agent_session_id
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.from_input_queue = 1
  AND member.ordinal > sqlc.arg(cursor_ordinal)
ORDER BY member.ordinal
LIMIT sqlc.arg(page_size);

-- name: GetCanonicalInputReplyCursor :one
SELECT member.ordinal
FROM messages AS input
JOIN native_agent_inputs AS binding ON binding.agent_id = input.agent_id
    AND binding.agent_session_id = input.agent_session_id
    AND binding.native_input_id = input.native_input_id
JOIN native_agent_reply_items AS member ON member.agent_id = binding.agent_id
    AND member.agent_session_id = binding.agent_session_id
    AND member.native_turn_id = binding.native_turn_id
    AND member.ordinal > binding.reply_after_ordinal
WHERE input.agent_id = sqlc.arg(agent_id)
  AND input.id = sqlc.arg(input_id)
  AND input.source = sqlc.arg(user_source)
  AND input.from_input_queue = 1
  AND binding.reply_after_ordinal IS NOT NULL
  AND member.message_id = sqlc.arg(message_id);

-- name: PublishNativeReplyTurn :execrows
INSERT INTO native_agent_reply_turns (agent_id, agent_session_id, native_turn_id, outcome, reply_count)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, native_turn_id) DO NOTHING;

-- name: GetNativeReplyTurn :one
SELECT * FROM native_agent_reply_turns WHERE agent_id = ? AND agent_session_id = ? AND native_turn_id = ?;

-- name: PublishNativeReplyItem :execrows
INSERT INTO native_agent_reply_items (agent_id, agent_session_id, native_turn_id, ordinal, message_id)
VALUES (?, ?, ?, ?, ?);

-- name: GetNativeReplyItem :one
SELECT * FROM native_agent_reply_items WHERE agent_id = ? AND agent_session_id = ? AND native_turn_id = ? AND ordinal = ?;

-- name: AssignExclusiveReplyItem :execrows
UPDATE messages
SET native_turn_id = sqlc.arg(native_turn_id), native_input_id = ''
WHERE messages.id = sqlc.arg(id)
  AND messages.agent_id = sqlc.arg(agent_id)
  AND messages.agent_session_id = sqlc.arg(agent_session_id)
  AND NOT EXISTS (
      SELECT 1 FROM native_agent_reply_items AS member
      WHERE member.agent_id = messages.agent_id
        AND member.agent_session_id = messages.agent_session_id
        AND member.message_id = messages.id
        AND member.native_turn_id <> sqlc.arg(native_turn_id)
  );

-- name: PublishNativeReplyBoundary :execrows
INSERT INTO native_agent_inputs (agent_id, agent_session_id, native_input_id, native_turn_id, reply_after_ordinal)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET native_turn_id = excluded.native_turn_id, reply_after_ordinal = excluded.reply_after_ordinal
WHERE (native_agent_inputs.native_turn_id = '' OR native_agent_inputs.native_turn_id = excluded.native_turn_id)
  AND (native_agent_inputs.reply_after_ordinal IS NULL OR native_agent_inputs.reply_after_ordinal = excluded.reply_after_ordinal);

-- name: BindNativeInputTurn :execrows
INSERT INTO native_agent_inputs (agent_id, agent_session_id, native_input_id, native_turn_id)
VALUES (?, ?, ?, ?)
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET native_turn_id = excluded.native_turn_id
WHERE native_agent_inputs.native_turn_id = '' OR native_agent_inputs.native_turn_id = excluded.native_turn_id;

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
  AND input.from_input_queue = 1
  AND NOT EXISTS (
      SELECT 1 FROM native_agent_inputs AS completed
      WHERE completed.agent_id = input.agent_id
        AND completed.agent_session_id = input.agent_session_id
        AND completed.native_input_id = input.native_input_id
        AND completed.reply_after_ordinal IS NOT NULL
  )
  AND NOT EXISTS (
      SELECT 1 FROM messages AS ended
      JOIN native_agent_inputs AS expected ON expected.agent_id = ended.agent_id
          AND expected.agent_session_id = ended.agent_session_id
          AND expected.native_input_id = input.native_input_id
          AND expected.native_turn_id = ended.native_turn_id
      WHERE ended.agent_id = input.agent_id
        AND ended.agent_session_id = input.agent_session_id
        AND ended.native_turn_id <> ''
        AND (ended.native_input_id = input.native_input_id OR ended.native_input_id = '')
        AND ended.seq > input.seq
        AND ended.input_turn_outcome IS NOT NULL
  )
ON CONFLICT (agent_id, agent_session_id, native_input_id) DO UPDATE
SET retired_outcome = excluded.retired_outcome
WHERE native_agent_inputs.retired_outcome IS NULL;
