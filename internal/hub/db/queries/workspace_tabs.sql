-- name: UpsertWorkspaceTab :exec
INSERT INTO workspace_tabs (workspace_id, tab_type, tab_id, position, tile_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (workspace_id, tab_type, tab_id) DO UPDATE SET
  position = excluded.position,
  tile_id = excluded.tile_id;

-- name: ListWorkspaceTabsByWorkspace :many
SELECT * FROM workspace_tabs
WHERE workspace_id = ?
ORDER BY position;

-- name: DeleteWorkspaceTab :exec
DELETE FROM workspace_tabs
WHERE workspace_id = ? AND tab_type = ? AND tab_id = ?;

-- name: DeleteWorkspaceTabsByWorkspace :exec
DELETE FROM workspace_tabs WHERE workspace_id = ?;
