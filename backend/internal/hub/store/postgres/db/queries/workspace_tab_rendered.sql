-- name: UpsertRenderedTab :exec
INSERT INTO workspace_tab_rendered (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position;

-- BulkUpsertRenderedTabs is the rendered-view counterpart to
-- BulkUpsertOwnedTabs. See that query for the column-major contract.
-- name: BulkUpsertRenderedTabs :exec
INSERT INTO workspace_tab_rendered (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
SELECT unnest(sqlc.arg(user_ids)::TEXT[]),
       unnest(sqlc.arg(workspace_ids)::TEXT[]),
       unnest(sqlc.arg(tab_types)::INTEGER[]),
       unnest(sqlc.arg(tab_ids)::TEXT[]),
       unnest(sqlc.arg(worker_ids)::TEXT[]),
       unnest(sqlc.arg(tile_ids)::TEXT[]),
       unnest(sqlc.arg(positions)::TEXT[])
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position;

-- BulkDeleteRenderedTabs is the rendered-view counterpart to
-- BulkDeleteOwnedTabs.
-- name: BulkDeleteRenderedTabs :exec
WITH keys AS (
    SELECT unnest(sqlc.arg(user_ids)::TEXT[]) AS user_id,
           unnest(sqlc.arg(tab_ids)::TEXT[]) AS tab_id
)
DELETE FROM workspace_tab_rendered t
USING keys k
WHERE t.user_id = k.user_id AND t.tab_id = k.tab_id;

-- ListRenderedTabsByWorkspaceIDs binds user_id as well as the workspace set.
-- workspace_tab_rendered is keyed on (user_id, tab_id) and workspace_id is a
-- plain FK to workspaces(id), so nothing ties a row's user_id to
-- owner(workspace_id): any user's row may name any existing workspace. Without
-- user_id this listing returns another tenant's tabs for a workspace the caller
-- legitimately reads. Same argument as ListDistinctWorkersByWorkspace on the
-- owned view.
-- name: ListRenderedTabsByWorkspaceIDs :many
SELECT * FROM workspace_tab_rendered
WHERE user_id = sqlc.arg(user_id) AND workspace_id = ANY(sqlc.arg(workspace_ids)::TEXT[])
ORDER BY workspace_id, position;

-- GetRenderedTab binds user_id as well as workspace_id and tab_id. Tab ids are
-- client-minted and unique only within one user, so (workspace_id, tab_type,
-- tab_id) is not a key: without user_id this :one returns an arbitrary tenant's
-- row. Mirrors GetOwnedTab.
-- name: GetRenderedTab :one
SELECT * FROM workspace_tab_rendered WHERE user_id = $1 AND workspace_id = $2 AND tab_type = $3 AND tab_id = $4;

-- LocateAccessibleRenderedTab finds a rendered tab by tab_id and
-- (optionally) tab_type across every workspace the user owns.
-- tab_type = 0 (TAB_TYPE_UNSPECIFIED) means "match any type";
-- tab ids are unique across types so the match is unambiguous.
-- Used by WorkspaceService.LocateTab.
-- r.user_id is bound as well as w.owner_user_id: the workspace filter proves
-- the CALLER owns the workspace, not that the ROW does. workspace_tab_rendered
-- is keyed on (user_id, tab_id) and its workspace_id is a plain FK, so another
-- tenant's row naming an owned workspace would satisfy the join and win the
-- LIMIT 1.
-- name: LocateAccessibleRenderedTab :one
SELECT r.* FROM workspace_tab_rendered r
JOIN workspaces w ON w.id = r.workspace_id
WHERE r.tab_id = sqlc.arg(tab_id)
  AND (sqlc.arg(tab_type)::integer = 0 OR r.tab_type = sqlc.arg(tab_type))
  AND w.is_deleted = FALSE
  AND w.owner_user_id = sqlc.arg(user_id)
  AND r.user_id = sqlc.arg(user_id)
LIMIT 1;
