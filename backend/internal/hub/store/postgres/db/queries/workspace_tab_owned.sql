-- name: UpsertOwnedTab :exec
INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
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
INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
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

-- BulkDeleteOwnedTabs deletes N (user_id, tab_id) pairs in one
-- round-trip. The two arrays must have the same length; the adapter
-- enforces that. The OFFSET-rownumber join lines each user_id up with
-- the tab_id at the same array index.
-- name: BulkDeleteOwnedTabs :exec
WITH keys AS (
    SELECT unnest(sqlc.arg(user_ids)::TEXT[]) AS user_id,
           unnest(sqlc.arg(tab_ids)::TEXT[]) AS tab_id
)
DELETE FROM workspace_tab_owned t
USING keys k
WHERE t.user_id = k.user_id AND t.tab_id = k.tab_id;

-- ListOwnedTabsByWorker binds user_id as well as worker_id.
-- workspace_tab_owned is keyed by (user_id, tab_id) and nothing ties a
-- row's user_id to the registrant of its worker_id, so worker_id alone
-- selects across tenants. user_id is the tenancy axis; worker_id
-- narrows within it.
-- name: ListOwnedTabsByWorker :many
SELECT * FROM workspace_tab_owned WHERE user_id = $1 AND worker_id = $2 ORDER BY workspace_id, position;

-- ListDistinctWorkersByWorkspace binds user_id as well as workspace_id.
-- A workspace's owner does not constrain the user_id of rows written
-- against that workspace_id, and the DISTINCT worker_id projection drops
-- the owner column, so the caller cannot filter what this query
-- over-selects.
-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tab_owned WHERE user_id = $1 AND workspace_id = $2;

-- GetOwnedTab binds user_id as well as workspace_id and tab_id. Tab ids
-- are client-minted and unique only within one user, so (workspace_id,
-- tab_id) is not a key: without user_id this :one returns an arbitrary
-- tenant's row.
-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE user_id = $1 AND workspace_id = $2 AND tab_id = $3;
