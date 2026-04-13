-- name: UpsertAutoContinueSchedule :exec
INSERT INTO auto_continue_schedules (
  agent_id,
  reason,
  content,
  due_at,
  jitter_ms,
  next_backoff_ms,
  state,
  source_payload
) VALUES (
  sqlc.arg(agent_id),
  sqlc.arg(reason),
  sqlc.arg(content),
  sqlc.arg(due_at),
  sqlc.arg(jitter_ms),
  sqlc.arg(next_backoff_ms),
  'active',
  sqlc.arg(source_payload)
)
ON CONFLICT(agent_id, reason) DO UPDATE SET
  content = excluded.content,
  due_at = excluded.due_at,
  jitter_ms = excluded.jitter_ms,
  next_backoff_ms = excluded.next_backoff_ms,
  state = 'active',
  source_payload = excluded.source_payload,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

-- name: GetAutoContinueSchedule :one
SELECT * FROM auto_continue_schedules
WHERE agent_id = ? AND reason = ?;

-- name: ListActiveAutoContinueSchedules :many
SELECT * FROM auto_continue_schedules
WHERE state = 'active'
ORDER BY due_at ASC;

-- name: CancelAutoContinueSchedule :exec
UPDATE auto_continue_schedules
SET state = 'cancelled',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE agent_id = ? AND reason = ? AND state = 'active';

-- name: CancelAllAutoContinueSchedulesByAgent :exec
UPDATE auto_continue_schedules
SET state = 'cancelled',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE agent_id = ? AND state = 'active';

-- name: MarkAutoContinueScheduleFired :exec
UPDATE auto_continue_schedules
SET state = 'fired',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE agent_id = ? AND reason = ? AND state = 'active';

-- name: IsAgentOpen :one
SELECT EXISTS(
  SELECT 1 FROM agents
  WHERE id = ? AND closed_at IS NULL
);
