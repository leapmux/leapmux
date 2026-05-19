-- name: SetWorkspaceSectionItem :exec
INSERT INTO workspace_section_items (user_id, workspace_id, section_id, position)
VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, workspace_id) DO UPDATE SET
  section_id = excluded.section_id,
  position = excluded.position;

-- name: GetWorkspaceSectionItem :one
SELECT * FROM workspace_section_items
WHERE user_id = ? AND workspace_id = ?;

-- name: ListWorkspaceSectionItemsByUser :many
-- workspace_id is the deterministic tiebreaker. wsi.position is a
-- lexorank string with NO uniqueness constraint, and two items
-- legitimately share a position: lexorank.first() always returns
-- 'n', so dragging two different workspaces as the first item into
-- two different sections both produce position='n'. When one of
-- those sections is later deleted, SectionService.DeleteSection
-- relocates its items into the default section (in a single
-- RunInTransaction loop that re-stamps positions with
-- lexorank.After). The re-stamp pass walks items in the same order
-- this query returns them, so two items at position 'n' can still
-- coexist briefly while the loop is mid-iteration. Without the
-- workspace_id tiebreaker the SQL planner is free to flip their
-- relative order on each refresh, and the sidebar visibly shuffles
-- across page loads.
SELECT wsi.* FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = ?
ORDER BY ws.position, wsi.position, wsi.workspace_id;

-- name: DeleteWorkspaceSectionItem :exec
DELETE FROM workspace_section_items
WHERE user_id = ? AND workspace_id = ?;

-- name: DeleteWorkspaceSectionItemsBySection :exec
DELETE FROM workspace_section_items
WHERE section_id = ?;

-- name: HasWorkspaceSectionItemsBySection :one
SELECT EXISTS(SELECT 1 FROM workspace_section_items WHERE section_id = ?) AS has_items;

-- name: IsWorkspaceInArchivedSection :one
SELECT COUNT(*) > 0 AS is_archived FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = ? AND wsi.workspace_id = ? AND ws.section_type = 3;
