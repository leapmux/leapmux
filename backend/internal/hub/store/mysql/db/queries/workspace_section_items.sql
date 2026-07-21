-- name: SetWorkspaceSectionItem :exec
INSERT INTO workspace_section_items (user_id, workspace_id, section_id, position)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  section_id = VALUES(section_id),
  position = VALUES(position);

-- name: GetWorkspaceSectionItem :one
SELECT * FROM workspace_section_items
WHERE user_id = ? AND workspace_id = ?;

-- name: ListWorkspaceSectionItemsByUser :many
-- workspace_id is the deterministic tiebreaker. wsi.position is a
-- lexorank string with NO uniqueness constraint, and two items
-- legitimately share a position: lexorank.first() always returns 'n',
-- so dragging two workspaces as the first item into two different
-- sections both produce position='n'. Section deletion's cascade
-- (SectionService.DeleteSection re-stamps positions via lexorank.After
-- in a single RunInTransaction loop) walks items in the order this
-- query returns them; two 'n' items can coexist mid-iteration.
-- Without the workspace_id tiebreaker the planner flips their
-- relative order across refreshes.
--
-- The workspace_id tiebreaker is byte-wise (case-sensitive) because every
-- table is created COLLATE=utf8mb4_bin, so no explicit cast is needed.
-- MySQL's session default `utf8mb4_general_ci` is case-INsensitive; the
-- table-level binary collation ensures two workspace_ids differing only in
-- case still sort deterministically (the storetest tiebreaker-stability
-- test catches any regression). SQLite and PostgreSQL already collate
-- case-sensitively by default.
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
