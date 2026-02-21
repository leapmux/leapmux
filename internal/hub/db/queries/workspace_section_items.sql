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
SELECT wsi.* FROM workspace_section_items wsi
JOIN workspace_sections ws ON wsi.section_id = ws.id
WHERE wsi.user_id = ?
ORDER BY ws.position, wsi.position;

-- name: DeleteWorkspaceSectionItem :exec
DELETE FROM workspace_section_items
WHERE user_id = ? AND workspace_id = ?;

-- name: DeleteWorkspaceSectionItemsBySection :exec
DELETE FROM workspace_section_items
WHERE section_id = ?;

-- name: MoveWorkspaceSectionItemsToSection :exec
UPDATE workspace_section_items SET section_id = ?
WHERE section_id = ?;
