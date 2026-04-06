-- name: CreateWorkspaceSection :exec
INSERT INTO workspace_sections (id, user_id, name, position, section_type, sidebar)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListWorkspaceSectionsByUserID :many
SELECT * FROM workspace_sections
WHERE user_id = $1
ORDER BY sidebar, position;

-- name: GetWorkspaceSectionByID :one
SELECT * FROM workspace_sections WHERE id = $1;

-- name: RenameWorkspaceSection :execresult
UPDATE workspace_sections SET name = $1
WHERE id = $2 AND user_id = $3 AND section_type = 1;

-- name: UpdateWorkspaceSectionPosition :exec
UPDATE workspace_sections SET position = $1
WHERE id = $2 AND user_id = $3;

-- name: UpdateWorkspaceSectionSidebarPosition :exec
UPDATE workspace_sections SET sidebar = $1, position = $2
WHERE id = $3 AND user_id = $4;

-- name: DeleteWorkspaceSection :execresult
DELETE FROM workspace_sections
WHERE id = $1 AND user_id = $2 AND section_type = 1;

-- name: HasDefaultSectionsForUser :one
SELECT EXISTS(
  SELECT 1 FROM workspace_sections
  WHERE user_id = $1 AND section_type != 1
);
