-- name: GetSemanticEventJournal :one
SELECT * FROM semantic_event_journal WHERE singleton = 1;

-- name: CanWatchTerminalHistory :one
SELECT EXISTS (SELECT 1 FROM terminals WHERE id = ? AND closed_at IS NULL);

-- name: CanWatchAgentHistory :one
SELECT EXISTS (SELECT 1 FROM agents WHERE id = ? AND closed_at IS NULL);

-- name: CanAnswerSemanticAgentRequest :one
SELECT EXISTS (SELECT 1 FROM agents WHERE id = ? AND closed_at IS NULL AND NOT workspace_archived);

-- name: IsClosedTerminalHistoryResource :one
SELECT EXISTS (SELECT 1 FROM terminals WHERE id = ? AND closed_at IS NOT NULL);

-- name: IsClosedAgentHistoryResource :one
SELECT EXISTS (SELECT 1 FROM agents WHERE id = ? AND closed_at IS NOT NULL);

-- name: CreateSemanticEventJournal :exec
INSERT INTO semantic_event_journal (singleton, worker_id, owner_user_id, journal_id, cursor_key, dirty, entry_count, payload_bytes)
VALUES (1, ?, ?, ?, ?, 1, 0, 0);

-- name: DeleteSemanticEventJournal :exec
DELETE FROM semantic_event_journal WHERE singleton = 1;

-- name: SetSemanticEventJournalDirty :exec
UPDATE semantic_event_journal SET dirty = ? WHERE singleton = 1;

-- name: GetSemanticEventFeedPosition :one
SELECT CAST(COALESCE((SELECT high_sequence FROM semantic_event_feed_heads WHERE tab_type = sqlc.arg(tab_type)), 0) AS INTEGER);

-- name: AdvanceSemanticEventFeed :one
INSERT INTO semantic_event_feed_heads (tab_type, high_sequence) VALUES (sqlc.arg(tab_type), 1)
ON CONFLICT (tab_type) DO UPDATE SET high_sequence = high_sequence + 1
WHERE high_sequence < 9223372036854775807
RETURNING high_sequence;

-- name: ChangeSemanticEventJournalUsage :one
UPDATE semantic_event_journal
SET entry_count = entry_count + sqlc.arg(entry_delta), payload_bytes = payload_bytes + sqlc.arg(byte_delta)
WHERE singleton = 1 RETURNING *;

-- name: InitSemanticEventTail :exec
INSERT INTO semantic_event_tails (tab_type, tab_id, tab_stream_id, high_sequence, floor_sequence, last_observed_unix_millis)
VALUES (?, ?, ?, 0, 0, ?) ON CONFLICT(tab_type, tab_id) DO NOTHING;

-- name: GetSemanticEventTail :one
SELECT * FROM semantic_event_tails WHERE tab_type = ? AND tab_id = ?;

-- name: ListSemanticEventTails :many
SELECT * FROM semantic_event_tails ORDER BY tab_type, tab_id;

-- name: CountSemanticEventTails :one
SELECT COUNT(*) FROM semantic_event_tails;

-- name: GetOldestSemanticEventTailExcept :one
SELECT * FROM semantic_event_tails WHERE tab_type <> ? OR tab_id <> ?
ORDER BY last_observed_unix_millis, tab_type, tab_id LIMIT 1;

-- name: DeleteSemanticEventTail :exec
DELETE FROM semantic_event_tails WHERE tab_type = ? AND tab_id = ?;

-- name: DeleteSemanticEventTails :exec
DELETE FROM semantic_event_tails;

-- name: AdvanceSemanticEventTail :one
UPDATE semantic_event_tails
SET high_sequence = high_sequence + 1,
    last_observed_unix_millis = MAX(last_observed_unix_millis, CAST(sqlc.arg(observed_unix_millis) AS INTEGER))
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id)
RETURNING high_sequence;

-- name: SealSemanticEventTail :exec
UPDATE semantic_event_tails SET final_sequence = high_sequence
WHERE tab_type = ? AND tab_id = ? AND resource_closed AND final_sequence IS NULL;

-- name: AdvanceSemanticEventFloor :exec
UPDATE semantic_event_tails SET floor_sequence = sqlc.arg(floor_sequence)
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id) AND floor_sequence < sqlc.arg(floor_sequence);

-- name: InsertSemanticEventEntry :exec
INSERT INTO semantic_event_entries (tab_type, tab_id, sequence, feed_sequence, observed_unix_millis, fact, request_open_instance, gap_reason)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: HasSemanticEventRequestOpening :one
SELECT EXISTS (
    SELECT 1 FROM semantic_event_entries WHERE tab_type = ? AND tab_id = ? AND request_open_instance = ?
);

-- name: ListSemanticEventEntriesAfter :many
SELECT * FROM semantic_event_entries
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id) AND sequence > sqlc.arg(after_sequence)
ORDER BY sequence LIMIT sqlc.arg(page_size);

-- name: GetOldestSemanticEventEntry :one
SELECT * FROM semantic_event_entries ORDER BY ingest_order LIMIT 1;

-- name: ListSemanticEventFeedEntriesAfter :many
SELECT * FROM semantic_event_entries
WHERE tab_type = sqlc.arg(tab_type) AND feed_sequence > sqlc.arg(after_sequence)
ORDER BY feed_sequence LIMIT sqlc.arg(page_size);

-- name: HasExpiredSemanticEventEntries :one
SELECT EXISTS (SELECT 1 FROM semantic_event_entries WHERE observed_unix_millis <= sqlc.arg(expired_at));

-- name: GetExpiredSemanticEventCutoffs :many
SELECT tab_type, tab_id, CAST(MAX(sequence) AS INTEGER) AS cutoff
FROM semantic_event_entries WHERE observed_unix_millis <= ? GROUP BY tab_type, tab_id;

-- name: GetExpiredSemanticEventCutoff :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER)
FROM semantic_event_entries
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id)
    AND observed_unix_millis <= sqlc.arg(expired_at);

-- name: ReadSemanticEventUsageThrough :one
SELECT COUNT(*) AS entries, CAST(COALESCE(SUM(length(fact)), 0) AS INTEGER) AS bytes
FROM semantic_event_entries
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id) AND sequence <= sqlc.arg(through_sequence);

-- name: DeleteSemanticEventsThrough :exec
DELETE FROM semantic_event_entries
WHERE tab_type = sqlc.arg(tab_type) AND tab_id = sqlc.arg(tab_id) AND sequence <= sqlc.arg(through_sequence);
