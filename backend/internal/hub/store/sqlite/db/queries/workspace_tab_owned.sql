-- name: UpsertOwnedTab :exec
INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
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
--     (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position)
--   VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?), ...
--   ON CONFLICT (user_id, tab_id) DO UPDATE SET ... (same as UpsertOwnedTab)
--
-- The adapter chunks the input to stay under SQLITE_MAX_VARIABLE_NUMBER
-- (999 by default; 7 params per row -> 142 rows per chunk).

-- BulkDeleteOwnedTabs deletes N (user_id, tab_id) pairs in one
-- statement. Adapter-built SQL (see BulkUpsertOwnedTabs note above):
--
--   DELETE FROM workspace_tab_owned
--   WHERE (user_id, tab_id) IN ((?, ?), (?, ?), ...);
--
-- Chunked to 2 params per key, 499 keys per chunk max.

-- ListOwnedTabsByWorker binds user_id as well as worker_id.
-- workspace_tab_owned is keyed by (user_id, tab_id) and nothing ties a
-- row's user_id to the registrant of its worker_id, so worker_id alone
-- selects across tenants. user_id is the tenancy axis; worker_id
-- narrows within it.
-- name: ListOwnedTabsByWorker :many
SELECT * FROM workspace_tab_owned WHERE user_id = ? AND worker_id = ? ORDER BY workspace_id, position;

-- ListDistinctWorkersByWorkspace binds user_id as well as workspace_id.
-- A workspace's owner does not constrain the user_id of rows written
-- against that workspace_id, and the DISTINCT worker_id projection drops
-- the owner column, so the caller cannot filter what this query
-- over-selects.
-- name: ListDistinctWorkersByWorkspace :many
SELECT DISTINCT worker_id FROM workspace_tab_owned WHERE user_id = ? AND workspace_id = ?;

-- GetOwnedTab binds user_id as well as workspace_id and tab_id. Tab ids
-- are client-minted and unique only within one user, so (workspace_id,
-- tab_id) is not a key: without user_id this :one returns an arbitrary
-- tenant's row.
-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE user_id = ? AND workspace_id = ? AND tab_id = ?;
