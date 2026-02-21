-- name: CreateWorkspaceSection :exec
INSERT INTO workspace_sections (id, user_id, name, position, section_type)
VALUES (?, ?, ?, ?, ?);

-- name: ListWorkspaceSectionsByUserID :many
SELECT * FROM workspace_sections
WHERE user_id = ?
ORDER BY position;

-- name: GetWorkspaceSectionByID :one
SELECT * FROM workspace_sections WHERE id = ?;

-- name: RenameWorkspaceSection :execresult
UPDATE workspace_sections SET name = ?
WHERE id = ? AND user_id = ? AND section_type = 1;

-- name: UpdateWorkspaceSectionPosition :exec
UPDATE workspace_sections SET position = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteWorkspaceSection :execresult
DELETE FROM workspace_sections
WHERE id = ? AND user_id = ? AND section_type = 1;

-- name: CountDefaultSectionsForUser :one
SELECT COUNT(*) FROM workspace_sections
WHERE user_id = ? AND section_type != 1;
