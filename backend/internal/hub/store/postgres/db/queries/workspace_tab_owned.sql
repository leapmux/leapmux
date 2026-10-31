-- name: UpsertOwnedTab :exec
-- The update branch must set rendered too, or a flip of the flag alone is lost.
-- The composite workspace key is checked on the update branch as well, so a
-- row cannot move into a workspace its user does not own.
INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position, rendered)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position,
    rendered     = EXCLUDED.rendered;

-- BulkUpsertOwnedTabs inserts N rows in one round-trip, parallel arrays
-- expanded via UNNEST. The caller passes eight equal-length slices;
-- shorter slices truncate the row count (UNNEST stops at the shortest
-- column), but the adapter guards length parity before invoking sqlc.
-- name: BulkUpsertOwnedTabs :exec
INSERT INTO workspace_tab_owned (user_id, workspace_id, tab_type, tab_id, worker_id, tile_id, position, rendered)
SELECT unnest(sqlc.arg(user_ids)::TEXT[]),
       unnest(sqlc.arg(workspace_ids)::TEXT[]),
       unnest(sqlc.arg(tab_types)::INTEGER[]),
       unnest(sqlc.arg(tab_ids)::TEXT[]),
       unnest(sqlc.arg(worker_ids)::TEXT[]),
       unnest(sqlc.arg(tile_ids)::TEXT[]),
       unnest(sqlc.arg(positions)::TEXT[]),
       unnest(sqlc.arg(rendered_flags)::BOOLEAN[])
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    tab_type     = EXCLUDED.tab_type,
    worker_id    = EXCLUDED.worker_id,
    tile_id      = EXCLUDED.tile_id,
    position     = EXCLUDED.position,
    rendered     = EXCLUDED.rendered;

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
--
-- A tab of a soft-deleted workspace reports ARCHIVED until the lifecycle drain
-- tombstones it, so a worker stops, and never resumes, the processes of a
-- workspace being deleted. DeleteWorkspace drops the placement in the same
-- transaction as the soft delete, so the placement is already gone then and
-- would read as ACTIVE. The inner join to workspaces always finds the row: the
-- composite foreign key ties every tab row to its workspace.
-- name: ListOwnedTabsByWorker :many
SELECT wto.user_id, wto.workspace_id, wto.tab_type, wto.tab_id, wto.worker_id,
       CASE WHEN w.deleted_at IS NOT NULL OR ws.section_type = sqlc.arg(archived_section_type)
            THEN sqlc.arg(archived_archive_state)::INTEGER
            ELSE sqlc.arg(active_archive_state)::INTEGER END AS archive_state
FROM workspace_tab_owned wto
JOIN workspaces w ON w.id = wto.workspace_id
LEFT JOIN workspace_section_items wsi
  ON wsi.user_id = wto.user_id AND wsi.workspace_id = wto.workspace_id
LEFT JOIN workspace_sections ws
  ON ws.id = wsi.section_id AND ws.user_id = wto.user_id
WHERE wto.user_id = sqlc.arg(user_id) AND wto.worker_id = sqlc.arg(worker_id)
ORDER BY wto.workspace_id, wto.position;

-- ListOwnedTabsByWorkspace binds user_id as well as workspace_id. The
-- composite workspace key already makes every row of a workspace its owner's,
-- so the bind changes no result for the owner; it makes a caller who does not
-- own the workspace see nothing, and it keeps the read owner-scoped by its
-- own WHERE clause.
--
-- Returns the tab ROWS, not just the distinct worker ids. The caller needs
-- both: the worker set to fan out to, and the (tab_type, tab_id) list each
-- worker must tear down. Reading them here -- inside the delete transaction --
-- is what makes the two atomic. When each caller resolved its own list
-- beforehand, a tab a peer opened in between was missed, a failed read
-- degraded silently to "close nothing", and both callers read only the
-- rendered rows, a strict SUBSET of this table, so a projection-hidden tab was
-- structurally unreachable rather than merely late.
-- name: ListOwnedTabsByWorkspace :many
SELECT worker_id, tab_type, tab_id FROM workspace_tab_owned
WHERE user_id = $1 AND workspace_id = $2
ORDER BY worker_id, tab_id;

-- GetOwnedTab is a point lookup on PRIMARY KEY (user_id, tab_id). Tab ids
-- are client-minted and unique only within one user, so user_id is half the
-- key, not a filter: without it this :one returns an arbitrary tenant's row.
-- name: GetOwnedTab :one
SELECT * FROM workspace_tab_owned WHERE user_id = $1 AND tab_id = $2;

-- ListRenderedTabsByWorkspaceIDs binds user_id as well as the workspace set.
-- Tab ids are unique only within one user, and the read must stay
-- owner-scoped by its own WHERE clause; the composite workspace key makes the
-- bind redundant for the owner, and makes a caller who does not own a listed
-- workspace see none of its tabs.
-- name: ListRenderedTabsByWorkspaceIDs :many
SELECT * FROM workspace_tab_owned
WHERE user_id = sqlc.arg(user_id) AND workspace_id = ANY(sqlc.arg(workspace_ids)::TEXT[]) AND rendered
ORDER BY workspace_id, position;

-- GetRenderedTab binds user_id as well as workspace_id and tab_id. Tab ids are
-- client-minted and unique only within one user, so (workspace_id, tab_type,
-- tab_id) is not a key: without user_id this :one returns an arbitrary tenant's
-- row. Mirrors GetOwnedTab.
-- name: GetRenderedTab :one
SELECT * FROM workspace_tab_owned
WHERE user_id = $1 AND workspace_id = $2 AND tab_type = $3 AND tab_id = $4 AND rendered;

-- LocateAccessibleRenderedTab finds a rendered tab by tab_id and
-- (optionally) tab_type across every workspace the user owns.
-- tab_type = 0 (TAB_TYPE_UNSPECIFIED) means "match any type";
-- tab ids are unique across types so the match is unambiguous.
-- Used by WorkspaceService.LocateTab. The composite workspace key makes
-- t.user_id the workspace's owner, so binding t.user_id is the ownership
-- check; the join only filters out a soft-deleted workspace.
-- name: LocateAccessibleRenderedTab :one
SELECT t.* FROM workspace_tab_owned t
JOIN workspaces w ON w.id = t.workspace_id
WHERE t.tab_id = sqlc.arg(tab_id)
  AND (sqlc.arg(tab_type)::integer = 0 OR t.tab_type = sqlc.arg(tab_type))
  AND t.rendered
  AND w.deleted_at IS NULL
  AND t.user_id = sqlc.arg(user_id)
LIMIT 1;
