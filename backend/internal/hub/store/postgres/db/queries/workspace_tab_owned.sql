-- name: UpsertOwnedTab :exec
INSERT INTO workspace_tab_owned (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (org_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position;

-- BulkUpsertOwnedTabs inserts N rows in one round-trip, parallel arrays
-- expanded via UNNEST. The caller passes seven equal-length slices;
-- shorter slices truncate the row count (UNNEST stops at the shortest
-- column), but the adapter guards length parity before invoking sqlc.
-- name: BulkUpsertOwnedTabs :exec
INSERT INTO workspace_tab_owned (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
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

-- name: DeleteOwnedTab :exec
DELETE FROM workspace_tab_owned WHERE org_id = $1 AND tab_id = $2;

-- BulkDeleteOwnedTabs deletes N (org_id, tab_id) pairs in one
-- round-trip. The two arrays must have the same length; the adapter
-- enforces that. The OFFSET-rownumber join lines each org_id up with
-- the tab_id at the same array index.
-- name: BulkDeleteOwnedTabs :exec
WITH keys AS (
    SELECT unnest(sqlc.arg(org_ids)::TEXT[]) AS org_id,
           unnest(sqlc.arg(tab_ids)::TEXT[]) AS tab_id
)
DELETE FROM workspace_tab_owned t
USING keys k
WHERE t.org_id = k.org_id AND t.tab_id = k.tab_id;

-- name: DeleteOwnedTabsByOrg :exec
DELETE FROM workspace_tab_owned WHERE org_id = $1;

-- name: ListOwnedTabsByWorkspace :many
SELECT * FROM workspace_tab_owned WHERE workspace_id = $1 ORDER BY position;

-- name: ListOwnedTabsByWorker :many
SELECT * FROM workspace_tab_owned WHERE worker_id = $1 ORDER BY workspace_id, position;

-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tab_owned WHERE workspace_id = $1;

-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE workspace_id = $1 AND tab_id = $2;
