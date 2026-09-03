-- name: UpsertWorkerTabPayload :exec
INSERT INTO worker_tab_payloads (user_id, tab_id, tab_type, payload, working_dir)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (user_id, tab_id) DO UPDATE SET
    tab_type = excluded.tab_type,
    payload = excluded.payload,
    working_dir = excluded.working_dir;

-- name: GetWorkerTabPayload :one
SELECT * FROM worker_tab_payloads WHERE user_id = ? AND tab_id = ?;

-- name: ListAllWorkerTabPayloads :many
SELECT * FROM worker_tab_payloads ORDER BY user_id, tab_id;

-- name: ListWorkerTabPayloadsByUser :many
-- Owner-scoped read backing the private-events bootstrap replay. The stream is
-- keyed by worker, not workspace, so the owner is the whole predicate -- and it
-- seeks the (user_id, tab_id) primary key rather than scanning the table.
SELECT * FROM worker_tab_payloads WHERE user_id = ? ORDER BY tab_id;

-- name: DeleteWorkerTabPayload :execresult
-- :execresult, not :exec, so RevokeRow can distinguish "deleted" from "no such
-- row" without a preceding SELECT. The probe it replaces was pure overhead once
-- the revoke event stopped needing the row's columns, and it left a TOCTOU window:
-- a concurrent revoke could make the probe succeed and this delete a no-op that
-- still reported success and published a duplicate TabPayloadRevoked.
DELETE FROM worker_tab_payloads WHERE user_id = ? AND tab_id = ?;
