-- name: UpsertTileActiveTab :exec
INSERT INTO workspace_tile_active_tabs (workspace_id, tile_id, tab_type, tab_id)
VALUES (?, ?, ?, ?)
ON CONFLICT (workspace_id, tile_id) DO UPDATE SET
  tab_type = excluded.tab_type,
  tab_id = excluded.tab_id;

-- name: ListTileActiveTabs :many
SELECT * FROM workspace_tile_active_tabs
WHERE workspace_id = ?;

-- name: DeleteTileActiveTabs :exec
DELETE FROM workspace_tile_active_tabs
WHERE workspace_id = ?;
