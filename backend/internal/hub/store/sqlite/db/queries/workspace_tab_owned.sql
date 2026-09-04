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
SELECT wto.user_id, wto.workspace_id, wto.tab_type, wto.tab_id, wto.worker_id,
       CASE WHEN ws.section_type = sqlc.arg(archived_section_type)
            THEN CAST(sqlc.arg(archived_archive_state) AS INTEGER)
            ELSE CAST(sqlc.arg(active_archive_state) AS INTEGER) END AS archive_state
FROM workspace_tab_owned wto
LEFT JOIN workspace_section_items wsi
  ON wsi.user_id = wto.user_id AND wsi.workspace_id = wto.workspace_id
LEFT JOIN workspace_sections ws
  ON ws.id = wsi.section_id AND ws.user_id = wto.user_id
WHERE wto.user_id = sqlc.arg(user_id) AND wto.worker_id = sqlc.arg(worker_id)
ORDER BY wto.workspace_id, wto.position;

-- ListOwnedTabsByWorkspace binds user_id as well as workspace_id. A
-- workspace's owner does not constrain the user_id of rows written against
-- that workspace_id, so without user_id this query over-selects rows the
-- caller cannot then filter.
--
-- Returns the tab ROWS, not just the distinct worker ids. The caller needs
-- both: the worker set to fan out to, and the (tab_type, tab_id) list each
-- worker must tear down. Reading them here -- inside the delete transaction --
-- is what makes the two atomic. When each caller resolved its own list
-- beforehand, a tab a peer opened in between was missed, a failed read
-- degraded silently to "close nothing", and both callers read
-- workspace_tab_rendered, a strict SUBSET of this table, so a
-- projection-hidden tab was structurally unreachable rather than merely late.
-- name: ListOwnedTabsByWorkspace :many
SELECT worker_id, tab_type, tab_id FROM workspace_tab_owned
WHERE user_id = ? AND workspace_id = ?
ORDER BY worker_id, tab_id;

-- GetOwnedTab is a point lookup on PRIMARY KEY (user_id, tab_id). Tab ids
-- are client-minted and unique only within one user, so user_id is half the
-- key, not a filter: without it this :one returns an arbitrary tenant's row.
-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE user_id = ? AND tab_id = ?;
