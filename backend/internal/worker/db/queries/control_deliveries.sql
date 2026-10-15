-- name: ClaimControlDelivery :execrows
INSERT INTO control_deliveries (id, agent_id, kind, operation_id, instance_id, state)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (agent_id, kind, operation_id, instance_id) DO NOTHING;

-- name: GetControlDelivery :one
SELECT * FROM control_deliveries WHERE id = ?;

-- name: SetControlDeliveryState :execrows
UPDATE control_deliveries SET state = sqlc.arg(state)
WHERE id = sqlc.arg(id) AND state = sqlc.arg(required_state);

-- name: ReleaseUnsentControlDelivery :execrows
DELETE FROM control_deliveries WHERE id = ? AND state = ?;
