-- name: CreateWorker :exec
INSERT INTO workers (id, org_id, name, hostname, os, arch, auth_token, registered_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerByID :one
SELECT * FROM workers WHERE id = ? AND org_id = ?;

-- name: GetWorkerByIDInternal :one
SELECT * FROM workers WHERE id = ?;

-- name: GetWorkerByAuthToken :one
SELECT * FROM workers WHERE auth_token = ? AND status != 3;

-- name: ListWorkersByOrgID :many
SELECT * FROM workers WHERE org_id = ? AND status = 1 ORDER BY created_at DESC LIMIT ? OFFSET ?;

-- name: ListVisibleWorkers :many
SELECT DISTINCT b.* FROM workers b
LEFT JOIN worker_shares bs ON b.id = bs.worker_id AND bs.user_id = sqlc.arg(user_id)
WHERE b.org_id = sqlc.arg(org_id)
  AND b.status = 1
  AND (
    b.registered_by = sqlc.arg(user_id)
    OR b.share_mode = 2
    OR (b.share_mode = 3 AND bs.user_id IS NOT NULL)
  )
ORDER BY b.created_at DESC
LIMIT sqlc.arg(limit) OFFSET sqlc.arg(offset);

-- name: GetVisibleWorker :one
SELECT DISTINCT b.* FROM workers b
LEFT JOIN worker_shares bs ON b.id = bs.worker_id AND bs.user_id = sqlc.arg(user_id)
WHERE b.id = sqlc.arg(worker_id)
  AND b.org_id = sqlc.arg(org_id)
  AND b.status = 1
  AND (
    b.registered_by = sqlc.arg(user_id)
    OR b.share_mode = 2
    OR (b.share_mode = 3 AND bs.user_id IS NOT NULL)
  );

-- name: RenameWorker :execresult
UPDATE workers SET name = ? WHERE id = ? AND registered_by = ?;

-- name: SetWorkerStatus :exec
UPDATE workers SET status = ? WHERE id = ?;

-- name: DeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND registered_by = ? AND status = 1;

-- name: ForceDeregisterWorker :execresult
UPDATE workers SET status = 2 WHERE id = ? AND status = 1;

-- name: MarkWorkerDeleted :exec
UPDATE workers SET status = 3 WHERE id = ?;

-- name: ListWorkspaceIDsByWorker :many
SELECT DISTINCT w.id FROM workspaces w
JOIN agents a ON a.workspace_id = w.id
WHERE a.worker_id = ? AND w.is_deleted = 0;

-- name: ListWorkspaceIDsByWorkerAndCreator :many
SELECT DISTINCT w.id FROM workspaces w
JOIN agents a ON a.workspace_id = w.id
WHERE a.worker_id = ? AND w.created_by = ? AND w.is_deleted = 0;

-- name: ListWorkersByOrgAndRegisteredBy :many
SELECT id FROM workers WHERE org_id = ? AND registered_by = ? AND status = 1;

-- name: ListWorkspaceIDsByOrgAndCreator :many
SELECT DISTINCT w.id FROM workspaces w
WHERE w.org_id = ? AND w.created_by = ? AND w.is_deleted = 0;

-- name: UpdateWorkerLastSeen :exec
UPDATE workers SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?;

-- name: UpdateWorkerShareMode :execresult
UPDATE workers SET share_mode = ? WHERE id = ? AND registered_by = ?;
