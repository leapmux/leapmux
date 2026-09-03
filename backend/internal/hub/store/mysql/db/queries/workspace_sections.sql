-- name: CreateWorkspaceSection :exec
INSERT INTO workspace_sections (id, user_id, name, position, section_type, sidebar)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListWorkspaceSectionsByUserID :many
SELECT * FROM workspace_sections
WHERE user_id = ?
ORDER BY sidebar, position;

-- name: GetWorkspaceSectionByID :one
SELECT * FROM workspace_sections WHERE id = ?;

-- name: RenameWorkspaceSection :execresult
-- The section type is a PARAMETER, not the literal 1 this used to carry.
-- SectionType is a proto enum, and a renumber there would silently change
-- which sections this matches -- if 1 came to mean a built-in type, the
-- sibling delete below would start matching the sections it exists to
-- protect. Binding the enum makes a renumber propagate instead.
UPDATE workspace_sections SET name = ?
WHERE id = ? AND user_id = ? AND section_type = ?;

-- name: UpdateWorkspaceSectionPosition :exec
UPDATE workspace_sections SET position = ?
WHERE id = ? AND user_id = ?;

-- name: UpdateWorkspaceSectionSidebarPosition :exec
UPDATE workspace_sections SET sidebar = ?, position = ?
WHERE id = ? AND user_id = ?;

-- name: DeleteWorkspaceSection :execresult
-- Section type is a parameter; see RenameWorkspaceSection above.
DELETE FROM workspace_sections
WHERE id = ? AND user_id = ? AND section_type = ?;

-- name: HasDefaultSectionsForUser :one
-- Section type is a parameter; see RenameWorkspaceSection above.
SELECT EXISTS(
  SELECT 1 FROM workspace_sections
  WHERE user_id = ? AND section_type != ?
);
