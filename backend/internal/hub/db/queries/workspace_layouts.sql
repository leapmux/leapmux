-- name: GetWorkspaceLayout :one
SELECT * FROM workspace_layouts
WHERE workspace_id = ?;

-- name: UpsertWorkspaceLayout :exec
INSERT INTO workspace_layouts (workspace_id, layout_json, updated_at)
VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT (workspace_id) DO UPDATE SET
  layout_json = excluded.layout_json,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now');

-- name: DeleteWorkspaceLayout :exec
DELETE FROM workspace_layouts
WHERE workspace_id = ?;
