-- name: EnsureLayoutEventHead :exec
INSERT INTO layout_event_heads (user_id, journal_id, cursor_key)
VALUES (sqlc.arg(user_id), sqlc.arg(journal_id), sqlc.arg(cursor_key))
ON CONFLICT (user_id) DO NOTHING;

-- name: ReserveLayoutEventPositions :one
UPDATE layout_event_heads SET high_sequence = high_sequence + sqlc.arg(event_count)
WHERE user_id = sqlc.arg(user_id) AND high_sequence <= sqlc.arg(sequence_limit)
RETURNING high_sequence, floor_sequence;

-- name: LayoutEventHeadExists :one
SELECT EXISTS (SELECT 1 FROM layout_event_heads WHERE user_id = sqlc.arg(user_id)) AS head_exists;

-- name: AdvanceLayoutEventFloor :exec
UPDATE layout_event_heads SET floor_sequence = sqlc.arg(floor_sequence)
WHERE user_id = sqlc.arg(user_id) AND floor_sequence < sqlc.arg(floor_sequence);

-- name: DeleteLayoutEventsThrough :exec
DELETE FROM layout_event_entries WHERE user_id = sqlc.arg(user_id) AND sequence <= sqlc.arg(through_sequence);

-- name: ReadLayoutEventPageHead :one
SELECT h.journal_id, h.cursor_key, h.high_sequence, h.floor_sequence,
       EXISTS (SELECT 1 FROM workspaces w
         WHERE w.id = sqlc.arg(workspace_id) AND w.id <> ''
           AND w.owner_user_id = sqlc.arg(user_id) AND w.deleted_at IS NULL) AS workspace_live,
       EXISTS (SELECT 1 FROM lifecycle_outbox o
         WHERE o.user_id = sqlc.arg(user_id) AND o.consumed_at IS NULL) AS owner_lifecycle_pending,
       scope_end.final_sequence
FROM layout_event_heads h
LEFT JOIN layout_workspace_ends scope_end
    ON scope_end.user_id = h.user_id AND scope_end.workspace_id = sqlc.arg(workspace_id)
WHERE h.user_id = sqlc.arg(user_id);

-- name: ListLayoutEventEntries :many
SELECT sequence, observed_unix_millis, fact
FROM layout_event_entries
WHERE user_id = sqlc.arg(user_id)
  AND sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(max_sequence)
ORDER BY sequence
LIMIT sqlc.arg(page_limit);

-- name: ClaimLayoutLifecycle :execrows
UPDATE lifecycle_outbox SET layout_fact_written = TRUE
WHERE id = sqlc.arg(outbox_id) AND user_id = sqlc.arg(user_id) AND NOT layout_fact_written;

-- name: MarkLayoutWorkspaceClosed :exec
INSERT INTO layout_workspace_ends (user_id, workspace_id, final_sequence)
VALUES (sqlc.arg(user_id), sqlc.arg(workspace_id), sqlc.arg(final_sequence));
