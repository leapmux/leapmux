-- name: CreateWorkspaceShare :exec
INSERT INTO workspace_shares (workspace_id, user_id) VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteWorkspaceShare :exec
DELETE FROM workspace_shares WHERE workspace_id = ? AND user_id = ?;

-- name: ListWorkspaceSharesByWorkspaceID :many
SELECT ws.workspace_id, ws.user_id, ws.created_at, u.username, u.display_name
FROM workspace_shares ws
JOIN users u ON u.id = ws.user_id
WHERE ws.workspace_id = ?
ORDER BY ws.created_at;

-- name: ClearWorkspaceShares :exec
DELETE FROM workspace_shares WHERE workspace_id = ?;
