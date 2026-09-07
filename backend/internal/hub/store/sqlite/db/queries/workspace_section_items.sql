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
-- A section item PLACES a workspace, so it must not outlive the workspace.
-- The workspace delete is a SOFT delete (workspaces.is_deleted), so the
-- ON DELETE CASCADE on workspace_id never fires and the row survives. The
-- join to workspaces drops it here instead. Without this the sidebar counts
-- an archived workspace nobody can see: the rows come from the workspace
-- list, but the Archived section menu counts ITEMS, so it offers
-- "Unarchive all" and "Empty archive..." for an empty archive, and
-- "Unarchive all" cannot clear them because the workspace is gone.
--
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
JOIN workspaces w ON wsi.workspace_id = w.id AND w.is_deleted = 0
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
-- Section type is a parameter, not the literal 3 this used to carry: a
-- SectionType renumber must propagate rather than silently change which
-- sections count as archived. See RenameWorkspaceSection.
SELECT COUNT(*) > 0 AS is_archived FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = ? AND wsi.workspace_id = ? AND ws.section_type = ?;
