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
-- The section type is a PARAMETER, not the literal 1 this used to carry.
-- SectionType is a proto enum, and a renumber there would silently change
-- which sections this matches -- if 1 came to mean a built-in type, the
-- sibling delete below would start matching the sections it exists to
-- protect. Binding the enum makes a renumber propagate instead.
UPDATE workspace_sections SET name = $1
WHERE id = $2 AND user_id = $3 AND section_type = $4;

-- name: UpdateWorkspaceSectionPosition :exec
UPDATE workspace_sections SET position = $1
WHERE id = $2 AND user_id = $3;

-- name: UpdateWorkspaceSectionSidebarPosition :exec
UPDATE workspace_sections SET sidebar = $1, position = $2
WHERE id = $3 AND user_id = $4;

-- name: DeleteWorkspaceSection :execresult
-- Section type is a parameter; see RenameWorkspaceSection above.
DELETE FROM workspace_sections
WHERE id = $1 AND user_id = $2 AND section_type = $3;

-- name: HasDefaultSectionsForUser :one
-- Section type is a parameter; see RenameWorkspaceSection above.
SELECT EXISTS(
  SELECT 1 FROM workspace_sections
  WHERE user_id = $1 AND section_type != $2
);
