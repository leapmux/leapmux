-- name: CreateWorkerNotification :exec
INSERT INTO worker_notifications (id, worker_id, type, payload) VALUES (?, ?, ?, ?);

-- name: ListPendingNotificationsByWorker :many
SELECT * FROM worker_notifications
WHERE worker_id = ? AND status = 1 AND attempts < max_attempts
ORDER BY created_at;

-- name: MarkNotificationDelivered :exec
UPDATE worker_notifications
SET status = 2, delivered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = ?;

-- name: IncrementNotificationAttempts :exec
UPDATE worker_notifications SET attempts = attempts + 1 WHERE id = ?;

-- name: MarkNotificationFailed :exec
UPDATE worker_notifications SET status = 3 WHERE id = ?;
