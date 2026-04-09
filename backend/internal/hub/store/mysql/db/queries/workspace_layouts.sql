-- name: GetWorkspaceLayout :one
SELECT * FROM workspace_layouts
WHERE workspace_id = ?;

-- name: UpsertWorkspaceLayout :exec
INSERT INTO workspace_layouts (workspace_id, layout_json, updated_at)
VALUES (?, ?, NOW(3))
ON DUPLICATE KEY UPDATE
  layout_json = VALUES(layout_json),
  updated_at = NOW(3);

-- name: DeleteWorkspaceLayout :exec
DELETE FROM workspace_layouts
WHERE workspace_id = ?;
