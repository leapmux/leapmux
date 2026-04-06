-- name: UpsertWorkspaceTab :exec
INSERT INTO workspace_tabs (workspace_id, worker_id, tab_type, tab_id, position, tile_id)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  worker_id = VALUES(worker_id),
  position = VALUES(position),
  tile_id = VALUES(tile_id);

-- name: DeleteWorkspaceTab :exec
DELETE FROM workspace_tabs
WHERE workspace_id = ? AND tab_type = ? AND tab_id = ?;

-- name: ListWorkspaceTabsByWorkspace :many
SELECT * FROM workspace_tabs
WHERE workspace_id = ?
ORDER BY position;

-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tabs
WHERE workspace_id = ?;

-- name: DeleteWorkspaceTabsByWorker :exec
DELETE FROM workspace_tabs WHERE worker_id = ?;

-- name: DeleteWorkspaceTabsByWorkspace :exec
DELETE FROM workspace_tabs WHERE workspace_id = ?;

-- GetMaxPosition is implemented directly in Go because sqlc maps
-- CAST(... AS CHAR) to interface{}, which requires an unsafe type switch.

-- name: DeleteWorkerTabsForWorkspace :exec
DELETE FROM workspace_tabs
WHERE worker_id = ? AND workspace_id = ?;

-- name: ListWorkspaceTabsByWorker :many
SELECT * FROM workspace_tabs
WHERE worker_id = ?
ORDER BY workspace_id, position;
