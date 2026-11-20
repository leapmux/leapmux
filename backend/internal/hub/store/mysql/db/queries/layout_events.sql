-- name: EnsureLayoutEventHead :exec
INSERT INTO layout_event_heads (user_id, journal_id, cursor_key)
VALUES (sqlc.arg(user_id), sqlc.arg(journal_id), sqlc.arg(cursor_key))
ON DUPLICATE KEY UPDATE user_id = layout_event_heads.user_id;

-- name: ReserveLayoutEventPositions :execrows
UPDATE layout_event_heads SET high_sequence = high_sequence + sqlc.arg(event_count)
WHERE user_id = sqlc.arg(user_id) AND high_sequence <= sqlc.arg(sequence_limit);

-- name: ReadLayoutEventHeadForWrite :one
SELECT * FROM layout_event_heads WHERE user_id = sqlc.arg(user_id) FOR UPDATE;

-- name: InsertLayoutEvent :exec
INSERT INTO layout_event_entries (user_id, sequence, observed_unix_millis, fact)
VALUES (sqlc.arg(user_id), sqlc.arg(sequence), sqlc.arg(observed_unix_millis), sqlc.arg(fact));

-- name: AdvanceLayoutEventFloor :exec
UPDATE layout_event_heads SET floor_sequence = sqlc.arg(floor_sequence)
WHERE user_id = sqlc.arg(user_id) AND floor_sequence < sqlc.arg(floor_sequence);

-- name: DeleteLayoutEventsThrough :exec
DELETE FROM layout_event_entries WHERE user_id = sqlc.arg(user_id) AND sequence <= sqlc.arg(through_sequence);

-- name: ReadLayoutEventPage :many
SELECT h.journal_id, h.cursor_key, h.high_sequence, h.floor_sequence,
       EXISTS (SELECT 1 FROM workspaces w
         WHERE w.id = sqlc.arg(workspace_id) AND w.owner_user_id = h.user_id AND w.deleted_at IS NULL) AS workspace_live,
       EXISTS (SELECT 1 FROM lifecycle_outbox o
         WHERE o.user_id = h.user_id AND o.consumed_at IS NULL) AS owner_lifecycle_pending,
       scope_end.final_sequence, e.sequence, e.observed_unix_millis, e.fact
FROM layout_event_heads h
LEFT JOIN layout_workspace_ends scope_end
    ON scope_end.user_id = h.user_id AND scope_end.workspace_id = sqlc.arg(workspace_id)
LEFT JOIN layout_event_entries e
    ON e.user_id = h.user_id AND e.sequence > sqlc.arg(after_sequence)
    AND (scope_end.final_sequence IS NULL OR e.sequence <= scope_end.final_sequence)
WHERE h.user_id = sqlc.arg(user_id)
ORDER BY e.sequence
LIMIT ?;

-- name: ClaimLayoutLifecycle :execrows
UPDATE lifecycle_outbox SET layout_fact_written = TRUE
WHERE id = sqlc.arg(outbox_id) AND user_id = sqlc.arg(user_id) AND NOT layout_fact_written;

-- name: MarkLayoutWorkspaceClosed :exec
INSERT INTO layout_workspace_ends (user_id, workspace_id, final_sequence)
VALUES (sqlc.arg(user_id), sqlc.arg(workspace_id), sqlc.arg(final_sequence));
