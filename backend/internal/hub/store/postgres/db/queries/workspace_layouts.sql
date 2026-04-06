-- name: GetWorkspaceLayout :one
SELECT * FROM workspace_layouts
WHERE workspace_id = $1;

-- name: UpsertWorkspaceLayout :exec
INSERT INTO workspace_layouts (workspace_id, layout_json, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (workspace_id) DO UPDATE SET
  layout_json = EXCLUDED.layout_json,
  updated_at = NOW();

-- name: DeleteWorkspaceLayout :exec
DELETE FROM workspace_layouts
WHERE workspace_id = $1;
