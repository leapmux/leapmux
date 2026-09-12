-- name: GetLatestMessageByAgentSpanAndSource :one
SELECT * FROM messages
WHERE agent_id = ? AND span_id = ? AND source = ? AND span_id <> ''
ORDER BY seq DESC
LIMIT 1;

-- name: EnrichMessageContent :one
-- Preserve the original content and reject concurrent changes to either source.
UPDATE messages
SET supplemental_content = COALESCE(CAST(sqlc.arg(supplemental_content) AS BLOB), X''),
    supplemental_content_compression = sqlc.arg(supplemental_content_compression),
    supplemental_revision = supplemental_revision + 1
WHERE id = sqlc.arg(id) AND agent_id = sqlc.arg(agent_id)
  AND content = sqlc.arg(original_content)
  AND content_compression = sqlc.arg(original_compression)
  AND supplemental_revision = sqlc.arg(previous_revision)
RETURNING *;

-- name: ListMessagesByAgentAndSpan :many
SELECT * FROM messages
WHERE agent_id = ? AND span_id = ? AND span_id <> ''
ORDER BY seq ASC;
