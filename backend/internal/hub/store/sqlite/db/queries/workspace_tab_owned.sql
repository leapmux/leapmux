-- name: UpsertOwnedTab :exec
INSERT INTO workspace_tab_owned (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (org_id, tab_id) DO UPDATE SET
    workspace_id = excluded.workspace_id,
    tab_type     = excluded.tab_type,
    worker_id    = excluded.worker_id,
    tile_id      = excluded.tile_id,
    position     = excluded.position;

-- BulkUpsertOwnedTabs runs the above upsert against N rows in one
-- statement. sqlc cannot generate a variable-arity multi-column INSERT,
-- so the adapter (workspace_tab_index.go) builds the SQL at runtime:
--
--   INSERT INTO workspace_tab_owned
--     (org_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
--   VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?), ...
--   ON CONFLICT (org_id, tab_id) DO UPDATE SET ... (same as UpsertOwnedTab)
--
-- The adapter chunks the input to stay under SQLITE_MAX_VARIABLE_NUMBER
-- (999 by default; 7 params per row -> 142 rows per chunk).

-- name: DeleteOwnedTab :exec
DELETE FROM workspace_tab_owned WHERE org_id = ? AND tab_id = ?;

-- BulkDeleteOwnedTabs deletes N (org_id, tab_id) pairs in one
-- statement. Adapter-built SQL (see BulkUpsertOwnedTabs note above):
--
--   DELETE FROM workspace_tab_owned
--   WHERE (org_id, tab_id) IN ((?, ?), (?, ?), ...);
--
-- Chunked to 2 params per key, 499 keys per chunk max.

-- name: DeleteOwnedTabsByOrg :exec
DELETE FROM workspace_tab_owned WHERE org_id = ?;

-- name: ListOwnedTabsByWorkspace :many
SELECT * FROM workspace_tab_owned WHERE workspace_id = ? ORDER BY position;

-- name: ListOwnedTabsByWorker :many
SELECT * FROM workspace_tab_owned WHERE worker_id = ? ORDER BY workspace_id, position;

-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tab_owned WHERE workspace_id = ?;

-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE workspace_id = ? AND tab_id = ?;
