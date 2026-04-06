-- name: UpsertWorkspaceTab :exec
INSERT INTO workspace_tabs (workspace_id, worker_id, tab_type, tab_id, position, tile_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, tab_type, tab_id) DO UPDATE SET
  worker_id = EXCLUDED.worker_id,
  position = EXCLUDED.position,
  tile_id = EXCLUDED.tile_id;

-- name: DeleteWorkspaceTab :exec
DELETE FROM workspace_tabs
WHERE workspace_id = $1 AND tab_type = $2 AND tab_id = $3;

-- name: ListWorkspaceTabsByWorkspace :many
SELECT * FROM workspace_tabs
WHERE workspace_id = $1
ORDER BY position;

-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tabs
WHERE workspace_id = $1;

-- name: DeleteWorkspaceTabsByWorker :exec
DELETE FROM workspace_tabs WHERE worker_id = $1;

-- name: DeleteWorkspaceTabsByWorkspace :exec
DELETE FROM workspace_tabs WHERE workspace_id = $1;

-- name: GetMaxTabPosition :one
SELECT COALESCE(MAX(position), '')::text AS max_position FROM workspace_tabs
WHERE workspace_id = $1;

-- name: DeleteWorkerTabsForWorkspace :exec
DELETE FROM workspace_tabs
WHERE worker_id = $1 AND workspace_id = $2;

-- name: ListWorkspaceTabsByWorker :many
SELECT * FROM workspace_tabs
WHERE worker_id = $1
ORDER BY workspace_id, position;
