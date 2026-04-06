-- name: CreateWorkerNotification :exec
INSERT INTO worker_notifications (id, worker_id, type, payload) VALUES ($1, $2, $3, $4);

-- name: ListPendingNotificationsByWorker :many
SELECT * FROM worker_notifications
WHERE worker_id = $1 AND status = 1 AND attempts < max_attempts
ORDER BY created_at;

-- name: MarkNotificationDelivered :exec
UPDATE worker_notifications
SET status = 2, delivered_at = NOW()
WHERE id = $1;

-- name: IncrementNotificationAttempts :exec
UPDATE worker_notifications SET attempts = attempts + 1 WHERE id = $1;

-- name: MarkNotificationFailed :exec
UPDATE worker_notifications SET status = 3 WHERE id = $1;
