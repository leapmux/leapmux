-- name: CreateWorkerShare :exec
INSERT INTO worker_shares (worker_id, user_id) VALUES (?, ?)
ON CONFLICT DO NOTHING;

-- name: DeleteWorkerShare :exec
DELETE FROM worker_shares WHERE worker_id = ? AND user_id = ?;

-- name: ListWorkerSharesByWorkerID :many
SELECT bs.worker_id, bs.user_id, bs.created_at, u.username, u.display_name
FROM worker_shares bs
JOIN users u ON u.id = bs.user_id
WHERE bs.worker_id = ?
ORDER BY bs.created_at;

-- name: ClearWorkerShares :exec
DELETE FROM worker_shares WHERE worker_id = ?;
