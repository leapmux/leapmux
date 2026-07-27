-- name: UpsertWorkerFileTab :exec
INSERT INTO worker_file_tabs (user_id, tab_id, workspace_id, file_path)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    workspace_id = excluded.workspace_id,
    file_path    = excluded.file_path;

-- name: GetWorkerFileTab :one
SELECT * FROM worker_file_tabs WHERE user_id = ? AND tab_id = ?;

-- name: ListAllWorkerFileTabs :many
SELECT * FROM worker_file_tabs ORDER BY user_id, tab_id;

-- name: ListWorkerFileTabsByUserAndWorkspace :many
-- Owner+workspace scoped read for the private-events snapshot, which had been
-- walking the whole table and filtering in Go. Both columns are bound, so this
-- can seek idx_worker_file_tabs_workspace(user_id, workspace_id).
SELECT * FROM worker_file_tabs WHERE user_id = ? AND workspace_id = ? ORDER BY tab_id;

-- name: DeleteWorkerFileTab :exec
DELETE FROM worker_file_tabs WHERE user_id = ? AND tab_id = ?;

-- name: UpdateWorkerFileTabWorkspace :exec
UPDATE worker_file_tabs SET workspace_id = ? WHERE user_id = ? AND tab_id = ?;
