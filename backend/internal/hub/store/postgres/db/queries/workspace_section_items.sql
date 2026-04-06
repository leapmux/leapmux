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
SELECT wsi.* FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = $1
ORDER BY ws.position, wsi.position;

-- name: DeleteWorkspaceSectionItem :exec
DELETE FROM workspace_section_items
WHERE user_id = $1 AND workspace_id = $2;

-- name: DeleteWorkspaceSectionItemsBySection :exec
DELETE FROM workspace_section_items
WHERE section_id = $1;

-- name: MoveWorkspaceSectionItemsToSection :exec
UPDATE workspace_section_items SET section_id = $1
WHERE section_id = $2;

-- name: HasWorkspaceSectionItemsBySection :one
SELECT EXISTS(SELECT 1 FROM workspace_section_items WHERE section_id = $1) AS has_items;

-- name: IsWorkspaceInArchivedSection :one
SELECT COUNT(*) > 0 AS is_archived FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = $1 AND wsi.workspace_id = $2 AND ws.section_type = 3;
