-- name: UpsertRenderedTab :exec
INSERT INTO workspace_tab_rendered (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position;

-- BulkUpsertRenderedTabs is the rendered-view counterpart to
-- BulkUpsertOwnedTabs. See that query for the column-major contract.
-- name: BulkUpsertRenderedTabs :exec
INSERT INTO workspace_tab_rendered (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
SELECT unnest(sqlc.arg(org_ids)::TEXT[]),
       unnest(sqlc.arg(workspace_ids)::TEXT[]),
       unnest(sqlc.arg(tab_types)::INTEGER[]),
       unnest(sqlc.arg(tab_ids)::TEXT[]),
       unnest(sqlc.arg(worker_ids)::TEXT[]),
       unnest(sqlc.arg(tile_ids)::TEXT[]),
       unnest(sqlc.arg(positions)::TEXT[])
ON CONFLICT (org_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position;

-- name: DeleteRenderedTab :exec
DELETE FROM workspace_tab_rendered WHERE org_id = $1 AND tab_id = $2;

-- BulkDeleteRenderedTabs is the rendered-view counterpart to
-- BulkDeleteOwnedTabs.
-- name: BulkDeleteRenderedTabs :exec
WITH keys AS (
    SELECT unnest(sqlc.arg(org_ids)::TEXT[]) AS org_id,
           unnest(sqlc.arg(tab_ids)::TEXT[]) AS tab_id
)
DELETE FROM workspace_tab_rendered t
USING keys k
WHERE t.org_id = k.org_id AND t.tab_id = k.tab_id;

-- name: DeleteRenderedTabsByOrg :exec
DELETE FROM workspace_tab_rendered WHERE org_id = $1;

-- name: ListRenderedTabsByWorkspace :many
SELECT * FROM workspace_tab_rendered WHERE workspace_id = $1 ORDER BY position;

-- name: ListRenderedTabsByWorkspaceIDs :many
SELECT * FROM workspace_tab_rendered
WHERE workspace_id = ANY(sqlc.arg(workspace_ids)::TEXT[])
ORDER BY workspace_id, position;

-- name: GetRenderedTab :one
SELECT * FROM workspace_tab_rendered WHERE workspace_id = $1 AND tab_type = $2 AND tab_id = $3;

-- LocateAccessibleRenderedTab finds a rendered tab by tab_id and
-- (optionally) tab_type across every workspace the user owns.
-- tab_type = 0 (TAB_TYPE_UNSPECIFIED) means "match any type";
-- tab ids are unique across types so the match is unambiguous.
-- Used by WorkspaceService.LocateTab.
-- name: LocateAccessibleRenderedTab :one
SELECT r.* FROM workspace_tab_rendered r
JOIN workspaces w ON w.id = r.workspace_id
WHERE r.tab_id = sqlc.arg(tab_id)
  AND (sqlc.arg(tab_type)::integer = 0 OR r.tab_type = sqlc.arg(tab_type))
  AND w.is_deleted = FALSE
  AND w.owner_user_id = sqlc.arg(user_id)
LIMIT 1;
