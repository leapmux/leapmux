-- name: GetWorkerTabPublication :one
SELECT request_hash, response_payload FROM worker_tab_publications
WHERE user_id = sqlc.arg(user_id) AND worker_id = sqlc.arg(worker_id) AND tab_id = sqlc.arg(tab_id);

-- name: InsertWorkerTabPublication :exec
INSERT INTO worker_tab_publications(user_id, worker_id, tab_id, request_hash, response_payload)
VALUES(sqlc.arg(user_id), sqlc.arg(worker_id), sqlc.arg(tab_id), sqlc.arg(request_hash), sqlc.arg(response_payload));

-- name: ReplaceWorkerTabPublication :execrows
UPDATE worker_tab_publications SET response_payload = sqlc.arg(response_payload)
WHERE user_id = sqlc.arg(user_id) AND worker_id = sqlc.arg(worker_id)
  AND tab_id = sqlc.arg(tab_id) AND request_hash = sqlc.arg(request_hash);
