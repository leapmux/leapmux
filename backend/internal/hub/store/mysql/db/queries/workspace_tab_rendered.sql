-- name: UpsertRenderedTab :exec
INSERT INTO workspace_tab_rendered (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    workspace_id = VALUES(workspace_id),
    tab_type     = VALUES(tab_type),
    worker_id    = VALUES(worker_id),
    tile_id      = VALUES(tile_id),
    position     = VALUES(position);

-- BulkUpsertRenderedTabs is the rendered-view counterpart to
-- BulkUpsertOwnedTabs (see workspace_tab_owned.sql for the SQL shape
-- the adapter constructs and the chunking constraints).

-- name: DeleteRenderedTab :exec
DELETE FROM workspace_tab_rendered WHERE org_id = ? AND tab_id = ?;

-- BulkDeleteRenderedTabs is the rendered-view counterpart to
-- BulkDeleteOwnedTabs (see workspace_tab_owned.sql).

-- name: DeleteRenderedTabsByOrg :exec
DELETE FROM workspace_tab_rendered WHERE org_id = ?;

-- name: ListRenderedTabsByWorkspace :many
SELECT * FROM workspace_tab_rendered WHERE workspace_id = ? ORDER BY position;

-- name: ListRenderedTabsByWorkspaceIDs :many
SELECT * FROM workspace_tab_rendered
WHERE workspace_id IN (sqlc.slice('workspace_ids'))
ORDER BY workspace_id, position;

-- name: GetRenderedTab :one
SELECT * FROM workspace_tab_rendered WHERE workspace_id = ? AND tab_type = ? AND tab_id = ?;

-- LocateAccessibleRenderedTab finds a rendered tab by tab_id and
-- (optionally) tab_type across every workspace the user owns.
-- tab_type = 0 (TAB_TYPE_UNSPECIFIED) means "match any type";
-- tab ids are unique across types so the match is unambiguous.
-- Used by WorkspaceService.LocateTab.
-- name: LocateAccessibleRenderedTab :one
SELECT r.* FROM workspace_tab_rendered r
JOIN workspaces w ON w.id = r.workspace_id
WHERE r.tab_id = sqlc.arg(tab_id)
  AND (sqlc.arg(tab_type) = 0 OR r.tab_type = sqlc.arg(tab_type))
  AND w.is_deleted = 0
  AND w.owner_user_id = sqlc.arg(user_id)
LIMIT 1;
