-- name: UpsertWorkerFileTab :exec
INSERT INTO worker_file_tabs (org_id, tab_id, workspace_id, file_path)
VALUES (?, ?, ?, ?)
ON CONFLICT (org_id, tab_id) DO UPDATE SET
    workspace_id = excluded.workspace_id,
    file_path    = excluded.file_path;

-- name: GetWorkerFileTab :one
SELECT * FROM worker_file_tabs WHERE org_id = ? AND tab_id = ?;

-- name: ListWorkerFileTabsByWorkspace :many
SELECT * FROM worker_file_tabs WHERE org_id = ? AND workspace_id = ? ORDER BY tab_id;

-- name: ListAllWorkerFileTabs :many
SELECT * FROM worker_file_tabs ORDER BY org_id, tab_id;

-- name: DeleteWorkerFileTab :exec
DELETE FROM worker_file_tabs WHERE org_id = ? AND tab_id = ?;

-- name: UpdateWorkerFileTabWorkspace :exec
UPDATE worker_file_tabs SET workspace_id = ? WHERE org_id = ? AND tab_id = ?;
