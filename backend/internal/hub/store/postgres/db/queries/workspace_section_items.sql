-- name: SetWorkspaceSectionItem :exec
INSERT INTO workspace_section_items (user_id, workspace_id, section_id, position)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, workspace_id) DO UPDATE SET
  section_id = EXCLUDED.section_id,
  position = EXCLUDED.position;

-- name: GetWorkspaceSectionItem :one
SELECT * FROM workspace_section_items
WHERE user_id = $1 AND workspace_id = $2;

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
SELECT wsi.* FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = $1
ORDER BY ws.position, wsi.position, wsi.workspace_id;

-- name: DeleteWorkspaceSectionItem :exec
DELETE FROM workspace_section_items
WHERE user_id = $1 AND workspace_id = $2;

-- name: DeleteWorkspaceSectionItemsBySection :exec
DELETE FROM workspace_section_items
WHERE section_id = $1;

-- name: HasWorkspaceSectionItemsBySection :one
SELECT EXISTS(SELECT 1 FROM workspace_section_items WHERE section_id = $1) AS has_items;

-- name: IsWorkspaceInArchivedSection :one
SELECT COUNT(*) > 0 AS is_archived FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = $1 AND wsi.workspace_id = $2 AND ws.section_type = 3;
