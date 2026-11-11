-- name: GetTabCreation :one
SELECT * FROM tab_creations WHERE user_id = ? AND worker_id = ? AND tab_id = ?;

-- name: InsertTabCreation :exec
INSERT INTO tab_creations(user_id, worker_id, tab_id, tab_type, request_hash, request_payload,
    publication_payload, runtime_payload, publication_result, state)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPendingTabCreations :many
SELECT * FROM tab_creations WHERE user_id = ? AND worker_id = ?
    AND (state IN (?, ?, ?) OR (state = ? AND cancel_progress_payload IS NOT NULL))
ORDER BY created_at, tab_id;

-- name: CommitTabCreation :execrows
UPDATE tab_creations SET state = ?, publication_result = ?, request_payload = X'',
    runtime_payload = X'', failure_message = ''
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: RejectTabCreation :execrows
UPDATE tab_creations SET state = ?, failure_message = ?, request_payload = X'', runtime_payload = X''
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: RequestTabCreationCancel :execrows
UPDATE tab_creations SET state = ?, cancel_action = ?
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: FinishTabCreationCancel :execrows
UPDATE tab_creations SET state = ?, close_response_payload = ?,
    request_payload = X'', runtime_payload = X''
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: ClearTabCreationCancelProgress :exec
UPDATE tab_creations SET cancel_progress_payload = NULL
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: HasPendingTabCreation :one
SELECT EXISTS(SELECT 1 FROM tab_creations WHERE user_id = ? AND worker_id = ? AND tab_type = ? AND tab_id = ? AND state IN (?, ?));

-- name: PublishTabCreation :execrows
UPDATE tab_creations SET state = ?, publication_result = ?, failure_message = ''
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: CompletePublishedTabCreation :execrows
UPDATE tab_creations SET state = ?, request_payload = X'', runtime_payload = X''
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: SaveTabCreationRuntime :execrows
UPDATE tab_creations SET runtime_payload = ?
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;

-- name: SaveTabCreationCancelProgress :execrows
UPDATE tab_creations SET cancel_progress_payload = ?
WHERE user_id = ? AND worker_id = ? AND tab_id = ? AND state = ?;
